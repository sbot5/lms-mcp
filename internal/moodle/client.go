package moodle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

type Client struct {
	base         *url.URL
	auth, secret string
	env          map[string]string
	hc           *http.Client
	mu           sync.Mutex
	lastRequest  time.Time
}
type Response struct {
	Body                        []byte
	URL                         string
	ETag, Modified, ContentType string
	NotModified                 bool
	FileRedirect                bool
}

func NewClient(baseURL, auth string, env map[string]string) (*Client, error) {
	key := "MOODLE_COOKIE"
	if auth == "token" {
		key = "MOODLE_TOKEN"
	}
	secret := env[key]
	if secret == "" {
		return nil, fmt.Errorf("%s is missing or empty in Moodle credential file", key)
	}
	if strings.ContainsAny(secret, "\r\n") {
		return nil, fmt.Errorf("invalid Moodle credential format")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Moodle base URL")
	}
	c := &Client{base: u, auth: auth, secret: secret, env: env, hc: &http.Client{Timeout: 60 * time.Second}}
	c.hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !c.Allowed(req.URL) || strings.Contains(req.URL.Path, "/login/") || (len(via) > 0 && via[0].Method != "GET") {
			return fmt.Errorf("Moodle redirect blocked; refresh the saved session if login expired")
		}
		if len(via) > 0 && strings.Contains(via[0].URL.Path, "/mod/resource/view.php") && strings.Contains(req.URL.Path, "/pluginfile.php/") {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return c, nil
}
func (c *Client) Allowed(u *url.URL) bool {
	clean := path.Clean(u.Path)
	if clean != strings.TrimRight(u.Path, "/") && u.Path != "/" {
		return false
	}
	return u.User == nil && u.Scheme == c.base.Scheme && strings.EqualFold(u.Host, c.base.Host) && (c.base.Path == "" || clean == c.base.Path || strings.HasPrefix(clean, strings.TrimRight(c.base.Path, "/")+"/"))
}
func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.base.String(), "/") + path
}
func CleanURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	q := u.Query()
	for k := range q {
		switch strings.ToLower(k) {
		case "token", "wstoken", "authtoken", "sesskey", "username":
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.User = nil
	u.Fragment = ""
	return u.String()
}

// Errors never include request URLs/bodies: Moodle file and calendar URLs can carry secrets.
func (c *Client) Request(ctx context.Context, method, raw, body string, etag, modified string, credential bool) (Response, error) {
	u, err := url.Parse(raw)
	if err != nil || !c.Allowed(u) {
		return Response{}, fmt.Errorf("Moodle request outside configured site refused")
	}
	if credential && c.auth == "token" && strings.Contains(u.Path, "/webservice/pluginfile.php") {
		q := u.Query()
		q.Set("token", c.secret)
		u.RawQuery = q.Encode()
	}
	for attempt := 0; attempt < 3; attempt++ {
		c.mu.Lock()
		delay := time.Until(c.lastRequest.Add(100 * time.Millisecond))
		if delay > 0 {
			select {
			case <-ctx.Done():
				c.mu.Unlock()
				return Response{}, ctx.Err()
			case <-time.After(delay):
			}
		}
		c.lastRequest = time.Now()
		c.mu.Unlock()
		req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
		if err != nil {
			return Response{}, fmt.Errorf("invalid Moodle request")
		}
		if method == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if credential && c.auth == "cookie" {
			req.Header.Set("Cookie", c.secret)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		if modified != "" {
			req.Header.Set("If-Modified-Since", modified)
		}
		r, err := c.hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			return Response{}, fmt.Errorf("Moodle request failed; check network, site URL and session (details redacted)")
		}
		if r.StatusCode == 429 || r.StatusCode >= 500 {
			r.Body.Close()
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return Response{}, ctx.Err()
				case <-time.After(time.Second * time.Duration(1<<attempt)):
				}
				continue
			}
			return Response{}, fmt.Errorf("Moodle temporarily unavailable (HTTP %d)", r.StatusCode)
		}
		if r.StatusCode == 401 || r.StatusCode == 403 {
			r.Body.Close()
			return Response{}, fmt.Errorf("Moodle access denied; renew the saved credential or check course permissions")
		}
		if r.StatusCode == http.StatusNotModified {
			r.Body.Close()
			return Response{NotModified: true}, nil
		}
		if r.StatusCode >= 300 && r.StatusCode < 400 {
			target, err := r.Location()
			r.Body.Close()
			if err == nil && c.Allowed(target) && strings.Contains(u.Path, "/mod/resource/view.php") && strings.Contains(target.Path, "/pluginfile.php/") {
				return Response{URL: CleanURL(target.String()), FileRedirect: true}, nil
			}
			return Response{}, fmt.Errorf("Moodle redirect refused")
		}
		if r.StatusCode != 200 {
			r.Body.Close()
			return Response{}, fmt.Errorf("Moodle HTTP %d", r.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, (64<<20)+1))
		r.Body.Close()
		if err != nil {
			return Response{}, fmt.Errorf("reading Moodle response failed")
		}
		if len(b) > 64<<20 {
			return Response{}, fmt.Errorf("Moodle response exceeds 64 MiB")
		}
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "text/html") && LooksLikeLogin(b) {
			return Response{}, fmt.Errorf("Moodle session expired; save a fresh Cookie after signing in")
		}
		return Response{Body: b, URL: CleanURL(r.Request.URL.String()), ETag: r.Header.Get("ETag"), Modified: r.Header.Get("Last-Modified"), ContentType: contentType}, nil
	}
	return Response{}, fmt.Errorf("Moodle request exhausted")
}
func LooksLikeLogin(b []byte) bool {
	s := strings.ToLower(string(b))
	return strings.Contains(s, "page-login-index") || strings.Contains(s, `name="logintoken"`) || strings.Contains(s, `name="password"`) || strings.Contains(s, `name='password'`)
}

