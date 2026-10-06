package httpx

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// EdGuard permits only GET requests to the regional Ed API host and to Ed's
// static attachment hosts. Everything else is refused.
type EdGuard struct {
	// APIHost is the regional API host, e.g. "edstem.org", "us.edstem.org".
	APIHost string
}

func (g EdGuard) Check(req *http.Request) error {
	if req.Method != http.MethodGet {
		return fmt.Errorf("read-only: Ed allows only GET, not %s", req.Method)
	}
	if !safeRequestURL(req.URL) {
		return fmt.Errorf("read-only: Ed requires HTTPS and a URL without embedded credentials")
	}
	q, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return fmt.Errorf("read-only: invalid Ed request query")
	}
	if _, ok := q["view"]; ok {
		return fmt.Errorf("read-only: Ed lesson progress requests were refused")
	}
	h := strings.ToLower(req.URL.Hostname())
	if h == strings.ToLower(g.APIHost) {
		return nil
	}
	// Attachments live on static.<region>.edusercontent.com.
	if h == "static.au.edusercontent.com" || h == "static.us.edusercontent.com" || h == "static.eu.edusercontent.com" {
		if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" || req.Header.Get("Proxy-Authorization") != "" {
			return fmt.Errorf("read-only: API credentials must not be sent to attachment hosts")
		}
		return nil
	}
	return fmt.Errorf("read-only: Ed request to an unexpected host was refused")
}

// Loopback HTTP is allowed for offline fixtures; real credentials require TLS.
func safeRequestURL(u *url.URL) bool {
	if u == nil || u.User != nil || u.Hostname() == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))
}

// MoodleAllowedMethods is the read-only AJAX allowlist (plan §4.4). Only these
// methodnames may be sent to lib/ajax/service.php.
var MoodleAllowedMethods = map[string]bool{
	"core_course_get_enrolled_courses_by_timeline_classification": true,
	"core_courseformat_get_state":                                 true,
	"core_course_get_updates_since":                               true,
	"core_course_check_updates":                                   true,
	"core_calendar_get_action_events_by_timesort":                 true,
	"core_calendar_get_calendar_upcoming_view":                    true,
	"core_get_fragment":                                           true,
	"core_courseformat_get_overview_information":                  true,
	"mod_forum_get_discussion_posts":                              true,
	"message_popup_get_popup_notifications":                       true,
	"core_message_get_conversations":                              true,
	"core_message_get_conversation_messages":                      true,
	"core_session_time_remaining":                                 true,
}

// moodleGetPaths is the read-only page/file allowlist (path suffixes under the
// Moodle base path). GET is allowed to these; everything else is refused.
var moodleGetPaths = []pathRule{
	{suffix: "/user/preferences.php", exact: true},
	{suffix: "/my/", exact: true},
	{suffix: "/calendar/export.php", exact: true},
	{suffix: "/calendar/export_execute.php", exact: true},
	{suffix: "/course/downloadcontent.php", exact: true},
	{suffix: "/mod/resource/view.php", exact: true},
	{suffix: "/mod/page/view.php", exact: true},
	{suffix: "/mod/book/view.php", exact: true},
	{suffix: "/mod/book/tool/print/index.php", exact: true},
	{suffix: "/mod/assign/view.php", exact: true},
	{suffix: "/mod/quiz/view.php", exact: true},
	{suffix: "/mod/url/view.php", exact: true},
	{suffix: "/grade/report/user/index.php", exact: true},
	{suffix: "/grade/report/overview/index.php", exact: true},
	{suffix: "/pluginfile.php/", exact: false},
	{suffix: "/webservice/pluginfile.php/", exact: false},
}

const (
	moodleAjaxPath    = "/lib/ajax/service.php"
	moodleNoLoginPath = "/lib/ajax/service-nologin.php"
	// publicConfigMethod is the only method allowed on the no-login endpoint.
	publicConfigMethod = "tool_mobile_get_public_config"
)

