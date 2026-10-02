package moodle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sbot5/lms-mcp/internal/httpx"
)

func newTestSession(t *testing.T, srv *httptest.Server) *Session {
	t.Helper()
	base, _ := url.Parse(srv.URL)
	doer, err := httpx.New(httpx.Options{Guard: httpx.MoodleGuard{Base: base}, RequestsPerS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return NewSession(doer, base, "MoodleSession=abc", false)
}

func TestPublicConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/lib/ajax/service-nologin.php") {
			http.NotFound(w, r)
			return
		}
		if c := r.Header.Get("Cookie"); c != "" {
			t.Errorf("public config must not send a cookie, got %q", c)
		}
		w.Write([]byte(`[{"error":false,"data":{"wwwroot":"https://m.edu","typeoflogin":1,"enablewebservices":1,"enablemobilewebservice":0,"maintenanceenabled":0,"release":"4.5"}}]`))
	}))
	defer srv.Close()

	pc, err := newTestSession(t, srv).PublicConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pc.TypeOfLogin != 1 || !pc.EnableWebServices || pc.EnableMobileWebService {
		t.Fatalf("parsed config wrong: %+v", pc)
	}
}

func TestSesskeyAndExpiry(t *testing.T) {
	var expired bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if expired {
			http.Redirect(w, r, "/login/index.php", http.StatusSeeOther)
			return
		}
		w.Write([]byte(`<html><script>M.cfg = {"sesskey":"xyz987","contextid":5,"userId":42};</script></html>`))
	}))
	defer srv.Close()
	s := newTestSession(t, srv)

	sk, err := s.Sesskey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sk.Value != "xyz987" || sk.ContextID != 5 || sk.UserID != 42 {
		t.Fatalf("sesskey parse wrong: %+v", sk)
	}

	expired = true
	if err := s.CheckSession(context.Background()); err != ErrSessionExpired {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
}

func TestCoursesAndErrorEnvelope(t *testing.T) {
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/lib/ajax/service.php") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Cookie") == "" {
			t.Error("AJAX call should carry the cookie")
		}
		switch mode {
		case "ok":
			w.Write([]byte(`[{"error":false,"data":{"courses":[{"id":10,"shortname":"ABC1234","fullname":"Algorithms","visible":1},{"id":11,"shortname":"XYZ9999","fullname":"Old","visible":1}]}}]`))
		case "expired":
			w.Write([]byte(`[{"error":true,"exception":{"errorcode":"servicerequireslogin","message":"login required"}}]`))
		}
	}))
	defer srv.Close()
	s := newTestSession(t, srv)

	courses, err := s.Courses(context.Background(), "xyz987")
	if err != nil {
		t.Fatal(err)
	}
	if len(courses) != 2 || courses[0].ShortName != "ABC1234" {
		t.Fatalf("courses wrong: %+v", courses)
	}

	mode = "expired"
	if _, err := s.Courses(context.Background(), "xyz987"); err != ErrSessionExpired {
		t.Fatalf("expected ErrSessionExpired from envelope, got %v", err)
	}
}

func TestAJAXMethodNotAllowlisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should have been blocked by the guard before reaching the server")
	}))
	defer srv.Close()
	s := newTestSession(t, srv)

	_, err := s.AJAX(context.Background(), "sk", "core_user_update_user", map[string]any{})
	if err == nil {
		t.Fatal("non-allowlisted method must be refused")
	}
	// The error must not leak the URL or cookie.
	if strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "MoodleSession") {
		t.Fatalf("error leaked sensitive data: %v", err)
	}
}
