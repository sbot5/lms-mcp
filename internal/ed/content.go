package ed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sbot5/lms-mcp/internal/httpx"
)

// Document is a JSON object returned by an Ed content endpoint. JSON numbers
// have the encoding/json default float64 representation.
type Document map[string]any

// Lessons returns lesson metadata, without opening lessons or recording views.
func (c *Client) Lessons(ctx context.Context, courseID int) ([]Document, error) {
	var raw any
	if err := c.Get(ctx, fmt.Sprintf("/courses/%d/lessons", courseID), &raw); err != nil {
		return nil, err
	}
	lessons, err := decodeDocuments(unwrap(raw, []string{"lessons"}))
	if err != nil {
		return nil, err
	}
	// Ed returns modules alongside lessons. Retain each lesson's module name
	// without introducing a second network request or changing the list API.
	envelope, ok := raw.(map[string]any)
	if ok {
		if data, exists := envelope["data"].(map[string]any); exists {
			envelope = data
		}
		if modules, exists := envelope["modules"]; exists {
			list, err := decodeDocuments(modules)
			if err != nil {
				return nil, errors.New("ed: invalid modules array")
			}
			byID := make(map[float64]string)
			for _, module := range list {
				id, idOK := module["id"].(float64)
				name, nameOK := module["name"].(string)
				if !idOK || !nameOK {
					return nil, errors.New("ed: invalid module metadata")
				}
				byID[id] = name
			}
			for _, lesson := range lessons {
				if id, ok := lesson["module_id"].(float64); ok {
					if name, exists := byID[id]; exists {
						lesson["module_name"] = name
					}
				}
			}
		}
	}
	return lessons, nil
}

// Lesson reads the slides in a lesson without the progress-recording view query.
func (c *Client) Lesson(ctx context.Context, id int) (Document, error) {
	return c.document(ctx, fmt.Sprintf("/lessons/%d", id), "lesson")
}

// Resources returns the Resources tab's file and link metadata.
func (c *Client) Resources(ctx context.Context, courseID int) ([]Document, error) {
	return c.documents(ctx, fmt.Sprintf("/courses/%d/resources", courseID), "resources")
}

// Questions reads quiz questions and any answers already released by Ed.
func (c *Client) Questions(ctx context.Context, slideID int) ([]Document, error) {
	return c.documents(ctx, fmt.Sprintf("/lessons/slides/%d/questions", slideID), "questions")
}

// Responses reads the signed-in user's quiz responses.
func (c *Client) Responses(ctx context.Context, slideID int) ([]Document, error) {
	return c.documents(ctx, fmt.Sprintf("/lessons/slides/%d/questions/responses", slideID), "responses", "quiz_responses")
}

// Challenge returns reading material only. Workspace tickets, JWTs, execution
// configuration and arbitrary future fields never leave the provider.
func (c *Client) Challenge(ctx context.Context, id int) (Document, error) {
	doc, err := c.document(ctx, fmt.Sprintf("/challenges/%d", id), "challenge")
	if err != nil {
		return nil, err
	}
	return readingFields(doc, "id", "title", "name", "content", "explanation", "type", "created_at", "updated_at"), nil
}

// Submissions reads submission outcomes, without retaining code workspaces or
// authentication material. Student access may return an unavailable APIError.
func (c *Client) Submissions(ctx context.Context, userID, challengeID int) ([]Document, error) {
	docs, err := c.documents(ctx, fmt.Sprintf("/users/%d/challenges/%d/submissions", userID, challengeID), "submissions")
	if err != nil {
		return nil, err
	}
	for i, doc := range docs {
		docs[i] = readingFields(doc, "id", "user_id", "challenge_id", "lesson_mark_id", "status", "is_completed", "testcase_pass_count", "testcase_total_count", "feedback_provided", "released", "published", "is_released", "created_at", "updated_at", "submitted_at", "score", "points", "max_points", "mark", "auto_mark", "rubric_mark", "mark_override", "comment", "feedback", "code", "source")
	}
	return docs, nil
}

// LessonAttempt reads the user's existing lesson attempt; it never creates one.
func (c *Client) LessonAttempt(ctx context.Context, lessonID, userID int) (Document, error) {
	doc, err := c.document(ctx, fmt.Sprintf("/lessons/%d/attempts/%d", lessonID, userID), "attempt", "lesson_attempt")
	if err != nil {
		return nil, err
	}
	return readingFields(doc, "id", "user_id", "lesson_id", "lesson_mark_id", "status", "released", "published", "is_released", "feedback_provided", "created_at", "updated_at", "started_at", "completed_at"), nil
}

