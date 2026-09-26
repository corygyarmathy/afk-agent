package model_test

import (
	"context"
	"errors"
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
		attempt int
		ref     model.Ref
		until   time.Time
		err     error
	}{
		{"the first attempt is the first candidate", two, 0, first, time.Time{}, nil},
		{"a later attempt is the next candidate", two, 1, second, time.Time{}, nil},
		{"past the bound defers for the wait", two, 2, model.Ref{}, now.Add(wait), nil},
		{"a limited budget defers to its reset", func(context.Context) (model.Candidates, error) {
			return nil, &model.LimitedError{ResetsAt: reset}
		}, 0, model.Ref{}, reset, nil},
		{"a limited budget with no reset defers for the wait", func(context.Context) (model.Candidates, error) {
			return nil, &model.LimitedError{}
		}, 0, model.Ref{}, now.Add(wait), nil},
		{"anything else is an error", func(context.Context) (model.Candidates, error) { return nil, wrong }, 0, model.Ref{}, time.Time{}, wrong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, until, err := model.Choose(context.Background(), tc.resolve, tc.attempt, 2, now, wait)
			if ref != tc.ref || !until.Equal(tc.until) || !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
				t.Errorf("Choose = %v, %v, %v; want %v, %v, %v", ref, until, err, tc.ref, tc.until, tc.err)
			}
		})
	}
}
