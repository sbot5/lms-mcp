package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sbot5/lms-mcp/internal/store"
)

type freshnessOut struct {
	Course      string `json:"course"`
	LastSuccess int64  `json:"last_success,omitempty"`
	Synced      bool   `json:"synced"`
	Stale       bool   `json:"stale"`
}

// All query results are object-rooted and carry cache freshness. The flexible
// item objects contain an explicit public field selection, never raw DB rows.
type contentOut struct {
	Items        []map[string]any `json:"items"`
	Item         map[string]any   `json:"item,omitempty"`
	Text         *string          `json:"text,omitempty"`
	Total        int              `json:"total,omitempty"`
	HasMore      bool             `json:"has_more"`
	NextCursor   string           `json:"next_cursor,omitempty"`
	NextOffset   int              `json:"next_offset,omitempty"`
	Freshness    []freshnessOut   `json:"freshness"`
	Availability string           `json:"availability,omitempty"`
	Truncated    bool             `json:"truncated"`
}

type contentListIn struct {
	Course           string   `json:"course,omitempty" jsonschema:"Local course ID returned by list_courses; never an Ed numeric course ID."`
	Limit            int      `json:"limit,omitempty"`
	Cursor           string   `json:"cursor,omitempty"`
	Since            int64    `json:"since,omitempty" jsonschema:"Unix seconds."`
	Category         string   `json:"category,omitempty"`
	Type             string   `json:"type,omitempty"`
	Staff            bool     `json:"staff,omitempty"`
	StaffOnly        bool     `json:"staff_only,omitempty"`
	Mine             bool     `json:"mine,omitempty"`
	Own              bool     `json:"own,omitempty"`
	Unanswered       bool     `json:"unanswered,omitempty"`
	Kinds            []string `json:"kinds,omitempty"`
	IncludeBaseline  bool     `json:"include_baseline,omitempty"`
	IncludeCompleted bool     `json:"include_completed,omitempty"`
	Days             int      `json:"days,omitempty"`
}

type contentItemIn struct {
	ItemID string `json:"item_id" jsonschema:"Provider-qualified item ID from a list tool."`
	Course string `json:"course,omitempty"`
}

type readMaterialIn struct {
	ItemID   string `json:"item_id"`
	Course   string `json:"course,omitempty"`
	FileID   string `json:"file_id,omitempty" jsonschema:"Optional indexed attachment ID; file paths are not accepted."`
	Offset   int    `json:"offset,omitempty" jsonschema:"Zero-based character offset."`
	MaxChars int    `json:"max_chars,omitempty"`
}

func (d Deps) registerContent(s *mcp.Server, annotations func(string) *mcp.ToolAnnotations) {
	mcp.AddTool(s, &mcp.Tool{Name: "list_posts", Description: "List local Ed discussion posts and announcements. Filter by local course ID, category, type, staff, unanswered or mine; replies are returned by get_post. Uses limit/cursor and never contacts Ed.", Annotations: annotations("List posts")}, d.listPosts)
	mcp.AddTool(s, &mcp.Tool{Name: "get_post", Description: "Read a local Ed post and its nested reply tree as Markdown, with cache freshness. Takes item_id from list_posts; never contacts Ed.", Annotations: annotations("Read post")}, d.getPost)
	mcp.AddTool(s, &mcp.Tool{Name: "list_materials", Description: "List local lecture materials, slides, lessons, resources and attachment indexes (课件). Filter by local course ID or kinds; uses limit/cursor and never contacts Ed.", Annotations: annotations("List materials")}, d.listMaterials)
	mcp.AddTool(s, &mcp.Tool{Name: "read_material", Description: "Read local lesson Markdown or a verified plain-text attachment (课件). Takes indexed item_id and optional file_id, offset/max_chars; binary document extraction is not available yet. Never accepts paths or contacts Ed.", Annotations: annotations("Read material")}, d.readMaterial)
	mcp.AddTool(s, &mcp.Tool{Name: "whats_new", Description: "List local content changes since Unix seconds, excluding initial-import baseline events by default. Filter by course, kinds or staff_only; uses limit/cursor and never contacts Ed.", Annotations: annotations("Recent changes")}, d.whatsNew)
	mcp.AddTool(s, &mcp.Tool{Name: "upcoming_deadlines", Description: "List local Ed assessment deadlines in the next days (default 7, max 365), retaining recorded submission/completion states. Filter by course and include_completed; uses limit/cursor.", Annotations: annotations("Upcoming deadlines")}, d.upcomingDeadlines)
	mcp.AddTool(s, &mcp.Tool{Name: "get_assessment", Description: "Read local Ed lesson/slide assessment requirements, own recorded responses, released answers, grades/marks, feedback, attachments and deadlines. Missing data is reported explicitly; takes item_id and never submits or refreshes.", Annotations: annotations("Assessment details")}, d.getAssessment)
}

