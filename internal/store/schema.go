package store

import (
	"database/sql"
	"fmt"
)

// migrations is the schema, as the ordered list of steps that build it from
// empty. The binary brings its own schema forward; there is no migration step
// an operator has to remember.
//
// Append to this list, never edit an entry in it: an edited entry is a schema
// that two binaries disagree about. Because the store is disposable
// (ADR 0001 §6), a migration that would be awkward to write has a second
// option that a real database does not - drop the table and let the queue
// re-derive from GitHub. Reach for that rather than for a clever ALTER.
var migrations = []string{
	// 1: jobs and idempotency history.
	`
	CREATE TABLE jobs (
		id           TEXT PRIMARY KEY,
		kind         TEXT NOT NULL,
		subject_type TEXT NOT NULL,
		subject_num  INTEGER NOT NULL,
		state        TEXT NOT NULL,
		attempts     INTEGER NOT NULL DEFAULT 0,

		-- Unix nanoseconds. NULL means "not scheduled", which is distinct
		-- from "scheduled for the epoch" and must not collapse into it.
		next_run_at  INTEGER,

		-- NULL together, or set together: a lease is a holder and an expiry.
		lease_holder     TEXT,
		lease_expires_at INTEGER,

		CHECK ((lease_holder IS NULL) = (lease_expires_at IS NULL))
	);

	CREATE INDEX jobs_due ON jobs (next_run_at);

	CREATE TABLE idempotency (
		key         TEXT PRIMARY KEY,
		job_id      TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
		reserved_at INTEGER NOT NULL
	);
	`,
	// 2: stays, apart from attempts (#62).
	`
	ALTER TABLE jobs ADD COLUMN stays INTEGER NOT NULL DEFAULT 0;
	`,
	// 3: episodes of an exhausted tier, so a restart carries on counting (#91).
	`
	CREATE TABLE episodes (
		job_id   TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
		running  TEXT NOT NULL,
		deferred TEXT NOT NULL,
		since    INTEGER NOT NULL, -- Unix nanoseconds
		times    INTEGER NOT NULL,
		told     INTEGER NOT NULL DEFAULT 0
	);
	`,
}

// migrate brings db up to len(migrations), using SQLite's own user_version as
// the record of where it is. user_version rather than a table of our own
// because it is already there, already in the file, and already updated inside
// the transaction that does the work.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("store is at schema version %d, this binary knows %d: it was written by a newer afk", version, len(migrations))
	}

	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA user_version does not take a bound parameter, and i+1 is an
		// int we produced, so the formatting is safe here.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record schema version %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}
