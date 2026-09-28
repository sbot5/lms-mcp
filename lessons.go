package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type edModule struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
type edLesson struct {
	ID       int       `json:"id"`
	CourseID int       `json:"course_id"`
	ModuleID int       `json:"module_id"`
	Index    int       `json:"index"`
	Title    string    `json:"title"`
	IsHidden bool      `json:"is_hidden"`
	Slides   []edSlide `json:"slides"`
}
type edSlide struct {
	ID          int    `json:"id"`
	Index       int    `json:"index"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	IsHidden    bool   `json:"is_hidden"`
	Content     string `json:"content"`
	FileURL     string `json:"file_url"`
	VideoURL    string `json:"video_url"`
	URL         string `json:"url"`
	ChallengeID int    `json:"challenge_id"`
}
type edQuestion struct {
	Data struct {
		Type        string   `json:"type"`
		Content     string   `json:"content"`
		Answers     []string `json:"answers"`
		Explanation string   `json:"explanation"`
	} `json:"data"`
}

func allowedAsset(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") && (h == "edusercontent.com" || strings.HasSuffix(h, ".edusercontent.com"))
}

// Assets use a separate client: Ed's API token must never reach a content host.
func downloadAsset(ctx context.Context, raw string) ([]byte, error) {
	if !allowedAsset(raw) {
		return nil, fmt.Errorf("untrusted attachment host")
	}
	c := http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedAsset(req.URL.String()) {
			return fmt.Errorf("attachment redirect refused")
		}
		return nil
	}}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
		if err != nil {
			return nil, err
		}
		r, err := c.Do(req)
		if err != nil {
			return nil, fmt.Errorf("attachment request failed")
		}
		if r.StatusCode == 429 || r.StatusCode >= 500 {
			r.Body.Close()
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Second * time.Duration(1<<attempt)):
				}
				continue
			}
		}
		if r.StatusCode != 200 {
			r.Body.Close()
			return nil, fmt.Errorf("attachment HTTP %d", r.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, (64<<20)+1))
		r.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(b) > 64<<20 {
			return nil, fmt.Errorf("attachment exceeds 64 MiB")
		}
		return b, nil
	}
	return nil, fmt.Errorf("attachment retry exhausted")
}

func collectAssets(content string, refs map[string]string) error {
	n, err := parseEdXML(content)
	if err != nil {
		return err
	}
	var walk func(*xmlNode)
	walk = func(n *xmlNode) {
		if n.tag == "image" || n.tag == "file" {
			u := n.attrs["url"]
			if u == "" {
				u = n.attrs["src"]
			}
			if u == "" {
				u = n.attrs["href"]
			}
			if allowedAsset(u) {
				refs[u] = n.attrs["filename"]
			}
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(n)
	return nil
}

func syncLessons(ctx context.Context, c *edClient, co courseConfig, s *syncState, files map[string][]byte, now time.Time, refreshAssets bool) (int, error) {
	pending, err := readPending(co)
	if err != nil {
		return 0, err
	}
	var list struct {
		Lessons []edLesson `json:"lessons"`
		Modules []edModule `json:"modules"`
	}
	if err := c.get(ctx, fmt.Sprintf("/courses/%d/lessons", co.ID), &list); err != nil {
		return 0, err
	}
	if list.Lessons == nil || list.Modules == nil {
		return 0, fmt.Errorf("invalid lessons list response")
	}
	modules := map[int]string{}
	for i, m := range list.Modules {
		modules[m.ID] = fmt.Sprintf("%02d - %s", i+1, safeName(strings.TrimLeft(m.Name, " -–—·•_"), 60))
	}
	slices.SortFunc(list.Lessons, func(a, b edLesson) int {
		if a.ModuleID != b.ModuleID {
			return a.ModuleID - b.ModuleID
		}
		if a.Index != b.Index {
			return a.Index - b.Index
		}
		return a.ID - b.ID
	})
	var idx strings.Builder
	fmt.Fprintf(&idx, "# %s — Ed Lessons\n\nAutomatically synchronized. Existing course materials are kept separately.\n\n", co.Code)
	next := map[int]lessonCache{}
	updates := 0
	for _, meta := range list.Lessons {
		if meta.IsHidden {
			continue
		}
		var res struct {
			Lesson edLesson `json:"lesson"`
		}
		if err := c.get(ctx, fmt.Sprintf("/lessons/%d", meta.ID), &res); err != nil {
			return 0, err
		}
		l := res.Lesson
		if l.ID != meta.ID || l.CourseID != co.ID {
			return 0, fmt.Errorf("lesson identity mismatch for %d", meta.ID)
		}
		if l.IsHidden {
			continue
		}
		refs := map[string]string{}
		challenges := map[int]string{}
		questions := map[int][]edQuestion{}
		for _, slide := range l.Slides {
			if slide.IsHidden {
				continue
			}
			if err := collectAssets(slide.Content, refs); err != nil {
				return 0, err
			}
			if allowedAsset(slide.FileURL) {
				refs[slide.FileURL] = slide.Title + ".pdf"
			}
			if slide.Type == "code" && slide.ChallengeID > 0 {
				var r struct {
					Challenge struct {
						Content string `json:"content"`
					} `json:"challenge"`
				}
				if err := c.get(ctx, fmt.Sprintf("/challenges/%d", slide.ChallengeID), &r); err != nil {
					return 0, err
				}
				challenges[slide.ID] = r.Challenge.Content
				if err := collectAssets(r.Challenge.Content, refs); err != nil {
					return 0, err
				}
			}
			if slide.Type == "quiz" {
				var r struct {
					Questions []edQuestion `json:"questions"`
				}
				if err := c.get(ctx, fmt.Sprintf("/lessons/slides/%d/questions", slide.ID), &r); err != nil {
					return 0, err
				}
				questions[slide.ID] = r.Questions
				for _, q := range r.Questions {
					for _, src := range append([]string{q.Data.Content, q.Data.Explanation}, q.Data.Answers...) {
						if err := collectAssets(src, refs); err != nil {
							return 0, err
						}
					}
				}
			}
		}
		assets := map[string]string{}
		assetHashes := map[string]string{}
		for raw, name := range refs {
			if !enabled(co.DownloadAttachments) {
				continue
			}
			u, _ := url.Parse(raw)
			if name == "" {
				name = filepath.Base(u.Path)
			}
			name = digest([]byte(raw))[:20] + "-" + safeName(name, 60)
			rel := filepath.Join("ed-lessons", "_assets", name)
			assets[raw] = name
			if existing, ok := files[rel]; ok {
				assetHashes[raw] = digest(existing)
				continue
			}
			path, err := safeTarget(co.Directory, rel)
			if err != nil {
				return 0, err
			}
			b, err := os.ReadFile(path)
			if err == nil {
				if expected := s.Files[rel]; expected != "" && digest(b) != expected && !slices.Contains(pending[rel], digest(b)) {
					return 0, fmt.Errorf("local attachment edit conflict: %s", path)
				}
				if !refreshAssets {
					files[rel] = b
					assetHashes[raw] = digest(b)
					continue
				}
			}
			if err != nil && !os.IsNotExist(err) {
				return 0, err
			}
			b, err = downloadAsset(ctx, raw)
			if err != nil {
				return 0, fmt.Errorf("lesson %d attachment: %w", l.ID, err)
			}
			files[rel] = b
			assetHashes[raw] = digest(b)
		}
		slices.SortFunc(l.Slides, func(a, b edSlide) int { return a.Index - b.Index })
		var body strings.Builder
		url := co.url("lessons", l.ID)
		fmt.Fprintf(&body, "---\ncourse_id: %d\nlesson_id: %d\nurl: %q\n---\n\n# %s\n", co.ID, l.ID, url, l.Title)
		for _, slide := range l.Slides {
			if slide.IsHidden {
				continue
			}
			fmt.Fprintf(&body, "\n## %s\n\n", slide.Title)
			writeXML := func(src string) error {
				md, _, err := renderBody(src, "", assets)
				if err == nil {
					body.WriteString(md + "\n\n")
				}
				return err
			}
			if err := writeXML(slide.Content); err != nil {
				return 0, err
			}
			switch slide.Type {
			case "pdf":
				target := slide.FileURL
				if local, ok := assets[target]; ok {
					target = "../_assets/" + local
				}
				fmt.Fprintf(&body, "[PDF](%s)\n", mdURL(target))
			case "video":
				fmt.Fprintf(&body, "[Video](%s)\n", mdURL(slide.VideoURL))
			case "webpage":
				fmt.Fprintf(&body, "[Webpage](%s)\n", mdURL(slide.URL))
			case "code":
				if err := writeXML(challenges[slide.ID]); err != nil {
					return 0, err
				}
			case "quiz":
				for i, q := range questions[slide.ID] {
					fmt.Fprintf(&body, "### Question %d\n\n", i+1)
					if err := writeXML(q.Data.Content); err != nil {
						return 0, err
					}
					for j, a := range q.Data.Answers {
						fmt.Fprintf(&body, "**%c.** ", 'A'+j)
						if err := writeXML(a); err != nil {
							return 0, err
						}
					}
					if err := writeXML(q.Data.Explanation); err != nil {
						return 0, err
					}
				}
			default:
				if slide.Type != "document" {
					fmt.Fprintf(&body, "[Open %s slide in Ed](%s/slides/%d)\n", mdEscape(slide.Type), url, slide.ID)
				}
			}
		}
		mod := modules[meta.ModuleID]
		if mod == "" {
			mod = "00 - Ungrouped"
		}
		rel := filepath.Join("ed-lessons", mod, fmt.Sprintf("%s [%d].md", safeName(l.Title, 80), l.ID))
		data := []byte(body.String())
		files[rel] = data
		next[l.ID] = lessonCache{digest(data), rel, l.Title, assetHashes}
		old, exists := s.Lessons[l.ID]
		if !exists || old.Hash != digest(data) || old.Path != rel || (old.Assets != nil && jsonHash(old.Assets) != jsonHash(assetHashes)) {
			action := "updated"
			if !exists {
				action = "new"
			}
			s.Changes = append(s.Changes, change{DetectedAt: now, Course: co.Code, Kind: "lesson", Action: action, ID: l.ID, Title: l.Title, URL: url, Staff: true})
			updates++
		}
		fmt.Fprintf(&idx, "- [%s](%s)\n", mdEscape(l.Title), mdURL(filepath.ToSlash(strings.TrimPrefix(rel, "ed-lessons"+string(filepath.Separator)))))
	}
	for id, old := range s.Lessons {
		if _, ok := next[id]; !ok {
			s.Changes = append(s.Changes, change{DetectedAt: now, Course: co.Code, Kind: "lesson", Action: "unavailable", ID: id, Title: old.Title})
			updates++
		}
	}
	s.Lessons = next
	files[filepath.Join("ed-lessons", "_index.md")] = []byte(idx.String())
	return updates, nil
}
