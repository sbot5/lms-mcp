// Package anonymize turns API responses into synthetic offline fixtures.
// Raw values stay in memory; callers persist only the returned JSON.
package anonymize

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Sanitizer maintains one ID mapping and one time shift across a capture.
// It is not safe for concurrent use. Use a new instance for each capture set.
type Sanitizer struct {
	ids      map[string]int
	strings  map[string]int
	names    map[string]string
	shift    time.Duration
	shiftSet bool
}

func New() *Sanitizer {
	return &Sanitizer{ids: make(map[string]int), strings: make(map[string]int), names: make(map[string]string)}
}

// JSON accepts one JSON document, synthesizes its values and returns indented
// JSON. Neither errors nor the output contain original free text or credentials.
func (s *Sanitizer) JSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, errors.New("anonymize: invalid JSON")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("anonymize: expected one JSON document")
	}
	if !s.shiftSet {
		if first, ok := earliestTime(value, ""); ok {
			s.shift = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Sub(first)
			if s.shift == 0 {
				s.shift = -365 * 24 * time.Hour
			}
		} else {
			s.shift = -365 * 24 * time.Hour
		}
		s.shiftSet = true
	}
	result := s.walk(value, "")
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, errors.New("anonymize: could not encode fixture")
	}
	return append(b, '\n'), nil
}

func normalized(key string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
}

// Names are data too: an API object may be keyed by a person's name, and
// markup/URL names may contain identifiers. Retain only this fixed Ed schema.
var schemaFields = nameSet(`
id user_id course_id module_id lesson_id slide_id question_id challenge_id
resource_id attempt_id lesson_mark_id parent_id accepted_id last_viewed_slide_id
created_by_bot_id user_ids student_id student_number
user users course courses role course_role lessons lesson modules module_name
resources resource questions responses quiz_responses challenge submissions
attempt lesson_attempt lesson_mark thread threads answers comments items data
index number title name code year session features settings discussion categories
category subcategory subsubcategory content document html passage explanation
solution selection text feedback comment source filename extension size link
embedding file_url video_url url avatar_url mime mime_type content_type
kind type state status is_hidden is_timed is_private is_anonymous is_answered
is_staff_answered is_student_answered is_endorsed is_locked is_megathread is_pinned
is_seen is_starred is_watched openable attempts slides vote_count view_count
reply_count new_reply_count correct is_completed feedback_provided is_released
released published staff_only has_pats auto_points score points max_points mark
auto_mark rubric_mark mark_override rubric_items testcase_pass_count
testcase_total_count created_at updated_at deleted_at submitted_at started_at
completed_at available_at due_at locked_at solutions_at release_at opens_at
cutoff_at closes_at effective_available_at effective_due_at effective_locked_at
effective_solutions_at glanced_at graded_at email display timestamp release_date
modified_time timecreated timemodified
`)

var queryNames = nameSet(`
id user_id course_id courseID module_id lesson_id slide_id question_id challenge_id
resource_id attempt_id lesson_mark_id thread_id mark_id dl limit offset sort
sort_key filter category q query rubric_items no_comments
`)

var markupTags = nameSet(`
document paragraph heading bold italic strike underline break code math link image
file video snippet pre web-snippet list list-item item blockquote callout table
table-row table-cell table-header spoiler mention poll option answer
html head body title p h1 h2 h3 h4 h5 h6 b strong i em s del u br a img span div
ul ol li tr td th thead tbody tfoot caption hr figure figcaption sub sup
`)

var markupAttrs = nameSet(`
id user_id course_id lesson_id slide_id question_id challenge_id resource_id
src href url filename alt title type language level style width height colspan
rowspan start reversed checked selected value class target rel xmlns
`)

func nameSet(names string) map[string]bool {
	result := make(map[string]bool)
	for _, name := range strings.Fields(names) {
		result[name] = true
	}
	return result
}

// Share the mapping across fields, query parameters, tags and attributes so
// repeated unknown names stay consistent without retaining their spelling.
func (s *Sanitizer) safeName(original string, allowed map[string]bool) string {
	if allowed[original] {
		return original
	}
	if result, exists := s.names[original]; exists {
		return result
	}
	result := "field_" + strconv.Itoa(len(s.names)+1)
	s.names[original] = result
	return result
}

