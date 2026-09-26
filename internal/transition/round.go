package transition

import (
	"context"
	"fmt"

	"github.com/corygyarmathy/afk-agent/internal/store"
)

// Round is the key for the next attempt at an effect that is read back: the
// first of <stem>-<i>, for i below bound, that no commit has reserved yet.
//
// It is how an effect lost between its commit and its performing is made
// again. The runner reserves a key in the commit and performs the effect
// after it, so a process killed in between leaves the key reserved and the
// effect undone, and a replay under that key skips it (ADR 0001 §5). The
// transition that reads the effect back from the tracker asks for the next
// round instead: deterministic across replays of the same round, and new for a
// round that follows one whose effect was lost.
//
// The bound is what stops an effect that never shows up on the tracker being
// made for ever. Past it, the round is an error, and the job fails where
// someone will see it.
func Round(ctx context.Context, s store.Store, stem string, bound int) (string, error) {
	if bound < 1 {
		bound = 1
	}
	for i := range bound {
		key := fmt.Sprintf("%s-%d", stem, i)
		reserved, err := s.Reserved(ctx, key)
		if err != nil {
			return "", err
		}
		if !reserved {
			return key, nil
		}
	}
	return "", fmt.Errorf("%s was tried %d times and never took effect", stem, bound)
}
