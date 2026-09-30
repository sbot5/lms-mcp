package ed

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

const edBaseURL = "https://edstem.org/api"

type Client struct {
	token       string
	hc          *http.Client
	mu          sync.Mutex
	lastRequest time.Time
}

func NewClient(token string, transport http.RoundTripper) *Client {
	return &Client{token: token, hc: &http.Client{Timeout: 30 * time.Second, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

// get requests edBaseURL+path and decodes the JSON response into out, which must be a pointer.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	for attempt := 0; attempt < 4; attempt++ {
		err := c.getOnce(ctx, path, out)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e, ok := err.(*edHTTPError)
		if !ok || (e.status != 429 && e.status < 500) || attempt == 3 {
			return err
		}
		delay := time.Second * time.Duration(1<<attempt)
		if e.retryAfter > delay {
			delay = e.retryAfter
		}
		if delay > 5*time.Minute {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return fmt.Errorf("retry exhausted")
}

type edHTTPError struct {
	path       string
	status     int
	retryAfter time.Duration
}

func (e *edHTTPError) Error() string {
	return fmt.Sprintf("GET %s: HTTP %d %s", e.path, e.status, http.StatusText(e.status))
}

func (c *Client) getOnce(ctx context.Context, path string, out any) error {
	c.mu.Lock()
	delay := time.Until(c.lastRequest.Add(100 * time.Millisecond))
	if delay > 0 {
		select {
		case <-ctx.Done():
			c.mu.Unlock()
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	c.lastRequest = time.Now()
	c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, edBaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.hc.Do(req)
	if err != nil {
		return err // already reads `Get "<url>": <cause>`, and the URL holds no token
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		delay, _ := time.ParseDuration(resp.Header.Get("Retry-After") + "s")
		if when, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			delay = time.Until(when)
		}
		return &edHTTPError{path, resp.StatusCode, delay}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", path, err)
	}
	return nil
}

type Course struct {
	ID      int    `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Year    string `json:"year"`
	Session string `json:"session" jsonschema:"e.g. Semester 2"`
	Role    string `json:"role" jsonschema:"the user's role in this course, e.g. student"`
}

type WhoamiResult struct {
	UserID  int      `json:"user_id,omitempty"`
	Name    string   `json:"name,omitempty"`
	Courses []Course `json:"courses"`
}

// whoami returns the signed-in user and every course they are enrolled in.
func (c *Client) Whoami(ctx context.Context) (WhoamiResult, error) {
	var resp struct {
		User struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
		Courses []struct {
			Course Course `json:"course"`
			Role   struct {
				Role string `json:"role"`
			} `json:"role"`
		} `json:"courses"`
	}
	if err := c.Get(ctx, "/user", &resp); err != nil {
		return WhoamiResult{}, err
	}

	res := WhoamiResult{UserID: resp.User.ID, Name: resp.User.Name, Courses: make([]Course, 0, len(resp.Courses))}
	for _, e := range resp.Courses {
		co := e.Course
		co.Role = e.Role.Role // Ed sends the role next to the course object, not inside it
		res.Courses = append(res.Courses, co)
	}
	// Ed returns courses in no stable order: newest semester first, then by code.
	slices.SortFunc(res.Courses, func(a, b Course) int {
		return cmp.Or(
			cmp.Compare(b.Year, a.Year),
			cmp.Compare(b.Session, a.Session),
			cmp.Compare(a.Code, b.Code),
		)
	})
	return res, nil
}

// threadPageSize is the limit each list request asks for. Ed silently caps limit (at 100 as of 2026-09),
// so a short page does not prove the list has ended.
const threadPageSize = 100

// Thread is a thread as the list endpoint returns it: the opening post, without replies.
type Thread struct {
	Content           string `json:"content"`
	Category          string `json:"category"`
	Subcategory       string `json:"subcategory"`
	Subsubcategory    string `json:"subsubcategory"`
	IsAnonymous       bool   `json:"is_anonymous"`
	IsAnswered        bool   `json:"is_answered"`
	IsStaffAnswered   bool   `json:"is_staff_answered"`
	IsStudentAnswered bool   `json:"is_student_answered"`
	IsEndorsed        bool   `json:"is_endorsed"`
	IsPrivate         bool   `json:"is_private"`
	IsLocked          bool   `json:"is_locked"`
	IsMegathread      bool   `json:"is_megathread"`
	AcceptedID        *int   `json:"accepted_id"`
	VoteCount         int    `json:"vote_count"`
	ViewCount         int    `json:"view_count"`
	ID                int    `json:"id"`
	CourseID          int    `json:"course_id"`
	Number            int    `json:"number"` // the #N shown in Ed
	UserID            int    `json:"user_id"`
	Type              string `json:"type"` // question, post or announcement
	Title             string `json:"title"`
	Document          string `json:"document"` // plain text; "content" holds the same post as Ed XML
	IsPinned          bool   `json:"is_pinned"`
	ReplyCount        int    `json:"reply_count"` // answers and comments at every depth
	CreatedAt         string `json:"created_at"`  // RFC 3339, e.g. 2026-09-19T14:18:30.09837+10:00
	UpdatedAt         string `json:"updated_at"`
}

// Reply is an answer or a comment. Replies nest: each one has comments of its own.
type Reply struct {
	Content        string  `json:"content"`
	ParentID       *int    `json:"parent_id"`
	IsAnonymous    bool    `json:"is_anonymous"`
	IsResolved     bool    `json:"is_resolved"`
	IsPrivate      bool    `json:"is_private"`
	CreatedByBotID *int    `json:"created_by_bot_id"`
	VoteCount      int     `json:"vote_count"`
	UpdatedAt      string  `json:"updated_at"`
	ID             int     `json:"id"`
	UserID         int     `json:"user_id"`
	Type           string  `json:"type"` // answer or comment
	Document       string  `json:"document"`
	IsEndorsed     bool    `json:"is_endorsed"`
	CreatedAt      string  `json:"created_at"`
	DeletedAt      string  `json:"deleted_at"` // empty unless the reply was deleted
	Comments       []Reply `json:"comments"`
}

// ThreadDetail is a thread with every reply under it.
type ThreadDetail struct {
	Thread
	Answers  []Reply `json:"answers"`
	Comments []Reply `json:"comments"`
}

type User struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	CourseRole string `json:"course_role"` // student, mentor or admin
}

// threads returns every thread in a course that the user can see: pinned first, then newest first.
func (c *Client) Threads(ctx context.Context, courseID int) ([]Thread, error) {
	var all []Thread
	seen := make(map[int]bool)
	for offset := 0; ; {
		path := fmt.Sprintf("/courses/%d/threads?limit=%d&offset=%d&sort=new", courseID, threadPageSize, offset)
		var page struct {
			Threads []Thread `json:"threads"`
		}
		if err := c.Get(ctx, path, &page); err != nil {
			return nil, err
		}
		if page.Threads == nil {
			return nil, fmt.Errorf("GET %s: missing threads array", path)
		}
		if len(page.Threads) == 0 {
			return all, nil // only an empty page ends the list; see threadPageSize
		}
		// Pages are cut by offset, so a thread posted mid-walk pushes an already-seen thread onto the next page.
		added := 0
		for _, t := range page.Threads {
			if !seen[t.ID] {
				seen[t.ID] = true
				all = append(all, t)
				added++
			}
		}
		if added == 0 {
			return nil, fmt.Errorf("GET %s: the whole page repeats earlier threads", path) // stop rather than loop forever
		}
		offset += len(page.Threads)
	}
}

// thread returns one thread with all its replies, plus the users in it keyed by id.
func (c *Client) Thread(ctx context.Context, id int) (ThreadDetail, map[int]User, error) {
	var resp struct {
		Thread ThreadDetail `json:"thread"`
		Users  []User       `json:"users"`
	}
	if err := c.Get(ctx, fmt.Sprintf("/threads/%d", id), &resp); err != nil {
		return ThreadDetail{}, nil, err
	}
	users := make(map[int]User, len(resp.Users))
	for _, u := range resp.Users {
		users[u.ID] = u
	}
	return resp.Thread, users, nil
}
