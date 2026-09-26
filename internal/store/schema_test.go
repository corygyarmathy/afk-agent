package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
	_ "modernc.org/sqlite"
)

// openRaw opens the store file directly, to inspect the schema the package
// built rather than the API it exposes.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newStoreFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Something has to be written before the WAL carries the schema.
	if _, err := s.Ensure(context.Background(), store.KindReview, store.Subject{Type: store.SubjectPR, Number: 1}, "start", time.Now()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	s.Close()
	return path
}

// allowed is every column the store may hold, with the reason it is run state
// rather than work state.
//
// This list is the enforcement of ADR 0001 §5 and of the issue's last
// acceptance criterion: "No table holds anything re-derivable from GitHub.
// This is the invariant worth a reviewer's attention; it is the one that decays
// quietly." A column added to save a round trip will fail here, which is the
// point - the failure is the conversation about whether it belongs.
//
// Before adding an entry, ask whether GitHub could answer it. An issue title, a
// body, a label, a comment, a CI conclusion, a diff, a branch name and a review
// verdict all can, and so none of them may appear below.
var allowed = map[string]map[string]string{
	"jobs": {
		"id":               "derived from kind and subject; the local name for the job",
		"kind":             "which transitions this job may take - the agent's own routing, not the tracker's",
		"subject_type":     "half of the pointer at GitHub; a pointer is not a copy",
		"subject_num":      "the other half of the pointer",
		"state":            "where this job is in its own state machine; GitHub has no opinion about it",
		"attempts":         "run state: how many times this has been tried locally",
		"stays":            "run state: how many local runs in a row chose to stay, which picks the candidate model",
		"next_run_at":      "scheduling; ADR 0001 §3 keeps waiting out of process, so it has to live somewhere",
		"lease_holder":     "a lease is local and has no tracker equivalent (CONTEXT.md: lease vs claim)",
		"lease_expires_at": "the expiry that makes a dead holder's job reclaimable",
	},
	"idempotency": {
		"key":         "the dedup history ADR 0001 §5 requires; nothing in GitHub records it",
		"job_id":      "which job reserved it, so it goes when the job goes",
		"reserved_at": "run state, for pruning history that can no longer matter",
	},
}

func TestNoRederivableColumns(t *testing.T) {
	db := openRaw(t, newStoreFile(t))
	ctx := context.Background()

	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(tables)

	for _, table := range tables {
		want, ok := allowed[table]
		if !ok {
			t.Errorf("table %q is not in the allow-list in schema_test.go.\n"+
				"Every table here must hold run state only (ADR 0001 §5). If this one does, "+
				"add it with the reason; if it holds anything GitHub could answer, it does not belong.", table)
			continue
		}
		got := columnsOf(t, db, table)
		for _, col := range got {
			if _, ok := want[col]; !ok {
				t.Errorf("column %s.%s is not in the allow-list in schema_test.go.\n"+
					"State the reason it is run state rather than something re-derivable from GitHub, "+
					"or take it out.", table, col)
			}
		}
		// The list going stale in the other direction is worth catching too:
		// a reason kept for a column that no longer exists is documentation
		// describing a schema nobody has.
		for col := range want {
			if !contains(got, col) {
				t.Errorf("the allow-list names %s.%s, which the schema no longer has; remove the entry", table, col)
			}
		}
	}

	for table := range allowed {
		if !contains(tables, table) {
			t.Errorf("the allow-list names table %q, which the schema no longer has; remove the entry", table)
		}
	}
}

func columnsOf(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	// PRAGMA does not take a bound parameter; table comes from sqlite_master,
	// not from anything a caller supplied.
	rows, err := db.Query(`SELECT name FROM pragma_table_info('` + table + `')`)
	if err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(cols)
	return cols
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestStoreIsInWALMode pins the two pragmas the store's crash behaviour depends
// on, because both are silently ignorable: a DSN typo leaves the file in
// SQLite's default journal mode and nothing reports it.
func TestStoreIsInWALMode(t *testing.T) {
	db := openRaw(t, newStoreFile(t))

	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q; want wal", mode)
	}
}

// TestSchemaVersionIsRecorded shows the store knows how far its schema has come,
// which is what lets a later binary bring it forward without an operator step.
func TestSchemaVersionIsRecorded(t *testing.T) {
	db := openRaw(t, newStoreFile(t))

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version < 1 {
		t.Errorf("user_version = %d after opening a store; want the migrations to have recorded themselves", version)
	}
}

// A store an older binary wrote is brought forward on open, and the jobs in it
// keep their place: a job that was under way has no stays yet, and runs its
// first candidate next.
func TestAStoreFromAnOlderBinaryIsBroughtForward(t *testing.T) {
	path := newStoreFile(t)
	db := openRaw(t, path)
	if _, err := db.Exec(`UPDATE jobs SET state = 'reviewing', attempts = 2`); err != nil {
		t.Fatal(err)
	}
	// Schema version 1 is version 2 without the stays.
	if _, err := db.Exec(`ALTER TABLE jobs DROP COLUMN stays; PRAGMA user_version = 1`); err != nil {
		t.Fatalf("take the store back to version 1: %v", err)
	}
	db.Close()

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	id := store.ID(store.KindReview, store.Subject{Type: store.SubjectPR, Number: 1})
	j, err := s.Job(context.Background(), id)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if j.State != "reviewing" || j.Attempts != 2 || j.Stays != 0 {
		t.Errorf("job = %s, attempts %d, stays %d; want reviewing, 2, 0", j.State, j.Attempts, j.Stays)
	}
}

// TestRefusesAStoreFromANewerBinary is the upgrade-path guard: a store written
// by a newer afk must not be opened and quietly half-understood by an older one.
func TestRefusesAStoreFromANewerBinary(t *testing.T) {
	path := newStoreFile(t)
	db := openRaw(t, path)
	if _, err := db.Exec(`PRAGMA user_version = 9999`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	db.Close()

	if _, err := store.Open(path); err == nil {
		t.Fatal("Open accepted a store from a newer binary; want an error")
	}
}
