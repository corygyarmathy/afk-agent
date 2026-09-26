package model

import (
	"context"
	"errors"
	"time"
)

// Choose is the model a job's run uses, as of now: the candidates resolve
// gives, and the one its stays pick among them - one stay for each candidate
// that has already failed transiently.
//
// A non-zero until is a job to defer rather than run, and to when. A limited
// budget defers to its reset, or for wait when it gave none; an exhausted tier
// defers for wait, which is a parameter because the provider says nothing
// about when it will be back. Anything else resolve says is an error: a
// capability no enrolled model has, or a tier nobody enrolled, is a
// configuration mistake, and a human's (ADR 0001 §10).
func Choose(ctx context.Context, resolve func(context.Context) (Candidates, error), stays, bound int, now time.Time, wait time.Duration) (ref Ref, until time.Time, err error) {
	candidates, err := resolve(ctx)
	var limited *LimitedError
	if errors.As(err, &limited) {
		if limited.ResetsAt.IsZero() {
			return Ref{}, now.Add(wait), nil
		}
		return Ref{}, limited.ResetsAt, nil
	}
	if err != nil {
		return Ref{}, time.Time{}, err
	}
	ref, err = candidates.Attempt(stays, bound)
	var exhausted *ExhaustedError
	if errors.As(err, &exhausted) {
		return Ref{}, now.Add(wait), nil
	}
	if err != nil {
		return Ref{}, time.Time{}, err
	}
	return ref, time.Time{}, nil
}