// LessonMark reads released marking details and rubric items.
func (c *Client) LessonMark(ctx context.Context, markID int) (Document, error) {
	doc, err := c.document(ctx, fmt.Sprintf("/lesson_marks/%d?rubric_items=true", markID), "lesson_mark")
	if err != nil {
		return nil, err
	}
	return readingFields(doc, "id", "user_id", "lesson_id", "lesson_mark_id", "released", "published", "is_released", "feedback_provided", "auto_mark", "rubric_mark", "mark_override", "comment", "score", "points", "max_points", "rubric_items", "created_at", "updated_at"), nil
}

func (c *Client) documents(ctx context.Context, path string, fields ...string) ([]Document, error) {
	var raw any
	if err := c.Get(ctx, path, &raw); err != nil {
		return nil, err
	}
	return decodeDocuments(unwrap(raw, fields))
}

func decodeDocuments(value any) ([]Document, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, errors.New("ed: response is missing the expected content array")
	}
	result := make([]Document, 0, len(list))
	for _, entry := range list {
		doc, ok := entry.(map[string]any)
		if !ok || len(doc) == 0 {
			return nil, errors.New("ed: content array contains an invalid object")
		}
		result = append(result, Document(doc))
	}
	return result, nil
}

func (c *Client) document(ctx context.Context, path string, fields ...string) (Document, error) {
	var raw any
	if err := c.Get(ctx, path, &raw); err != nil {
		return nil, err
	}
	value := unwrap(raw, fields)
	doc, ok := value.(map[string]any)
	if !ok || len(doc) == 0 {
		return nil, errors.New("ed: response is missing the expected content object")
	}
	return Document(doc), nil
}

// unwrap accepts the named endpoint envelope, a data envelope, or a direct
// object/array. An unrelated or null envelope is never a successful empty list.
func unwrap(raw any, fields []string) any {
	if obj, ok := raw.(map[string]any); ok {
		for _, key := range fields {
			if value, exists := obj[key]; exists {
				return value
			}
		}
		if data, exists := obj["data"]; exists {
			return unwrap(data, fields)
		}
		if _, exists := obj["id"]; !exists {
			return nil
		}
	}
	return raw
}

// readingFields projects scalars and explicitly allowed nested feedback/rubric
// fields. Unknown objects are discarded rather than retaining hidden secrets.
func readingFields(doc Document, fields ...string) Document {
	result := make(Document)
	for _, key := range fields {
		value, exists := doc[key]
		if !exists {
			continue
		}
		switch value := value.(type) {
		case nil, string, bool, float64:
			result[key] = value
		case []any:
			if key == "rubric_items" {
				items := make([]any, 0, len(value))
				for _, entry := range value {
					if item, ok := entry.(map[string]any); ok {
						items = append(items, readingFields(item, "id", "name", "description", "comment", "mark", "score", "points", "max_points"))
					}
				}
				result[key] = items
			}
		case map[string]any:
			if key == "feedback" {
				result[key] = readingFields(value, "content", "comment", "text", "score", "points", "max_points", "status")
			}
		}
	}
	return result
}

// ResourceURL is the authenticated, read-only resource download endpoint.
func (c *Client) ResourceURL(id int) string {
	return strings.TrimRight(c.base, "/") + fmt.Sprintf("/resources/%d/download?dl=1", id)
}

// OpenFile performs a conditional GET through the same read-only guard. The
// caller owns the response body for successful 200 and 304 responses. The
// supplied httpx client must use its default FollowRedirects=false setting.
func (c *Client) OpenFile(ctx context.Context, rawURL, etag, modified string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	base, baseErr := url.Parse(c.base)
	if err != nil || baseErr != nil || !u.IsAbs() || u.Host == "" || u.User != nil {
		return nil, errors.New("ed: invalid attachment URL")
	}
	for redirects := 0; ; redirects++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, errors.New("ed: invalid attachment request")
		}
		if httpx.SameHost(u, base.Scheme, base.Host) && c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		if modified != "" {
			req.Header.Set("If-Modified-Since", modified)
		}
		resp, err := c.doer.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotModified {
			return resp, nil
		}
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			location := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if redirects >= 5 || location == "" {
				return nil, errors.New("ed: attachment redirect refused")
			}
			next, err := url.Parse(location)
			if err != nil {
				return nil, errors.New("ed: invalid attachment redirect")
			}
			u = u.ResolveReference(next)
		default:
			err := responseError(resp)
			_ = resp.Body.Close()
			return nil, err
		}
	}
}