func contentCursor(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	if len(raw) > 256 {
		return 0, fmt.Errorf("invalid cursor")
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	var obj map[string]json.RawMessage
	if err != nil || json.Unmarshal(b, &obj) != nil || len(obj) != 1 {
		return 0, fmt.Errorf("invalid cursor")
	}
	var n int
	if json.Unmarshal(obj["o"], &n) != nil || n <= 0 || EncodeCursor(n) != raw {
		return 0, fmt.Errorf("invalid cursor")
	}
	return n, nil
}

func (d Deps) checkCourse(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > 512 {
		return fmt.Errorf("invalid local course ID")
	}
	courses, err := d.DB.ListCourses()
	if err != nil {
		return fmt.Errorf("local course index unavailable")
	}
	for _, c := range courses {
		if c.ID == id {
			return nil
		}
	}
	return fmt.Errorf("unknown local course ID; use list_courses")
}

func (d Deps) freshness(ids []string) ([]freshnessOut, error) {
	out := make([]freshnessOut, 0, len(ids))
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		v, ok, err := d.DB.GetMeta("ed:last_success:" + id)
		if err != nil {
			return nil, fmt.Errorf("cache freshness unavailable")
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		out = append(out, freshnessOut{Course: id, LastSuccess: max(n, 0), Synced: ok && n > 0, Stale: n <= 0 || time.Now().Unix()-n > 3600})
	}
	return out, nil
}

func (d Deps) page(entries []map[string]any, in contentListIn) (contentOut, error) {
	offset, err := contentCursor(in.Cursor)
	if err != nil {
		return contentOut{}, err
	}
	if err := d.checkCourse(in.Course); err != nil {
		return contentOut{}, err
	}
	lo, hi, _ := Paginate(len(entries), offset, in.Limit)
	out := contentOut{Items: make([]map[string]any, 0), Freshness: make([]freshnessOut, 0), Total: len(entries)}
	ids := []string{}
	if in.Course != "" {
		ids = append(ids, in.Course)
	} else {
		courses, e := d.DB.ListCourses()
		if e != nil {
			return contentOut{}, fmt.Errorf("local course index unavailable")
		}
		for _, course := range courses {
			if course.EdCourseID != 0 {
				ids = append(ids, course.ID)
			}
		}
	}
	out.Freshness, err = d.freshness(ids)
	if err != nil {
		return contentOut{}, err
	}
	end := lo
	for i := lo; i < hi; i++ {
		candidate := out
		candidate.NextCursor = ""
		candidate.Items = append(slices.Clone(out.Items), entries[i])
		candidateIDs := slices.Clone(ids)
		if course, ok := entries[i]["course"].(string); ok {
			candidateIDs = append(candidateIDs, course)
		}
		candidate.Freshness, err = d.freshness(candidateIDs)
		if err != nil {
			return contentOut{}, err
		}
		candidate.HasMore = i+1 < len(entries)
		if candidate.HasMore {
			candidate.NextCursor = EncodeCursor(i + 1)
		}
		b, _ := json.Marshal(candidate)
		if len(b) > BudgetChars && len(out.Items) > 0 {
			out.Truncated = true
			break
		}
		if err := boundContent(&candidate); err != nil {
			return contentOut{}, err
		}
		out, ids, end = candidate, candidateIDs, i+1
	}
	out.HasMore = end < len(entries)
	out.NextCursor = ""
	if out.HasMore {
		out.NextCursor = EncodeCursor(end)
	}
	err = boundContent(&out)
	return out, err
}

func staffRole(role string) bool {
	switch strings.ToLower(role) {
	case "staff", "ta", "tutor", "instructor", "lecturer", "teacher", "admin":
		return true
	}
	return false
}

func publicURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return ""
	}
	// Signed attachment queries and session keys are never part of tool output.
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

