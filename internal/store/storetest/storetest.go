// Package storetest holds the store helpers tests across the module share: a
// temporary store on disk, and a job seeded into a given state, which is the
// fixture a transition is tested from.
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
