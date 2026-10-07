package anonymize

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func sanitize(t *testing.T, value any) ([]byte, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New().JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b, decode(t, b)
}

func TestReferencesRemainConsistentAndMappingsAreDeterministic(t *testing.T) {
	raw := []byte(`{"items":[{"id":987654,"user_id":765432,"parent_id":null},{"id":765432,"parent_id":"987654","user_ids":[765432,987654],"accepted_id":0}],"users":{"765432":{"id":765432}},"student_number":"11223344"}`)
	a, err := New().JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New().JSON(raw)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("unstable mapping: %s / %s err=%v", a, b, err)
	}
	value := decode(t, a)
	items := value["items"].([]any)
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	if first["id"] != second["parent_id"] || first["user_id"] != second["id"] {
		t.Fatalf("reference mismatch: %s", a)
	}
	ids := second["user_ids"].([]any)
	if ids[0] != second["id"] || ids[1] != first["id"] || second["accepted_id"] != float64(0) {
		t.Fatalf("ID arrays or sentinel IDs changed incorrectly: %s", a)
	}
	users := value["users"].(map[string]any)
	for key, entry := range users {
		if key != toString(entry.(map[string]any)["id"]) {
			t.Fatalf("ID-keyed map does not match: %s", a)
		}
	}
	if strings.Contains(string(a), "987654") || strings.Contains(string(a), "11223344") {
		t.Fatalf("source identifier retained: %s", a)
	}
	// One sanitizer can process an in-memory capture set while retaining IDs.
	s := New()
	x, _ := s.JSON([]byte(`{"id":900,"created_at":"2026-01-01T00:00:00Z"}`))
	y, _ := s.JSON([]byte(`{"parent_id":900,"created_at":"2026-01-02T00:00:00Z"}`))
	if decode(t, x)["id"] != decode(t, y)["parent_id"] {
		t.Fatal("mapping was lost between fixture documents")
	}
	day1, _ := time.Parse(time.RFC3339, decode(t, x)["created_at"].(string))
	day2, _ := time.Parse(time.RFC3339, decode(t, y)["created_at"].(string))
	if day2.Sub(day1) != 24*time.Hour {
		t.Fatal("time shift changed between fixture documents")
	}
}

func TestUnknownStringsNamesAndCredentialsCannotLeak(t *testing.T) {
	secret := "PRIVATE_SENTINEL_" + strings.Repeat("A", 90)
	b, value := sanitize(t, map[string]any{
		"future":  map[string]any{"new_text": secret, "nested": []any{secret}},
		"dynamic": map[string]any{"Sentinel Person": "private", "sentinel-person@private.example": "private"},
		"name":    "Sentinel Person", "email": "sentinel-person@private.example", "student_number": "123456789",
		"authorization": secret, "session_cookie": secret, "accessToken": secret, "ticket": secret, "jWt": secret,
		"status": "completed", "type": "pdf", "future_enum": "completed", "code": "COUR1234",
		"filename": "Personal lecture notes.pdf",
	})
	for _, forbidden := range []string{"PRIVATE_SENTINEL", "Sentinel Person", "sentinel-person", "private.example", "123456789", "COUR1234", "Personal lecture notes"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("identifying value was retained: %s", forbidden)
		}
	}
	for _, key := range []string{"authorization", "session_cookie", "accessToken", "ticket", "jWt"} {
		if _, exists := value[key]; exists {
			t.Fatalf("credential field retained: %s", key)
		}
	}
	if value["status"] != "completed" || value["type"] != "pdf" || value["future_enum"] == "completed" {
		t.Fatal("fixed enums and unknown string fields were not distinguished")
	}
	if !courseCode.MatchString(value["code"].(string)) {
		t.Fatal("synthetic course code lost its schema shape")
	}
	if !strings.HasSuffix(value["filename"].(string), ".pdf") || !strings.HasSuffix(value["email"].(string), "@example.invalid") {
		t.Fatal("synthetic filename/email did not retain useful shape")
	}
}

func TestAllTimesMoveByOneOffset(t *testing.T) {
	first := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	_, value := sanitize(t, map[string]any{
		"created_at":   first.Format(time.RFC3339),
		"due_at":       first.Add(48 * time.Hour).Format(time.RFC3339Nano),
		"release_date": first.Add(24 * time.Hour).Format("2006-01-02"),
		"timestamp":    first.Unix(), "modified_time": first.Add(time.Hour).UnixMilli(),
		"unknown_date_text": first.Add(72 * time.Hour).Format(time.RFC3339),
	})
	shifted, _ := time.Parse(time.RFC3339, value["created_at"].(string))
	if shifted.Equal(first) || shifted.Year() != 2020 {
		t.Fatalf("time was not shifted to a synthetic era: %+v", value)
	}
	for key, delta := range map[string]time.Duration{"due_at": 48 * time.Hour, "release_date": 24 * time.Hour, "unknown_date_text": 72 * time.Hour} {
		got, _, ok := parseTime(value[key].(string))
		if !ok || got.Sub(shifted) != delta {
			t.Fatalf("time relationship changed for %s: %+v", key, value)
		}
	}
	if int64(value["timestamp"].(float64)) != shifted.Unix() || int64(value["modified_time"].(float64)) != shifted.Add(time.Hour).UnixMilli() {
		t.Fatal("numeric times used a different offset")
	}
}

