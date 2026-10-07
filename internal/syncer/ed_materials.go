package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/files"
	"github.com/sbot5/lms-mcp/internal/render"
	"github.com/sbot5/lms-mcp/internal/store"
)

// collectEdMaterials collects one course without publishing its staged files.
// The caller publishes the batch before its SQL transaction and rolls it back
// on failure. Unavailable inventories are deliberately absent from Kinds.
func collectEdMaterials(ctx context.Context, p *Providers, course store.Course, full bool) (snapshot store.ContentSnapshot, batch *files.Batch, err error) {
	if p == nil || p.Ed == nil || p.Cfg == nil || course.EdCourseID <= 0 {
		return snapshot, nil, fmt.Errorf("ed materials: invalid provider or course")
	}
	me, err := p.Ed.Whoami(ctx)
	if err != nil {
		return snapshot, nil, err
	}
	if me.UserID <= 0 {
		return snapshot, nil, fmt.Errorf("ed materials: missing own user ID")
	}
	lessonsEnabled, resourcesEnabled := true, true
	for _, enrolled := range me.Courses {
		if enrolled.ID == course.EdCourseID && enrolled.Features != nil {
			lessonsEnabled = enrolled.Features.Lessons == nil || *enrolled.Features.Lessons
			resourcesEnabled = enrolled.Features.Resources == nil || *enrolled.Features.Resources
			break
		}
	}
	root := p.Cfg.DataPath(files.SafeName(course.Code+"-"+course.Term)+"-ed-"+strconv.Itoa(course.EdCourseID), "ed")
	batch, err = files.NewBatch(root, p.Cfg.MaxFileMB)
	if err != nil {
		return snapshot, nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, batch.Rollback())
			batch = nil
		}
	}()
	c := edMaterialCollector{ctx: ctx, p: p, course: course, ownID: me.UserID, batch: batch, full: full, snapshot: &snapshot}
	if lessonsEnabled {
		lessons, err := p.Ed.Lessons(ctx, course.EdCourseID)
		if ed.IsUnavailable(err) {
			c.warning("lessons")
		} else if err != nil {
			return snapshot, batch, err
		} else {
			snapshot.Kinds = append(snapshot.Kinds, "lesson")
			slidesComplete := true
			seen := map[int]bool{}
			for _, lesson := range lessons {
				id := edMaterialID(lesson["id"])
				if id == 0 || seen[id] {
					return snapshot, batch, fmt.Errorf("ed materials: invalid or duplicate lesson ID")
				}
				seen[id] = true
				complete, collectErr := c.lesson(lesson)
				if collectErr != nil {
					return snapshot, batch, collectErr
				}
				slidesComplete = slidesComplete && complete
			}
			if slidesComplete {
				snapshot.Kinds = append(snapshot.Kinds, "slide")
			}
		}
	}
	if resourcesEnabled {
		resources, err := p.Ed.Resources(ctx, course.EdCourseID)
		if ed.IsUnavailable(err) {
			c.warning("resources")
		} else if err != nil {
			return snapshot, batch, err
		} else {
			snapshot.Kinds = append(snapshot.Kinds, "resource")
			seen := map[int]bool{}
			for _, resource := range resources {
				id := edMaterialID(resource["id"])
				if id == 0 || seen[id] {
					return snapshot, batch, fmt.Errorf("ed materials: invalid or duplicate resource ID")
				}
				seen[id] = true
				if err := c.resource(resource); err != nil {
					return snapshot, batch, err
				}
			}
		}
	}
	return snapshot, batch, nil
}

type edMaterialCollector struct {
	ctx       context.Context
	p         *Providers
	course    store.Course
	ownID     int
	batch     *files.Batch
	full      bool
	snapshot  *store.ContentSnapshot
	downloads map[string]files.Record
	marks     map[string]edMarkChoice
}

type edMarkChoice struct {
	id   int
	time int64
}

func edMarkTime(mark, inherited ed.Document) int64 {
	for _, doc := range []ed.Document{mark, inherited} {
		for _, key := range []string{"updated_at", "submitted_at", "created_at"} {
			if stamp := edMaterialTime(doc[key]); stamp != 0 {
				return stamp
			}
		}
	}
	return 0
}

