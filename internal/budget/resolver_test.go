package budget_test

import "testing"

// The observation the resolver takes is the same one admission takes. Two
// readings that could disagree is the thing this conversion exists to prevent:
// a job in flight when the window closes resolves against exactly what the
// pool saw.
func TestResolverBudgetIsTheSameObservation(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	s := decode(t, live, now)

	b := s.Resolver()
	if !b.Limited {
		t.Fatalf("the resolver's budget is not limited and the state is: %s", s)
	}
	if want := s.ResetsAt(); !b.ResetsAt.Equal(want) {
		t.Fatalf("resets at %s, want %s", b.ResetsAt, want)
	}

	// And an unlimited one carries no timestamp for the resolver to defer to.
	ok := decode(t, `{"usage":{"rolling":{"status":"ok","percent":3,"resetsAt":"2026-09-11T18:00:00Z"}}}`, now)
	if b := ok.Resolver(); b.Limited || !b.ResetsAt.IsZero() {
		t.Fatalf("an unlimited budget resolved as %+v", b)
	}
}