func tags(value string) []string {
	z := html.NewTokenizer(strings.NewReader(value))
	var tags []string
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return tags
		}
		if tt == html.StartTagToken || tt == html.EndTagToken || tt == html.SelfClosingTagToken {
			tags = append(tags, tt.String()+":"+z.Token().Data)
		}
	}
}

func TestXMLHTMLRetainsTagsNewlinesAndTextMagnitude(t *testing.T) {
	text := "A private long paragraph with personal identifiers.\nAnother line 中文 preserved only in shape.\n"
	markup := `<document><paragraph>` + text + `</paragraph><file url="https://private.example/files/999?token=JWT_SENTINEL" filename="Secret personal file.pdf"/><image src="https://private.example/avatar/999.png" ticket="TICKET_SENTINEL"/><b data-future="ATTRIBUTE_SENTINEL">Secret Person</b><!--COMMENT_SENTINEL--></document>`
	b, value := sanitize(t, map[string]any{"id": 999, "content": markup, "unknown": text})
	got := value["content"].(string)
	if strings.Join(tags(got), ",") != strings.Join(tags(markup), ",") {
		t.Fatalf("tag structure changed: %s", got)
	}
	if strings.Count(got, "\n") != strings.Count(markup, "\n") {
		t.Fatal("markup line breaks changed")
	}
	plain := value["unknown"].(string)
	if utf8.RuneCountInString(plain) != utf8.RuneCountInString(text) || strings.Count(plain, "\n") != strings.Count(text, "\n") {
		t.Fatal("free text's length or line breaks changed")
	}
	for _, forbidden := range []string{"private.example", "Secret Person", "Secret personal", "JWT_SENTINEL", "TICKET_SENTINEL", "ATTRIBUTE_SENTINEL", "COMMENT_SENTINEL", "personal identifiers"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("markup leaked %s: %s", forbidden, b)
		}
	}
}

func TestURLCredentialsHostPathAndReferenceAreSanitized(t *testing.T) {
	_, value := sanitize(t, map[string]any{
		"id":         123456,
		"url":        "https://private-user:private-password@school.private.example/files/123456?Token=PRIVATE&sesskey=PRIVATE&id=123456&name=Private#Personal",
		"avatar_url": "https://school.private.example/avatar/123456.png",
		"zero_url":   "https://school.private.example/0?id=0",
	})
	u, err := url.Parse(value["url"].(string))
	if err != nil || u.User != nil || !strings.HasSuffix(u.Hostname(), ".example.invalid") {
		t.Fatalf("unsafe synthetic URL: %v err=%v", u, err)
	}
	if u.Query().Get("Token") != "" || u.Query().Get("sesskey") != "" || u.Query().Get("id") != toString(value["id"]) || !strings.HasSuffix(u.Path, "/"+toString(value["id"])) {
		t.Fatalf("URL identifiers and credentials were not rewritten consistently: %v", u)
	}
	avatar, _ := url.Parse(value["avatar_url"].(string))
	if !strings.HasSuffix(avatar.Path, "/"+toString(value["id"])+".png") {
		t.Fatal("ID in an avatar filename was not remapped consistently")
	}
}

func TestEnrollmentYearAndCodeStaySyntheticAndParseable(t *testing.T) {
	_, value := sanitize(t, map[string]any{
		"year": "2026", "code": "DEMO0001", "created_at": "2026-08-01T00:00:00Z",
	})
	if value["year"] == "2026" || value["year"] != "2020" || value["code"] == "DEMO0001" || !courseCode.MatchString(value["code"].(string)) {
		t.Fatalf("enrollment schema values were leaked or became unparseable: %+v", value)
	}
}

func TestInvalidJSONErrorsHaveNoOriginalInput(t *testing.T) {
	for _, raw := range []string{`{"secret":"PRIVATE_SENTINEL"`, `{} {}`, `[] trailing PRIVATE_SENTINEL`} {
		_, err := New().JSON([]byte(raw))
		if err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
			t.Fatalf("invalid JSON was accepted or disclosed: %v", err)
		}
	}
}
