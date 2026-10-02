package store

import (
	"database/sql"
	"fmt"
)

// migrations is the ordered list of schema migrations. Index i builds schema
// version i+1. Open applies every migration past the recorded version, each in
// its own transaction, and records it in schema_migrations; re-running Open
// with no pending migrations does nothing.
var migrations = []func(*sql.Tx) error{
	migrateV1,
}

// migrate creates the bookkeeping table and applies any pending migrations.
func (db *DB) migrate() error {
	db.wmu.Lock()
	defer db.wmu.Unlock()

	if _, err := db.pool.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	applied_at INTEGER NOT NULL
)`); err != nil {
		return err
	}

	var current int
	if err := db.pool.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}

	for i := current; i < len(migrations); i++ {
		version := i + 1
		tx, err := db.pool.Begin()
		if err != nil {
			return err
		}
		if err := migrations[i](tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: apply migration %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, nowUnix()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", version, err)
		}
	}
	return nil
}

func migrateV1(tx *sql.Tx) error {
	for _, stmt := range schemaV1 {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// schemaV1 is the full set of objects for schema version 1. Every table the M1
// contract lists is created now, including those only populated in later
// milestones (grades, deadlines), so the on-disk format is stable from the
// first release.
//
// Booleans are stored as INTEGER 0/1. Timestamps are unix seconds. Columns
// default to ” or 0 so partial writes never store NULL.
//
// Cross-table foreign keys are deliberately NOT declared in v1 even though
// foreign_keys=ON is set. A sync may legitimately write an item before its
// course row, or a file before its item row, and ordering those writes is the
// caller's concern, not a hard constraint that should abort a transaction. The
// pragma is still on so that any FK added in a later migration is enforced.
//
// Full-text search. item_fts is a standalone FTS5 table (unicode61 tokenizer):
// it stores its own copy of each item's title and body and carries the owning
// item's id in an UNINDEXED column. We deliberately do NOT use FTS5
// external-content (content='items', content_rowid=...), because items are
// keyed by an opaque string id such as "ed:thread:123", not an integer rowid;
// external content would force a second id->rowid mapping plus delete/insert
// trigger bookkeeping to keep the shadow index in step. Storing the searchable
// text twice costs a little disk but keeps both sides trivial: ReindexItem
// replaces the row for an id (DELETE then INSERT keyed by item_id) and Search
// is a single MATCH joined back to items on item_id for filtering and metadata.
// Removed items are left in the index; Search filters them out through the join
// (items.removed_at = 0), so no extra FTS deletes are needed on removal.
var schemaV1 = []string{
	`CREATE TABLE courses (
	id               TEXT PRIMARY KEY,
	code             TEXT NOT NULL DEFAULT '',
	term             TEXT NOT NULL DEFAULT '',
	title            TEXT NOT NULL DEFAULT '',
	ed_course_id     INTEGER NOT NULL DEFAULT 0,
	moodle_course_id INTEGER NOT NULL DEFAULT 0,
	active           INTEGER NOT NULL DEFAULT 0,
	is_excluded      INTEGER NOT NULL DEFAULT 0
)`,

	`CREATE TABLE items (
	id             TEXT PRIMARY KEY,
	course_id      TEXT NOT NULL DEFAULT '',
	provider       TEXT NOT NULL DEFAULT '',
	kind           TEXT NOT NULL DEFAULT '',
	parent_id      TEXT NOT NULL DEFAULT '',
	title          TEXT NOT NULL DEFAULT '',
	url            TEXT NOT NULL DEFAULT '',
	author_role    TEXT NOT NULL DEFAULT '',
	author_display TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL DEFAULT 0,
	updated_at     INTEGER NOT NULL DEFAULT 0,
	body_md        TEXT NOT NULL DEFAULT '',
	content_hash   TEXT NOT NULL DEFAULT '',
	meta_json      TEXT NOT NULL DEFAULT '',
	removed_at     INTEGER NOT NULL DEFAULT 0
)`,
	`CREATE INDEX idx_items_course ON items (course_id, provider, kind)`,
	`CREATE INDEX idx_items_updated ON items (updated_at)`,
	`CREATE INDEX idx_items_parent ON items (parent_id)`,

	`CREATE VIRTUAL TABLE item_fts USING fts5 (
	item_id UNINDEXED,
	title,
	body,
	tokenize = 'unicode61'
)`,

	`CREATE TABLE files (
	id            TEXT PRIMARY KEY,
	item_id       TEXT NOT NULL DEFAULT '',
	name          TEXT NOT NULL DEFAULT '',
	mime          TEXT NOT NULL DEFAULT '',
	size          INTEGER NOT NULL DEFAULT 0,
	source_url    TEXT NOT NULL DEFAULT '',
	local_path    TEXT NOT NULL DEFAULT '',
	sha256        TEXT NOT NULL DEFAULT '',
	etag          TEXT NOT NULL DEFAULT '',
	last_modified TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT '',
	error         TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX idx_files_item ON files (item_id)`,

	`CREATE TABLE grades (
	course_id   TEXT NOT NULL DEFAULT '',
	item_key    TEXT NOT NULL DEFAULT '',
	name        TEXT NOT NULL DEFAULT '',
	grade       TEXT NOT NULL DEFAULT '',
	grade_max   TEXT NOT NULL DEFAULT '',
	percentage  TEXT NOT NULL DEFAULT '',
	feedback_md TEXT NOT NULL DEFAULT '',
	graded_at   INTEGER NOT NULL DEFAULT 0,
	hash        TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (course_id, item_key)
)`,

	`CREATE TABLE deadlines (
	course_id         TEXT NOT NULL DEFAULT '',
	item_id           TEXT NOT NULL DEFAULT '',
	kind              TEXT NOT NULL DEFAULT '',
	opens_at          INTEGER NOT NULL DEFAULT 0,
	due_at            INTEGER NOT NULL DEFAULT 0,
	cutoff_at         INTEGER NOT NULL DEFAULT 0,
	submission_status TEXT NOT NULL DEFAULT '',
	completed         INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (course_id, item_id, kind)
)`,
	`CREATE INDEX idx_deadlines_due ON deadlines (due_at)`,

	`CREATE TABLE events (
	id          TEXT PRIMARY KEY,
	course_id   TEXT NOT NULL DEFAULT '',
	item_id     TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL DEFAULT '',
	detected_at INTEGER NOT NULL DEFAULT 0,
	title       TEXT NOT NULL DEFAULT '',
	summary     TEXT NOT NULL DEFAULT '',
	baseline    INTEGER NOT NULL DEFAULT 0,
	notified_at INTEGER NOT NULL DEFAULT 0
)`,
	`CREATE INDEX idx_events_detected ON events (detected_at)`,
	`CREATE INDEX idx_events_course ON events (course_id)`,
	`CREATE INDEX idx_events_unnotified ON events (notified_at)`,

	`CREATE TABLE sync_runs (
	id          TEXT PRIMARY KEY,
	scope       TEXT NOT NULL DEFAULT '',
	"trigger"   TEXT NOT NULL DEFAULT '',
	started_at  INTEGER NOT NULL DEFAULT 0,
	finished_at INTEGER NOT NULL DEFAULT 0,
	status      TEXT NOT NULL DEFAULT '',
	counts_json TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX idx_sync_runs_scope ON sync_runs (scope, started_at)`,

	`CREATE TABLE leases (
	name        TEXT PRIMARY KEY,
	holder      TEXT NOT NULL DEFAULT '',
	acquired_at INTEGER NOT NULL DEFAULT 0,
	expires_at  INTEGER NOT NULL DEFAULT 0
)`,

	`CREATE TABLE meta (
	"key" TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
)`,
}
