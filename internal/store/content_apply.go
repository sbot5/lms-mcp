package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ApplyEdSnapshot publishes a course and its events/checkpoint atomically.
// A failed download never reaches this function. Removed items keep their body
// and files, and unavailable inventories keep their previous rows.
func (db *DB) ApplyEdSnapshot(ctx context.Context, courseID string, snap ContentSnapshot, now int64) (SnapshotResult, error) {
	result := SnapshotResult{}
	if courseID == "" || now <= 0 {
		return result, errors.New("invalid content snapshot identity or time")
	}
	known := map[string]bool{"thread": true, "announcement": true, "reply": true, "lesson": true, "slide": true, "resource": true}
	seen := map[string]Item{}
	for _, it := range snap.Items {
		if it.ID == "" || it.CourseID != courseID || it.Provider != "ed" || !known[it.Kind] || it.ContentHash == "" {
			return result, errors.New("invalid Ed content item")
		}
		if _, exists := seen[it.ID]; exists {
			return result, errors.New("duplicate Ed content item")
		}
		seen[it.ID] = it
	}
	for _, k := range snap.Kinds {
		if !known[k] {
			return result, errors.New("invalid Ed inventory kind")
		}
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, err
	}
	prefix := "ed:" + hex.EncodeToString(nonce[:])
	err := db.write(func(tx *sql.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		checkLease := func() error {
			if snap.LeaseHolder == "" {
				return nil
			}
			var holder string
			var expires int64
			if err := tx.QueryRow(`SELECT holder, expires_at FROM leases WHERE name='sync'`).Scan(&holder, &expires); err != nil || holder != snap.LeaseHolder || expires <= nowUnix() {
				return errors.New("sync lease is no longer owned")
			}
			return nil
		}
		if err := checkLease(); err != nil {
			return err
		}
		var checkpoint string
		if err := tx.QueryRow(`SELECT value FROM meta WHERE "key"=?`, "ed:last_success:"+courseID).Scan(&checkpoint); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		baseline := checkpoint == ""
		eventIndex := 0
		emit := func(itemID, kind, title string) error {
			eventIndex++
			_, err := tx.Exec(`INSERT INTO events (`+eventCols+`) VALUES (?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("%s:%d", prefix, eventIndex), courseID, itemID, kind, now, title, "", boolToInt(baseline), 0)
			if err == nil {
				result.Changes++
			}
			return err
		}
		for _, it := range snap.Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			old, oldErr := scanItem(tx.QueryRow(`SELECT `+itemCols+` FROM items WHERE id=?`, it.ID))
			if oldErr != nil && !errors.Is(oldErr, sql.ErrNoRows) {
				return oldErr
			}
			if oldErr == nil && (old.CourseID != courseID || old.Provider != "ed") {
				return errors.New("Ed item belongs to another course")
			}
			var availability struct {
				Unavailable bool `json:"content_unavailable"`
			}
			if json.Unmarshal([]byte(it.MetaJSON), &availability) == nil && availability.Unavailable && oldErr == nil {
				it.BodyMD, it.ContentHash = old.BodyMD, old.ContentHash
			}
			changed := oldErr != nil || old.ContentHash != it.ContentHash || old.RemovedAt != 0
			_, err := tx.Exec(`INSERT INTO items (`+itemCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET course_id=excluded.course_id,provider=excluded.provider,kind=excluded.kind,parent_id=excluded.parent_id,title=excluded.title,url=excluded.url,author_role=excluded.author_role,author_display=excluded.author_display,created_at=excluded.created_at,updated_at=excluded.updated_at,body_md=excluded.body_md,content_hash=excluded.content_hash,meta_json=excluded.meta_json,removed_at=0`, it.ID, it.CourseID, it.Provider, it.Kind, it.ParentID, it.Title, it.URL, it.AuthorRole, it.AuthorDisplay, it.CreatedAt, it.UpdatedAt, it.BodyMD, it.ContentHash, it.MetaJSON, 0)
			if err != nil {
				return err
			}
			if err := reindexItemTx(tx, it.ID, it.Title, it.BodyMD); err != nil {
				return err
			}
			if changed {
				kind := ""
				switch it.Kind {
				case "announcement":
					kind = "announcement"
				case "thread":
					if it.AuthorRole == "staff" || it.AuthorRole == "ta" {
						kind = "staff_post"
					}
				case "reply":
					var meta struct {
						ToMe bool `json:"reply_to_me"`
					}
					_ = json.Unmarshal([]byte(it.MetaJSON), &meta)
					if meta.ToMe && errors.Is(oldErr, sql.ErrNoRows) {
						kind = "reply_to_me"
					}
				case "lesson", "slide", "resource":
					kind = "material_changed"
					if errors.Is(oldErr, sql.ErrNoRows) {
						kind = "new_material"
					}
				}
				if kind != "" {
					if err := emit(it.ID, kind, it.Title); err != nil {
						return err
					}
				}
			}
		}
		for _, kind := range snap.Kinds {
			rows, err := tx.Query(`SELECT id FROM items WHERE course_id=? AND provider='ed' AND kind=? AND removed_at=0`, courseID, kind)
			if err != nil {
				return err
			}
			var missing []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				if _, ok := seen[id]; !ok {
					missing = append(missing, id)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, id := range missing {
				if _, err := tx.Exec(`UPDATE items SET removed_at=? WHERE id=?`, now, id); err != nil {
					return err
				}
				if _, err := tx.Exec(`DELETE FROM deadlines WHERE course_id=? AND item_id=?`, courseID, id); err != nil {
					return err
				}
			}
		}
		// A complete parent inventory can confirm removal even while another
		// unavailable lesson prevents a complete slide inventory. Retire only
		// descendants of definitively removed parents, keeping their archive.
		for {
			res, err := tx.Exec(`UPDATE items SET removed_at=? WHERE course_id=? AND provider='ed' AND removed_at=0 AND parent_id IN (SELECT id FROM items WHERE course_id=? AND provider='ed' AND removed_at<>0)`, now, courseID, courseID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
		}
		if _, err := tx.Exec(`DELETE FROM deadlines WHERE course_id=? AND item_id IN (SELECT id FROM items WHERE course_id=? AND provider='ed' AND removed_at<>0)`, courseID, courseID); err != nil {
			return err
		}
		for _, f := range snap.Files {
			if _, ok := seen[f.ItemID]; !ok || f.ID == "" {
				return errors.New("file does not belong to snapshot")
			}
			var owner string
			ownerErr := tx.QueryRow(`SELECT item_id FROM files WHERE id=?`, f.ID).Scan(&owner)
			if ownerErr != nil && !errors.Is(ownerErr, sql.ErrNoRows) {
				return ownerErr
			}
			if ownerErr == nil && owner != f.ItemID {
				return errors.New("file belongs to another item")
			}
			if _, err := tx.Exec(`INSERT INTO files (`+fileCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET item_id=excluded.item_id,name=excluded.name,mime=excluded.mime,size=excluded.size,source_url=excluded.source_url,local_path=excluded.local_path,sha256=excluded.sha256,etag=excluded.etag,last_modified=excluded.last_modified,status=excluded.status,error=excluded.error`, f.ID, f.ItemID, f.Name, f.MIME, f.Size, f.SourceURL, f.LocalPath, f.SHA256, f.ETag, f.LastModified, f.Status, f.Error); err != nil {
				return err
			}
		}
		for _, g := range snap.Grades {
			if _, ok := seen[g.ItemKey]; !ok || g.CourseID != courseID || g.Hash == "" {
				return errors.New("invalid grade identity")
			}
			var old string
			err := tx.QueryRow(`SELECT hash FROM grades WHERE course_id=? AND item_key=?`, courseID, g.ItemKey).Scan(&old)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, e := tx.Exec(`INSERT INTO grades (`+gradeCols+`) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(course_id,item_key) DO UPDATE SET name=excluded.name,grade=excluded.grade,grade_max=excluded.grade_max,percentage=excluded.percentage,feedback_md=excluded.feedback_md,graded_at=excluded.graded_at,hash=excluded.hash`, g.CourseID, g.ItemKey, g.Name, g.Grade, g.GradeMax, g.Percentage, g.FeedbackMD, g.GradedAt, g.Hash); e != nil {
				return e
			}
			if old != g.Hash {
				if e := emit(g.ItemKey, "new_grade", g.Name); e != nil {
					return e
				}
			}
		}
		for _, d := range snap.Deadlines {
			if _, ok := seen[d.ItemID]; !ok || d.CourseID != courseID || d.Kind == "" {
				return errors.New("invalid deadline identity")
			}
			var oldDue, oldOpens, oldCutoff, oldCompleted int64
			var oldStatus string
			err := tx.QueryRow(`SELECT due_at,opens_at,cutoff_at,submission_status,completed FROM deadlines WHERE course_id=? AND item_id=? AND kind=?`, courseID, d.ItemID, d.Kind).Scan(&oldDue, &oldOpens, &oldCutoff, &oldStatus, &oldCompleted)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if fields, explicit := snap.DeadlineFields[d.ItemID]; explicit && err == nil {
				if !fields.Opens {
					d.OpensAt = oldOpens
				}
				if !fields.Due {
					d.DueAt = oldDue
				}
				if !fields.Cutoff {
					d.CutoffAt = oldCutoff
				}
				if !fields.Status {
					d.SubmissionStatus, d.Completed = oldStatus, oldCompleted != 0
				}
			}
			if _, e := tx.Exec(`INSERT INTO deadlines (`+deadlineCols+`) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(course_id,item_id,kind) DO UPDATE SET opens_at=excluded.opens_at,due_at=excluded.due_at,cutoff_at=excluded.cutoff_at,submission_status=excluded.submission_status,completed=excluded.completed`, d.CourseID, d.ItemID, d.Kind, d.OpensAt, d.DueAt, d.CutoffAt, d.SubmissionStatus, boolToInt(d.Completed)); e != nil {
				return e
			}
			if d.DueAt != oldDue {
				kind := "deadline_changed"
				if errors.Is(err, sql.ErrNoRows) {
					kind = "deadline_added"
				}
				if d.DueAt == 0 {
					kind = "deadline_removed"
				}
				if e := emit(d.ItemID, kind, "Assessment deadline changed"); e != nil {
					return e
				}
			}
		}
		warnings, err := json.Marshal(snap.Warnings)
		if err != nil {
			return err
		}
		for key, value := range map[string]string{"ed:last_success:" + courseID: strconv.FormatInt(now, 10), "ed:warnings:" + courseID: string(warnings)} {
			if _, err := tx.Exec(`INSERT INTO meta("key",value) VALUES(?,?) ON CONFLICT("key") DO UPDATE SET value=excluded.value`, key, value); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkLease(); err != nil {
			return err
		}
		result.Items = len(snap.Items)
		result.Files = len(snap.Files)
		return nil
	})
	if err != nil {
		return SnapshotResult{}, err
	}
	return result, nil
}
