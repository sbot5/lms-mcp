package store

import (
	"database/sql"
	"errors"
)

// SyncRun is one sync attempt.
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

// "trigger" is a SQLite keyword, so the column is always quoted.
const syncRunCols = `id, scope, "trigger", started_at, finished_at, status, counts_json, error`

// StartSyncRun records a new sync attempt.
func (db *DB) StartSyncRun(r SyncRun) error {
	return db.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sync_runs (`+syncRunCols+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Scope, r.Trigger, r.StartedAt, r.FinishedAt, r.Status, r.CountsJSON, r.Error)
		return err
	})
}

// FinishSyncRun updates the terminal fields of a sync run.
func (db *DB) FinishSyncRun(id, status, countsJSON, errMsg string, finishedAt int64) error {
	return db.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE sync_runs SET status = ?, counts_json = ?, error = ?, finished_at = ? WHERE id = ?`,
			status, countsJSON, errMsg, finishedAt, id)
		return err
	})
}

// LastSyncRun returns the most recently started run for a scope; ok is false
// when none exists.
func (db *DB) LastSyncRun(scope string) (SyncRun, bool, error) {
	var r SyncRun
	err := db.pool.QueryRow(`SELECT `+syncRunCols+` FROM sync_runs WHERE scope = ? ORDER BY started_at DESC, id DESC LIMIT 1`, scope).
		Scan(&r.ID, &r.Scope, &r.Trigger, &r.StartedAt, &r.FinishedAt, &r.Status, &r.CountsJSON, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncRun{}, false, nil
	}
	if err != nil {
		return SyncRun{}, false, err
	}
	return r, true, nil
}
