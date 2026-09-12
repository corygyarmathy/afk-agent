package budget_test

import (
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
)

// Nothing in this package's tests reaches the network: every Observer under
// test is given a Fetch. That is a property of the seam rather than of the
// discipline here - Observer.Fetch is a field precisely so that "the tests do
// not reach upstream" is something the compiler helps with, and
// scripts/offline-test.sh is what proves it.

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The document, as the endpoint returned it on 2026-09-11: monthly limited at
// 100% while rolling and weekly both read ok. It is the acceptance criterion's
// own example and every test that needs a live-shaped response uses it.
const live = `{"usage":{
	"rolling":{"status":"ok","percent":12,"resetsAt":"2026-09-11T18:00:00Z"},
	"weekly":{"status":"ok","percent":41,"resetsAt":"2026-09-15T00:00:00Z"},
	"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-27T00:00:00Z"}
}}`

func decode(t *testing.T, doc string, observedAt time.Time) budget.State {
	t.Helper()
	s, err := budget.Decode(strings.NewReader(doc), observedAt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return s
}

// The acceptance criterion: limited state is an OR across all three windows.
// Verified live on 2026-09-11, monthly rate-limited at 100% while rolling and
// weekly both read ok - a check on one window would have said everything was
// fine.
func TestLimitedIsAnOrAcrossWindows(t *testing.T) {
	s := decode(t, live, at(t, "2026-09-11T12:00:00Z"))

	if !s.Limited() {
		t.Fatalf("one window is rate-limited and the state is not: %s", s)
	}
	for _, name := range []string{"rolling", "weekly"} {
		w, ok := s.Window(name)
		if !ok {
			t.Fatalf("no %s window in %s", name, s)
		}
		if w.Limited() {
			t.Fatalf("%s reads ok upstream and limited here", name)
		}
	}
	if got, want := s.ResetsAt(), at(t, "2026-09-27T00:00:00Z"); !got.Equal(want) {
		t.Fatalf("resets at %s, want the limited window's %s", got, want)
	}
	if peak := s.Peak(); peak.Name != "monthly" {
		t.Fatalf("the peak window is %q, want the limited one", peak.Name)
	}
}

// Limited is an OR, so the account stays limited until every limited window has
// reset. Coming back when the first one reopens is coming back to the same
// answer.
func TestResetsAtIsTheLastLimitedWindow(t *testing.T) {
	const doc = `{"usage":{
		"rolling":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-11T13:00:00Z"},
		"weekly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-15T00:00:00Z"},
		"monthly":{"status":"ok","percent":50,"resetsAt":"2026-09-27T00:00:00Z"}
	}}`
	s := decode(t, doc, at(t, "2026-09-11T12:00:00Z"))

	if got, want := s.ResetsAt(), at(t, "2026-09-15T00:00:00Z"); !got.Equal(want) {
		t.Fatalf("resets at %s, want %s: the monthly window is not limited and the weekly one reopens last", got, want)
	}
}

// A status this build has not heard of reads as not limited. The safe direction
// here is the opposite of the resolver's: refusing to work on a word we do not
// recognise stops the agent on a provider's copy-edit, while continuing costs
// at most a transient failure that is already handled as one.
func TestAnUnknownStatusIsNotLimited(t *testing.T) {
	s := decode(t, `{"usage":{"rolling":{"status":"degraded","percent":3}}}`, time.Now())
	if s.Limited() {
		t.Fatalf("an unrecognised status read as limited: %s", s)
	}
}

// Peak names a window the endpoint actually reported, even when every window
// reads zero. Starting the walk at the zero Window instead means an idle
// account peaks at a nameless one, and the log line it lands in reads " 0% ()".
func TestPeakNamesAWindowTheEndpointReported(t *testing.T) {
	s := decode(t, `{"usage":{
		"rolling":{"status":"ok","percent":0},
		"weekly":{"status":"ok","percent":0}
	}}`, at(t, "2026-09-11T12:00:00Z"))

	if peak := s.Peak(); peak.Name == "" {
		t.Fatalf("the peak of %s is a window nothing reported: %s", s, peak)
	}
	if peak := (budget.State{}).Peak(); peak.Name != "" {
		t.Fatalf("an empty state peaked at %q", peak.Name)
	}
}
