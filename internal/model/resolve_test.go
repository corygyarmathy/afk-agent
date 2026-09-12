package model_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/model"
)

// The catalogue every test here resolves against: a frozen excerpt of
// models.dev, cut verbatim from the live document so that the decode is tested
// against the shape upstream actually emits rather than one we invented.
//
// Frozen, so a price change upstream cannot turn a green test red overnight,
// and so the rows below can assert on numbers. Nothing in this package's tests
// reaches the network; scripts/offline-test.sh is what proves that rather than
// this comment.
func fixture(t *testing.T) *model.Catalogue {
	t.Helper()
	f, err := os.Open("testdata/catalogue.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := model.DecodeCatalogue(f)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ref(t *testing.T, s string) model.Ref {
	t.Helper()
	r, err := model.ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func refs(t *testing.T, ss ...string) []model.Ref {
	t.Helper()
	out := make([]model.Ref, len(ss))
	for i, s := range ss {
		out[i] = ref(t, s)
	}
	return out
}

// enrolment builds an enrolment from tier name to ordered refs.
func enrolment(t *testing.T, tiers ...model.TierEnrolment) *model.Enrolment {
	t.Helper()
	e, err := model.NewEnrolment(tiers...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestResolve(t *testing.T) {
	cat := fixture(t)

	// Two tiers standing in for the shape a NixOS module will write. Which
	// models are in which tier is a parameter and not this package's decision;
	// these are here to be resolved against, not to be a recommendation.
	enrol := enrolment(t,
		model.TierEnrolment{Name: "planning", Models: refs(t,
			"opencode-go/glm-5.3",
			"anthropic/claude-opus-5",
		)},
		model.TierEnrolment{Name: "implementation", Models: refs(t,
			"opencode-go/glm-5.3-flash",
			"opencode-go/deepseek-v4.1-flash",
			"opencode-go/minimax-m2.7",
		)},
		model.TierEnrolment{Name: "unpriced", Models: refs(t,
			"qiniu-ai/glm-4.5-air",
		)},
		model.TierEnrolment{Name: "withdrawn", Models: refs(t,
			"opencode-go/a-model-upstream-has-never-heard-of",
		)},
		model.TierEnrolment{Name: "small", Models: refs(t,
			"opencode-go/glm-5",
		)},
	)

	tests := []struct {
		name string
		reqs model.Requirements
		// want is the candidate list, in order. Empty means an error is
		// expected, and wantErr says which.
		want    []string
		wantErr func(t *testing.T, err error)
	}{
		{
			name: "enrolment order is the preference order",
			reqs: model.Requirements{
				Tier:         "implementation",
				Capabilities: []model.Capability{model.CapToolCall},
			},
			want: []string{
				"opencode-go/glm-5.3-flash",
				"opencode-go/deepseek-v4.1-flash",
				"opencode-go/minimax-m2.7",
			},
		},
		{
			name: "a capability filters, it does not reorder",
			reqs: model.Requirements{
				Tier:         "implementation",
				Capabilities: []model.Capability{model.CapAttachment},
			},
			// minimax-m2.7 has attachment: false, and drops out. The two that
			// remain keep the order they were enrolled in.
			want: []string{
				"opencode-go/glm-5.3-flash",
				"opencode-go/deepseek-v4.1-flash",
			},
		},
		{
			name: "an image job in a tier with no vision names the capability",
			reqs: model.Requirements{
				Tier:         "planning",
				Capabilities: []model.Capability{model.InputModality("image")},
			},
			// glm-5.3 is text-only. claude-opus-5 does take images, so this
			// row would pass if only one model were consulted - it is here to
			// pin that the whole tier is walked.
			want: []string{"anthropic/claude-opus-5"},
		},
		{
			name: "a capability nothing in the tier has is an error naming it",
			reqs: model.Requirements{
				Tier: "implementation",
				// Every model in this tier takes text, and two of the three
				// take images; none takes audio.
				Capabilities: []model.Capability{model.InputModality("audio")},
			},
			wantErr: func(t *testing.T, err error) {
				var nc *model.NoCandidateError
				if !errors.As(err, &nc) {
					t.Fatalf("want *NoCandidateError, got %T: %v", err, err)
				}
				if len(nc.Missing) != 1 || nc.Missing[0] != model.InputModality("audio") {
					t.Fatalf("want Missing == [input:audio], got %v", nc.Missing)
				}
				if !strings.Contains(err.Error(), "input:audio") {
					t.Fatalf("error does not name the capability: %v", err)
				}
			},
		},
		{
			name: "a ceiling excludes a model that has been repriced above it",
			reqs: model.Requirements{
				Tier:         "planning",
				Capabilities: []model.Capability{model.CapToolCall},
				// glm-5.3 is 1.4 in, claude-opus-5 is 5.
				Ceiling: model.Ceiling{Input: 2},
			},
			want: []string{"opencode-go/glm-5.3"},
		},
		{
			name: "a ceiling is checked against the dearest band, not the headline",
			reqs: model.Requirements{
				Tier:         "ceiling-bands",
				Capabilities: []model.Capability{model.CapToolCall},
				// grok-4.6 advertises 2 in, and 4 above 200k context. A
				// ceiling of 3 must exclude it: the run that lands in the
				// dearer band is exactly the one the ceiling was written for.
				Ceiling: model.Ceiling{Input: 3},
			},
			wantErr: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "input 4 over ceiling 3") {
					t.Fatalf("want the dearest band quoted, got: %v", err)
				}
			},
		},
		{
			name: "an unpriced model breaches any ceiling",
			reqs: model.Requirements{
				Tier:    "unpriced",
				Ceiling: model.Ceiling{Input: 1000},
			},
			wantErr: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "unpriced") {
					t.Fatalf("want the model rejected as unpriced, got: %v", err)
				}
			},
		},
		{
			name: "no ceiling admits an unpriced model",
			reqs: model.Requirements{Tier: "unpriced"},
			want: []string{"qiniu-ai/glm-4.5-air"},
		},
		{
			name: "a context floor excludes a model that cannot hold the job",
			reqs: model.Requirements{
				Tier:       "small",
				MinContext: 500_000,
			},
			wantErr: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "context 202752, needs 500000") {
					t.Fatalf("want both numbers in the error, got: %v", err)
				}
			},
		},
		{
			name: "an enrolled model the catalogue has never heard of is reported, not assumed",
			reqs: model.Requirements{Tier: "withdrawn"},
			wantErr: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "not in the catalogue") {
					t.Fatalf("want the unknown model reported, got: %v", err)
				}
			},
		},
		{
			name: "a tier nobody enrolled anything in names the tiers that exist",
			reqs: model.Requirements{Tier: "premium"},
			wantErr: func(t *testing.T, err error) {
				var ut *model.UnknownTierError
				if !errors.As(err, &ut) {
					t.Fatalf("want *UnknownTierError, got %T: %v", err, err)
				}
				if !strings.Contains(err.Error(), "implementation") {
					t.Fatalf("error does not list the enrolled tiers: %v", err)
				}
			},
		},
		{
			name: "a resolution with no tier is refused rather than defaulted",
			reqs: model.Requirements{Capabilities: []model.Capability{model.CapToolCall}},
			wantErr: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "no tier required") {
					t.Fatalf("got: %v", err)
				}
			},
		},
		{
			name: "an unrecognised capability excludes everything rather than being dropped",
			reqs: model.Requirements{
				Tier:         "implementation",
				Capabilities: []model.Capability{model.Capability("telepathy")},
			},
			wantErr: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "telepathy") {
					t.Fatalf("want the unknown capability named, got: %v", err)
				}
			},
		},
	}

	// The band test needs a tier of its own, and building it here keeps the
	// enrolment above readable.
	bandEnrol := enrolment(t, model.TierEnrolment{Name: "ceiling-bands", Models: refs(t, "opencode-go/grok-4.6")})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := enrol
			if tt.reqs.Tier == "ceiling-bands" {
				e = bandEnrol
			}
			got, err := model.Resolve(tt.reqs, cat, e, model.Budget{})
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("want an error, got candidates %v", got)
				}
				if got != nil {
					t.Fatalf("an error must come with no candidates, got %v", got)
				}
				tt.wantErr(t, err)
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			var names []string
			for _, r := range got {
				names = append(names, r.String())
			}
			if len(names) != len(tt.want) {
				t.Fatalf("got %v, want %v", names, tt.want)
			}
			for i := range names {
				if names[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", names, tt.want)
				}
			}
		})
	}
}

