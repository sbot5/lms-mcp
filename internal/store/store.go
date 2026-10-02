package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// dsnParams configures every pooled connection. modernc.org/sqlite validates
// these shorthand keys and runs them as PRAGMAs on each new connection, so the
// whole pool (all readers and the writer) shares one configuration:
//
//	journal_mode=WAL    multiple processes read while one writes
//	busy_timeout=5000   wait up to 5s for a competing writer instead of erroring
//	foreign_keys=ON     enforce declared foreign keys
//	synchronous=NORMAL  the safe, fast pairing for WAL
//	_txlock=immediate   BEGIN IMMEDIATE, so a write transaction takes the write
//	                    lock up front instead of failing on a read->write upgrade
//
// Applying them through the DSN (rather than a one-off Exec after Open) is what
// makes them hold for every connection the pool later opens, not just the first.
const dsnParams = "_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1&_synchronous=NORMAL&_txlock=immediate"

// DB is a handle to the local SQLite index. It is safe for concurrent use by
// multiple goroutines and by multiple processes sharing the same file.
type DB struct {
	pool *sql.DB
	wmu  sync.Mutex // held for the duration of every write (and write transaction)
	path string
}

// Open opens or creates the database at path, applies the WAL/pragma
// configuration documented on dsnParams to every connection, and migrates to
// the latest schema. Running it again on an already-migrated database is a
// no-op. It is safe for several processes to Open the same path concurrently.
func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store: empty database path")
	}
	pool, err := sql.Open(driverName, path+"?"+dsnParams)
	if err != nil {
		return nil, err
	}
	db := &DB{pool: pool, path: path}
	if err := pool.Ping(); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.migrate(); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

// Close releases the underlying connection pool.
func (db *DB) Close() error { return db.pool.Close() }

// SchemaVersion reports the highest applied migration version (0 if none).
func (db *DB) SchemaVersion() (int, error) {
	var v int
	if err := db.pool.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// write runs fn inside a single IMMEDIATE transaction while holding the process
// write lock, committing on success and rolling back on any error. All writes
// in this package go through it, which is what serializes writers in-process.
func (db *DB) write(fn func(*sql.Tx) error) error {
	db.wmu.Lock()
	defer db.wmu.Unlock()
	tx, err := db.pool.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// GetMeta returns the value stored for key, or ok=false if the key is unset.
func (db *DB) GetMeta(key string) (string, bool, error) {
	var v string
	err := db.pool.QueryRow(`SELECT value FROM meta WHERE "key" = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta stores value under key, replacing any previous value.
func (db *DB) SetMeta(key, value string) error {
	return db.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO meta ("key", value) VALUES (?, ?)
ON CONFLICT("key") DO UPDATE SET value = excluded.value`, key, value)
		return err
	})
}

// nowUnix returns the current time in unix seconds.
func nowUnix() int64 { return time.Now().Unix() }

// boolToInt maps a Go bool to the 0/1 integer stored in SQLite.
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// appendLimitOffset writes a LIMIT/OFFSET clause. A limit <= 0 means "no limit"
// (SQLite's LIMIT -1); an offset <= 0 is omitted. It returns the extended args.
func appendLimitOffset(b *strings.Builder, args []any, limit, offset int) []any {
	lim := limit
	if lim <= 0 {
		lim = -1
	}
	b.WriteString(" LIMIT ?")
	args = append(args, lim)
	if offset > 0 {
		b.WriteString(" OFFSET ?")
		args = append(args, offset)
	}
	return args
}
