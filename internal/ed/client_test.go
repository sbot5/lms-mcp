package ed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sbot5/lms-mcp/internal/httpx"
)

// newTestClient wires an Ed client to srv through a real EdGuard, so the test
// also exercises the read-only transport.
func newTestClient(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	doer, err := httpx.New(httpx.Options{
		Guard:        httpx.EdGuard{APIHost: u.Hostname()},
		RequestsPerS: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewClient(doer, srv.URL+"/api", token)
}

func TestWhoamiThroughGuard(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/user" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"user":{"id":7,"name":"Me"},"courses":[
			{"course":{"id":1,"code":"ABC1234","name":"Algorithms","year":"2026","session":"Sem 2"},"role":{"role":"student"}},
			{"course":{"id":2,"code":"XYZ9999","name":"Old","year":"2025","session":"Sem 1"},"role":{"role":"student"}}
		]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "tok-abc")
	res, err := c.Whoami(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Fatalf("Authorization = %q, want Bearer tok-abc", gotAuth)
	}
	if res.UserID != 7 || len(res.Courses) != 2 {
		t.Fatalf("unexpected whoami: %+v", res)
	}
	// Newest session first.
	if res.Courses[0].Code != "ABC1234" {
		t.Fatalf("sort order wrong: %+v", res.Courses)
	}
}

func TestProxyModeSendsNoAuthHeader(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.Write([]byte(`{"user":{"id":1,"name":""},"courses":[]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "") // proxy mode: empty token
	if _, err := c.Whoami(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hadAuth {
		t.Fatal("proxy mode must not send an Authorization header")
	}
}