func safeMetadata(raw string) (map[string]any, bool) {
	var input map[string]any
	if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &input) != nil || input == nil {
		return map[string]any{}, false
	}
	out := map[string]any{}
	for _, key := range []string{"category", "subcategory", "type", "is_own", "is_answered", "is_private", "number", "responses", "submissions", "released_answers", "availability", "content_unavailable"} {
		if value, ok := input[key]; ok {
			out[key] = safeMetaValue(value, 0)
		}
	}
	return out, true
}

func safeMetaValue(value any, depth int) any {
	if depth > 16 {
		return nil
	}
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for _, key := range []string{"id", "slide_id", "question_id", "type", "response", "answer", "value", "content", "text", "code", "language", "status", "grade", "score", "feedback", "created_at", "updated_at", "choices", "correct", "result", "submitted_at", "state", "openable", "content_unavailable", "opens_at", "due_at", "cutoff_at", "closed_at", "released_at", "available_at", "started_at", "finished_at", "schedule", "enabled", "title", "body_md", "question", "options", "is_correct", "max_score", "mark", "marks", "grade_max"} {
			if child, ok := v[key]; ok {
				out[key] = safeMetaValue(child, depth+1)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, child := range v {
			out = append(out, safeMetaValue(child, depth+1))
		}
		return out
	default:
		return value
	}
}

func itemEntry(it store.Item, body bool) map[string]any {
	metadata, ok := safeMetadata(it.MetaJSON)
	author := "Student"
	if staffRole(it.AuthorRole) {
		author = it.AuthorDisplay
	}
	if own, _ := metadata["is_own"].(bool); own {
		author = "You"
	}
	out := map[string]any{"id": it.ID, "course": it.CourseID, "kind": it.Kind, "title": it.Title, "url": publicURL(it.URL), "parent_id": it.ParentID, "author_role": it.AuthorRole, "author": author, "created_at": it.CreatedAt, "updated_at": it.UpdatedAt, "metadata": metadata, "metadata_available": ok}
	if body {
		out["body_md"] = it.BodyMD
	}
	return out
}

func (d Deps) listPosts(_ context.Context, _ *mcp.CallToolRequest, in contentListIn) (*mcp.CallToolResult, contentOut, error) {
	if err := d.checkCourse(in.Course); err != nil {
		return nil, contentOut{}, err
	}
	rows, err := d.DB.ListItems(store.ItemFilter{CourseID: in.Course, Provider: "ed", Kinds: []string{"thread", "announcement"}, Since: in.Since})
	if err != nil {
		return nil, contentOut{}, fmt.Errorf("local posts unavailable")
	}
	entries := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		metadata, _ := safeMetadata(it.MetaJSON)
		own, _ := metadata["is_own"].(bool)
		answered, answeredKnown := metadata["is_answered"].(bool)
		if (in.Staff || in.StaffOnly) && !staffRole(it.AuthorRole) || (in.Mine || in.Own) && !own || in.Unanswered && (!answeredKnown || answered) {
			continue
		}
		if in.Category != "" && metadata["category"] != in.Category {
			continue
		}
		if in.Type != "" && metadata["type"] != in.Type && it.Kind != in.Type {
			continue
		}
		entries = append(entries, itemEntry(it, false))
	}
	out, err := d.page(entries, in)
	return nil, out, err
}

func (d Deps) contentItem(in contentItemIn, kinds []string) (store.Item, error) {
	if in.ItemID == "" || len(in.ItemID) > 512 {
		return store.Item{}, fmt.Errorf("invalid item_id")
	}
	if err := d.checkCourse(in.Course); err != nil {
		return store.Item{}, err
	}
	it, ok, err := d.DB.GetItem(in.ItemID)
	if err != nil || !ok || it.RemovedAt != 0 || it.Provider != "ed" || !slices.Contains(kinds, it.Kind) || (in.Course != "" && it.CourseID != in.Course) {
		return store.Item{}, fmt.Errorf("item not available in the requested local Ed index")
	}
	return it, nil
}

func (d Deps) detail(it store.Item) (contentOut, error) {
	fresh, err := d.freshness([]string{it.CourseID})
	availability := "available"
	if len(fresh) > 0 && !fresh[0].Synced {
		availability = "not_synced"
	}
	metadata, _ := safeMetadata(it.MetaJSON)
	if state, ok := metadata["availability"].(map[string]any); ok {
		if value, ok := state["state"].(string); ok && value != "" {
			availability = value
		}
	}
	if unavailable, _ := metadata["content_unavailable"].(bool); unavailable {
		availability = "content_unavailable"
	}
	return contentOut{Items: make([]map[string]any, 0), Item: itemEntry(it, true), Freshness: fresh, Availability: availability}, err
}

