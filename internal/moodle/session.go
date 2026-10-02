package moodle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sbot5/lms-mcp/internal/httpx"
)

// ErrSessionExpired means the Moodle session cookie is no longer valid; the
// user must sign in again. Ed sync and the calendar feed are unaffected.
var ErrSessionExpired = errors.New("moodle: session expired; sign in again")

// Session is a browser-session Moodle client. It issues only the read-only
// requests the httpx MoodleGuard permits: the no-login public-config probe,
// cheap page reads, and allowlisted AJAX calls. It never logs the cookie,
// sesskey or a request URL.
type Session struct {
	doer      *httpx.Client
	base      *url.URL
	cookie    string
	proxyAuth bool
}

// NewSession wraps doer (which must enforce an httpx.MoodleGuard for base).
// cookie is the raw "Name=value" header; when proxyAuth is true, cookie is
// empty and the agent proxy injects the Cookie header.
func NewSession(doer *httpx.Client, base *url.URL, cookie string, proxyAuth bool) *Session {
	return &Session{doer: doer, base: base, cookie: cookie, proxyAuth: proxyAuth}
}

func (s *Session) url(path, query string) string {
	u := strings.TrimRight(s.base.String(), "/") + path
	if query != "" {
		u += "?" + query
	}
	return u
}

// PublicConfig is the unauthenticated site probe (tool_mobile_get_public_config).
type PublicConfig struct {
	WWWRoot                string `json:"wwwroot"`
	TypeOfLogin            int    `json:"typeoflogin"`
	LaunchURL              string `json:"launchurl"`
	EnableWebServices      bool   `json:"enablewebservices"`
	EnableMobileWebService bool   `json:"enablemobilewebservice"`
	Maintenance            bool   `json:"maintenanceenabled"`
	Release                string `json:"release"`
	IdentityProviders      []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"identityproviders"`
}

// moodle encodes booleans as 0/1 in some fields; accept both.
func (c *PublicConfig) UnmarshalJSON(b []byte) error {
	type raw struct {
		WWWRoot                string          `json:"wwwroot"`
		TypeOfLogin            int             `json:"typeoflogin"`
		LaunchURL              string          `json:"launchurl"`
		EnableWebServices      flexBool        `json:"enablewebservices"`
		EnableMobileWebService flexBool        `json:"enablemobilewebservice"`
		Maintenance            flexBool        `json:"maintenanceenabled"`
		Release                string          `json:"release"`
		IdentityProviders      json.RawMessage `json:"identityproviders"`
	}
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	c.WWWRoot, c.TypeOfLogin, c.LaunchURL = r.WWWRoot, r.TypeOfLogin, r.LaunchURL
	c.EnableWebServices = bool(r.EnableWebServices)
	c.EnableMobileWebService = bool(r.EnableMobileWebService)
	c.Maintenance = bool(r.Maintenance)
	c.Release = r.Release
	if len(r.IdentityProviders) > 0 {
		_ = json.Unmarshal(r.IdentityProviders, &c.IdentityProviders)
	}
	return nil
}

type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	*f = s == "true" || s == "1" || s == `"1"`
	return nil
}

// PublicConfig probes the site without a cookie.
func (s *Session) PublicConfig(ctx context.Context) (PublicConfig, error) {
	body := ajaxBody("tool_mobile_get_public_config", struct{}{})
	raw, err := s.ajaxData(ctx, "/lib/ajax/service-nologin.php", "info=tool_mobile_get_public_config", body, false)
	if err != nil {
		return PublicConfig{}, err
	}
	var pc PublicConfig
	if err := json.Unmarshal(raw, &pc); err != nil {
		return PublicConfig{}, fmt.Errorf("moodle: could not parse public config")
	}
	return pc, nil
}

// Sesskey holds the session key and identifiers scraped from a page's M.cfg.
type Sesskey struct {
	Value     string
	ContextID int
	UserID    int
}

var (
	reSesskey   = regexp.MustCompile(`"sesskey":"([^"]+)"`)
	reContextID = regexp.MustCompile(`"contextid":(\d+)`)
	reUserID    = regexp.MustCompile(`"userId":(\d+)`)
)

// Sesskey fetches /user/preferences.php (logs no Moodle event) and extracts the
// session key. ErrSessionExpired is returned when the session is invalid.
func (s *Session) Sesskey(ctx context.Context) (Sesskey, error) {
	b, err := s.get(ctx, "/user/preferences.php", "")
	if err != nil {
		return Sesskey{}, err
	}
	m := reSesskey.FindSubmatch(b)
	if m == nil {
		if LooksLikeLogin(b) {
			return Sesskey{}, ErrSessionExpired
		}
		return Sesskey{}, fmt.Errorf("moodle: sesskey not found on the page")
	}
	sk := Sesskey{Value: string(m[1])}
	if c := reContextID.FindSubmatch(b); c != nil {
		fmt.Sscanf(string(c[1]), "%d", &sk.ContextID)
	}
	if u := reUserID.FindSubmatch(b); u != nil {
		fmt.Sscanf(string(u[1]), "%d", &sk.UserID)
	}
	return sk, nil
}

