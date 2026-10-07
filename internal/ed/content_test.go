package ed

import (
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

	"github.com/sbot5/lms-mcp/internal/httpx"
)

func contentFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ed", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestContentEndpointsThroughGuard(t *testing.T) {
	cases := []struct {
		name, path, query string
		get               func(*Client) ([]Document, error)
	}{
		{"lessons", "/api/courses/10/lessons", "", func(c *Client) ([]Document, error) { return c.Lessons(context.Background(), 10) }},
		{"lesson", "/api/lessons/101", "", func(c *Client) ([]Document, error) {
			d, e := c.Lesson(context.Background(), 101)
			return []Document{d}, e
		}},
		{"resources", "/api/courses/10/resources", "", func(c *Client) ([]Document, error) { return c.Resources(context.Background(), 10) }},
		{"questions", "/api/lessons/slides/203/questions", "", func(c *Client) ([]Document, error) { return c.Questions(context.Background(), 203) }},
		{"responses", "/api/lessons/slides/203/questions/responses", "", func(c *Client) ([]Document, error) { return c.Responses(context.Background(), 203) }},
		{"challenge", "/api/challenges/301", "", func(c *Client) ([]Document, error) {
			d, e := c.Challenge(context.Background(), 301)
			return []Document{d}, e
		}},
		{"submissions", "/api/users/1/challenges/301/submissions", "", func(c *Client) ([]Document, error) { return c.Submissions(context.Background(), 1, 301) }},
		{"attempt", "/api/lessons/101/attempts/1", "", func(c *Client) ([]Document, error) {
			d, e := c.LessonAttempt(context.Background(), 101, 1)
			return []Document{d}, e
		}},
		{"mark", "/api/lesson_marks/901", "rubric_items=true", func(c *Client) ([]Document, error) {
			d, e := c.LessonMark(context.Background(), 901)
			return []Document{d}, e
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := contentFixture(t, tc.name)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != tc.path || r.URL.RawQuery != tc.query {
					t.Errorf("unexpected request: method=%s path=%s query=%s", r.Method, r.URL.Path, r.URL.RawQuery)
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
					t.Error("API request did not carry its synthetic credential")
				}
				_, _ = w.Write(fixture)
			}))
			t.Cleanup(srv.Close)
			docs, err := tc.get(newTestClient(t, srv, "synthetic-test-token"))
			if err != nil || len(docs) != 1 {
				t.Fatalf("content = %+v err=%v", docs, err)
			}
			if _, ok := docs[0]["id"].(float64); !ok {
				t.Fatalf("JSON id does not have float64 representation: %T", docs[0]["id"])
			}
			if tc.name == "lessons" && docs[0]["module_name"] != "Synthetic module" {
				t.Fatalf("module metadata was lost: %+v", docs)
			}
			if tc.name == "challenge" || tc.name == "submissions" || tc.name == "attempt" || tc.name == "mark" {
				encoded, err := json.Marshal(docs)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "must-not-persist") || strings.Contains(string(encoded), "unknown_future_permission") {
					t.Fatalf("sensitive fields escaped the provider: %s", encoded)
				}
			}
			if tc.name == "submissions" && (docs[0]["is_released"] != true || docs[0]["user_id"] != float64(1) || docs[0]["code"] != "print('synthetic result')") {
				t.Fatalf("needed own-submission fields were removed: %+v", docs)
			}
		})
	}
}

func TestContentArrayEnvelopes(t *testing.T) {
	for _, body := range []string{`{"resources":[]}`, `[]`, `{"data":[]}`, `{"data":{"resources":[]}}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			t.Cleanup(srv.Close)
			docs, err := newTestClient(t, srv, "").Resources(context.Background(), 10)
			if err != nil || docs == nil || len(docs) != 0 {
				t.Fatalf("explicit empty array = %#v err=%v", docs, err)
			}
		})
	}
	for _, body := range []string{`{}`, `{"other":[]}`, `{"resources":null}`, `{"resources":{}}`, `{"resources":[null]}`, `{"resources":[{}]}`, `{"data":{"other":[]}}`, `{"resources":[]} trailing`, `{"resources":[]} {"resources":[]}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			t.Cleanup(srv.Close)
			if docs, err := newTestClient(t, srv, "").Resources(context.Background(), 10); err == nil {
				t.Fatalf("invalid array reported success: %#v", docs)
			}
		})
	}
}

