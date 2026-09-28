package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"
)

type courseResult struct {
	Course         string `json:"course"`
	Threads        int    `json:"threads"`
	Lessons        int    `json:"lessons"`
	Changes        int    `json:"changes"`
	DetailsFetched int    `json:"details_fetched"`
	Error          string `json:"error,omitempty"`
}
type syncResult struct {
	Courses []courseResult `json:"courses"`
}

func threadFingerprint(t edThread) string { t.ViewCount = 0; return jsonHash(t) }
func recordFingerprint(r record) string {
	copy := record{}
	for k, v := range r {
		if k != "view_count" && k != "updated_at" {
			copy[k] = v
		}
	}
	return jsonHash(copy)
}

// Offset pagination has no snapshot cursor. Require two consecutive matching walks;
// a busy course fails visibly and retries on the next run instead of claiming completeness.
func stableThreads(ctx context.Context, c *edClient, id int) ([]edThread, error) {
	var last string
	for attempt := 0; attempt < 3; attempt++ {
		ts, err := c.threads(ctx, id)
		if err != nil {
			return nil, err
		}
		slices.SortFunc(ts, func(a, b edThread) int { return a.ID - b.ID })
		keys := make([]string, 0, len(ts))
		for _, t := range ts {
			if t.ID <= 0 || t.CourseID != id {
				return nil, fmt.Errorf("invalid thread identity")
			}
			keys = append(keys, threadFingerprint(t))
		}
		h := jsonHash(keys)
		if attempt > 0 && h == last {
			return ts, nil
		}
		last = h
	}
	return nil, fmt.Errorf("thread list changed during pagination; retry sync")
}

func syncCourse(ctx context.Context, c *edClient, cfg syncConfig, co courseConfig, force bool) (courseResult, error) {
	result := courseResult{Course: co.Code}
	lock, err := lockCourse(co)
	if err != nil {
		return result, fmt.Errorf("course already syncing or lock unavailable: %w", err)
	}
	defer lock.Close()
	s, err := readState(co)
	if err != nil {
		return result, err
	}
	now := time.Now().UTC()
	before := len(s.Changes)
	baseline := s.LastSuccess.IsZero()
	files := map[string][]byte{}
	if enabled(co.Discussions) {
		ts, err := stableThreads(ctx, c, co.ID)
		if err != nil {
			return result, err
		}
		next := map[int]threadCache{}
		records := []record{}
		for _, t := range ts {
			if t.IsPrivate && !co.IncludePrivate {
				continue
			}
			old, exists := s.Threads[t.ID]
			fingerprint := jsonHash(struct {
				Thread             string
				Private            bool
				Code, Name, Region string
			}{threadFingerprint(t), co.IncludePrivate, co.Code, co.Name, co.region()})
			if force || !exists || old.Fingerprint != fingerprint || now.Sub(old.CheckedAt) >= time.Duration(cfg.FullRefreshHours)*time.Hour {
				detail, users, err := c.thread(ctx, t.ID)
				if err != nil {
					return result, err
				}
				if detail.ID != t.ID || detail.CourseID != co.ID {
					return result, fmt.Errorf("thread identity mismatch")
				}
				if detail.IsPrivate && !co.IncludePrivate {
					continue
				}
				r, err := threadRecord(detail, users, co)
				if err != nil {
					return result, fmt.Errorf("thread %d: %w", t.ID, err)
				}
				result.DetailsFetched++
				if !exists || recordFingerprint(old.Record) != recordFingerprint(r) {
					action := "updated"
					if !exists {
						action = "new"
					}
					s.Changes = append(s.Changes, change{DetectedAt: now, Course: co.Code, Kind: "thread", Action: action, ID: t.ID, Number: t.Number, Title: detail.Title, URL: fmt.Sprint(r["url"]), Staff: hasStaffChange(old.Record, r), Private: detail.IsPrivate})
				}
				old = threadCache{fingerprint, now, r}
			}
			next[t.ID] = old
			records = append(records, old.Record)
		}
		for id, old := range s.Threads {
			if _, ok := next[id]; !ok {
				s.Changes = append(s.Changes, change{DetectedAt: now, Course: co.Code, Kind: "thread", Action: "unavailable", ID: id, Title: fmt.Sprint(old.Record["title"]), URL: fmt.Sprint(old.Record["url"]), Private: old.Record["is_private"] == true})
			}
		}
		s.Threads = next
		var jsonl bytes.Buffer
		enc := json.NewEncoder(&jsonl)
		enc.SetEscapeHTML(false)
		for _, r := range records {
			if err := enc.Encode(r); err != nil {
				return result, err
			}
		}
		files["_discussion.jsonl"] = jsonl.Bytes()
		files["_discussion.md"] = []byte(discussionMarkdown(co, records))
	}
	if co.Lessons {
		if _, err := syncLessons(ctx, c, co, &s, files, now, force); err != nil {
			return result, err
		}
	}
	result.Threads = len(s.Threads)
	result.Lessons = len(s.Lessons)
	result.Changes = len(s.Changes) - before
	if baseline {
		for i := before; i < len(s.Changes); i++ {
			s.Changes[i].Action = "baseline"
		}
	}
	s.LastSuccess = now
	files["_updates.md"] = []byte(updatesMarkdown(co, s.Changes))
	if err := writeOutputs(co, &s, files); err != nil {
		return result, err
	}
	return result, nil
}

