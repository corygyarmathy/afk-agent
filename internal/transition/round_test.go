package transition_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// A round is the first key under the stem that no commit has reserved, the
// same one on a replay, and none at all past the bound.
func TestRoundIsTheFirstUnreservedKeyUnderTheBound(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")

	next := func() (string, error) { return transition.Round(ctx, s, "hand-off-pr-12", 0, 2) }
	reserve := func(key string) {
		t.Helper()
		if _, ok, err := s.Acquire(ctx, job.ID, "test", time.Now(), time.Minute); err != nil || !ok {
			t.Fatalf("Acquire = %v, %v", ok, err)
		}
		if err := s.Commit(ctx, store.Commit{JobID: job.ID, Holder: "test", State: "start", Keys: []string{key}, Release: true}); err != nil {
			t.Fatal(err)
		}
	}

	for range 2 {
		if key, err := next(); err != nil || key != "hand-off-pr-12-0" {
			t.Fatalf("round = %q, %v, want hand-off-pr-12-0 until it is reserved", key, err)
		}
	}
	reserve("hand-off-pr-12-0")
	if key, err := next(); err != nil || key != "hand-off-pr-12-1" {
		t.Fatalf("round = %q, %v, want hand-off-pr-12-1", key, err)
	}
	reserve("hand-off-pr-12-1")
	key, err := next()
	spent, ok := transition.Spent(err)
	if !ok || !strings.Contains(err.Error(), "never took effect") || spent.Rounds != 2 || spent.Next != 2 {
		t.Fatalf("round = %q, %v, want the bound spent, with the next allowance from 2", key, err)
	}

	// An allowance from where the last one ran out uses the keys after it,
	// never the ones already spent.
	if from, err := transition.Next(ctx, s, "hand-off-pr-12"); err != nil || from != 2 {
		t.Fatalf("next = %d, %v, want 2", from, err)
	}
	if key, err := transition.Round(ctx, s, "hand-off-pr-12", 2, 2); err != nil || key != "hand-off-pr-12-2" {
		t.Fatalf("round = %q, %v, want hand-off-pr-12-2", key, err)
	}
}

// An effect's error is kept for the decision that reads why it never took
// effect, under its own stem, and forgotten once it succeeds.
func TestAnEffectsErrorIsNotedUntilItSucceeds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "notes", "job.json")
	fails := errors.New("403 Forbidden")
	do := func(err error) func(context.Context) error {
		return func(context.Context) error { return err }
	}

	if err := transition.Noting(path, "push-a", do(fails))(ctx); !errors.Is(err, fails) {
		t.Fatalf("err = %v, want the effect's own", err)
	}
	if got := transition.Noted(path, "push-a"); got != "403 Forbidden" {
		t.Errorf("noted %q, want the effect's error", got)
	}
	if got := transition.Noted(path, "push-b"); got != "" {
		t.Errorf("noted %q for another stem, want nothing", got)
	}
	if err := transition.Noting(path, "push-a", do(nil))(ctx); err != nil {
		t.Fatal(err)
	}
	if got := transition.Noted(path, "push-a"); got != "" {
		t.Errorf("noted %q after a success, want nothing", got)
	}
}