// Availability and score belong to the same selected mark. An older hidden
// submission must not overwrite the status of the newer published one.
func (c *edMaterialCollector) selectMark(itemID string, id int, stamp int64, meta map[string]any) bool {
	if c.marks == nil {
		c.marks = map[string]edMarkChoice{}
	}
	if previous, ok := c.marks[itemID]; ok && previous.id != id {
		if stamp > 0 && previous.time > 0 {
			if stamp < previous.time || (stamp == previous.time && id < previous.id) {
				return false
			}
		} else if id < previous.id {
			return false
		}
	}
	c.marks[itemID] = edMarkChoice{id, stamp}
	for i := len(c.snapshot.Grades) - 1; i >= 0; i-- {
		if c.snapshot.Grades[i].ItemKey == itemID {
			c.snapshot.Grades = append(c.snapshot.Grades[:i], c.snapshot.Grades[i+1:]...)
		}
	}
	delete(meta, "mark")
	return true
}

func (c *edMaterialCollector) warning(inventory string) {
	c.snapshot.Warnings = append(c.snapshot.Warnings, "Ed "+inventory+" unavailable to this account")
}

func (c *edMaterialCollector) lesson(listed ed.Document) (bool, error) {
	id := edMaterialID(listed["id"])
	item := c.item("lesson", id, listed)
	meta := edMaterialMeta(listed)
	meta["availability"] = edMaterialAvailability(listed)
	unavailable := edMaterialUnavailable(listed)
	meta["content_unavailable"] = unavailable
	if unavailable {
		edMaterialFinalize(&item, meta)
		c.snapshot.Items = append(c.snapshot.Items, item)
		c.deadline(item, listed, nil)
		return false, nil
	}
	detail, err := c.p.Ed.Lesson(c.ctx, id)
	if err != nil {
		return false, err
	}
	if detailID := edMaterialID(detail["id"]); detailID != id {
		return false, fmt.Errorf("ed materials: lesson detail ID mismatch")
	}
	merged := ed.Document{}
	for key, value := range listed {
		merged[key] = value
	}
	for key, value := range detail {
		merged[key] = value
	}
	if edMaterialUnavailable(merged) {
		meta = edMaterialMeta(merged)
		meta["availability"] = edMaterialAvailability(merged)
		meta["content_unavailable"] = true
		item = c.item("lesson", id, merged)
		edMaterialFinalize(&item, meta)
		c.snapshot.Items = append(c.snapshot.Items, item)
		c.deadline(item, merged, nil)
		return false, nil
	}
	slides, ok := merged["slides"].([]any)
	if !ok {
		return false, fmt.Errorf("ed materials: lesson detail is missing slides")
	}
	meta = edMaterialMeta(merged)
	meta["availability"] = edMaterialAvailability(merged)
	meta["content_unavailable"] = false
	item = c.item("lesson", id, merged)
	module := files.SafeName(edMaterialString(merged, "module_name", "module"))
	if module == "file" {
		module = "module"
	}
	if moduleID := edMaterialID(merged["module_id"]); moduleID > 0 {
		module = strconv.Itoa(moduleID) + "-" + module
	}
	dir := "lessons/" + module + "/" + strconv.Itoa(id) + "-" + files.SafeName(item.Title)
	assetDir := "lessons/" + module + "/_assets/" + strconv.Itoa(id)
	var body strings.Builder
	body.WriteString("# " + render.Escape(item.Title) + "\n\n")
	intro, introFiles, err := c.reading(item.ID, merged, assetDir)
	if err != nil {
		return false, err
	}
	body.WriteString(intro)
	meta["attachments"] = introFiles
	complete := true
	seen := map[int]bool{}
	for _, raw := range slides {
		doc, ok := raw.(map[string]any)
		if !ok {
			return false, fmt.Errorf("ed materials: invalid slide object")
		}
		slideID := edMaterialID(doc["id"])
		if slideID == 0 || seen[slideID] {
			return false, fmt.Errorf("ed materials: invalid or duplicate slide ID")
		}
		seen[slideID] = true
		slide, slideMeta, slideComplete, err := c.slide(ed.Document(doc), item.ID, assetDir, merged)
		if err != nil {
			return false, err
		}
		complete = complete && slideComplete
		c.snapshot.Items = append(c.snapshot.Items, slide)
		body.WriteString("\n\n## " + render.Escape(slide.Title) + "\n\n" + slide.BodyMD)
		for _, key := range []string{"responses", "submissions", "released_answers"} {
			if values, ok := slideMeta[key].([]any); ok {
				previous, _ := meta[key].([]any)
				meta[key] = append(previous, values...)
			}
		}
	}
	attempt, err := c.p.Ed.LessonAttempt(c.ctx, id, c.ownID)
	if ed.IsUnavailable(err) {
		c.warning("lesson attempts")
		meta["attempt_availability"] = "unavailable"
	} else if err != nil {
		return false, err
	} else {
		if !c.owned(attempt) {
			return false, fmt.Errorf("ed materials: lesson attempt belongs to another user")
		}
		meta["attempt"] = edMaterialScrub(attempt)
		if markID := edMaterialID(attempt["lesson_mark_id"]); markID > 0 {
			if err := c.mark(item, markID, attempt, meta); err != nil {
				return false, err
			}
		}
	}
	c.deadline(item, merged, attempt)
	item.BodyMD = strings.TrimSpace(body.String())
	if !complete {
		meta["content_unavailable"] = true
	}
	// A partially unavailable quiz retains its previous published Markdown,
	// matching the database's content_unavailable preservation policy.
	if complete {
		record, err := c.batch.StageBytes(dir+"/lesson.md", []byte(item.BodyMD+"\n"))
		if err != nil {
			return false, err
		}
		c.addFile(item.ID+":markdown", item.ID, "lesson.md", "", record)
	}
	edMaterialFinalize(&item, meta)
	c.snapshot.Items = append(c.snapshot.Items, item)
	return complete, nil
}