func (d Deps) getPost(_ context.Context, _ *mcp.CallToolRequest, in contentItemIn) (*mcp.CallToolResult, contentOut, error) {
	it, err := d.contentItem(in, []string{"thread", "announcement", "reply"})
	if err != nil {
		return nil, contentOut{}, err
	}
	out, err := d.detail(it)
	if err != nil {
		return nil, out, err
	}
	rows, err := d.DB.ListItems(store.ItemFilter{CourseID: it.CourseID, Provider: "ed", Kinds: []string{"reply"}})
	if err != nil {
		return nil, out, fmt.Errorf("local replies unavailable")
	}
	children := map[string][]store.Item{}
	for _, child := range rows {
		children[child.ParentID] = append(children[child.ParentID], child)
	}
	seen := map[string]bool{it.ID: true}
	var tree func(string, int) []any
	tree = func(parent string, depth int) []any {
		replies := make([]any, 0)
		if depth >= 32 {
			out.Truncated = len(children[parent]) > 0 || out.Truncated
			return replies
		}
		for _, child := range children[parent] {
			if seen[child.ID] {
				out.Truncated = true
				continue
			}
			seen[child.ID] = true
			entry := itemEntry(child, true)
			entry["replies"] = tree(child.ID, depth+1)
			replies = append(replies, entry)
		}
		return replies
	}
	out.Item["replies"] = tree(it.ID, 0)
	err = boundContent(&out)
	return nil, out, err
}

var materialKinds = []string{"lesson", "slide", "resource"}

func (d Deps) fileEntries(itemID string) ([]any, error) {
	files, err := d.DB.ListFiles(itemID)
	if err != nil {
		return nil, fmt.Errorf("local attachment index unavailable")
	}
	out := make([]any, 0, len(files))
	for _, f := range files {
		out = append(out, map[string]any{"id": f.ID, "item_id": f.ItemID, "name": f.Name, "mime": f.MIME, "size": f.Size, "status": f.Status, "url": publicURL(f.SourceURL)})
	}
	return out, nil
}

func (d Deps) listMaterials(_ context.Context, _ *mcp.CallToolRequest, in contentListIn) (*mcp.CallToolResult, contentOut, error) {
	if err := d.checkCourse(in.Course); err != nil {
		return nil, contentOut{}, err
	}
	kinds := in.Kinds
	if len(kinds) == 0 {
		kinds = materialKinds
	}
	for _, k := range kinds {
		if !slices.Contains(materialKinds, k) {
			return nil, contentOut{}, fmt.Errorf("material kinds must be lesson, slide or resource")
		}
	}
	rows, err := d.DB.ListItems(store.ItemFilter{CourseID: in.Course, Provider: "ed", Kinds: kinds, Since: in.Since})
	if err != nil {
		return nil, contentOut{}, fmt.Errorf("local materials unavailable")
	}
	entries := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		entry := itemEntry(it, false)
		entry["files"], err = d.fileEntries(it.ID)
		if err != nil {
			return nil, contentOut{}, err
		}
		entries = append(entries, entry)
	}
	out, err := d.page(entries, in)
	return nil, out, err
}

const maxLocalTextBytes = 16 << 20

func textAttachment(f store.File) bool {
	mime := strings.ToLower(strings.TrimSpace(strings.Split(f.MIME, ";")[0]))
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	if mime != "" && mime != "application/octet-stream" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(f.Name))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(f.LocalPath))
	}
	return ext == ".txt" || ext == ".md" || ext == ".csv" || ext == ".tsv"
}

