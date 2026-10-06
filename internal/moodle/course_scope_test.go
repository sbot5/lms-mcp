package moodle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sbot5/lms-mcp/internal/httpx"
)

func TestCoursesUsesCurrentTimeline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch []struct {
			Args struct {
				Classification string `json:"classification"`
			} `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch) != 1 {
			t.Errorf("invalid request batch: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if batch[0].Args.Classification != "inprogress" {
			t.Errorf("classification = %q, want inprogress", batch[0].Args.Classification)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"error":false,"data":{"courses":[{"id":8,"shortname":"ABC1234","fullname":"Current course","visible":1}]}}]`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	doer, err := httpx.New(httpx.Options{Guard: httpx.MoodleGuard{Base: base}})
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(doer, base, "fixture-session", false)
	courses, err := session.Courses(context.Background(), "fixture-key")
	if err != nil || len(courses) != 1 || courses[0].ID != 8 {
		t.Fatalf("courses: %+v, %v", courses, err)
	}
}