func (c *edMaterialCollector) slide(doc ed.Document, lessonID, assetDir string, lesson ed.Document) (store.Item, map[string]any, bool, error) {
	id := edMaterialID(doc["id"])
	item := c.item("slide", id, doc)
	item.ParentID = lessonID
	meta := edMaterialMeta(doc)
	meta["availability"] = edMaterialAvailability(doc)
	if edMaterialUnavailable(doc) {
		meta["content_unavailable"] = true
		edMaterialFinalize(&item, meta)
		c.deadline(item, doc, nil)
		return item, meta, false, nil
	}
	body, attachments, err := c.reading(item.ID, doc, assetDir)
	if err != nil {
		return item, meta, false, err
	}
	meta["attachments"] = attachments
	complete := true
	if edMaterialString(doc, "type") == "quiz" {
		questions, err := c.p.Ed.Questions(c.ctx, id)
		if ed.IsUnavailable(err) {
			c.warning("quiz questions")
			meta["questions_availability"] = "unavailable"
			complete = false
		} else if err != nil {
			return item, meta, false, err
		} else {
			cleanQuestions := []any{}
			released := []any{}
			for _, question := range questions {
				available := edMaterialReleased(question) || edMaterialSolutionsReleased(lesson) || edMaterialSolutionsReleased(doc)
				if edMaterialExplicitlyHidden(question) {
					available = false
				}
				clean := edMaterialQuestion(question, available)
				cleanQuestions = append(cleanQuestions, clean)
				data, _ := question["data"].(map[string]any)
				questionBody, questionFiles, err := c.reading(item.ID, ed.Document(data), assetDir)
				if err != nil {
					return item, meta, false, err
				}
				body += "\n\n" + questionBody
				attachments = append(attachments, questionFiles...)
				meta["attachments"] = attachments
				if available {
					released = append(released, clean)
				}
			}
			meta["questions"] = cleanQuestions
			meta["released_answers"] = released
			body += edMaterialJSONBlock("Questions", cleanQuestions)
		}
		responses, err := c.p.Ed.Responses(c.ctx, id)
		if ed.IsUnavailable(err) {
			c.warning("quiz responses")
			meta["responses_availability"] = "unavailable"
		} else if err != nil {
			return item, meta, false, err
		} else {
			own := []any{}
			for _, response := range responses {
				if !c.owned(response) {
					return item, meta, false, fmt.Errorf("ed materials: quiz response belongs to another user")
				}
				available := edMaterialReleased(response) || edMaterialSolutionsReleased(lesson) || edMaterialSolutionsReleased(doc)
				if edMaterialExplicitlyHidden(response) {
					available = false
				}
				own = append(own, edMaterialAssessment(response, available, true, 0))
			}
			meta["responses"] = own
			body += edMaterialJSONBlock("Your responses", own)
		}
	}
	if challengeID := edMaterialID(doc["challenge_id"]); challengeID > 0 {
		challenge, err := c.p.Ed.Challenge(c.ctx, challengeID)
		if err != nil {
			return item, meta, false, err
		}
		challengeBody, challengeFiles, err := c.reading(item.ID, challenge, assetDir)
		if err != nil {
			return item, meta, false, err
		}
		meta["challenge"] = edMaterialScrub(challenge)
		meta["attachments"] = append(attachments, challengeFiles...)
		body += "\n\n" + challengeBody
		submissions, err := c.p.Ed.Submissions(c.ctx, c.ownID, challengeID)
		if ed.IsUnavailable(err) {
			c.warning("challenge submissions")
			meta["submissions_availability"] = "unavailable"
		} else if err != nil {
			return item, meta, false, err
		} else {
			own := []any{}
			for _, submission := range submissions {
				if !c.owned(submission) {
					return item, meta, false, fmt.Errorf("ed materials: submission belongs to another user")
				}
				own = append(own, edMaterialScrub(submission))
				if markID := edMaterialID(submission["lesson_mark_id"]); markID > 0 {
					if err := c.mark(item, markID, submission, meta); err != nil {
						return item, meta, false, err
					}
				}
			}
			meta["submissions"] = own
			body += edMaterialJSONBlock("Your submissions", own)
		}
	}
	item.BodyMD = strings.TrimSpace(body)
	meta["content_unavailable"] = !complete
	edMaterialFinalize(&item, meta)
	c.deadline(item, doc, nil)
	return item, meta, complete, nil
}