// CheckSession reports whether the session is still valid.
func (s *Session) CheckSession(ctx context.Context) error {
	_, err := s.Sesskey(ctx)
	return err
}

// SessionCourse is one enrolled course from the timeline classification call.
type SessionCourse struct {
	ID        int    `json:"id"`
	ShortName string `json:"shortname"`
	FullName  string `json:"fullname"`
	Visible   int    `json:"visible"`
}

// Courses lists the user's courses via the "all" timeline classification.
func (s *Session) Courses(ctx context.Context, sesskey string) ([]SessionCourse, error) {
	raw, err := s.AJAX(ctx, sesskey, "core_course_get_enrolled_courses_by_timeline_classification",
		map[string]any{"classification": "all", "limit": 0, "offset": 0, "sort": "fullname"})
	if err != nil {
		return nil, err
	}
	var data struct {
		Courses []SessionCourse `json:"courses"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("moodle: could not parse course list")
	}
	return data.Courses, nil
}

// AJAX calls one allowlisted web-service method and returns its data payload.
func (s *Session) AJAX(ctx context.Context, sesskey, methodname string, args any) (json.RawMessage, error) {
	if sesskey == "" {
		return nil, fmt.Errorf("moodle: sesskey required")
	}
	body := ajaxBody(methodname, args)
	query := "sesskey=" + url.QueryEscape(sesskey) + "&info=" + url.QueryEscape(methodname)
	return s.ajaxData(ctx, "/lib/ajax/service.php", query, body, true)
}

// ajaxBody builds the single-call AJAX batch body.
func ajaxBody(methodname string, args any) []byte {
	b, _ := json.Marshal([]map[string]any{{"index": 0, "methodname": methodname, "args": args}})
	return b
}

// ajaxData posts an AJAX batch and returns the first element's data, mapping
// Moodle's error envelope to a Go error.
func (s *Session) ajaxData(ctx context.Context, path, query string, body []byte, withCookie bool) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url(path, query), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("moodle: bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	if withCookie {
		s.setCookie(req)
	}
	b, err := s.do(req)
	if err != nil {
		return nil, err
	}
	return parseAJAX(b)
}

// parseAJAX handles both the normal array envelope and a request-level error
// object.
func parseAJAX(b []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		// Request-level failure: {"error":"...","errorcode":"..."}.
		var e struct {
			Error     string `json:"error"`
			ErrorCode string `json:"errorcode"`
		}
		if json.Unmarshal(trimmed, &e) == nil && (e.Error != "" || e.ErrorCode != "") {
			return nil, ajaxError(e.ErrorCode, e.Error)
		}
		return nil, fmt.Errorf("moodle: unexpected response")
	}
	var arr []struct {
		Error     bool            `json:"error"`
		Data      json.RawMessage `json:"data"`
		Exception struct {
			Message   string `json:"message"`
			ErrorCode string `json:"errorcode"`
		} `json:"exception"`
	}
	if err := json.Unmarshal(trimmed, &arr); err != nil || len(arr) == 0 {
		return nil, fmt.Errorf("moodle: unrecognized AJAX response")
	}
	first := arr[0]
	if first.Error {
		return nil, ajaxError(first.Exception.ErrorCode, first.Exception.Message)
	}
	return first.Data, nil
}

func ajaxError(code, _ string) error {
	switch code {
	case "servicerequireslogin", "requireloginerror", "sessionerroruser", "invalidsesskey":
		return ErrSessionExpired
	case "sitemaintenance", "maintenance":
		return fmt.Errorf("moodle: site under maintenance")
	default:
		// Never include the server message; it can echo request content.
		return fmt.Errorf("moodle: web service error (%s)", code)
	}
}

// get performs a read-only GET and returns the body, mapping a login redirect
// to ErrSessionExpired.
func (s *Session) get(ctx context.Context, path, query string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url(path, query), nil)
	if err != nil {
		return nil, fmt.Errorf("moodle: bad request")
	}
	s.setCookie(req)
	return s.do(req)
}

func (s *Session) setCookie(req *http.Request) {
	if !s.proxyAuth && s.cookie != "" {
		req.Header.Set("Cookie", s.cookie)
	}
}

// do sends req through the guarded client, treating a login/SAML redirect as an
// expired session and capping the body.
func (s *Session) do(req *http.Request) ([]byte, error) {
	resp, err := s.doer.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc, err := resp.Location(); err == nil && isLoginRedirect(loc, s.base) {
			return nil, ErrSessionExpired
		}
		return nil, fmt.Errorf("moodle: unexpected redirect")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("moodle: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("moodle: reading response failed")
	}
	return b, nil
}

// isLoginRedirect reports whether a redirect target is a login/SSO page or a
// different host (all of which mean the session is no longer valid).
func isLoginRedirect(loc, base *url.URL) bool {
	if !strings.EqualFold(loc.Host, base.Host) {
		return true
	}
	p := loc.Path
	return strings.Contains(p, "/login/") || strings.Contains(p, "/auth/")
}
