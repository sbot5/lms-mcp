// Package httpx is the shared HTTP layer. It enforces lms-mcp's read-only
// invariant at the transport level, adds a stable User-Agent, rate-limits per
// host, retries 429/5xx within a bounded window, and redacts request URLs from
// errors (Moodle file and calendar URLs can carry secrets).
//
// Every outbound request goes through a Guard, which refuses anything the
// read-only policy does not explicitly permit.
package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// UserAgent identifies lms-mcp honestly to the remote site. Some CDNs reject
// Go's default agent, and the project does not impersonate another client.
func UserAgent(version string) string { return "lms-mcp/" + version }

// Guard decides whether a request is allowed under the read-only policy.
// Returning a non-nil error blocks the request before it is sent.
type Guard interface {
	Check(req *http.Request) error
}

// Client is a policy-enforcing HTTP client for one provider/host.
type Client struct {
	hc        *http.Client
	guard     Guard
	userAgent string
	minGap    time.Duration // minimum spacing between requests
	maxRetry  int

	mu   sync.Mutex
	last time.Time
}

// Options configure a Client.
type Options struct {
	Guard        Guard
	UserAgent    string
	Transport    http.RoundTripper // nil uses a default; tests inject one
	Timeout      time.Duration     // 0 => 60s
	RequestsPerS float64           // 0 => 5
	MaxRetry     int               // 0 => 3
	// FollowRedirects, when false (the default), returns the 3xx response to
	// the caller instead of following it. Read-only code inspects Location
	// itself (e.g. resource view -> pluginfile) and must never be redirected
	// to a login or another host silently.
	FollowRedirects bool
}

// New builds a Client. guard must be non-nil.
func New(o Options) (*Client, error) {
	if o.Guard == nil {
		return nil, fmt.Errorf("httpx: Guard is required")
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	rps := o.RequestsPerS
	if rps == 0 {
		rps = 5
	}
	retry := o.MaxRetry
	if retry == 0 {
		retry = 3
	}
	hc := &http.Client{
		Timeout:   timeout,
		Transport: o.Transport,
	}
	if !o.FollowRedirects {
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return &Client{
		hc:        hc,
		guard:     o.Guard,
		userAgent: o.UserAgent,
		minGap:    time.Duration(float64(time.Second) / rps),
		maxRetry:  retry,
	}, nil
}

// redactedError hides the request URL, which may carry a token or sesskey.
type redactedError struct {
	what string
	code int
}

func (e *redactedError) Error() string {
	if e.code != 0 {
		return fmt.Sprintf("%s (HTTP %d)", e.what, e.code)
	}
	return e.what
}

// StatusCode returns the HTTP status that caused the error, or 0.
func StatusCode(err error) int {
	if e, ok := err.(*redactedError); ok {
		return e.code
	}
	return 0
}

// Do runs req through the guard, applies rate limiting and retries, and
// returns the response. The caller owns resp.Body. Errors never contain the
// request URL.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if err := c.guard.Check(req); err != nil {
		return nil, err // guard messages are already URL-free
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	var body []byte
	if req.GetBody != nil {
		// allow retries to resend the body
		rc, err := req.GetBody()
		if err == nil {
			body, _ = io.ReadAll(rc)
			rc.Close()
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if err := c.wait(req.Context()); err != nil {
			return nil, err
		}
		attemptReq := req
		if attempt > 0 && body != nil {
			attemptReq = req.Clone(req.Context())
			attemptReq.Body = io.NopCloser(strings.NewReader(string(body)))
		}
		resp, err := c.hc.Do(attemptReq)
		if err != nil {
			if req.Context().Err() != nil {
				return nil, req.Context().Err()
			}
			// net/http wraps the URL in err; do not surface it.
			lastErr = &redactedError{what: "request failed; check network, site and session"}
		} else if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			lastErr = &redactedError{what: "server temporarily unavailable", code: resp.StatusCode}
			if attempt < c.maxRetry {
				if werrr := sleep(req.Context(), backoff(attempt, retryAfter)); werrr != nil {
					return nil, werrr
				}
				continue
			}
		} else {
			return resp, nil
		}
		if attempt < c.maxRetry && StatusCode(lastErr) == 0 {
			if werrr := sleep(req.Context(), backoff(attempt, 0)); werrr != nil {
				return nil, werrr
			}
		}
	}
	return nil, lastErr
}

func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	gap := time.Until(c.last.Add(c.minGap))
	c.last = time.Now().Add(max(gap, 0))
	c.mu.Unlock()
	if gap > 0 {
		return sleep(ctx, gap)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoffBase is the unit of exponential backoff; tests lower it.
var backoffBase = time.Second

func backoff(attempt int, retryAfter time.Duration) time.Duration {
	d := backoffBase * time.Duration(1<<attempt)
	if retryAfter > d {
		d = retryAfter
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := time.ParseDuration(v + "s"); err == nil {
		return secs
	}
	if when, err := http.ParseTime(v); err == nil {
		return time.Until(when)
	}
	return 0
}

// SameHost reports whether u has the given scheme and host (case-insensitive
// host). Helper for guards.
func SameHost(u *url.URL, scheme, host string) bool {
	return u.Scheme == scheme && strings.EqualFold(u.Host, host)
}
