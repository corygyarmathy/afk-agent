package model_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/model"
)

func TestChoose(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	wait := 30 * time.Minute
	reset := now.Add(2 * time.Hour)
	first := model.Ref{Provider: "a", Model: "one"}
	second := model.Ref{Provider: "b", Model: "two"}
	two := func(context.Context) (model.Candidates, error) { return model.Candidates{first, second}, nil }
	wrong := errors.New("no enrolled model has the capability")

	for _, tc := range []struct {
		name    string
		resolve func(context.Context) (model.Candidates, error)
		stays   int
		ref     model.Ref
		until   time.Time
		// exhausted is whether the wait says the tier ran out, which is the
		// one deferral an operator may be told about.
		exhausted bool
		err       error
	}{
		{"no stays is the first candidate", two, 0, first, time.Time{}, false, nil},
		{"a stay is the next candidate", two, 1, second, time.Time{}, false, nil},
		{"past the bound defers for the wait, exhausted", two, 2, model.Ref{}, now.Add(wait), true, nil},
		{"a limited budget defers to its reset, not exhausted", func(context.Context) (model.Candidates, error) {
			return nil, &model.LimitedError{ResetsAt: reset}
		}, 0, model.Ref{}, reset, false, nil},
		{"a limited budget with no reset defers for the wait, not exhausted", func(context.Context) (model.Candidates, error) {
			return nil, &model.LimitedError{}
		}, 0, model.Ref{}, now.Add(wait), false, nil},
		{"anything else is an error", func(context.Context) (model.Candidates, error) { return nil, wrong }, 0, model.Ref{}, time.Time{}, false, wrong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, w, err := model.Choose(context.Background(), tc.resolve, tc.stays, 2, now, wait)
			if ref != tc.ref || !w.Until.Equal(tc.until) || !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
				t.Errorf("Choose = %v, %v, %v; want %v, %v, %v", ref, w.Until, err, tc.ref, tc.until, tc.err)
			}
			var exhausted *model.ExhaustedError
			if got := errors.As(w.Exhausted, &exhausted); got != tc.exhausted {
				t.Errorf("Choose's wait says exhausted %v (%v), want %v", got, w.Exhausted, tc.exhausted)
			}
		})
	}
}

// A transient failure stays for the next candidate, or defers the tier that
// has none, carrying the failure: what ran the tier out is what the operator
// is told (#98). Anything else stays, for the next run to find.
func TestFailed(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	wait := 30 * time.Minute
	two := func(context.Context) (model.Candidates, error) {
		return model.Candidates{{Provider: "a", Model: "one"}, {Provider: "b", Model: "two"}}, nil
	}
	failed := errors.New("the run was still going after 30m, and was killed")

	t.Run("a candidate left stays", func(t *testing.T) {
		if w := model.Failed(context.Background(), two, 0, 2, now, wait, failed); w != (model.Wait{}) {
			t.Errorf("Failed = %+v, want the zero Wait: a stay", w)
		}
	})
	t.Run("the last candidate defers the tier with its failure", func(t *testing.T) {
		w := model.Failed(context.Background(), two, 1, 2, now, wait, failed)
		if !w.Until.Equal(now.Add(wait)) {
			t.Errorf("Failed defers until %v, want %v", w.Until, now.Add(wait))
		}
		var exhausted *model.ExhaustedError
		if !errors.As(w.Exhausted, &exhausted) {
			t.Fatalf("Failed's wait = %v, want the tier exhausted", w.Exhausted)
		}
		if !errors.Is(w.Exhausted, failed) || !strings.Contains(w.Exhausted.Error(), failed.Error()) {
			t.Errorf("the exhausted tier (%v) does not carry what the last run failed with", w.Exhausted)
		}
	})
	t.Run("a limited budget stays", func(t *testing.T) {
		limited := func(context.Context) (model.Candidates, error) { return nil, &model.LimitedError{} }
		if w := model.Failed(context.Background(), limited, 1, 2, now, wait, failed); w != (model.Wait{}) {
			t.Errorf("Failed = %+v, want the zero Wait: a stay", w)
		}
	})
	t.Run("a resolution that fails stays", func(t *testing.T) {
		wrong := func(context.Context) (model.Candidates, error) { return nil, errors.New("no enrolment") }
		if w := model.Failed(context.Background(), wrong, 1, 2, now, wait, failed); w != (model.Wait{}) {
			t.Errorf("Failed = %+v, want the zero Wait: a stay", w)
		}
	})
}
