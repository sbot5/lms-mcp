package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/render"
	"github.com/sbot5/lms-mcp/internal/store"
)

func contentDigest(value any) string {
	b, _ := json.Marshal(value)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

func edTimestamp(value string) int64 {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func threadSourceFingerprint(t ed.Thread) string {
	return contentDigest(struct {
		ID, UserID, Replies                         int
		Title, Updated, Kind, Category, Subcategory string
		Private, Answered                           bool
	}{t.ID, t.UserID, t.ReplyCount, t.Title, t.UpdatedAt, t.Type, t.Category, t.Subcategory, t.IsPrivate, t.IsAnswered})
}

func stableEdThreadList(ctx context.Context, client *ed.Client, courseID int) ([]ed.Thread, error) {
	last := ""
	for attempt := 0; attempt < 3; attempt++ {
		threads, err := client.Threads(ctx, courseID)
		if err != nil {
			return nil, err
		}
		slices.SortFunc(threads, func(a, b ed.Thread) int {
			if a.ID < b.ID {
				return -1
			}
			if a.ID > b.ID {
				return 1
			}
			return 0
		})
		fingerprints := make([]string, 0, len(threads))
		for _, t := range threads {
			if t.ID <= 0 || t.CourseID != courseID {
				return nil, fmt.Errorf("Ed thread inventory has an invalid identity")
			}
			fingerprints = append(fingerprints, threadSourceFingerprint(t))
		}
		current := contentDigest(fingerprints)
		if attempt > 0 && last == current {
			return threads, nil
		}
		last = current
	}
	return nil, fmt.Errorf("Ed discussion inventory changed during pagination; retry synchronization")
}

func authorDisplay(user ed.User, anonymous bool) (string, string) {
	if anonymous {
		return "student", "Student"
	}
	switch strings.ToLower(user.CourseRole) {
	case "admin", "staff", "instructor", "teacher", "lecturer":
		return "staff", user.Name
	case "mentor", "tutor", "ta", "teaching_assistant":
		return "ta", user.Name
	default:
		return "student", "Student"
	}
}

// collectEdDiscussions fetches metadata incrementally, with a complete stable
// inventory every run and detail reconciliation at least once per 24 hours.
func collectEdDiscussions(ctx context.Context, p *Providers, db *store.DB, course store.Course, full bool) (store.ContentSnapshot, error) {
	snap := store.ContentSnapshot{Kinds: []string{"thread", "announcement", "reply"}}
	who, err := p.Ed.Whoami(ctx)
	if err != nil {
		return snap, err
	}
	if who.UserID <= 0 {
		return snap, fmt.Errorf("Ed identity is missing")
	}
	threads, err := stableEdThreadList(ctx, p.Ed, course.EdCourseID)
	if err != nil {
		return snap, err
	}
	cached, err := db.ListItems(store.ItemFilter{CourseID: course.ID, Provider: "ed", Kinds: snap.Kinds})
	if err != nil {
		return snap, err
	}
	byID := map[string]store.Item{}
	for _, it := range cached {
		byID[it.ID] = it
	}
	now := time.Now().Unix()
	for _, t := range threads {
		id := "ed:thread:" + strconv.Itoa(t.ID)
		fingerprint := threadSourceFingerprint(t)
		old, exists := byID[id]
		var meta struct {
			Fingerprint string `json:"source_fingerprint"`
			CheckedAt   int64  `json:"checked_at"`
		}
		_ = json.Unmarshal([]byte(old.MetaJSON), &meta)
		if !full && exists && meta.Fingerprint == fingerprint && meta.CheckedAt > 0 && now-meta.CheckedAt < 86400 {
			snap.Items = append(snap.Items, old)
			keep := map[string]bool{id: true}
			for changed := true; changed; {
				changed = false
				for _, it := range cached {
					if it.Kind == "reply" && !keep[it.ID] && keep[it.ParentID] {
						keep[it.ID] = true
						snap.Items = append(snap.Items, it)
						changed = true
					}
				}
			}
			continue
		}
		detail, users, err := p.Ed.Thread(ctx, t.ID)
		if err != nil {
			return snap, err
		}
		if detail.ID != t.ID || detail.CourseID != course.EdCourseID {
			return snap, fmt.Errorf("Ed thread detail identity mismatch")
		}
		md, _, err := render.Body(detail.Content, detail.Document, nil)
		if err != nil {
			return snap, err
		}
		role, display := authorDisplay(users[detail.UserID], detail.IsAnonymous)
		meaning := map[string]any{"category": detail.Category, "subcategory": detail.Subcategory, "type": detail.Type, "number": detail.Number, "is_private": detail.IsPrivate, "is_answered": detail.IsAnswered, "is_own": detail.UserID == who.UserID}
		hash := contentDigest([]any{detail.Title, md, role, display, meaning})
		meaning["source_fingerprint"], meaning["checked_at"] = fingerprint, now
		metadata, _ := json.Marshal(meaning)
		kind := "thread"
		if detail.Type == "announcement" {
			kind = "announcement"
		}
		snap.Items = append(snap.Items, store.Item{ID: id, CourseID: course.ID, Provider: "ed", Kind: kind, Title: detail.Title, URL: fmt.Sprintf("https://%s/%s/courses/%d/discussion/%d", ed.RegionHost(p.Cfg.Ed.Region), p.Cfg.Ed.Region, course.EdCourseID, detail.ID), AuthorRole: role, AuthorDisplay: display, CreatedAt: edTimestamp(detail.CreatedAt), UpdatedAt: edTimestamp(detail.UpdatedAt), BodyMD: md, ContentHash: hash, MetaJSON: string(metadata)})
		seenReplies := map[int]bool{}
		var addReplies func([]ed.Reply, string) error
		addReplies = func(replies []ed.Reply, parent string) error {
			for _, reply := range replies {
				if reply.DeletedAt != "" {
					// Ed can keep live comments below a deleted reply. Attach them
					// to the nearest visible ancestor without retaining the deleted body.
					if err := addReplies(reply.Comments, parent); err != nil {
						return err
					}
					continue
				}
				if reply.ID <= 0 || seenReplies[reply.ID] {
					return fmt.Errorf("Ed reply has an invalid or repeated identity")
				}
				seenReplies[reply.ID] = true
				body, _, err := render.Body(reply.Content, reply.Document, nil)
				if err != nil {
					return err
				}
				role, name := authorDisplay(users[reply.UserID], reply.IsAnonymous)
				rid := "ed:reply:" + strconv.Itoa(reply.ID)
				metadata := map[string]any{"type": reply.Type, "is_private": reply.IsPrivate, "is_own": reply.UserID == who.UserID, "reply_to_me": detail.UserID == who.UserID && reply.UserID > 0 && reply.UserID != who.UserID}
				raw, _ := json.Marshal(metadata)
				snap.Items = append(snap.Items, store.Item{ID: rid, CourseID: course.ID, Provider: "ed", Kind: "reply", ParentID: parent, Title: detail.Title, URL: fmt.Sprintf("https://%s/%s/courses/%d/discussion/%d", ed.RegionHost(p.Cfg.Ed.Region), p.Cfg.Ed.Region, course.EdCourseID, detail.ID), AuthorRole: role, AuthorDisplay: name, CreatedAt: edTimestamp(reply.CreatedAt), UpdatedAt: edTimestamp(reply.UpdatedAt), BodyMD: body, ContentHash: contentDigest([]any{body, role, name, metadata}), MetaJSON: string(raw)})
				if err := addReplies(reply.Comments, rid); err != nil {
					return err
				}
			}
			return nil
		}
		if err := addReplies(detail.Answers, id); err != nil {
			return snap, err
		}
		if err := addReplies(detail.Comments, id); err != nil {
			return snap, err
		}
	}
	return snap, nil
}
