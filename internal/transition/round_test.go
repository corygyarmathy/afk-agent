package transition_test

import (
	"context"
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

	next := func() (string, error) { return transition.Round(ctx, s, "hand-off-pr-12", 2) }
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
	if key, err := next(); err == nil || !strings.Contains(err.Error(), "never took effect") {
		t.Fatalf("round = %q, %v, want the bound spent", key, err)
	}
}