func (d Deps) readAttachment(f store.File) (string, string, error) {
	if !textAttachment(f) {
		return "", "", fmt.Errorf("binary document extraction is not supported before M4")
	}
	if d.DataRoot == "" || f.Status != "ok" || f.LocalPath == "" || f.Size < 0 || f.Size > maxLocalTextBytes {
		return "", "", fmt.Errorf("verified plain-text attachment unavailable")
	}
	expected, err := hex.DecodeString(f.SHA256)
	if err != nil || len(expected) != sha256.Size {
		return "", "", fmt.Errorf("attachment checksum unavailable")
	}
	base, err := filepath.Abs(d.DataRoot)
	if err != nil {
		return "", "", fmt.Errorf("attachment root unavailable")
	}
	rootInfo, err := os.Lstat(base)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("attachment root unavailable")
	}
	rel := f.LocalPath
	if filepath.IsAbs(rel) {
		rel, err = filepath.Rel(base, rel)
	}
	if err != nil || !filepath.IsLocal(rel) || strings.Contains(rel, ":") {
		return "", "", fmt.Errorf("attachment path refused")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", "", fmt.Errorf("attachment root unavailable")
	}
	defer root.Close()
	part := ""
	for _, segment := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		part = filepath.Join(part, segment)
		info, e := root.Lstat(part)
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("attachment path refused")
		}
	}
	file, err := root.Open(rel)
	if err != nil {
		return "", "", fmt.Errorf("attachment unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != f.Size {
		return "", "", fmt.Errorf("attachment size mismatch")
	}
	b, err := io.ReadAll(io.LimitReader(file, maxLocalTextBytes+1))
	if err != nil || int64(len(b)) != f.Size {
		return "", "", fmt.Errorf("attachment size mismatch")
	}
	hash := sha256.Sum256(b)
	if !slices.Equal(hash[:], expected) {
		return "", "", fmt.Errorf("attachment checksum mismatch")
	}
	if !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return "", "", fmt.Errorf("attachment is not UTF-8 plain text")
	}
	return string(b), filepath.Join(base, rel), nil
}

func (d Deps) readMaterial(_ context.Context, _ *mcp.CallToolRequest, in readMaterialIn) (*mcp.CallToolResult, contentOut, error) {
	if in.Offset < 0 || in.MaxChars < 0 {
		return nil, contentOut{}, fmt.Errorf("offset and max_chars must be nonnegative")
	}
	it, err := d.contentItem(contentItemIn{ItemID: in.ItemID, Course: in.Course}, materialKinds)
	if err != nil {
		return nil, contentOut{}, err
	}
	out, err := d.detail(it)
	if err != nil {
		return nil, out, err
	}
	delete(out.Item, "body_md")
	text := it.BodyMD
	if text == "" || in.FileID != "" {
		files, e := d.DB.ListFiles(it.ID)
		if e != nil {
			return nil, out, fmt.Errorf("local attachments unavailable")
		}
		found := false
		for _, f := range files {
			if (in.FileID != "" && f.ID != in.FileID) || (in.FileID == "" && !textAttachment(f)) {
				continue
			}
			var path string
			text, path, err = d.readAttachment(f)
			if err != nil {
				return nil, out, err
			}
			out.Item["file_id"], out.Item["local_path"] = f.ID, path
			found = true
			break
		}
		if !found {
			if in.FileID != "" {
				return nil, out, fmt.Errorf("file_id does not belong to this material")
			}
			if out.Availability == "available" {
				out.Availability = "text_not_available; binary document extraction is not supported before M4"
			}
		}
	}
	runes := []rune(text)
	lo := min(in.Offset, len(runes))
	limit := in.MaxChars
	if limit == 0 {
		limit = 8000
	}
	page := string(runes[lo:min(lo+min(limit, BudgetChars), len(runes))])
	out.Text = &page
	out.NextOffset = lo + utf8.RuneCountInString(page)
	out.HasMore = out.NextOffset < len(runes)
	out.Truncated = out.HasMore
	if err := boundContent(&out); err != nil {
		return nil, out, err
	}
	out.NextOffset = lo + utf8.RuneCountInString(*out.Text)
	out.HasMore = out.NextOffset < len(runes)
	out.Truncated = out.Truncated || out.HasMore
	return nil, out, nil
}

