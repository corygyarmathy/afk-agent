package model

import (
	"context"
	"errors"
	"time"
)

// Wait is a run deferred rather than made: until when, and why.
//
// The zero Wait is no deferral at all.
type Wait struct {
	Until time.Time

	// Exhausted is the tier having run out, or nil for a limited budget. The
	// two defer the same way and are not the same event: a budget has its own
	// notification, at admission, and an exhausted tier is the deferral an
	// operator may be told about (ADR 0001 §10). An error rather than an
	// *ExhaustedError so that it is nil exactly when there is nothing to say.
	Exhausted error
}

// Choose is the model a job's run uses, as of now: the candidates resolve
// gives, and the one its stays pick among them - one stay for each candidate
// that has already failed transiently.
//
// A Wait with an Until is a job to defer rather than run, and to when. A
// limited budget defers to its reset, or for wait when it gave none; an
// exhausted tier defers for wait, which is a parameter because the provider
// says nothing about when it will be back. Anything else resolve says is an
// error: a capability no enrolled model has, or a tier nobody enrolled, is a
// configuration mistake, and a human's (ADR 0001 §10).
func Choose(ctx context.Context, resolve func(context.Context) (Candidates, error), stays, bound int, now time.Time, wait time.Duration) (Ref, Wait, error) {
	candidates, err := resolve(ctx)
	var limited *LimitedError
	if errors.As(err, &limited) {
		if limited.ResetsAt.IsZero() {
			return Ref{}, Wait{Until: now.Add(wait)}, nil
		}
		return Ref{}, Wait{Until: limited.ResetsAt}, nil
	}
	if err != nil {
		return Ref{}, Wait{}, err
	}
	ref, err := candidates.Attempt(stays, bound)
	var exhausted *ExhaustedError
	if errors.As(err, &exhausted) {
		return Ref{}, Wait{Until: now.Add(wait), Exhausted: exhausted}, nil
	}
	if err != nil {
		return Ref{}, Wait{}, err
	}
	return ref, Wait{}, nil
}
