package model_test

import (
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/model"
)

func TestDecodeEnrolment(t *testing.T) {
	const doc = `{
	  "tiers": [
	    {"name": "planning",       "models": ["opencode-go/glm-5.3", "anthropic/claude-opus-5"]},
	    {"name": "implementation", "models": ["opencode-go/glm-5.3-flash"]}
	  ]
	}`

	e, err := model.DecodeEnrolment(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}

	// The declared order survives, which is why the wire format is an array
	// rather than an object keyed by tier name.
	if got := e.Tiers(); len(got) != 2 || got[0] != "planning" || got[1] != "implementation" {
		t.Fatalf("tiers came back as %v", got)
	}

	planning, ok := e.Tier("planning")
	if !ok {
		t.Fatal("planning is missing")
	}
	if len(planning) != 2 || planning[0].String() != "opencode-go/glm-5.3" || planning[1].String() != "anthropic/claude-opus-5" {
		t.Fatalf("planning came back as %v", planning)
	}

	// Absent rather than empty: "this tier does not exist" and "nothing in it
	// qualified" are different mistakes.
	if _, ok := e.Tier("premium"); ok {
		t.Fatal("a tier nobody declared came back present")
	}
}

// An unreadable enrolment stops the agent rather than degrading it. The
// degradation that is designed for is a stale catalogue, and it works precisely
// because the enrolment underneath it is known good.
func TestDecodeEnrolmentRefuses(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "no tiers",
			doc:  `{"tiers": []}`,
			want: "no tiers",
		},
		{
			name: "a tier with no models",
			doc:  `{"tiers": [{"name": "planning", "models": []}]}`,
			want: "has no models",
		},
		{
			name: "a tier with no name",
			doc:  `{"tiers": [{"name": "", "models": ["p/m"]}]}`,
			want: "has no name",
		},
		{
			name: "the same tier twice",
			doc:  `{"tiers": [{"name": "t", "models": ["p/a"]}, {"name": "t", "models": ["p/b"]}]}`,
			want: "declared twice",
		},
		{
			name: "the same model twice in one tier",
			doc:  `{"tiers": [{"name": "t", "models": ["p/a", "p/a"]}]}`,
			want: "enrols p/a twice",
		},
		{
			name: "a model reference that is not provider/model",
			doc:  `{"tiers": [{"name": "t", "models": ["glm-5.3"]}]}`,
			want: "is not provider/model",
		},
		{
			// A misspelled key must not be read past. The enrolment is the
			// whole of eligibility, and silently ignoring half of it is the
			// one way it can be wrong without anyone finding out.
			name: "a field nobody recognises",
			doc:  `{"tiers": [{"name": "t", "modles": ["p/a"]}]}`,
			want: "unknown field",
		},
		{
			name: "not JSON at all",
			doc:  `<!doctype html>`,
			want: "decode enrolment",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.DecodeEnrolment(strings.NewReader(tt.doc))
			if err == nil {
				t.Fatal("decoded without error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want an error mentioning %q, got: %v", tt.want, err)
			}
		})
	}
}

// A model may sit in more than one tier. Enrolling the same capable model as
// the floor of a cheap tier and the head of an expensive one is an ordinary
// thing for an operator to want, and nothing here should stop them.
func TestEnrolmentAllowsAModelInTwoTiers(t *testing.T) {
	e, err := model.NewEnrolment(
		model.TierEnrolment{Name: "planning", Models: refs(t, "opencode-go/glm-5.3")},
		model.TierEnrolment{Name: "implementation", Models: refs(t, "opencode-go/glm-5.3", "opencode-go/glm-5.3-flash")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.Tier("implementation"); len(got) != 2 {
		t.Fatalf("implementation came back as %v", got)
	}
}

// The enrolment is the whole of eligibility: a model the catalogue is full of
// praise for is not a candidate until a human has written it down
// (ADR 0001 §9).
func TestNothingIsEligibleWithoutEnrolment(t *testing.T) {
	cat := fixture(t)
	if _, ok := cat.Lookup(ref(t, "anthropic/claude-sonnet-4-6")); !ok {
		t.Fatal("the fixture should carry claude-sonnet-4-6 for this test to mean anything")
	}

	// A tier that does not enrol it, though it would satisfy every requirement
	// below.
	enrol := enrolment(t, model.TierEnrolment{Name: "planning", Models: refs(t, "opencode-go/glm-5")})
	got, err := model.Resolve(model.Requirements{
		Tier:         "planning",
		Capabilities: []model.Capability{model.CapToolCall},
	}, cat, enrol, model.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Provider == "anthropic" {
			t.Fatalf("an unenrolled model reached the candidate list: %s", r)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want only the enrolled model, got %v", got)
	}
}