func (d Deps) whatsNew(_ context.Context, _ *mcp.CallToolRequest, in contentListIn) (*mcp.CallToolResult, contentOut, error) {
	if err := d.checkCourse(in.Course); err != nil {
		return nil, contentOut{}, err
	}
	filter := store.EventFilter{CourseID: in.Course, Since: in.Since, Kinds: in.Kinds}
	if !in.IncludeBaseline {
		filter.Baseline = falsePtr()
	}
	rows, err := d.DB.ListEvents(filter)
	if err != nil {
		return nil, contentOut{}, fmt.Errorf("local events unavailable")
	}
	entries := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		it, ok, err := d.DB.GetItem(e.ItemID)
		if err != nil {
			return nil, contentOut{}, fmt.Errorf("local event item unavailable")
		}
		if ok && (it.Provider != "ed" || it.CourseID != e.CourseID) {
			continue
		}
		if (in.Staff || in.StaffOnly) && e.Kind != "staff_post" && e.Kind != "announcement" && (!ok || !staffRole(it.AuthorRole)) {
			continue
		}
		entries = append(entries, map[string]any{"id": e.ID, "course": e.CourseID, "item_id": e.ItemID, "kind": e.Kind, "detected_at": e.DetectedAt, "title": e.Title, "summary": e.Summary, "baseline": e.Baseline})
	}
	out, err := d.page(entries, in)
	return nil, out, err
}

func (d Deps) upcomingDeadlines(_ context.Context, _ *mcp.CallToolRequest, in contentListIn) (*mcp.CallToolResult, contentOut, error) {
	if err := d.checkCourse(in.Course); err != nil {
		return nil, contentOut{}, err
	}
	days := in.Days
	if days == 0 {
		days = 7
	}
	if days < 1 || days > 365 {
		return nil, contentOut{}, fmt.Errorf("days must be between 1 and 365")
	}
	start := time.Now().Unix()
	rows, err := d.DB.ListDeadlines(start, start+int64(days)*86400)
	if err != nil {
		return nil, contentOut{}, fmt.Errorf("local deadlines unavailable")
	}
	slices.SortFunc(rows, func(a, b store.Deadline) int {
		if a.DueAt != b.DueAt {
			if a.DueAt < b.DueAt {
				return -1
			}
			return 1
		}
		return strings.Compare(a.CourseID+"\x00"+a.ItemID+"\x00"+a.Kind, b.CourseID+"\x00"+b.ItemID+"\x00"+b.Kind)
	})
	entries := make([]map[string]any, 0, len(rows))
	for _, deadline := range rows {
		if (in.Course != "" && deadline.CourseID != in.Course) || (!in.IncludeCompleted && deadline.Completed) {
			continue
		}
		it, ok, e := d.DB.GetItem(deadline.ItemID)
		if e != nil {
			return nil, contentOut{}, fmt.Errorf("deadline item unavailable")
		}
		if !ok || it.Provider != "ed" || it.RemovedAt != 0 || it.CourseID != deadline.CourseID {
			continue
		}
		entry := deadlineEntry(deadline)
		entry["title"] = it.Title
		entries = append(entries, entry)
	}
	out, err := d.page(entries, in)
	return nil, out, err
}

func deadlineEntry(d store.Deadline) map[string]any {
	status := d.SubmissionStatus
	if status == "" {
		status = "unknown"
	}
	return map[string]any{"course": d.CourseID, "item_id": d.ItemID, "kind": d.Kind, "opens_at": d.OpensAt, "due_at": d.DueAt, "cutoff_at": d.CutoffAt, "submission_status": status, "completed": d.Completed, "completion_known": d.Completed || status != "unknown"}
}

