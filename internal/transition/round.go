package transition

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/corygyarmathy/afk-agent/internal/statefile"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// Round is the key for the next attempt at an effect that is read back: the
// first of <stem>-<i>, for i from from and fewer than bound past it, that no
// commit has reserved yet.
//
// It is how an effect lost between its commit and its performing is made
// again. The runner reserves a key in the commit and performs the effect
// after it, so a process killed in between leaves the key reserved and the
// effect undone, and a replay under that key skips it (ADR 0001 §5). The
// transition that reads the effect back from the tracker asks for the next
// round instead: deterministic across replays of the same round, and new for a
// round that follows one whose effect was lost.
//
// A key is reserved for good, whether its effect landed, errored or was lost,
// so the rounds an effect has had are the keys under its stem. The bound is
// what stops an effect that never shows up on the tracker being made for ever,
// and from is where it counts from: 0 for a stem that is new with each try at
// the work, and a base the caller keeps for one that is not (Next). Past the
// bound, the round is a *SpentError.
func Round(ctx context.Context, s store.Store, stem string, from, bound int) (string, error) {
	if bound < 1 {
		bound = 1
	}
	for i := from; i < from+bound; i++ {
		key := fmt.Sprintf("%s-%d", stem, i)
		reserved, err := s.Reserved(ctx, key)
		if err != nil {
			return "", err
		}
		if !reserved {
			return key, nil
		}
	}
	return "", &SpentError{Stem: stem, Rounds: bound, Next: from + bound}
}

// Next is where a new allowance of rounds under stem starts: the first round no
// commit has reserved. A caller whose stem outlives one try at the work keeps
// it, and passes it to Round as from.
func Next(ctx context.Context, s store.Store, stem string) (int, error) {
	for i := 0; ; i++ {
		reserved, err := s.Reserved(ctx, fmt.Sprintf("%s-%d", stem, i))
		if err != nil || !reserved {
			return i, err
		}
	}
}

// SpentError is an effect whose rounds ran out: it was made Rounds times and
// never took effect. Next is where a new allowance would start.
type SpentError struct {
	Stem   string
	Rounds int
	Next   int
}

func (e *SpentError) Error() string {
	return fmt.Sprintf("%s was made %d times and never took effect", e.Stem, e.Rounds)
}

// Spent reports whether err is rounds running out, and how many there were.
func Spent(err error) (*SpentError, bool) {
	var spent *SpentError
	ok := errors.As(err, &spent)
	return spent, ok
}

// Noting is do, keeping its error at path under stem when it fails and
// forgetting it when it succeeds. The runner performs an effect after the
// commit, so what it returns reaches the log and nothing else: this is how the
// decision that finds the effect never took effect says why.
//
// A note that cannot be written is dropped. The effect's own error is the one
// that matters, and it is returned either way.
func Noting(path, stem string, do func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		err := do(ctx)
		if err != nil {
			_ = statefile.Save(path, note{Stem: stem, Error: err.Error()})
		} else if Noted(path, stem) != "" {
			_ = os.Remove(path)
		}
		return err
	}
}

// Noted is the error Noting last kept at path for stem, or empty if the last
// effect to note there was another's, or none failed.
func Noted(path, stem string) string {
	var n note
	if err := statefile.Load(path, &n); err != nil || n.Stem != stem {
		return ""
	}
	return n.Error
}

type note struct {
	Stem  string `json:"stem"`
	Error string `json:"error"`
}
