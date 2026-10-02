package store

import (
	"database/sql"
	"errors"
)

// AcquireLease grants the named lease to holder for ttlSeconds. It returns:
//
//   - ok=true, inserting the lease, when no lease is held;
//   - ok=true, refreshing it, when the same holder already holds it;
//   - ok=true, stealing it, when a different holder's lease has expired;
//   - ok=false, changing nothing, when a different holder holds a live lease.
func (db *DB) AcquireLease(name, holder string, ttlSeconds int) (ok bool, err error) {
	err = db.write(func(tx *sql.Tx) error {
		now := nowUnix()
		expires := now + int64(ttlSeconds)

		var (
			curHolder  string
			curExpires int64
		)
		switch e := tx.QueryRow(`SELECT holder, expires_at FROM leases WHERE name = ?`, name).Scan(&curHolder, &curExpires); {
		case errors.Is(e, sql.ErrNoRows):
			if _, err := tx.Exec(`INSERT INTO leases (name, holder, acquired_at, expires_at) VALUES (?, ?, ?, ?)`,
				name, holder, now, expires); err != nil {
				return err
			}
			ok = true
		case e != nil:
			return e
		case curHolder == holder:
			// Same holder: refresh the expiry.
			if _, err := tx.Exec(`UPDATE leases SET expires_at = ? WHERE name = ?`, expires, name); err != nil {
				return err
			}
			ok = true
		case curExpires <= now:
			// Different holder but expired: steal it.
			if _, err := tx.Exec(`UPDATE leases SET holder = ?, acquired_at = ?, expires_at = ? WHERE name = ?`,
				holder, now, expires, name); err != nil {
				return err
			}
			ok = true
		default:
			// Different holder, still live.
			ok = false
		}
		return nil
	})
	return ok, err
}

// RenewLease extends the lease only if holder still owns it. ok is false when a
// different holder owns it or no lease exists.
func (db *DB) RenewLease(name, holder string, ttlSeconds int) (ok bool, err error) {
	err = db.write(func(tx *sql.Tx) error {
		expires := nowUnix() + int64(ttlSeconds)
		res, e := tx.Exec(`UPDATE leases SET expires_at = ? WHERE name = ? AND holder = ?`, expires, name, holder)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		ok = n > 0
		return nil
	})
	return ok, err
}

// ReleaseLease deletes the lease only if holder owns it. Releasing a lease that
// is absent or held by someone else is a no-op, not an error.
func (db *DB) ReleaseLease(name, holder string) error {
	return db.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM leases WHERE name = ? AND holder = ?`, name, holder)
		return err
	})
}
