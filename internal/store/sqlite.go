package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite"
)

// sqliteStore is the Store backed by a SQLite file in the state directory.
type sqliteStore struct {
	db *sql.DB
}

// Open opens or creates the store at path and brings its schema forward.
//
// The driver is modernc.org/sqlite, which is SQLite transpiled to Go rather
// than bound to it through cgo: the binary stays a single static artefact and
// cross-compiles, which a cgo driver would cost us. See ADR 0004 for why the
// store is a database at all, and why this is the repository's one dependency.
func Open(path string) (Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}

	// One connection, so "single writer" is a property of the process rather
	// than a convention its callers have to keep. It also means no transition
	// ever meets SQLITE_BUSY from its own process; busy_timeout in the DSN is
	// for the other process, which ADR 0001 §4 guarantees exists - a hand-run
	// `afk run` alongside a running worker pool.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate store %s: %w", path, err)
	}
	return &sqliteStore{db: db}, nil
}

// dsn builds the connection string, with the pragmas the store depends on.
func dsn(path string) string {
	q := url.Values{}
	// WAL so a reader never blocks the writer, and so a crash recovers from
	// the log rather than from a rollback journal that may not be there.
	q.Add("_pragma", "journal_mode(WAL)")
	// FULL rather than WAL's usual NORMAL. NORMAL can lose the last few
	// transactions to a power cut, and the thing those transactions hold is
	// idempotency keys - losing one is a duplicate comment on someone's pull
	// request. Writes happen once per transition, so the fsync costs nothing
	// we can measure.
	q.Add("_pragma", "synchronous(FULL)")
	// The idempotency -> jobs reference is only a constraint if this is on;
	// SQLite defaults it off.
	q.Add("_pragma", "foreign_keys(on)")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + path + "?" + q.Encode()
}

func (s *sqliteStore) Close() error { return s.db.Close() }

// nullNanos converts a time to the INTEGER-or-NULL the schema stores. The zero
// time is NULL, which is how "not scheduled" stays distinct from "scheduled for
// the epoch".
func nullNanos(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixNano()
}

func fromNanos(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(0, n.Int64).UTC()
}

const jobColumns = `id, kind, subject_type, subject_num, state, attempts, stays, next_run_at, lease_holder, lease_expires_at`

// scanJob reads one row of jobColumns.
func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var (
		j       Job
		next    sql.NullInt64
		holder  sql.NullString
		expires sql.NullInt64
	)
	err := row.Scan(&j.ID, &j.Kind, &j.Subject.Type, &j.Subject.Number, &j.State, &j.Attempts, &j.Stays, &next, &holder, &expires)
	if err != nil {
		return Job{}, err
	}
	j.NextRunAt = fromNanos(next)
	if holder.Valid {
		j.Lease = &Lease{Holder: holder.String, ExpiresAt: fromNanos(expires)}
	}
	return j, nil
}