func sensitive(key string) bool {
	n := normalized(key)
	for _, part := range []string{"token", "ticket", "jwt", "sesskey", "password", "secret", "credential", "authorization", "cookie", "signature", "apikey", "privatekey"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return n == "auth" || n == "bearer" || n == "key"
}

func idField(key string) bool {
	n := normalized(key)
	return n == "id" || n == "studentnumber" || strings.HasSuffix(n, "id") || strings.HasSuffix(n, "ids")
}

func timeField(key string) bool {
	n := normalized(key)
	return strings.HasSuffix(key, "_at") || strings.HasSuffix(n, "timestamp") || strings.HasSuffix(n, "date") || strings.HasSuffix(n, "time") || n == "timecreated" || n == "timemodified"
}

func (s *Sanitizer) id(value any) any {
	var raw string
	switch v := value.(type) {
	case json.Number:
		if v == "0" || strings.HasPrefix(string(v), "-") {
			return v
		}
		raw = string(v)
	case string:
		if v == "" || v == "0" {
			return v
		}
		raw = v
	default:
		return s.walk(value, "")
	}
	if n, ok := s.ids[raw]; ok {
		return n
	}
	n := len(s.ids) + 1
	s.ids[raw] = n
	return n
}

func (s *Sanitizer) label(original string) int {
	if n, ok := s.strings[original]; ok {
		return n
	}
	n := len(s.strings) + 1
	s.strings[original] = n
	return n
}

func (s *Sanitizer) walk(value any, key string) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any)
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys) // stable mapping regardless of Go map iteration order
		for _, k := range keys {
			if sensitive(k) {
				continue
			}
			outKey := s.safeName(k, schemaFields)
			if _, err := strconv.ParseUint(k, 10, 64); err == nil {
				outKey = toString(s.id(json.Number(k)))
			}
			result[outKey] = s.walk(v[k], k)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = s.walk(item, key)
		}
		return result
	case json.Number:
		if idField(key) {
			return s.id(v)
		}
		if t, ok := yearTime(string(v), key); ok {
			return t.Add(s.shift).Year()
		}
		if t, ok := numericTime(v, key); ok {
			if len(strings.TrimPrefix(string(v), "-")) >= 13 {
				return t.Add(s.shift).UnixMilli()
			}
			return t.Add(s.shift).Unix()
		}
		if !schemaFields[key] {
			// Unknown numeric fields can be phone numbers or coordinates. Their
			// renamed key alone does not make the original value anonymous.
			return s.label("numeric:" + string(v))
		}
		return v
	case string:
		if idField(key) {
			return s.id(v)
		}
		if t, ok := yearTime(v, key); ok {
			return strconv.Itoa(t.Add(s.shift).Year())
		}
		if t, layout, ok := parseTime(v); ok {
			return t.Add(s.shift).Format(layout)
		}
		if allowedEnum(key, v) {
			return v
		}
		return s.text(v, key)
	default:
		return value // booleans and null have no identifying free text
	}
}

func parseTime(value string) (time.Time, string, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02", http.TimeFormat} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, layout, true
		}
	}
	return time.Time{}, "", false
}

func yearTime(value, key string) (time.Time, bool) {
	if normalized(key) != "year" || len(value) != 4 {
		return time.Time{}, false
	}
	year, err := strconv.Atoi(value)
	if err != nil || year < 1900 || year > 2200 {
		return time.Time{}, false
	}
	return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC), true
}

func numericTime(value json.Number, key string) (time.Time, bool) {
	if !timeField(key) {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, false
	}
	if len(string(value)) >= 13 {
		return time.UnixMilli(n), true
	}
	return time.Unix(n, 0), true
}

func earliestTime(value any, key string) (time.Time, bool) {
	var earliest time.Time
	add := func(t time.Time, ok bool) {
		if ok && (earliest.IsZero() || t.Before(earliest)) {
			earliest = t
		}
	}
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			if !sensitive(k) {
				add(earliestTime(item, k))
			}
		}
	case []any:
		for _, item := range v {
			add(earliestTime(item, key))
		}
	case string:
		t, _, ok := parseTime(v)
		add(t, ok)
		add(yearTime(v, key))
	case json.Number:
		add(numericTime(v, key))
		add(yearTime(string(v), key))
	}
	return earliest, !earliest.IsZero()
}

// Only fixed schema enums may retain their original spelling. Unknown fields
// and unknown future enum values always pass through synthetic text generation.
func allowedEnum(key, value string) bool {
	var values string
	switch normalized(key) {
	case "type", "kind":
		values = "document pdf video quiz survey code jupyter rstudio postgres web karel webpage html codecast workspace-partition lesson question answer comment announcement post file link image text multiple-choice multiple_choice multiple-select multiple_select short-answer short_answer"
	case "state", "status":
		values = "active scheduled unattempted attempted completed locked open closed submitted pending correct incorrect passed failed released unpublished published graded not_started in_progress"
	case "role", "courserole":
		values = "student staff mentor admin tutor instructor teacher assistant owner"
	case "extension":
		values = "pdf pptx docx ipynb txt md png jpg jpeg gif svg webp mp4 webm py java js ts go c cpp zip"
	case "mime", "mimetype", "contenttype":
		values = "application/pdf application/zip application/json text/plain text/html text/markdown image/png image/jpeg image/svg+xml video/mp4 video/webm"
	default:
		return false
	}
	return value != "" && slices.Contains(strings.Fields(values), value)
}

