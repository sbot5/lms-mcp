// Package store is the local SQLite index that mirrors Ed and Moodle course
// data for lms-mcp.
//
// It wraps a *sql.DB using the pure-Go modernc.org/sqlite driver (so
// GOOS=windows cross-builds keep working without CGO) and owns the schema,
// migrations, full-text search, cross-process leases and the small key/value
// metadata table. It knows nothing about Ed or Moodle HTTP: callers pass plain
// structs and precomputed content hashes.
//
// Concurrency model. The database may be opened by several processes at once;
// WAL mode plus a 5s busy_timeout let one process write while others read and
// make a competing writer wait rather than fail. Within a single process reads
// go straight to the connection pool and writes are serialized by an in-process
// mutex, so two goroutines never race for SQLite's single write lock and the
// driver never surfaces SQLITE_BUSY from local contention. Every multi-statement
// write runs inside one IMMEDIATE transaction.
//
// Timestamps are unix seconds (int64); empty string and 0 mean "unset". Nothing
// here logs row contents, credentials or URLs: SourceURL arrives already
// stripped and is merely stored.
package store