func (c *edMaterialCollector) resource(doc ed.Document) error {
	id := edMaterialID(doc["id"])
	item := c.item("resource", id, doc)
	meta := edMaterialMeta(doc)
	meta["availability"] = edMaterialAvailability(doc)
	if edMaterialUnavailable(doc) {
		meta["content_unavailable"] = true
	} else if link := edMaterialString(doc, "link", "video_url"); link != "" {
		item.BodyMD = "[" + render.Escape(item.Title) + "](" + render.URL(edMaterialCleanURL(link)) + ")"
		if edMaterialVideo(link, edMaterialString(doc, "extension", "mime")) {
			c.addFile(item.ID+":link", item.ID, item.Title, link, files.Record{Status: "video_link"})
		}
	} else {
		name := item.Title
		if extension := strings.TrimPrefix(edMaterialString(doc, "extension"), "."); extension != "" && !strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(extension)) {
			name += "." + extension
		}
		fileURL := edMaterialString(doc, "file_url", "url")
		if fileURL == "" {
			fileURL = c.p.Ed.ResourceURL(id)
		}
		dir := "resources/" + files.SafeName(edMaterialString(doc, "category")) + "/" + strconv.Itoa(id)
		file, err := c.download(item.ID, fileURL, name, dir)
		if err != nil {
			return err
		}
		meta["attachments"] = []any{file}
		item.BodyMD = "[" + render.Escape(name) + "](" + render.URL(edMaterialCleanURL(fileURL)) + ")"
	}
	edMaterialFinalize(&item, meta)
	c.snapshot.Items = append(c.snapshot.Items, item)
	return nil
}

// reading mirrors attachments before rendering, so signed URLs never enter
// stored Markdown and the local links refer to the eventual published files.
func (c *edMaterialCollector) reading(itemID string, doc ed.Document, assetDir string) (string, []any, error) {
	content := edMaterialString(doc, "content")
	refs, err := render.AssetURLs(content)
	if err != nil {
		return "", nil, fmt.Errorf("ed materials: malformed reading XML")
	}
	if fileURL := edMaterialString(doc, "file_url"); fileURL != "" {
		refs[fileURL] = edMaterialString(doc, "filename", "name", "title")
	}
	keys := make([]string, 0, len(refs))
	for key := range refs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assets := map[string]string{}
	attachments := []any{}
	for _, fileURL := range keys {
		name := refs[fileURL]
		if name == "" {
			parsed, _ := url.Parse(fileURL)
			if parsed != nil {
				name = path.Base(parsed.Path)
			}
		}
		if fileURL == edMaterialString(doc, "file_url") && edMaterialString(doc, "type") == "pdf" && !strings.HasSuffix(strings.ToLower(name), ".pdf") {
			name += ".pdf"
		}
		file, err := c.download(itemID, fileURL, name, assetDir)
		if err != nil {
			return "", nil, err
		}
		attachments = append(attachments, file)
		if file["status"] == "ok" {
			assets[fileURL] = path.Base(assetDir) + "/" + file["local_name"].(string)
		}
	}
	md, _, err := render.Body(content, edMaterialString(doc, "document", "passage", "html"), assets)
	if err != nil {
		return "", nil, fmt.Errorf("ed materials: malformed reading XML")
	}
	md = edMaterialScrubText(md)
	for _, key := range []string{"video_url", "url"} {
		if link := edMaterialString(doc, key); link != "" {
			label := "Link"
			if key == "video_url" {
				label = "Video"
			}
			md += "\n\n[" + label + "](" + render.URL(edMaterialCleanURL(link)) + ")"
		}
	}
	for _, raw := range attachments {
		file := raw.(map[string]any)
		if edMaterialCleanURL(edMaterialString(doc, "file_url")) == file["source_url"] {
			target := file["source_url"].(string)
			if file["status"] == "ok" {
				target = "../_assets/" + path.Base(assetDir) + "/" + file["local_name"].(string)
			}
			md += "\n\n[" + render.Escape(file["name"].(string)) + "](" + render.URL(target) + ")"
		}
	}
	return strings.TrimSpace(md), attachments, nil
}