var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var courseCode = regexp.MustCompile(`^[A-Z]{3,4}[0-9]{4}$`)

func (s *Sanitizer) text(value, key string) string {
	if value == "" {
		return ""
	}
	n := normalized(key)
	if n == "code" && courseCode.MatchString(value) {
		prefix := "ABC"
		if len(value) == 8 {
			prefix = "DEMO"
		}
		label := strconv.Itoa(s.label(value) % 10000)
		result := prefix + strings.Repeat("0", 4-len(label)) + label
		if result == value {
			result = strings.Repeat("Z", len(prefix)) + result[len(prefix):]
		}
		return result
	}
	if u, err := url.Parse(value); err == nil && (u.Host != "" || strings.Contains(n, "url") || n == "href" || n == "src" || n == "link") {
		return s.url(u)
	}
	if strings.Contains(n, "email") || emailPattern.MatchString(value) {
		return "student-" + strconv.Itoa(s.label(value)) + "@example.invalid"
	}
	if strings.Contains(n, "filename") || n == "file" {
		return s.filename(value)
	}
	if strings.Contains(value, "<") && strings.Contains(value, ">") {
		return s.markup(value)
	}
	if n == "name" || strings.HasSuffix(n, "name") || n == "display" {
		return synthetic(value)
	}
	return synthetic(value)
}

func (s *Sanitizer) filename(value string) string {
	base := path.Base(strings.ReplaceAll(value, "\\", "/"))
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(base)), ".")
	if !allowedEnum("extension", ext) {
		ext = "txt"
	}
	name := "file-" + strconv.Itoa(s.label(value))
	target := utf8.RuneCountInString(strings.TrimSuffix(base, path.Ext(base)))
	if target > len(name) {
		name += strings.Repeat("_", target-len(name))
	}
	return name + "." + ext
}

func (s *Sanitizer) url(u *url.URL) string {
	result := &url.URL{Scheme: "https", Host: "host-" + strconv.Itoa(s.label(u.Host)) + ".example.invalid"}
	segments := strings.Split(u.Path, "/")
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		if _, err := strconv.ParseUint(segment, 10, 64); err == nil {
			segments[i] = toString(s.id(json.Number(segment)))
		} else if path.Ext(segment) != "" {
			stem := strings.TrimSuffix(segment, path.Ext(segment))
			ext := strings.TrimPrefix(strings.ToLower(path.Ext(segment)), ".")
			if _, err := strconv.ParseUint(stem, 10, 64); err == nil && allowedEnum("extension", ext) {
				segments[i] = toString(s.id(json.Number(stem))) + "." + ext
			} else {
				segments[i] = s.filename(segment)
			}
		} else {
			segments[i] = "part-" + strconv.Itoa(s.label(segment))
		}
	}
	result.Path = strings.Join(segments, "/")
	query := make(url.Values)
	keys := make([]string, 0, len(u.Query()))
	for key := range u.Query() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if sensitive(key) {
			continue
		}
		for _, value := range u.Query()[key] {
			query.Add(s.safeName(key, queryNames), toString(s.walk(value, key)))
		}
	}
	result.RawQuery = query.Encode()
	if u.Fragment != "" {
		result.Fragment = "section-" + strconv.Itoa(s.label(u.Fragment))
	}
	return result.String()
}

func (s *Sanitizer) markup(value string) string {
	z := html.NewTokenizer(strings.NewReader(value))
	var result strings.Builder
	for {
		typeID := z.Next()
		switch typeID {
		case html.ErrorToken:
			return result.String()
		case html.TextToken:
			result.WriteString(synthetic(string(z.Text())))
		case html.CommentToken:
			result.WriteString("<!--" + synthetic(string(z.Text())) + "-->")
		case html.DoctypeToken:
			result.WriteString("<!DOCTYPE html>")
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			result.WriteByte('<')
			result.WriteString(s.safeName(token.Data, markupTags))
			for _, attr := range token.Attr {
				if sensitive(attr.Key) {
					continue
				}
				result.WriteByte(' ')
				result.WriteString(s.safeName(attr.Key, markupAttrs))
				result.WriteString("=\"")
				v := s.walk(attr.Val, attr.Key)
				result.WriteString(html.EscapeString(toString(v)))
				result.WriteByte('"')
			}
			if typeID == html.SelfClosingTagToken {
				result.WriteByte('/')
			}
			result.WriteByte('>')
		case html.EndTagToken:
			result.WriteString("</" + s.safeName(z.Token().Data, markupTags) + ">")
		}
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Keep each whitespace rune and the text's rune count, without carrying any
// original letters, punctuation or digits into a fixture.
func synthetic(value string) string {
	const sample = "synthetic text "
	var result strings.Builder
	n := 0
	for _, r := range value {
		if unicode.IsSpace(r) {
			result.WriteRune(r)
			continue
		}
		result.WriteByte(sample[n%len(sample)])
		n++
	}
	return result.String()
}
