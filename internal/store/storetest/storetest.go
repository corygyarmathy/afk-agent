// Package storetest holds the store helpers tests across the module share: a
// temporary store on disk, a job seeded into a given state, which is the
// fixture a transition is tested from, a job brought to rest, and a job read
// back.
package storetest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
)

// Open opens a store in a temporary directory, closed when the test ends.
func Open(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// SubjectType is the tracker subject a job of a kind attaches to.
func SubjectType(kind store.Kind) store.SubjectType {
	if kind == store.KindImplement {
		return store.SubjectIssue
	}
	return store.SubjectPR
}

// Seed puts a job in the store in a given state, which is the "fixture state" a
// transition is tested from.
func Seed(t *testing.T, s store.Store, kind store.Kind, num int, state string) store.Job {
	t.Helper()
	j, err := s.Ensure(context.Background(), kind, store.Subject{Type: SubjectType(kind), Number: num}, state, time.Now())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return j
}

// Job reads a job back from the store, failing the test if it cannot.
func Job(t *testing.T, s store.Store, id string) store.Job {
	t.Helper()
	j, err := s.Job(context.Background(), id)
	if err != nil {
		t.Fatalf("Job(%s): %v", id, err)
	}
	return j
}

// Rest puts a job in a state with nothing scheduled and no lease, the way a
// transition that finished or handed back leaves it.
func Rest(t *testing.T, s store.Store, id, state string, attempts int, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, ok, err := s.Acquire(ctx, id, "a-transition", now, time.Minute); err != nil || !ok {
		t.Fatalf("Acquire(%s) = %v, %v", id, ok, err)
	}
	if err := s.Commit(ctx, store.Commit{JobID: id, Holder: "a-transition", State: state, Attempts: attempts, Release: true}); err != nil {
		t.Fatalf("Commit(%s): %v", id, err)
	}
}