func (c *edMaterialCollector) download(itemID, sourceURL, name, dir string) (map[string]any, error) {
	cleanURL := edMaterialCleanURL(sourceURL)
	key := edMaterialHash(cleanURL)[:16]
	localName := key + "-" + files.SafeName(name)
	record := files.Record{Status: "video_link"}
	var err error
	if !edMaterialVideo(sourceURL, name) {
		if c.downloads == nil {
			c.downloads = map[string]files.Record{}
		}
		rel := dir + "/" + localName
		var exists bool
		record, exists = c.downloads[rel]
		if !exists {
			record, err = c.batch.StageDownload(c.ctx, rel, func(ctx context.Context, tag, modified string) (*http.Response, error) {
				return c.p.Ed.OpenFile(ctx, sourceURL, tag, modified)
			}, c.full)
			if err != nil {
				return nil, err
			}
			c.downloads[rel] = record
		}
	}
	c.addFile(itemID+":asset:"+key, itemID, name, cleanURL, record)
	return map[string]any{"name": name, "local_name": localName, "source_url": cleanURL, "sha256": record.SHA256, "status": record.Status, "size": record.Size}, nil
}

func (c *edMaterialCollector) addFile(id, itemID, name, source string, record files.Record) {
	for _, file := range c.snapshot.Files {
		if file.ID == id {
			return
		}
	}
	c.snapshot.Files = append(c.snapshot.Files, store.File{ID: id, ItemID: itemID, Name: name, MIME: record.MIME, Size: record.Size, SourceURL: edMaterialCleanURL(source), LocalPath: record.LocalPath, SHA256: record.SHA256, ETag: record.ETag, LastModified: record.LastModified, Status: record.Status})
}

func (c *edMaterialCollector) item(kind string, id int, doc ed.Document) store.Item {
	title := edMaterialString(doc, "title", "name")
	if title == "" {
		title = strings.Title(kind) + " " + strconv.Itoa(id)
	}
	return store.Item{ID: "ed:" + kind + ":" + strconv.Itoa(id), CourseID: c.course.ID, Provider: "ed", Kind: kind, Title: edMaterialScrubText(title), URL: edMaterialCleanURL(edMaterialString(doc, "url")), CreatedAt: edMaterialTime(doc["created_at"]), UpdatedAt: edMaterialTime(doc["updated_at"])}
}

func (c *edMaterialCollector) owned(doc ed.Document) bool {
	value, exists := doc["user_id"]
	return !exists || edMaterialID(value) == c.ownID
}

