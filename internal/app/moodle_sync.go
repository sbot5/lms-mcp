package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sbot5/lms-mcp/internal/moodle"
	"github.com/sbot5/lms-mcp/internal/render"
)

type moodleFileState struct {
	Path              string `json:"path"`
	Hash              string `json:"hash"`
	URL               string `json:"url"`
	Name              string `json:"name"`
	ETag              string `json:"etag,omitempty"`
	Modified          string `json:"last_modified,omitempty"`
	RemoteFingerprint string `json:"remote_fingerprint"`
}

func syncMoodleCourse(ctx context.Context, c *moodle.Client, cfg moodleConfig, course moodleCourse, force bool) (courseResult, error) {
	co := course.storage(cfg)
	result := courseResult{Course: co.Code}
	lock, err := lockCourse(co)
	if err != nil {
		return result, fmt.Errorf("Moodle output lock unavailable: %w", err)
	}
	defer lock.Close()
	s, err := readState(co)
	if err != nil {
		return result, err
	}
	pending, err := readPending(co)
	if err != nil {
		return result, err
	}
	var items []moodle.Material
	if c.AuthMode() == "token" {
		items, err = c.APIMaterials(ctx, course.ID)
	} else {
		items, err = c.CookieMaterials(ctx, course.ID)
	}
	if err != nil {
		return result, err
	}
	if len(items) == 0 && len(s.MoodleFiles) > 0 {
		return result, fmt.Errorf("Moodle returned no material files; preserving previous checkpoint for access/theme verification")
	}
	slices.SortFunc(items, func(a, b moodle.Material) int { return strings.Compare(a.Key, b.Key) })
	next := map[string]moodleFileState{}
	files := map[string][]byte{}
	now := time.Now().UTC()
	before := len(s.Changes)
	stagedBytes := 0
	var index strings.Builder
	fmt.Fprintf(&index, "# %s — Moodle materials\n\n", course.Code)
	for _, item := range items {
		if _, duplicate := next[item.Key]; duplicate {
			continue
		}
		rel := filepath.Join("files", digest([]byte(item.Key))[:16]+"-"+safeName(item.Name, 90))
		old, exists := s.MoodleFiles[item.Key]
		path, err := safeTarget(co.Directory, rel)
		if err != nil {
			return result, err
		}
		local, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			return result, readErr
		}
		if readErr == nil && s.Files[rel] != "" && digest(local) != s.Files[rel] && !slices.Contains(pending[rel], digest(local)) {
			return result, fmt.Errorf("local Moodle file edit conflict: %s", path)
		}
		fingerprint := jsonHash(item)
		cached := exists && readErr == nil && digest(local) == old.Hash
		var response moodle.Response
		if !force && cached && c.AuthMode() == "token" && item.Modified > 0 && old.RemoteFingerprint == fingerprint {
			response = moodle.Response{Body: local, ETag: old.ETag, Modified: old.Modified}
		} else {
			etag, modified := "", ""
			if !force && cached && old.RemoteFingerprint == fingerprint {
				etag = old.ETag
				modified = old.Modified
			}
			response, err = c.Request(ctx, "GET", item.URL, "", etag, modified, true)
			if err != nil {
				return result, err
			}
			if response.NotModified {
				if !cached {
					return result, fmt.Errorf("Moodle returned 304 without a valid local file")
				}
				response = moodle.Response{Body: local, ETag: old.ETag, Modified: old.Modified}
			} else {
				result.Downloads++
			}
		}
		if moodle.LooksLikeLogin(response.Body) {
			return result, fmt.Errorf("Moodle returned a login form instead of a file")
		}
		var apiError struct {
			Exception string `json:"exception"`
			ErrorCode string `json:"errorcode"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(response.Body, &apiError) == nil && apiError.ErrorCode != "" && (apiError.Exception != "" || apiError.Error != "") {
			return result, fmt.Errorf("Moodle returned a service error instead of a file (details redacted)")
		}
		// Files may be HTML resources, but a generic Moodle error page is never a material.
		if strings.Contains(strings.ToLower(string(response.Body[:min(len(response.Body), 8192)])), "page-error") {
			return result, fmt.Errorf("Moodle returned an error page instead of a file")
		}
		hash := digest(response.Body)
		stagedBytes += len(response.Body)
		if stagedBytes > 512<<20 {
			return result, fmt.Errorf("Moodle course exceeds the 512 MiB staging limit; split the export before retrying")
		}
		files[rel] = response.Body
		next[item.Key] = moodleFileState{rel, hash, moodle.CleanURL(item.URL), item.Name, response.ETag, response.Modified, fingerprint}
		if !exists || old.Hash != hash || old.Path != rel {
			action := "updated"
			if !exists {
				action = "new"
			}
			s.Changes = append(s.Changes, change{DetectedAt: now, Course: co.Code, Kind: "material", Action: action, Title: item.Name, URL: moodle.CleanURL(item.URL), Staff: true})
		}
		fmt.Fprintf(&index, "- [%s](%s) — %s\n", render.Escape(item.Name), render.URL(filepath.ToSlash(rel)), render.Escape(item.Section))
	}
	for key, old := range s.MoodleFiles {
		if _, ok := next[key]; !ok {
			s.Changes = append(s.Changes, change{DetectedAt: now, Course: co.Code, Kind: "material", Action: "unavailable", Title: old.Name, URL: old.URL, Staff: true})
		}
	}
	if course.CalendarEnv != "" {
		events, err := c.Calendar(ctx, course.CalendarEnv)
		if err != nil {
			return result, err
		}
		data, err := json.MarshalIndent(events, "", "  ")
		if err != nil {
			return result, err
		}
		files["_calendar.json"] = data
		files["_calendar.md"] = []byte(calendarMarkdown(events))
		if s.CalendarHash != digest(data) {
			s.Changes = append(s.Changes, change{DetectedAt: now, Course: co.Code, Kind: "calendar", Action: "updated", Title: "Calendar events changed", Staff: true})
		}
		s.CalendarHash = digest(data)
		fmt.Fprint(&index, "\n[Calendar](_calendar.md)\n")
	}
	if s.LastSuccess.IsZero() {
		for i := before; i < len(s.Changes); i++ {
			s.Changes[i].Action = "baseline"
		}
	}
	s.MoodleFiles = next
	s.LastSuccess = now
	result.Materials = len(next)
	result.Changes = len(s.Changes) - before
	files["_index.md"] = []byte(index.String())
	files["_updates.md"] = []byte(updatesMarkdown(co, s.Changes))
	if err = writeOutputs(co, &s, files); err != nil {
		return result, err
	}
	return result, nil
}