type Material struct {
	Key, URL, Name, Section string
	Size                    int64
	Modified                int64
}

func (c *Client) APIMaterials(ctx context.Context, courseID int) ([]Material, error) {
	v := url.Values{"wstoken": {c.secret}, "wsfunction": {"core_course_get_contents"}, "moodlewsrestformat": {"json"}, "courseid": {fmt.Sprint(courseID)}}
	r, err := c.Request(ctx, "POST", c.endpoint("/webservice/rest/server.php"), v.Encode(), "", "", true)
	if err != nil {
		return nil, err
	}
	var sections []struct {
		Name    string `json:"name"`
		Modules []struct {
			ID       int `json:"id"`
			Contents []struct {
				Type     string `json:"type"`
				Filename string `json:"filename"`
				Filepath string `json:"filepath"`
				FileURL  string `json:"fileurl"`
				Size     int64  `json:"filesize"`
				Modified int64  `json:"timemodified"`
			} `json:"contents"`
		} `json:"modules"`
	}
	if err = json.Unmarshal(r.Body, &sections); err != nil || sections == nil {
		return nil, fmt.Errorf("Moodle course API failed; check token service permissions (response redacted)")
	}
	items := []Material{}
	for _, s := range sections {
		for _, m := range s.Modules {
			if m.ID <= 0 && len(m.Contents) > 0 {
				return nil, fmt.Errorf("Moodle API returned a module without a valid ID")
			}
			for _, f := range m.Contents {
				if f.Type != "file" {
					continue
				}
				if f.Filename == "" {
					return nil, fmt.Errorf("Moodle API returned a file without a name")
				}
				u, err := url.Parse(f.FileURL)
				if err != nil || !c.Allowed(u) || !strings.Contains(u.Path, "/webservice/pluginfile.php") {
					return nil, fmt.Errorf("Moodle returned an unsupported file origin or endpoint")
				}
				items = append(items, Material{Key: fmt.Sprintf("module-%d:%s%s", m.ID, f.Filepath, f.Filename), URL: CleanURL(f.FileURL), Name: f.Filename, Section: s.Name, Size: f.Size, Modified: f.Modified})
			}
		}
	}
	return items, nil
}

func (c *Client) AuthMode() string { return c.auth }