// A limited budget is not a shortage of candidates, and must not be reported as
// one: the job defers to the reset the provider gave rather than failing as
// though the enrolment were wrong (ADR 0001 §11).
func TestResolveDefersWhenBudgetIsLimited(t *testing.T) {
	resetsAt := time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC)
	_, err := model.Resolve(
		model.Requirements{Tier: "implementation"},
		fixture(t),
		enrolment(t, model.TierEnrolment{Name: "implementation", Models: refs(t, "opencode-go/glm-5.3-flash")}),
		model.Budget{Limited: true, ResetsAt: resetsAt},
	)
	var le *model.LimitedError
	if !errors.As(err, &le) {
		t.Fatalf("want *LimitedError, got %T: %v", err, err)
	}
	if !le.ResetsAt.Equal(resetsAt) {
		t.Fatalf("want the provider's reset carried through, got %v", le.ResetsAt)
	}
	var nc *model.NoCandidateError
	if errors.As(err, &nc) {
		t.Fatal("a limited budget must not look like an empty tier")
	}
}

// Resolve is pure, so resolving twice from the same inputs gives the same
// answer. Worth an explicit test because the natural implementation walks a
// map, and a map walk that reached the candidate order would produce a resolver
// that picks a different model on alternate Tuesdays.
func TestResolveIsDeterministic(t *testing.T) {
	cat := fixture(t)
	enrol := enrolment(t, model.TierEnrolment{Name: "t", Models: refs(t,
		"opencode-go/glm-5.3-flash",
		"opencode-go/deepseek-v4.1-flash",
		"opencode-go/minimax-m2.7",
	)})
	reqs := model.Requirements{Tier: "t", Capabilities: []model.Capability{model.CapToolCall}}

	first, err := model.Resolve(reqs, cat, enrol, model.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		again, err := model.Resolve(reqs, cat, enrol, model.Budget{})
		if err != nil {
			t.Fatal(err)
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("run %d gave %v, first gave %v", i, again, first)
			}
		}
	}
}