func (s *sqliteStore) Ensure(ctx context.Context, kind Kind, subject Subject, initialState string, runAt time.Time) (Job, error) {
	if !kind.Valid() {
		return Job{}, fmt.Errorf("unknown job kind %q", kind)
	}
	if !subject.Type.Valid() {
		return Job{}, fmt.Errorf("unknown subject type %q", subject.Type)
	}

	id := ID(kind, subject)
	// DO NOTHING rather than an upsert: an existing job is returned as it is.
	// Re-deriving the queue from GitHub calls this for every eligible subject
	// on every pass, and a pass must not reset the state or the attempt count
	// of work already under way.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs (id, kind, subject_type, subject_num, state, attempts, next_run_at)
		VALUES (?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT (id) DO NOTHING`,
		id, string(kind), string(subject.Type), subject.Number, initialState, nullNanos(runAt))
	if err != nil {
		return Job{}, fmt.Errorf("ensure job %s: %w", id, err)
	}
	return s.Job(ctx, id)
}

func (s *sqliteStore) Job(ctx context.Context, id string) (Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("%w: %s", ErrNoJob, id)
	}
	if err != nil {
		return Job{}, fmt.Errorf("read job %s: %w", id, err)
	}
	return j, nil
}

func (s *sqliteStore) Jobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs ORDER BY next_run_at IS NULL, next_run_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("list jobs: %w", err)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// leaseWhere is the condition under which a job may be leased: nothing holds
// it, what holds it has expired, or the asker already holds it. Reclaiming an
// expired lease needs no operator action, which is the point - the holder may
// simply be dead.
//
// The third arm makes taking a lease you already hold a renewal rather than a
// refusal, which is what a worker restarting under its own name does. Its
// parameters are (now, holder), in that order.
const leaseWhere = `(lease_holder IS NULL OR lease_expires_at <= ? OR lease_holder = ?)`

func (s *sqliteStore) Acquire(ctx context.Context, id, holder string, now time.Time, ttl time.Duration) (Job, bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET lease_holder = ?, lease_expires_at = ?
		WHERE id = ? AND `+leaseWhere,
		holder, now.Add(ttl).UnixNano(), id, now.UnixNano(), holder)
	if err != nil {
		return Job{}, false, fmt.Errorf("acquire lease on %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Job{}, false, fmt.Errorf("acquire lease on %s: %w", id, err)
	}
	if n == 0 {
		// Either the job is not there or a live holder has it. Distinguish,
		// because "no such job" is an operator typo and "held" is not.
		if _, err := s.Job(ctx, id); err != nil {
			return Job{}, false, err
		}
		return Job{}, false, nil
	}
	j, err := s.Job(ctx, id)
	return j, err == nil, err
}

func (s *sqliteStore) Due(ctx context.Context, holder string, now time.Time, ttl time.Duration) (Job, bool, error) {
	// Selecting the job and leasing it are one statement, so two workers
	// calling Due concurrently cannot both come away with it: the second one's
	// subquery no longer matches, because the first one's UPDATE has already
	// set lease_holder.
	row := s.db.QueryRowContext(ctx, `
		UPDATE jobs SET lease_holder = ?, lease_expires_at = ?
		WHERE id = (
			SELECT id FROM jobs
			WHERE next_run_at IS NOT NULL AND next_run_at <= ?
			  AND `+leaseWhere+`
			ORDER BY next_run_at, id
			LIMIT 1
		)
		RETURNING `+jobColumns,
		holder, now.Add(ttl).UnixNano(), now.UnixNano(), now.UnixNano(), holder)

	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("lease next due job: %w", err)
	}
	return j, true, nil
}

func (s *sqliteStore) Commit(ctx context.Context, c Commit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("commit %s: %w", c.JobID, err)
	}
	defer tx.Rollback()

	// The holder check is inside the transaction, so a lease that expires
	// between the check and the write cannot let two holders both commit.
	var (
		dbHolder  sql.NullString
		dbExpires sql.NullInt64
	)
	err = tx.QueryRowContext(ctx, `SELECT lease_holder, lease_expires_at FROM jobs WHERE id = ?`, c.JobID).
		Scan(&dbHolder, &dbExpires)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNoJob, c.JobID)
	}
	if err != nil {
		return fmt.Errorf("commit %s: %w", c.JobID, err)
	}
	if !dbHolder.Valid || dbHolder.String != c.Holder {
		return fmt.Errorf("%w: %s is not held by %q", ErrNotHeld, c.JobID, c.Holder)
	}

	var (
		holder  any = c.Holder
		expires any = dbExpires.Int64
	)
	if c.Release {
		holder, expires = nil, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET state = ?, attempts = ?, stays = ?, next_run_at = ?, lease_holder = ?, lease_expires_at = ?
		WHERE id = ?`,
		c.State, c.Attempts, c.Stays, nullNanos(c.NextRunAt), holder, expires, c.JobID); err != nil {
		return fmt.Errorf("commit %s: %w", c.JobID, err)
	}

	// Same transaction as the state change, which is the whole point: a key
	// written afterwards is a key that a crash in between loses.
	for _, key := range c.Keys {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO idempotency (key, job_id, reserved_at) VALUES (?, ?, ?)
			ON CONFLICT (key) DO NOTHING`,
			key, c.JobID, time.Now().UnixNano()); err != nil {
			return fmt.Errorf("reserve key %q: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", c.JobID, err)
	}
	return nil
}

func (s *sqliteStore) Reserved(ctx context.Context, key string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM idempotency WHERE key = ?`, key).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check key %q: %w", key, err)
	}
	return true, nil
}

func (s *sqliteStore) Release(ctx context.Context, id, holder string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET lease_holder = NULL, lease_expires_at = NULL
		WHERE id = ? AND lease_holder = ?`, id, holder)
	if err != nil {
		return fmt.Errorf("release lease on %s: %w", id, err)
	}
	return nil
}