func TestContentObjectEnvelopesAndSensitiveNestedFields(t *testing.T) {
	for _, body := range []string{
		`{"challenge":{"id":301,"content":"Synthetic reading","workspace":{"ticket":"must-not-persist"}}}`,
		`{"data":{"challenge":{"id":301,"content":"Synthetic reading"}}}`,
		`{"data":{"id":301,"content":"Synthetic reading"}}`,
		`{"id":301,"content":"Synthetic reading"}`,
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			t.Cleanup(srv.Close)
			doc, err := newTestClient(t, srv, "").Challenge(context.Background(), 301)
			if err != nil || doc["id"] != float64(301) || doc["content"] != "Synthetic reading" || len(doc) != 2 {
				t.Fatalf("reading projection = %+v err=%v", doc, err)
			}
		})
	}
	for _, body := range []string{`{}`, `{"challenge":null}`, `{"challenge":[]}`, `{"challenge":{}}`, `{"other":{"id":301}}`, `{"data":{"other":{"id":301}}}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			t.Cleanup(srv.Close)
			if doc, err := newTestClient(t, srv, "").Challenge(context.Background(), 301); err == nil {
				t.Fatalf("invalid object reported success: %+v", doc)
			}
		})
	}
	projected := readingFields(Document{"content": map[string]any{"ticket": "must-not-persist"}, "feedback": map[string]any{"comment": "Synthetic feedback", "jwt": "must-not-persist"}}, "content", "feedback")
	encoded, _ := json.Marshal(projected)
	if strings.Contains(string(encoded), "must-not-persist") || !strings.Contains(string(encoded), "Synthetic feedback") {
		t.Fatalf("nested sensitive fields were retained: %s", encoded)
	}
}

func TestAPIErrorUnavailableAndAuthentication(t *testing.T) {
	cases := []struct {
		status      int
		body        string
		unavailable bool
	}{
		{403, `{}`, true}, {404, `{}`, true}, {401, `{}`, false},
		{400, `{"code":"bad_token","message":"must-not-print"}`, false},
		{403, `{"code":"bad_token"}`, false}, {403, `{"code":"unauthorized"}`, false},
		{200, `{"code":"bad_token"}`, false},
		{400, `{"code":"must-not-print","message":"must-not-print"}`, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.body), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			var out any
			err := newTestClient(t, srv, "").Get(context.Background(), "/user?synthetic_secret=must-not-print", &out)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || IsUnavailable(fmt.Errorf("wrapped: %w", err)) != tc.unavailable {
				t.Fatalf("classification: err=%v status=%+v unavailable=%v", err, apiErr, IsUnavailable(err))
			}
			if strings.Contains(fmt.Sprintf("%+v %v", apiErr, err), "must-not-print") || strings.Contains(err.Error(), srv.URL) {
				t.Fatal("API error disclosed a request URL or response content")
			}
		})
	}
	if IsUnavailable(context.Canceled) || IsUnavailable(nil) {
		t.Fatal("non-API error classified as an unavailable feature")
	}
}

func TestOpenFileConditionalGETAndResourceURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/resources/401/download" || r.URL.RawQuery != "dl=1" {
			t.Error("download request did not use its read-only resource endpoint")
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
			t.Error("same-origin API download lacked its synthetic credential")
		}
		if r.Header.Get("If-None-Match") == `"synthetic-etag"` && r.Header.Get("If-Modified-Since") == "Thu, 01 Jan 2026 01:00:00 GMT" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"synthetic-etag"`)
		_, _ = w.Write([]byte("synthetic attachment"))
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, "synthetic-test-token")
	resp, err := c.OpenFile(context.Background(), c.ResourceURL(401), "", "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "synthetic attachment" || resp.Header.Get("ETag") != `"synthetic-etag"` {
		t.Fatalf("response body/header not handed to caller: body=%q err=%v", body, err)
	}
	resp, err = c.OpenFile(context.Background(), c.ResourceURL(401), `"synthetic-etag"`, "Thu, 01 Jan 2026 01:00:00 GMT")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional result HTTP %d", resp.StatusCode)
	}
}