func TestAttempt(t *testing.T) {
	c := model.Candidates(refs(t, "p/a", "p/b", "p/c"))

	tests := []struct {
		name    string
		n       int
		bound   int
		want    string
		wantErr bool
		// tried is the number ExhaustedError should report having used.
		tried int
	}{
		{name: "the first attempt is the first enrolled model", n: 0, bound: 0, want: "p/a"},
		{name: "a retry moves to the next model in the same tier", n: 1, bound: 0, want: "p/b"},
		{name: "and the next", n: 2, bound: 0, want: "p/c"},
		{name: "past the end of the tier is exhausted", n: 3, bound: 0, wantErr: true, tried: 3},
		{name: "a bound shorter than the tier stops early", n: 2, bound: 2, wantErr: true, tried: 2},
		{name: "a bound longer than the tier does not invent models", n: 3, bound: 99, wantErr: true, tried: 3},
		{name: "a bound of zero means the tier is the only bound", n: 2, bound: 0, want: "p/c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Attempt(tt.n, tt.bound)
			if tt.wantErr {
				var ex *model.ExhaustedError
				if !errors.As(err, &ex) {
					t.Fatalf("want *ExhaustedError, got %T: %v", err, err)
				}
				if ex.Tried != tt.tried {
					t.Fatalf("want Tried == %d, got %d", tt.tried, ex.Tried)
				}
				if ex.Enrolled != len(c) {
					t.Fatalf("want Enrolled == %d, got %d", len(c), ex.Enrolled)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}
