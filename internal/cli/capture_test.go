package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/httpx"
)

func captureClient(t *testing.T, server *httptest.Server) *ed.Client {
	t.Helper()
	u, _ := url.Parse(server.URL)
	h, err := httpx.New(httpx.Options{Guard: httpx.EdGuard{APIHost: u.Hostname()}, RequestsPerS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return ed.NewClient(h, server.URL+"/api", "synthetic-capture-token")
}

func TestCaptureEnumeratedRoutesAndOwnIdentity(t *testing.T) {
	cases := []struct {
		kind     string
		id       int
		courseID int
		path     string
		query    string
		whoami   bool
	}{
		{"whoami", 0, 0, "/api/user", "", false},
		{"lessons", 0, 91, "/api/courses/91/lessons", "", false},
		{"resources", 0, 91, "/api/courses/91/resources", "", false},
		{"threads", 0, 91, "/api/courses/91/threads", "limit=100&offset=0&sort=new", false},
		{"lesson", 92, 0, "/api/lessons/92", "", false},
		{"questions", 92, 0, "/api/lessons/slides/92/questions", "", false},
		{"responses", 92, 0, "/api/lessons/slides/92/questions/responses", "", false},
		{"challenge", 92, 0, "/api/challenges/92", "", false},
		{"mark", 92, 0, "/api/lesson_marks/92", "rubric_items=true", false},
		{"thread", 92, 0, "/api/threads/92", "", false},
		{"submissions", 92, 0, "/api/users/812/challenges/92/submissions", "", true},
		{"attempt", 92, 0, "/api/lessons/92/attempts/812", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-capture-token" || r.URL.Query().Has("view") {
					t.Error("capture violated read-only/auth invariants")
				}
				if tc.whoami && n == 1 {
					if r.URL.Path != "/api/user" {
						t.Error("own-submission capture did not discover the signed-in identity")
					}
					_, _ = io.WriteString(w, `{"user":{"id":812},"courses":[]}`)
					return
				}
				if r.URL.Path != tc.path || r.URL.RawQuery != tc.query {
					t.Errorf("unexpected route: %s?%s", r.URL.Path, r.URL.RawQuery)
				}
				_, _ = io.WriteString(w, `{"future":{"name":"PRIVATE_NAME_SENTINEL","new_field":"PRIVATE_TEXT_SENTINEL"},"id":812,"user_id":812,"ticket":"PRIVATE_TICKET_SENTINEL","jwt":"PRIVATE_JWT_SENTINEL"}`)
			}))
			t.Cleanup(server.Close)
			dir := t.TempDir()
			file := filepath.Join(dir, "fixture.json")
			err := captureEd(context.Background(), captureClient(t, server), captureOptions{kind: tc.kind, id: tc.id, courseID: tc.courseID, output: file})
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(file)
			if err != nil || !json.Valid(b) || strings.Contains(string(b), "PRIVATE_") || strings.Contains(string(b), "812") {
				t.Fatalf("unsanitized or invalid output: %s err=%v", b, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "fixture.json" {
				t.Fatalf("capture retained another file: %v err=%v", entries, err)
			}
			wantRequests := int32(1)
			if tc.whoami {
				wantRequests++
			}
			if requests.Load() != wantRequests {
				t.Fatalf("requests = %d, want %d", requests.Load(), wantRequests)
			}
		})
	}
}

func TestCaptureRefusesEndpointsCredentialsAndAlternateIdentity(t *testing.T) {
	for _, args := range [][]string{
		{"capture"}, {"capture", "moodle"},
		{"capture", "ed", "-endpoint", "https://example.invalid/private"},
		{"capture", "ed", "-token", "PRIVATE_SENTINEL"},
		{"capture", "ed", "-cookie", "PRIVATE_SENTINEL"},
		{"capture", "ed", "-user-id", "812"},
		{"capture", "ed", "-kind", "lesson", "-id", "92", "-out", "fixture.json", "extra"},
		{"capture", "ed", "-kind", "/lessons/92?view=1", "-id", "92", "-out", "fixture.json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			err := Run(context.Background(), args, strings.NewReader(""), &out, &diagnostics)
			if err == nil || strings.Contains(fmt.Sprint(err)+diagnostics.String()+out.String(), "PRIVATE_SENTINEL") {
				t.Fatalf("unsafe arguments accepted or disclosed: err=%v out=%s diagnostics=%s", err, &out, &diagnostics)
			}
		})
	}
	var help bytes.Buffer
	if err := Run(context.Background(), []string{"help"}, strings.NewReader(""), &help, io.Discard); err != nil || !strings.Contains(help.String(), "capture ed") {
		t.Fatal("capture missing from command help")
	}
}

func TestCaptureValidatesBeforeAnyRequestOrWrite(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	t.Cleanup(server.Close)
	for _, options := range []captureOptions{
		{kind: "lesson", id: 0},
		{kind: "lessons", courseID: 91, id: 12},
		{kind: "whoami", id: 1},
		{kind: "thread", id: -1},
		{kind: "unknown", id: 1},
		{kind: "submissions", id: 1, courseID: 2},
	} {
		dir := t.TempDir()
		options.output = filepath.Join(dir, "fixture.json")
		if err := captureEd(context.Background(), captureClient(t, server), options); err == nil {
			t.Fatalf("invalid capture accepted: %+v", options)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("validation failure wrote a fixture")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid capture contacted the provider")
	}
}

func TestCaptureFailedAuthenticationMissingIdentityAndCancellationWriteNothing(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"user":{},"message":"PRIVATE_SENTINEL"}`)
			}))
			t.Cleanup(server.Close)
			dir := t.TempDir()
			file := filepath.Join(dir, "fixture.json")
			err := captureEd(context.Background(), captureClient(t, server), captureOptions{kind: "submissions", id: 92, output: file})
			if err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
				t.Fatalf("authentication/identity failure ignored or disclosed: %v", err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("failed capture retained a raw or sanitized file")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err = captureEd(ctx, captureClient(t, server), captureOptions{kind: "whoami", output: file})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("capture lost context cancellation: %v", err)
			}
		})
	}
}
