package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustReq(t *testing.T, method, raw, body string) *http.Request {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, raw, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		b := body
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(b)), nil }
	}
	return req
}

func TestEdGuard(t *testing.T) {
	g := EdGuard{APIHost: "edstem.org"}
	ok := []*http.Request{
		mustReq(t, "GET", "https://edstem.org/api/user", ""),
		mustReq(t, "GET", "https://static.au.edusercontent.com/files/x", ""),
	}
	for _, r := range ok {
		if err := g.Check(r); err != nil {
			t.Errorf("want allow %s %s, got %v", r.Method, r.URL, err)
		}
	}
	bad := []*http.Request{
		mustReq(t, "POST", "https://edstem.org/api/threads", ""),
		mustReq(t, "GET", "https://evil.example/api/user", ""),
		mustReq(t, "GET", "https://edstem.org.evil.com/api/user", ""),
	}
	for _, r := range bad {
		if err := g.Check(r); err == nil {
			t.Errorf("want refuse %s %s", r.Method, r.URL)
		}
	}
}

func moodleGuard(t *testing.T, base string) MoodleGuard {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	return MoodleGuard{Base: u}
}

func TestMoodleGuardGet(t *testing.T) {
	g := moodleGuard(t, "https://m.example.edu")
	allow := []string{
		"https://m.example.edu/user/preferences.php",
		"https://m.example.edu/pluginfile.php/123/mod_resource/content/1/a.pdf",
		"https://m.example.edu/mod/resource/view.php?id=9&redirect=1",
		"https://m.example.edu/calendar/export_execute.php?userid=1&authtoken=t",
	}
	for _, u := range allow {
		if err := g.Check(mustReq(t, "GET", u, "")); err != nil {
			t.Errorf("want allow GET %s, got %v", u, err)
		}
	}
	refuse := []string{
		"https://m.example.edu/login/logout.php?sesskey=abc",
		"https://m.example.edu/mod/forum/post.php",
		"https://m.example.edu/user/preferences.php?sesskey=abc", // sesskey on GET
		"https://other.example.edu/user/preferences.php",
	}
	for _, u := range refuse {
		if err := g.Check(mustReq(t, "GET", u, "")); err == nil {
			t.Errorf("want refuse GET %s", u)
		}
	}
}

func TestMoodleGuardAjax(t *testing.T) {
	g := moodleGuard(t, "https://m.example.edu")
	okBody := `[{"index":0,"methodname":"core_courseformat_get_state","args":{}}]`
	if err := g.Check(mustReq(t, "POST", "https://m.example.edu/lib/ajax/service.php?sesskey=k", okBody)); err != nil {
		t.Errorf("want allow allowlisted AJAX, got %v", err)
	}
	badMethod := `[{"index":0,"methodname":"core_course_edit_module","args":{}}]`
	if err := g.Check(mustReq(t, "POST", "https://m.example.edu/lib/ajax/service.php?sesskey=k", badMethod)); err == nil {
		t.Error("want refuse non-allowlisted AJAX method")
	}
	mixed := `[{"methodname":"core_courseformat_get_state"},{"methodname":"core_user_update_user"}]`
	if err := g.Check(mustReq(t, "POST", "https://m.example.edu/lib/ajax/service.php?sesskey=k", mixed)); err == nil {
		t.Error("want refuse batch containing a non-allowlisted method")
	}
	if err := g.Check(mustReq(t, "POST", "https://m.example.edu/mod/forum/post.php", okBody)); err == nil {
		t.Error("want refuse POST to a non-AJAX path")
	}

	// The no-login public-config probe is allowed only for that one method.
	probe := `[{"index":0,"methodname":"tool_mobile_get_public_config","args":{}}]`
	if err := g.Check(mustReq(t, "POST", "https://m.example.edu/lib/ajax/service-nologin.php?info=x", probe)); err != nil {
		t.Errorf("want allow public-config probe, got %v", err)
	}
	if err := g.Check(mustReq(t, "POST", "https://m.example.edu/lib/ajax/service-nologin.php", okBody)); err == nil {
		t.Error("no-login endpoint must reject non-public-config methods")
	}
}

func TestMoodleGuardSubdir(t *testing.T) {
	g := moodleGuard(t, "https://host.edu/moodle")
	if err := g.Check(mustReq(t, "GET", "https://host.edu/moodle/user/preferences.php", "")); err != nil {
		t.Errorf("subdir allow failed: %v", err)
	}
	if err := g.Check(mustReq(t, "GET", "https://host.edu/user/preferences.php", "")); err == nil {
		t.Error("path outside the base subdir should be refused")
	}
}

// allowAll is a permissive guard for exercising Client mechanics.
type allowAll struct{}

func (allowAll) Check(*http.Request) error { return nil }

func TestClientRetriesAndRedacts(t *testing.T) {
	old := backoffBase
	backoffBase = time.Millisecond
	defer func() { backoffBase = old }()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	c, err := New(Options{Guard: allowAll{}, RequestsPerS: 1000, MaxRetry: 3})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(mustReq(t, "GET", srv.URL, ""))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if hits != 3 {
		t.Fatalf("expected 3 attempts, got %d", hits)
	}
}

func TestClientGuardBlocks(t *testing.T) {
	c, _ := New(Options{Guard: EdGuard{APIHost: "edstem.org"}, RequestsPerS: 1000})
	_, err := c.Do(mustReq(t, "POST", "https://edstem.org/api/x", ""))
	if err == nil {
		t.Fatal("guard should block POST")
	}
	if strings.Contains(err.Error(), "edstem.org/api/x") {
		t.Fatal("error must not contain the URL")
	}
}

// failTransport returns a url.Error that embeds the request URL, as net/http
// does, so the test can confirm the Client strips it.
type failTransport struct{}

func (failTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: fmt.Errorf("dial tcp: refused")}
}

func TestClientErrorRedactsURL(t *testing.T) {
	old := backoffBase
	backoffBase = time.Millisecond
	defer func() { backoffBase = old }()
	c, _ := New(Options{Guard: allowAll{}, RequestsPerS: 1000, Transport: failTransport{}})
	_, err := c.Do(mustReq(t, "GET", "https://host.example/secret?token=abc", ""))
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "token=abc") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked the URL: %v", err)
	}
}
