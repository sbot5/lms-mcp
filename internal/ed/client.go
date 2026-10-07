package ed

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/sbot5/lms-mcp/internal/httpx"
)

// RegionHost maps a region code to the Ed API host.
func RegionHost(region string) string {
	switch region {
	case "us":
		return "us.edstem.org"
	case "eu":
		return "eu.edstem.org"
	default:
		return "edstem.org"
	}
}

// Client talks to the Ed API through the shared read-only HTTP layer.
type Client struct {
	doer  *httpx.Client
	base  string // e.g. https://edstem.org/api
	token string // "" when the agent proxy injects Authorization (ED_AUTH=proxy)
}

// NewClient builds an Ed client against baseURL (e.g. https://edstem.org/api).
// doer must already enforce EdGuard. token may be empty, in which case no
// Authorization header is sent and the agent proxy is expected to add it.
func NewClient(doer *httpx.Client, baseURL, token string) *Client {
	return &Client{doer: doer, base: baseURL, token: token}
}

// APIBaseURL is the Ed API base URL for a region code.
func APIBaseURL(region string) string {
	return "https://" + RegionHost(region) + "/api"
}

// Get requests base+path and decodes the JSON response into out (a pointer).
// Rate limiting and 429/5xx retries happen in the httpx layer.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return errors.New("ed: invalid API request")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return err // httpx already redacts the URL
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil || len(b) > 32<<20 {
		return errors.New("ed: could not read API response within 32 MiB")
	}
	var failure struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(b, &failure) == nil && (failure.Code == "bad_token" || failure.Code == "unauthorized") {
		return &APIError{StatusCode: resp.StatusCode, Code: failure.Code}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errors.New("ed: invalid JSON response")
	}
	return nil
}

// APIError reports an unsuccessful Ed response without retaining its URL,
// headers or body. Code is populated only for recognized authentication errors.
type APIError struct {
	StatusCode int
	Code       string
}

func (e *APIError) Error() string {
	if e.Code == "bad_token" || e.Code == "unauthorized" {
		return fmt.Sprintf("ed: authentication failed (HTTP %d)", e.StatusCode)
	}
	return fmt.Sprintf("ed: HTTP %d", e.StatusCode)
}

// IsUnavailable distinguishes an absent/forbidden optional feature from a
// broken credential. Authentication failures must still stop the caller.
func IsUnavailable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code != "bad_token" && apiErr.Code != "unauthorized" &&
		(apiErr.StatusCode == http.StatusForbidden || apiErr.StatusCode == http.StatusNotFound)
}

func responseError(resp *http.Response) error {
	err := &APIError{StatusCode: resp.StatusCode}
	var body struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body) == nil {
		switch strings.ToLower(body.Code) {
		case "bad_token", "unauthorized":
			err.Code = strings.ToLower(body.Code)
		}
	}
	return err
}

type Course struct {
	Features *CourseFeatures `json:"features,omitempty"`
	ID       int             `json:"id"`
	Code     string          `json:"code"`
	Name     string          `json:"name"`
	Year     string          `json:"year"`
	Session  string          `json:"session" jsonschema:"e.g. Semester 2"`
	Role     string          `json:"role" jsonschema:"the user's role in this course, e.g. student"`
}

// Nil flags are unknown; only an explicit false means a tab is disabled.
type CourseFeatures struct {
	Lessons   *bool `json:"lessons,omitempty"`
	Resources *bool `json:"resources,omitempty"`
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