func syncAll(ctx context.Context, c *edClient, cfg syncConfig, force bool) (syncResult, error) {
	r := syncResult{Courses: []courseResult{}}
	var errs []error
	for _, co := range cfg.Courses {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		fmt.Fprintf(os.Stderr, "syncing %s...\n", co.Code)
		x, err := syncCourse(ctx, c, cfg, co, force)
		if err != nil {
			x.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", co.Code, err))
		}
		r.Courses = append(r.Courses, x)
		fmt.Fprintf(os.Stderr, "%s: threads=%d lessons=%d changes=%d error=%s\n", co.Code, x.Threads, x.Lessons, x.Changes, x.Error)
	}
	return r, errors.Join(errs...)
}

func updatesMarkdown(co courseConfig, changes []change) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# %s — Ed updates\n\nTimes are detection times (UTC). First sync creates a baseline.\nUnavailable means deleted, hidden, or no longer visible; old lesson files are retained.\n\n", co.Code)
	for i := len(changes) - 1; i >= 0; i-- {
		e := changes[i]
		if e.Private && !co.IncludePrivate {
			continue
		}
		fmt.Fprintf(&b, "- %s · %s %s · #%d [%s](%s)\n", e.DetectedAt.Format(time.RFC3339), e.Kind, e.Action, e.Number, mdEscape(e.Title), e.URL)
	}
	return b.String()
}

type newsInput struct {
	Since     string `json:"since,omitempty" jsonschema:"RFC3339 time; defaults to the last 7 days"`
	Course    string `json:"course,omitempty" jsonschema:"Course code; omit for all configured courses"`
	StaffOnly bool   `json:"staff_only,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type newsResult struct {
	Changes     []change             `json:"changes"`
	LastSuccess map[string]time.Time `json:"last_success"`
}

func whatsNew(cfg syncConfig, in newsInput) (newsResult, error) {
	r := newsResult{Changes: []change{}, LastSuccess: map[string]time.Time{}}
	since := time.Now().Add(-7 * 24 * time.Hour)
	if in.Since != "" {
		var err error
		since, err = time.Parse(time.RFC3339, in.Since)
		if err != nil {
			return r, fmt.Errorf("since must be RFC3339")
		}
	}
	if in.Limit == 0 {
		in.Limit = 100
	}
	if in.Limit < 1 || in.Limit > 1000 {
		return r, fmt.Errorf("limit must be 1..1000")
	}
	found := false
	for _, co := range cfg.Courses {
		if in.Course != "" && in.Course != co.Code {
			continue
		}
		found = true
		s, err := readState(co)
		if err != nil {
			return r, err
		}
		r.LastSuccess[co.Code] = s.LastSuccess
		for _, e := range s.Changes {
			if e.Private && !cfg.Privacy.IncludePrivatePosts {
				continue
			}
			if e.Action != "baseline" && e.DetectedAt.After(since) && (!in.StaffOnly || e.Staff) {
				r.Changes = append(r.Changes, e)
			}
		}
	}
	if !found {
		return r, fmt.Errorf("unknown course code")
	}
	slices.SortStableFunc(r.Changes, func(a, b change) int { return b.DetectedAt.Compare(a.DetectedAt) })
	if len(r.Changes) > in.Limit {
		r.Changes = r.Changes[:in.Limit]
	}
	return r, nil
}

func hasStaffChange(old, current record) bool {
	if current["author_is_staff"] == true {
		return true
	}
	decode := func(r record) []record {
		var rows []record
		b, _ := json.Marshal(r["replies"])
		_ = json.Unmarshal(b, &rows)
		return rows
	}
	previous := map[string]string{}
	for _, r := range decode(old) {
		previous[fmt.Sprint(r["id"])] = jsonHash(r)
	}
	for _, r := range decode(current) {
		if r["author_is_staff"] == true && previous[fmt.Sprint(r["id"])] != jsonHash(r) {
			return true
		}
	}
	return false
}
