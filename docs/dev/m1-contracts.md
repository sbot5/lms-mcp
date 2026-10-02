# M1 package contracts

Exact Go signatures for M1 so the packages fit together whoever writes them. Keep this in sync with the code. Rules in `AGENTS.md` win over anything here.

Pinned dependency versions (compatible with the CI matrix go 1.25.x and stable; sqlite 1.60+ needs go 1.26, do not use):

- `modernc.org/sqlite v1.59.0`
- `github.com/danieljoos/wincred v1.2.3`
- `golang.org/x/term v0.35.0`

## internal/secrets

Named credentials. On Windows, Credential Manager; elsewhere and in cloud sessions, environment variables (read-only).

```go
package secrets

import "errors"

// ErrReadOnly is returned by Set and Delete on the environment-backed store.
var ErrReadOnly = errors.New("secrets: environment-backed store is read-only")

// Name is a stable credential key.
type Name string

const (
	EdToken      Name = "ed.token"       // env: ED_API_TOKEN
	MoodleCookie Name = "moodle.cookie"  // env: MOODLE_COOKIE
	MoodleICal   Name = "moodle.ical_url"// env: MOODLE_ICAL_URL
	MoodleBase   Name = "moodle.base_url"// env: MOODLE_BASE_URL
)

// Store reads and writes credentials by Name.
type Store interface {
	Get(n Name) (value string, ok bool, err error)
	Set(n Name, value string) error
	Delete(n Name) error
	Backend() string // "wincred" or "env", for doctor/status output only
}

// Open returns the Credential Manager store on Windows, otherwise the
// environment store. target is the Credential Manager prefix (e.g.
// "lms-mcp"); ignored by the env store.
func Open(target string) (Store, error)

// Proxy reports whether the environment delegates a provider's auth header
// to the agent proxy: ED_AUTH=proxy or MOODLE_AUTH=proxy. The caller then
// sends no Authorization/Cookie header for that provider.
func Proxy(provider string) bool // provider is "ed" or "moodle"
```

Notes:
- The env store maps each Name to the env var in the comment. `Get` returns `ok=false` when the var is empty or unset. On the env store, with `ED_AUTH=proxy`, `Get(EdToken)` returns `ok=false` (there is no token; the proxy injects it) — callers check `Proxy("ed")` to tell "missing" from "delegated". Same for Moodle.
- Windows store: one Generic credential per Name, UserName set to the Name, TargetName `target + ":" + Name`. Never log values.
- Build tags split `store_windows.go` (wincred) from `store_other.go` (returns the env store, so `go build` on Linux/macOS still works and the env store is testable everywhere). `Open` on non-Windows always returns the env store.
- Tests: table-driven on the env store using `t.Setenv`. No wincred calls in tests (they need Windows).

## internal/store

SQLite-backed local index. Wraps `*sql.DB` (driver `sqlite`, modernc.org/sqlite), WAL, migrations, leases, FTS5. No knowledge of Ed/Moodle HTTP; callers pass plain structs.

```go
package store

// Open opens or creates the database at path, sets
// `PRAGMA journal_mode=WAL; busy_timeout=5000; foreign_keys=ON;
// synchronous=NORMAL`, and migrates to the latest schema. Safe for
// concurrent use by multiple processes (WAL + busy_timeout) and goroutines
// (an internal connection pool; serialize writes with a single *sql.Conn or
// a mutex as needed).
func Open(path string) (*DB, error)
func (db *DB) Close() error
func (db *DB) SchemaVersion() (int, error)

// Courses.
type Course struct {
	ID            string // stable key, e.g. "ABC1234-2026S2"
	Code          string
	Term          string
	Title         string
	EdCourseID    int    // 0 if none
	MoodleCourseID int   // 0 if none
	Active        bool
	Excluded      bool
}
func (db *DB) UpsertCourse(c Course) error
func (db *DB) ListCourses() ([]Course, error)
func (db *DB) SetCourseExcluded(id string, excluded bool) error

// Items: posts, announcements, slides, pages, modules, forum posts, etc.
type Item struct {
	ID            string // "ed:thread:123", "moodle:cm:789", "moodle:post:12"
	CourseID      string
	Provider      string // "ed" | "moodle"
	Kind          string // "thread","reply","announcement","slide","material","page","forum_post",...
	ParentID      string // "" if top-level
	Title         string
	URL           string
	AuthorRole    string // "staff","ta","student",""
	AuthorDisplay string // teacher/TA name, or "Student" for peers (D20)
	CreatedAt     int64  // unix seconds, 0 if unknown
	UpdatedAt     int64
	BodyMD        string
	ContentHash   string // caller-computed over normalized content
	MetaJSON      string // provider-specific extras
	RemovedAt     int64  // 0 unless detected gone remotely
}
func (db *DB) UpsertItem(it Item) (changed bool, err error) // changed=true if new or ContentHash differs
func (db *DB) GetItem(id string) (Item, bool, error)
func (db *DB) ListItems(f ItemFilter) ([]Item, error)
func (db *DB) MarkItemsRemoved(courseID, provider, kind string, keepIDs []string) (removed int, err error)

