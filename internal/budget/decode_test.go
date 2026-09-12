package budget_test

import (
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
)

// A window this build has never heard of is still part of the OR. The endpoint
// is undocumented, and a fourth window decoded into no field at all would be a
// limit the agent works straight through.
func TestAnUnknownWindowIsStillPartOfTheOr(t *testing.T) {
	const doc = `{"usage":{
		"rolling":{"status":"ok","percent":1,"resetsAt":"2026-09-11T18:00:00Z"},
		"hourly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-11T13:00:00Z"}
	}}`
	s := decode(t, doc, at(t, "2026-09-11T12:00:00Z"))

	if !s.Limited() {
		t.Fatalf("a window nobody named is limited and the state is not: %s", s)
	}
	// Known windows first, in their own order; anything else after them.
	if got := s.Windows[0].Name; got != "rolling" {
		t.Fatalf("windows are ordered %v, want the known one first", got)
	}
}

// A response with no usage object is an error rather than an empty state: an
// empty state reads as "no window is limited", which would admit every job on
// the strength of a document that said nothing.
func TestDecodeRefusesADocumentThatSaysNothing(t *testing.T) {
	for name, doc := range map[string]string{
		"empty object":   `{}`,
		"no windows":     `{"usage":{}}`,
		"an error page":  `<html><body>502</body></html>`,
		"a bad resetsAt": `{"usage":{"rolling":{"status":"ok","percent":1,"resetsAt":"tomorrow"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if s, err := budget.Decode(strings.NewReader(doc), time.Now()); err == nil {
				t.Fatalf("decoded %s as %s", name, s)
			}
		})
	}
}

// A window that is not limited need not say when it rolls, and an absent
// timestamp is not a malformed one.
func TestAnAbsentResetsAtIsNotAnError(t *testing.T) {
	s := decode(t, `{"usage":{"rolling":{"status":"ok","percent":3}}}`, time.Now())
	if w, _ := s.Window("rolling"); !w.ResetsAt.IsZero() {
		t.Fatalf("resets at %s, want the zero time", w.ResetsAt)
	}
	if s.Limited() {
		t.Fatalf("an ok window reads limited: %s", s)
	}
}