func (c *edMaterialCollector) mark(item store.Item, markID int, inherited ed.Document, meta map[string]any) error {
	mark, err := c.p.Ed.LessonMark(c.ctx, markID)
	if ed.IsUnavailable(err) {
		c.warning("lesson marks")
		if c.selectMark(item.ID, markID, edMarkTime(nil, inherited), meta) {
			meta["marks_availability"] = "unavailable"
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !c.owned(mark) {
		return fmt.Errorf("ed materials: lesson mark belongs to another user")
	}
	gradedAt := edMarkTime(mark, inherited)
	if !c.selectMark(item.ID, markID, gradedAt, meta) {
		return nil
	}
	if !edMaterialReleased(mark) && !edMaterialReleased(inherited) {
		meta["marks_availability"] = "withheld"
		return nil
	}
	for _, doc := range []ed.Document{mark, inherited} {
		for _, key := range []string{"released", "published", "is_released"} {
			if value, ok := doc[key].(bool); ok && !value {
				meta["marks_availability"] = "withheld"
				return nil
			}
		}
	}
	score := edMaterialNumber(mark["mark_override"])
	if score == "" {
		score = edMaterialNumber(mark["score"])
		if score == "" {
			score = edMaterialNumber(mark["points"])
		}
	}
	if score == "" {
		a, b := edMaterialNumber(mark["auto_mark"]), edMaterialNumber(mark["rubric_mark"])
		if a != "" || b != "" {
			first, _ := strconv.ParseFloat(a, 64)
			second, _ := strconv.ParseFloat(b, 64)
			score = strconv.FormatFloat(first+second, 'f', -1, 64)
		}
	}
	if score == "" {
		meta["marks_availability"] = "unknown"
		return nil
	}
	meta["marks_availability"] = "available"
	feedback := edMaterialScrubText(edMaterialString(mark, "comment"))
	grade := store.Grade{CourseID: c.course.ID, ItemKey: item.ID, Name: item.Title, Grade: score, GradeMax: edMaterialNumber(mark["max_points"]), FeedbackMD: feedback, GradedAt: gradedAt}
	grade.Hash = edMaterialHash(score + "\x00" + grade.GradeMax + "\x00" + feedback)
	for i := range c.snapshot.Grades {
		if c.snapshot.Grades[i].ItemKey == item.ID {
			previousMark, _ := meta["mark"].(map[string]any)
			if c.snapshot.Grades[i].GradedAt > gradedAt || (c.snapshot.Grades[i].GradedAt == gradedAt && edMaterialID(previousMark["id"]) > markID) {
				return nil
			}
			c.snapshot.Grades[i] = grade
			meta["mark"] = edMaterialScrub(mark)
			return nil
		}
	}
	c.snapshot.Grades = append(c.snapshot.Grades, grade)
	meta["mark"] = edMaterialScrub(mark)
	return nil
}

func (c *edMaterialCollector) deadline(item store.Item, doc, attempt ed.Document) {
	if item.Kind == "slide" {
		present := false
		for _, key := range []string{"available_at", "effective_available_at", "due_at", "effective_due_at", "locked_at", "effective_locked_at"} {
			if _, ok := doc[key]; ok {
				present = true
				break
			}
		}
		if !present {
			return
		}
	}
	d := store.Deadline{CourseID: c.course.ID, ItemID: item.ID, Kind: item.Kind}
	var known store.DeadlinePresence
	d.OpensAt, known.Opens = edMaterialWindow(doc, "effective_available_at", "available_at")
	d.DueAt, known.Due = edMaterialWindow(doc, "effective_due_at", "due_at")
	d.CutoffAt, known.Cutoff = edMaterialWindow(doc, "effective_locked_at", "locked_at")
	statusDoc := doc
	if _, exists := attempt["status"]; exists {
		statusDoc = attempt
	}
	if status, ok := statusDoc["status"].(string); ok {
		switch status {
		case "unattempted", "attempted", "in_progress", "submitted", "completed", "draft", "unsubmitted":
			d.SubmissionStatus, known.Status = status, true
		}
	}
	d.Completed = d.SubmissionStatus == "completed" || d.SubmissionStatus == "submitted"
	if !known.Opens || !known.Due || !known.Cutoff || !known.Status {
		c.snapshot.Warnings = append(c.snapshot.Warnings, "Ed "+item.Kind+" deadline has missing or invalid fields; previous unknown fields retained")
	}
	if known.Opens || known.Due || known.Cutoff || known.Status {
		c.snapshot.Deadlines = append(c.snapshot.Deadlines, d)
		if c.snapshot.DeadlineFields == nil {
			c.snapshot.DeadlineFields = map[string]store.DeadlinePresence{}
		}
		c.snapshot.DeadlineFields[item.ID] = known
	}
}

// Missing or malformed timestamps are unknown. Explicit null, empty and zero
// values are known removals, including effective overrides of regular values.
func edMaterialWindow(doc ed.Document, effective, regular string) (int64, bool) {
	value, exists := doc[effective]
	if !exists {
		value, exists = doc[regular]
	}
	if !exists {
		return 0, false
	}
	if value == nil {
		return 0, true
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
		return 0, true
	}
	if number := edMaterialNumber(value); number == "0" {
		return 0, true
	}
	if parsed := edMaterialTime(value); parsed > 0 {
		return parsed, true
	}
	return 0, false
}

func edMaterialMeta(doc ed.Document) map[string]any {
	result := map[string]any{"marks_availability": "unknown"}
	for _, key := range []string{"type", "kind", "state", "status", "openable", "module_id", "module_name", "index", "available_at", "due_at", "locked_at", "solutions_at", "effective_available_at", "effective_due_at", "effective_locked_at", "effective_solutions_at", "category", "challenge_id"} {
		if raw, exists := doc[key]; exists {
			if value, ok := edMaterialScalar(raw); ok {
				result[key] = value
			}
		}
	}
	return result
}

func edMaterialAvailability(doc ed.Document) map[string]any {
	result := map[string]any{"content_unavailable": edMaterialUnavailable(doc)}
	for _, key := range []string{"state", "openable", "available_at", "effective_available_at", "solutions_at", "effective_solutions_at"} {
		if raw, exists := doc[key]; exists {
			if value, ok := edMaterialScalar(raw); ok {
				result[key] = value
			}
		}
	}
	return result
}

func edMaterialUnavailable(doc ed.Document) bool {
	if value, ok := doc["openable"].(bool); ok && !value {
		return true
	}
	return edMaterialString(doc, "state") == "scheduled"
}

func edMaterialReleased(doc ed.Document) bool {
	if edMaterialExplicitlyHidden(doc) {
		return false
	}
	for _, key := range []string{"released", "published", "is_released"} {
		if value, ok := doc[key].(bool); ok && value {
			return true
		}
	}
	return false
}

func edMaterialExplicitlyHidden(doc ed.Document) bool {
	for _, key := range []string{"released", "published", "is_released"} {
		if value, ok := doc[key].(bool); ok && !value {
			return true
		}
	}
	return false
}

func edMaterialSolutionsReleased(doc ed.Document) bool {
	for _, key := range []string{"solutions_released", "is_solution_released"} {
		if value, ok := doc[key].(bool); ok && value {
			return true
		}
	}
	value, exists := doc["effective_solutions_at"]
	if !exists {
		value = doc["solutions_at"]
	}
	at := edMaterialTime(value)
	return at > 0 && at <= time.Now().Unix()
}

func edMaterialQuestion(doc ed.Document, released bool) any {
	return edMaterialAssessment(doc, released, false, 0)
}

// Assessment documents are an export boundary: unknown fields and arbitrary
// nested objects never enter the database or the generated lesson Markdown.
// The same projection applies to question data, choices and the user's answer
// values, including shapes whose future fields have innocuous names.
func edMaterialAssessment(value any, released, response bool, depth int) any {
	if depth > 16 {
		return nil
	}
	if doc, ok := value.(ed.Document); ok {
		value = map[string]any(doc)
	}
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, child := range value {
			switch key {
			case "id", "question_id", "slide_id", "user_id", "index", "option_id", "choice_id":
				if number := edMaterialNumber(child); number != "" {
					parsed, _ := strconv.ParseFloat(number, 64)
					if parsed >= 0 && math.Trunc(parsed) == parsed {
						result[key] = parsed
					}
				}
			case "title", "type", "content", "document", "text", "label", "language", "status", "created_at", "updated_at", "submitted_at":
				if text, ok := child.(string); ok {
					result[key] = edMaterialScrubText(text)
				}
			case "code", "source":
				if text, ok := child.(string); ok && (released || response) {
					result[key] = edMaterialScrubText(text)
				}
			case "released", "published", "is_released", "selected", "is_selected", "is_completed":
				if flag, ok := child.(bool); ok {
					result[key] = flag
				}
			case "correct", "is_correct":
				if flag, ok := child.(bool); ok && (released || response) {
					result[key] = flag
				}
			case "data":
				if _, ok := child.(map[string]any); ok {
					result[key] = edMaterialAssessment(child, released, response, depth+1)
				}
			case "answers", "choices", "options", "value":
				result[key] = edMaterialAssessment(child, released, response, depth+1)
			case "answer", "response", "selection":
				if released || response {
					result[key] = edMaterialAssessment(child, released, response, depth+1)
				}
			case "solution", "explanation", "correct_answer":
				if released {
					result[key] = edMaterialAssessment(child, released, response, depth+1)
				}
			}
		}
		return result
	case []any:
		result := make([]any, 0, len(value))
		for _, child := range value {
			if projected := edMaterialAssessment(child, released, response, depth+1); projected != nil {
				result = append(result, projected)
			}
		}
		return result
	default:
		projected, _ := edMaterialScalar(value)
		return projected
	}
}

func edMaterialScalar(value any) (any, bool) {
	switch value := value.(type) {
	case nil:
		return nil, true
	case string:
		return edMaterialScrubText(value), true
	case bool:
		return value, true
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	case int:
		return value, true
	case json.Number:
		if number := edMaterialNumber(value); number != "" {
			return value, true
		}
	}
	return nil, false
}

func edMaterialFinalize(item *store.Item, meta map[string]any) {
	data, _ := json.Marshal(edMaterialScrub(meta))
	item.MetaJSON = string(data)
	item.ContentHash = edMaterialHash(item.Kind + "\x00" + item.Title + "\x00" + item.BodyMD + "\x00" + item.MetaJSON)
}

func edMaterialHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func edMaterialString(doc ed.Document, keys ...string) string {
	for _, key := range keys {
		if value, ok := doc[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}
func edMaterialID(value any) int {
	text := edMaterialNumber(value)
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || number <= 0 || number >= float64(math.MaxInt) || math.Trunc(number) != number {
		return 0
	}
	return int(number)
}
func edMaterialNumber(value any) string {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case int:
		number = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return ""
		}
		number = parsed
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return ""
		}
		number = parsed
	default:
		return ""
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return ""
	}
	return strconv.FormatFloat(number, 'f', -1, 64)
}
func edMaterialTime(value any) int64 {
	if text, ok := value.(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil && parsed.Unix() > 0 {
			return parsed.UTC().Unix()
		}
	}
	if seconds, err := strconv.ParseFloat(edMaterialNumber(value), 64); err == nil && seconds > 0 && seconds <= 253402300799 && math.Trunc(seconds) == seconds {
		return int64(seconds)
	}
	return 0
}
func edMaterialJSONBlock(title string, value []any) string {
	if len(value) == 0 {
		return ""
	}
	data, _ := json.MarshalIndent(value, "", "  ")
	fence := "```"
	for strings.Contains(string(data), fence) {
		fence += "`"
	}
	return "\n\n### " + title + "\n\n" + fence + "json\n" + string(data) + "\n" + fence
}

var edMaterialURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"')]+`)
var edMaterialURLInteger = regexp.MustCompile(`^[0-9]{1,20}$`)

func edMaterialScrubText(value string) string {
	return edMaterialURLPattern.ReplaceAllStringFunc(value, func(raw string) string { return edMaterialCleanURL(html.UnescapeString(raw)) })
}
func edMaterialCleanURL(value string) string {
	if value == "" {
		return ""
	}
	u, err := url.Parse(html.UnescapeString(value))
	if err != nil || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") || u.Host == "" {
		return ""
	}
	u.User = nil
	u.Fragment = ""
	u.Scheme = strings.ToLower(u.Scheme)
	// Retain only numeric identifiers, a numeric page/part and the download
	// flag. The original signed URL remains confined to OpenFile's request.
	query := url.Values{}
	for key, values := range u.Query() {
		lower := strings.ToLower(key)
		for _, parameter := range values {
			switch lower {
			case "id", "part":
				if edMaterialURLInteger.MatchString(parameter) {
					query.Add(lower, parameter)
				}
			case "dl":
				if parameter == "0" || parameter == "1" || strings.EqualFold(parameter, "true") || strings.EqualFold(parameter, "false") {
					query.Add(lower, strings.ToLower(parameter))
				}
			}
		}
	}
	for key := range query {
		sort.Strings(query[key])
	}
	u.RawQuery = query.Encode()
	return u.String()
}
func edMaterialScrub(value any) any {
	switch value := value.(type) {
	case ed.Document:
		return edMaterialScrub(map[string]any(value))
	case map[string]any:
		result := map[string]any{}
		for key, child := range value {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "ticket") || strings.Contains(lower, "jwt") || strings.Contains(lower, "secret") || lower == "authorization" || lower == "cookie" || lower == "password" {
				continue
			}
			result[key] = edMaterialScrub(child)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			result[i] = edMaterialScrub(child)
		}
		return result
	case string:
		return edMaterialScrubText(value)
	default:
		return value
	}
}
func edMaterialVideo(source, name string) bool {
	u, _ := url.Parse(source)
	extension := strings.ToLower(path.Ext(name))
	if extension == "" && u != nil {
		extension = strings.ToLower(path.Ext(u.Path))
	}
	if extension == "" {
		extension = "." + strings.TrimPrefix(strings.ToLower(name), ".")
	}
	return strings.HasPrefix(strings.ToLower(name), "video/") || extension == ".mp4" || extension == ".webm" || extension == ".mov" || extension == ".m4v" || extension == ".avi" || extension == ".mkv"
}