type pathRule struct {
	suffix string
	exact  bool // exact match of the suffix, vs. prefix match
}

// MoodleGuard enforces the browser-session read-only policy (plan §4):
// same-site only; GET to the page/file allowlist (and never with a sesskey);
// POST only to lib/ajax/service.php and only with allowlisted methodnames.
type MoodleGuard struct {
	Base *url.URL // the Moodle base URL (may include a subdirectory path)
}

func (g MoodleGuard) Check(req *http.Request) error {
	u := req.URL
	if !safeRequestURL(u) || g.Base == nil {
		return fmt.Errorf("read-only: Moodle requires HTTPS and a URL without embedded credentials")
	}
	if !SameHost(u, g.Base.Scheme, g.Base.Host) {
		return fmt.Errorf("read-only: Moodle request outside the configured site was refused")
	}
	clean := path.Clean(u.Path)
	basePath := strings.TrimRight(g.Base.Path, "/")
	if basePath != "" && clean != basePath && !strings.HasPrefix(clean, basePath+"/") {
		return fmt.Errorf("read-only: Moodle request outside the configured site path was refused")
	}

	switch req.Method {
	case http.MethodGet:
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return fmt.Errorf("read-only: invalid Moodle request query")
		}
		if _, ok := q["sesskey"]; ok {
			return fmt.Errorf("read-only: a GET carrying sesskey was refused")
		}
		if g.allowedGet(clean, basePath) {
			return nil
		}
		return fmt.Errorf("read-only: Moodle path %q is not on the read-only allowlist", trimBase(clean, basePath))
	case http.MethodPost:
		switch trimBase(clean, basePath) {
		case moodleAjaxPath:
			return checkAjaxBody(req, MoodleAllowedMethods)
		case moodleNoLoginPath:
			return checkAjaxBody(req, map[string]bool{publicConfigMethod: true})
		default:
			return fmt.Errorf("read-only: Moodle POST is allowed only to the AJAX endpoint")
		}
	default:
		return fmt.Errorf("read-only: Moodle allows only GET and AJAX POST, not %s", req.Method)
	}
}

func (g MoodleGuard) allowedGet(clean, basePath string) bool {
	rel := trimBase(clean, basePath)
	for _, r := range moodleGetPaths {
		if r.exact && rel == path.Clean(r.suffix) {
			return true
		}
		if !r.exact && strings.HasPrefix(rel, r.suffix) {
			return true
		}
	}
	return false
}

func trimBase(clean, basePath string) string {
	if basePath == "" {
		return clean
	}
	if clean == basePath {
		return "/"
	}
	return strings.TrimPrefix(clean, basePath)
}

// checkAjaxBody parses the AJAX batch and refuses any method not in allowed.
func checkAjaxBody(req *http.Request, allowed map[string]bool) error {
	if req.GetBody == nil {
		return fmt.Errorf("read-only: AJAX request without an inspectable body was refused")
	}
	rc, err := req.GetBody()
	if err != nil {
		return fmt.Errorf("read-only: could not inspect the AJAX body")
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return fmt.Errorf("read-only: could not read the AJAX body")
	}
	// Moodle reads the exact lowercase key. Decoding into a Go struct would
	// also accept METHODNAME, so an alias could hide the actual method sent.
	var calls []map[string]json.RawMessage
	if err := json.Unmarshal(b, &calls); err != nil {
		return fmt.Errorf("read-only: AJAX body was not a recognizable batch")
	}
	if len(calls) == 0 {
		return fmt.Errorf("read-only: empty AJAX batch was refused")
	}
	for _, c := range calls {
		for key := range c {
			if key != "methodname" && strings.EqualFold(key, "methodname") {
				return fmt.Errorf("read-only: ambiguous AJAX method field was refused")
			}
		}
		var method string
		if err := json.Unmarshal(c["methodname"], &method); err != nil || !allowed[method] {
			return fmt.Errorf("read-only: AJAX method is not on the allowlist")
		}
	}
	return nil
}
