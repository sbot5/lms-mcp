package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEdGuardBlocksProgressAndUnsafeCredentials(t *testing.T) {
	g := EdGuard{APIHost: "edstem.org"}
	for _, raw := range []string{
		"https://edstem.org/api/lessons/1?view=1",
		"https://edstem.org/api/lessons/1?view=0&view=1",
		"http://edstem.org/api/user",
		"https://user:private@edstem.org/api/user",
		"https://untrusted.edusercontent.com/files/x",
	} {
		if err := g.Check(mustReq(t, "GET", raw, "")); err == nil {
			t.Errorf("unsafe request allowed: %s", raw)
		}
	}
	req := mustReq(t, "GET", "https://static.au.edusercontent.com/files/x", "")
	req.Header.Set("Authorization", "Bearer private")
	if err := g.Check(req); err == nil {
		t.Error("API credential allowed on a static attachment request")
	}
}

func TestRedirectRechecksReadOnlyPolicy(t *testing.T) {
	var unsafeHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/start" {
			http.Redirect(w, r, "/api/lessons/1?view=1", http.StatusFound)
			return
		}
		unsafeHits.Add(1)
		io.WriteString(w, "changed progress")
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c, err := New(Options{Guard: EdGuard{APIHost: u.Hostname()}, FollowRedirects: true, MaxRetry: -1})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(mustReq(t, "GET", srv.URL+"/api/start", ""))
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || unsafeHits.Load() != 0 {
		t.Fatalf("unsafe redirect: err=%v hits=%d", err, unsafeHits.Load())
	}
}

func TestRedirectDropsCredentialsAcrossOrigins(t *testing.T) {
	var received string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Authorization") + r.Header.Get("Cookie")
		io.WriteString(w, "file")
	}))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer first.Close()
	c, _ := New(Options{Guard: allowAll{}, FollowRedirects: true})
	req := mustReq(t, "GET", first.URL, "")
	req.Header.Set("Authorization", "Bearer private")
	req.Header.Set("Cookie", "session=private")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if received != "" {
		t.Fatal("credentials forwarded to another origin")
	}
}

func TestMoodleGuardRejectsCredentialURLsAndAmbiguousQueries(t *testing.T) {
	g := moodleGuard(t, "https://m.example.edu")
	for _, raw := range []string{
		"https://user:private@m.example.edu/user/preferences.php",
		"https://m.example.edu/user/preferences.php?sesskey=&sesskey=private",
		"https://m.example.edu/user/preferences.php?sesskey=private;other=1",
	} {
		err := g.Check(mustReq(t, "GET", raw, ""))
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Errorf("unsafe query accepted or leaked: %v", err)
		}
	}
}

func TestMoodleAJAXRefusesAmbiguousMethodFields(t *testing.T) {
	g := moodleGuard(t, "https://m.example.edu")
	for _, body := range []string{
		`[{"methodname":"core_user_update_users","METHODNAME":"core_session_time_remaining","args":{}}]`,
		`[{"METHODNAME":"core_session_time_remaining","methodname":"core_user_update_users","args":{}}]`,
		`[{"METHODNAME":"core_session_time_remaining","args":{}}]`,
		`[{"methodname":"core_session_time_remaining","MethodName":"core_user_update_users","args":{}}]`,
	} {
		if err := g.Check(mustReq(t, "POST", "https://m.example.edu/lib/ajax/service.php?sesskey=private", body)); err == nil {
			t.Errorf("ambiguous method fields passed the read-only guard: %s", body)
		}
	}
}