type ItemFilter struct {
	CourseID string
	Provider string
	Kinds    []string
	Since    int64 // UpdatedAt >= Since
	Limit    int   // 0 = no limit
	Offset   int
}

// Files: downloaded materials and attachments.
type File struct {
	ID           string
	ItemID       string
	Name         string
	MIME         string
	Size         int64
	SourceURL    string // token/sesskey stripped before storing
	LocalPath    string
	SHA256       string
	ETag         string
	LastModified string
	Status       string // "ok","video_link","too_large","failed","pending"
	Error        string
}
func (db *DB) UpsertFile(f File) error
func (db *DB) GetFile(id string) (File, bool, error)
func (db *DB) ListFiles(itemID string) ([]File, error)

// Grades, deadlines (schema created now; populated in M2/M5).
type Grade struct {
	CourseID, ItemKey, Name string
	Grade, GradeMax         string
	Percentage              string
	FeedbackMD              string
	GradedAt                int64
	Hash                    string
}
func (db *DB) UpsertGrade(g Grade) (changed bool, err error)
func (db *DB) ListGrades(courseID string) ([]Grade, error)

type Deadline struct {
	CourseID, ItemID, Kind string
	OpensAt, DueAt, CutoffAt int64
	SubmissionStatus         string
	Completed                bool
}
func (db *DB) UpsertDeadline(d Deadline) error
func (db *DB) ListDeadlines(from, to int64) ([]Deadline, error)

// Events: detected changes feeding whats_new and notifications.
type Event struct {
	ID         string // dedup key, e.g. "ed:thread:123:staff_post"
	CourseID   string
	ItemID     string
	Kind       string // staff_post, announcement, reply_to_me, new_material, material_changed, new_grade, feedback, deadline_added, deadline_changed
	DetectedAt int64
	Title      string
	Summary    string
	Baseline   bool  // true for first-import; excluded from "recent changes"
	NotifiedAt int64 // 0 until a notification fired
}
func (db *DB) InsertEvent(e Event) error // idempotent on ID
func (db *DB) ListEvents(f EventFilter) ([]Event, error)
func (db *DB) MarkNotified(ids []string, at int64) error

type EventFilter struct {
	Since     int64
	CourseID  string
	Kinds     []string
	Baseline  *bool // nil = either; false = exclude baseline
	Unnotified bool
	Limit     int
	Offset    int
}

// Full-text search over titles and bodies (FTS5, unicode61). Returns item
// IDs ranked by bm25 with a snippet.
type SearchHit struct {
	ItemID  string
	Title   string
	Snippet string
	Score   float64
}
func (db *DB) ReindexItem(id, title, body string) error // called by UpsertItem; also usable standalone
func (db *DB) Search(query string, f ItemFilter) ([]SearchHit, error)

// sync_runs: one row per sync attempt.
type SyncRun struct {
	ID         string
	Scope      string // "all","ed","moodle","course:<id>"
	Trigger    string // "manual","schedule","mcp"
	StartedAt  int64
	FinishedAt int64
	Status     string // "running","ok","error","partial"
	CountsJSON string
	Error      string
}
func (db *DB) StartSyncRun(r SyncRun) error
func (db *DB) FinishSyncRun(id, status, countsJSON, errMsg string, finishedAt int64) error
func (db *DB) LastSyncRun(scope string) (SyncRun, bool, error)

// Leases: cross-process mutual exclusion for sync.
// Acquire inserts or steals an expired lease and returns ok=false if a live
// lease is held by someone else. Release deletes it. Renew extends it.
func (db *DB) AcquireLease(name, holder string, ttlSeconds int) (ok bool, err error)
func (db *DB) RenewLease(name, holder string, ttlSeconds int) (ok bool, err error)
func (db *DB) ReleaseLease(name, holder string) error

// meta: small key/value (schema version, first-use timestamps, etc.).
func (db *DB) GetMeta(key string) (string, bool, error)
func (db *DB) SetMeta(key, value string) error
```

Notes:
- Migrations: a slice of `func(*sql.Tx) error` applied in order inside a transaction, recording the version in a `schema_migrations` table or `meta`. `Open` runs pending migrations. Define schema v1 with every table above plus the FTS5 virtual table `fts(title, body, content='items', content_rowid=...)` or an external-content table keyed by item id; pick whichever keeps `Search` simple and document it.
- All timestamps are unix seconds (int64). Empty string and 0 mean "unset".
- `UpsertItem` computes nothing; the caller sets `ContentHash`. It returns `changed=true` when the row is new or the stored hash differs, and updates the FTS index. First import is recorded by the caller as a baseline event.
- Writes should be safe under concurrent processes; wrap multi-statement writes in transactions and rely on `busy_timeout`.
- Tests: open a temp-file DB (not `:memory:`, so WAL and multi-conn behave realistically), exercise migrations idempotency, upsert/changed semantics, FTS search (including a CJK and an English query), lease acquire/steal/renew/release across two "holders", and event dedup. Confirm `GOOS=windows go build` works (modernc.org/sqlite is pure Go).
- No logging of credentials or token-bearing URLs; `SourceURL` is already stripped by the caller, but never log row contents wholesale.
```