type contentRoundTripper func(*http.Request) (*http.Response, error)

func (f contentRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestOpenFileRedirectDoesNotSendCredentialToStaticHost(t *testing.T) {
	var fileRequests atomic.Int32
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileRequests.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Method != http.MethodGet {
			t.Error("static attachment received a credential or non-GET request")
		}
		_, _ = w.Write([]byte("synthetic static bytes"))
	}))
	t.Cleanup(fileSrv.Close)
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-test-token" {
			t.Error("initial API download did not receive its credential")
		}
		w.Header().Set("Location", "https://static.au.edusercontent.com/files/example.pdf")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(apiSrv.Close)
	apiBase, _ := url.Parse(apiSrv.URL)
	doer, err := httpx.New(httpx.Options{
		Guard: httpx.EdGuard{APIHost: apiBase.Hostname()}, RequestsPerS: 1000,
		Transport: contentRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.URL.Hostname() == "static.au.edusercontent.com" {
				clone := req.Clone(req.Context())
				clone.URL, _ = url.Parse(fileSrv.URL + req.URL.RequestURI())
				return http.DefaultTransport.RoundTrip(clone)
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(doer, apiSrv.URL+"/api", "synthetic-test-token")
	resp, err := c.OpenFile(context.Background(), c.ResourceURL(401), "", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if fileRequests.Load() != 1 {
		t.Fatalf("static requests = %d", fileRequests.Load())
	}
}

func TestOpenFileCrossPortRedirectDropsCredential(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("different-origin API URL received a credential")
		}
		_, _ = w.Write([]byte("synthetic bytes"))
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.URL+"/api/file")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, "synthetic-test-token")
	resp, err := c.OpenFile(context.Background(), c.ResourceURL(401), "", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestOpenFileRedirectLimitAndReadOnlyGuard(t *testing.T) {
	for _, limit := range []int{5, 6} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(requests.Add(1))
				if n <= limit {
					w.Header().Set("Location", fmt.Sprintf("/api/file/%d", n))
					w.WriteHeader(http.StatusFound)
					return
				}
				_, _ = w.Write([]byte("synthetic bytes"))
			}))
			t.Cleanup(srv.Close)
			c := newTestClient(t, srv, "")
			resp, err := c.OpenFile(context.Background(), c.ResourceURL(401), "", "")
			if limit == 5 {
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
			} else if err == nil || requests.Load() != 6 {
				t.Fatalf("redirect bound not enforced: requests=%d err=%v", requests.Load(), err)
			}
		})
	}
	for _, location := range []string{"https://example.invalid/private?token=must-not-print", "/api/lessons/101?view=1", "/api/file?view=0", "http://synthetic-user:must-not-print@localhost/file", "/api/file?broken=%"} {
		t.Run(location, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))
			t.Cleanup(srv.Close)
			c := newTestClient(t, srv, "")
			if _, err := c.OpenFile(context.Background(), c.ResourceURL(401), "", ""); err == nil || strings.Contains(err.Error(), "must-not-print") || requests.Load() != 1 {
				t.Fatalf("unsafe redirect was followed or disclosed: requests=%d err=%v", requests.Load(), err)
			}
		})
	}
}

func TestOpenFileErrorIsRedactedAndContextIsPreserved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"must-not-print"}`))
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, "")
	if _, err := c.OpenFile(context.Background(), srv.URL+"/api/file?token=must-not-print", "", ""); err == nil || IsUnavailable(err) || strings.Contains(err.Error(), "must-not-print") {
		t.Fatalf("attachment status error was hidden or disclosed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.OpenFile(ctx, c.ResourceURL(401), "", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
}
