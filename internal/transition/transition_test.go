package transition_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// noop is a well-formed transition that decides nothing interesting. Tests that
// are about the registry rather than about execution use it as a body.
func noop(context.Context, transition.In) (transition.Result, error) {
	return transition.Result{State: "done"}, nil
}

func fixture(name string, mut ...func(*transition.Transition)) transition.Transition {
	t := transition.Transition{
		Name: name,
		Kind: store.KindReview,
		From: "start",
		Run:  noop,
	}
	for _, m := range mut {
		m(&t)
	}
	return t
}

func TestNewRegistryRejectsMalformedTransitions(t *testing.T) {
	tests := []struct {
		name string
		give []transition.Transition
		want string
	}{
		{
			name: "no name",
			give: []transition.Transition{fixture("", func(t *transition.Transition) { t.Name = "" })},
			want: "has no name",
		},
		{
			name: "unknown job kind",
			give: []transition.Transition{fixture("review", func(t *transition.Transition) { t.Kind = "audit" })},
			want: `unknown job kind "audit"`,
		},
		{
			name: "no state to run from",
			give: []transition.Transition{fixture("review", func(t *transition.Transition) { t.From = "" })},
			want: "no state to run from",
		},
		{
			name: "nothing to run",
			give: []transition.Transition{fixture("review", func(t *transition.Transition) { t.Run = nil })},
			want: "nothing to run",
		},
		{
			name: "empty token name",
			give: []transition.Transition{fixture("review", func(t *transition.Transition) { t.Tokens = []string{""} })},
			want: "empty resource token name",
		},
		{
			name: "token declared twice",
			give: []transition.Transition{fixture("review", func(t *transition.Transition) { t.Tokens = []string{"build", "build"} })},
			want: `resource token "build" declared twice`,
		},
		{
			name: "two transitions with one name",
			give: []transition.Transition{
				fixture("review"),
				fixture("review", func(t *transition.Transition) { t.From = "elsewhere" }),
			},
			want: `two transitions named "review"`,
		},
		{
			// The ambiguity that matters: the state a job is in has to decide
			// what happens next, and two candidates mean it does not.
			name: "two transitions from one state",
			give: []transition.Transition{
				fixture("review"),
				fixture("review-again"),
			},
			want: `both run a review job from state "start"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := transition.NewRegistry(tt.give...)
			if err == nil {
				t.Fatalf("NewRegistry = nil error; want one mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("NewRegistry error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestRegistryLooksUpByNameAndByState(t *testing.T) {
	reg := transition.MustRegistry(
		fixture("review"),
		fixture("revise", func(t *transition.Transition) {
			t.Kind, t.From = store.KindRevise, "revising"
		}),
	)

	if got, ok := reg.Get("review"); !ok || got.Name != "review" {
		t.Errorf("Get(review) = %+v, %v; want the review transition", got, ok)
	}
	if _, ok := reg.Get("nonesuch"); ok {
		t.Error("Get(nonesuch) found something")
	}

	// The dispatcher's lookup: a job's kind and state decide what runs next.
	if got, ok := reg.Next(store.KindRevise, "revising"); !ok || got.Name != "revise" {
		t.Errorf("Next(revise, revising) = %+v, %v; want the revise transition", got, ok)
	}
	// Same state, different kind, is a different job entirely.
	if _, ok := reg.Next(store.KindImplement, "revising"); ok {
		t.Error("Next(implement, revising) found a revise job's transition")
	}
	// A state with no move from it is how a terminal state looks.
	if _, ok := reg.Next(store.KindReview, "done"); ok {
		t.Error("Next(review, done) found a transition out of a terminal state")
	}
}

func TestRegistryNamesAreOrdered(t *testing.T) {
	reg := transition.MustRegistry(
		fixture("review", func(t *transition.Transition) { t.From = "a" }),
		fixture("apply", func(t *transition.Transition) { t.From = "b" }),
		fixture("hand-off", func(t *transition.Transition) { t.From = "c" }),
	)
	want := []string{"apply", "hand-off", "review"}
	got := reg.Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}

// A transition is testable from a fixture state with no network, no model, no
// daemon and no store: it is handed a job and the time, and returns a decision.
// This test is that property, stated as a test rather than as a convention.
func TestATransitionIsAFunctionOfItsJobAndTheClock(t *testing.T) {
	now := time.Date(2026, 9, 12, 4, 0, 0, 0, time.UTC)
	waitForCI := transition.Transition{
		Name: "watch-ci",
		Kind: store.KindImplement,
		From: "ci",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			// Waiting is never in-process (ADR 0001 §3): the same state,
			// scheduled for later, is what waiting looks like.
			return transition.Result{State: in.Job.State, RunAt: in.Now.Add(time.Minute)}, nil
		},
	}

	job := store.Job{ID: "implement-issue-2", Kind: store.KindImplement, State: "ci"}
	res, err := waitForCI.Run(context.Background(), transition.In{Job: job, Now: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.State != "ci" {
		t.Errorf("State = %q, want ci", res.State)
	}
	if want := now.Add(time.Minute); !res.RunAt.Equal(want) {
		t.Errorf("RunAt = %s, want %s", res.RunAt, want)
	}
}