func (d Deps) getAssessment(_ context.Context, _ *mcp.CallToolRequest, in contentItemIn) (*mcp.CallToolResult, contentOut, error) {
	it, err := d.contentItem(in, []string{"lesson", "slide"})
	if err != nil {
		return nil, contentOut{}, err
	}
	out, err := d.detail(it)
	if err != nil {
		return nil, out, err
	}
	children, err := d.DB.ListItems(store.ItemFilter{CourseID: it.CourseID, Provider: "ed", Kinds: []string{"slide"}})
	if err != nil {
		return nil, out, fmt.Errorf("assessment items unavailable")
	}
	ids := map[string]bool{it.ID: true}
	for changed := true; changed; {
		changed = false
		for _, child := range children {
			if ids[child.ParentID] && !ids[child.ID] {
				ids[child.ID] = true
				changed = true
			}
		}
	}
	metadata, _ := safeMetadata(it.MetaJSON)
	responsesAvailable := metadata["responses"] != nil
	submissionsAvailable := metadata["submissions"] != nil
	answersAvailable := metadata["released_answers"] != nil
	slides := make([]any, 0)
	files, err := d.fileEntries(it.ID)
	if err != nil {
		return nil, out, err
	}
	for _, child := range children {
		if !ids[child.ID] || child.ID == it.ID {
			continue
		}
		entry := itemEntry(child, true)
		childMeta, _ := safeMetadata(child.MetaJSON)
		responsesAvailable = responsesAvailable || childMeta["responses"] != nil
		submissionsAvailable = submissionsAvailable || childMeta["submissions"] != nil
		answersAvailable = answersAvailable || childMeta["released_answers"] != nil
		childFiles, e := d.fileEntries(child.ID)
		if e != nil {
			return nil, out, e
		}
		entry["files"] = childFiles
		slides = append(slides, entry)
		files = append(files, childFiles...)
	}
	// A slide inherits its lesson's assessment window, but not another slide's answers.
	deadlineIDs := map[string]bool{}
	for id := range ids {
		deadlineIDs[id] = true
	}
	if it.Kind == "slide" && it.ParentID != "" {
		parent, ok, e := d.DB.GetItem(it.ParentID)
		if e != nil {
			return nil, out, fmt.Errorf("assessment parent unavailable")
		}
		if ok && parent.Kind == "lesson" && parent.Provider == "ed" && parent.CourseID == it.CourseID && parent.RemovedAt == 0 {
			deadlineIDs[parent.ID] = true
		}
	}
	grades, err := d.DB.ListGrades(it.CourseID)
	if err != nil {
		return nil, out, fmt.Errorf("assessment grades unavailable")
	}
	gradeRows := make([]any, 0)
	for _, g := range grades {
		if ids[g.ItemKey] {
			gradeRows = append(gradeRows, map[string]any{"item_id": g.ItemKey, "name": g.Name, "grade": g.Grade, "grade_max": g.GradeMax, "percentage": g.Percentage, "feedback_md": g.FeedbackMD, "graded_at": g.GradedAt})
		}
	}
	deadlines, err := d.DB.ListDeadlines(0, 0)
	if err != nil {
		return nil, out, fmt.Errorf("assessment deadlines unavailable")
	}
	deadlineRows := make([]any, 0)
	for _, deadline := range deadlines {
		if deadline.CourseID == it.CourseID && deadlineIDs[deadline.ItemID] {
			deadlineRows = append(deadlineRows, deadlineEntry(deadline))
		}
	}
	out.Item["slides"], out.Item["files"] = slides, files
	out.Item["grades"], out.Item["deadlines"] = gradeRows, deadlineRows
	out.Item["grades_available"], out.Item["deadlines_available"] = len(gradeRows) > 0, len(deadlineRows) > 0
	out.Item["responses_available"], out.Item["submissions_available"], out.Item["released_answers_available"] = responsesAvailable, submissionsAvailable, answersAvailable
	err = boundContent(&out)
	return nil, out, err
}

// Budget the encoded object, including escaped text, metadata and reply trees.
// List pagination advances only over entries actually returned to the caller.
func boundContent(out *contentOut) error {
	for cap := 8192; ; cap /= 2 {
		b, err := json.Marshal(out)
		if err != nil {
			return fmt.Errorf("local content could not be encoded")
		}
		if len(b) <= BudgetChars {
			return nil
		}
		out.Truncated = true
		if cap < 16 {
			return fmt.Errorf("stored identifiers exceed the output budget")
		}
		for i := range out.Items {
			out.Items[i] = shrinkObject(out.Items[i], cap)
		}
		if out.Item != nil {
			out.Item = shrinkObject(out.Item, cap)
		}
		if out.Text != nil {
			v := clipBytes(*out.Text, cap)
			out.Text = &v
		}
	}
}

func clipBytes(s string, cap int) string {
	if len(s) <= cap {
		return s
	}
	for cap > 0 && !utf8.RuneStart(s[cap]) {
		cap--
	}
	return s[:cap]
}

func shrinkObject(in map[string]any, cap int) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		switch key {
		case "id", "item_id", "course", "parent_id", "file_id", "local_path":
			out[key] = value
		default:
			out[key] = shrinkValue(value, cap)
		}
	}
	return out
}

func shrinkValue(value any, cap int) any {
	switch v := value.(type) {
	case string:
		return clipBytes(v, cap)
	case map[string]any:
		return shrinkObject(v, cap)
	case []any:
		limit := min(len(v), max(cap/64, 1))
		out := make([]any, 0, limit)
		for _, child := range v[:limit] {
			out = append(out, shrinkValue(child, cap))
		}
		return out
	default:
		return value
	}
}
