package model

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Requirements are what a job kind declares it needs of a model
// (ADR 0001 §9). Data, so a job kind's requirements are a literal that a test
// can read and a reviewer can check against the issue that asked for them.
type Requirements struct {
	// Tier is the quality floor. Required: a resolution with no tier has
	// nothing to choose among, and defaulting to some tier would be this
	// package picking a parameter.
	Tier Tier

	// Capabilities are the things the model must be able to do. Every one must
	// hold; there is no partial satisfaction, because a model that can do
	// three of four things a job needs fails on the fourth halfway through.
	Capabilities []Capability

	// MinContext is the smallest context window that will do, in tokens. Zero
	// means the requirement is not expressed.
	MinContext int

	// Ceiling is the dearest advertised price this job kind is worth, per
	// million tokens. A zero field is no cap on that field.
	//
	// It is not a budget and does not meter spend - the account's ceiling is
	// enforced at the provider, where it cannot undercount and where it sees
	// interactive use too (ADR 0001 §12). What this catches is narrower and
	// real: a model that was enrolled when it was cheap and has since been
	// repriced. Eligibility is the enrolment's job, so "only free models" is
	// expressed by enrolling only free models; the ceiling exists for the case
	// where the list a human wrote stopped meaning what they meant by it.
	//
	// Checked against the dearest band a model publishes, not its headline
	// one - see Model.Price.
	Ceiling Ceiling
}

// Ceiling is the dearest advertised price a job kind is worth, per million
// tokens. A zero field is no cap on that field, and the zero Ceiling caps
// nothing.
//
// A type of its own rather than a Price, because a cap and a price are not the
// same thing: a price can be unknown and a cap cannot, and "only free models"
// is not expressible here on purpose - see Requirements.Ceiling.
type Ceiling struct {
	Input     float64
	Output    float64
	CacheRead float64
}

// Budget is observed budget state, as the provider reports it (ADR 0001 §11).
//
// One account-wide dollar budget with no per-model dimension: when it is spent,
// every model is spent at once. So it never reorders candidates and never
// prefers a cheaper one - there is no cheaper one to prefer, and a resolver that
// pretended otherwise would be estimating. It is an input to Resolve only
// because being limited means there is no candidate at all, which is a
// different answer from "none qualified" and must not be confused with it.
//
// Observing it is internal/budget's work; budget.State.Resolver is the
// conversion. This package stays pure and does not reach for it.
type Budget struct {
	// Limited is true when any window reports a rate-limited status. An OR
	// across windows, computed by the observer: a check on one window reads
	// fine while another is at 100%.
	Limited bool

	// ResetsAt is when the limited window reopens, absolute. Deferring to a
	// timestamp the provider gave is what keeps the agent from polling a
	// closed door.
	ResetsAt time.Time
}

// Candidates is an ordered candidate list: the enrolled models in a tier that
// satisfy a job kind's requirements, in the order the human enrolled them.
type Candidates []Ref

// Resolve maps requirements, the catalogue and observed budget state to an
// ordered candidate list (ADR 0001 §9).
//
// Pure. It reads no clock, opens no socket and touches no store, which is the
// property that makes the table below it worth writing: every interesting case
// - a capability nothing in the tier has, a repriced model, a catalogue that
// has never heard of an enrolled model - is a row rather than an integration
// test against a live provider.
func Resolve(reqs Requirements, cat *Catalogue, enrol *Enrolment, budget Budget) (Candidates, error) {
	if reqs.Tier == "" {
		return nil, fmt.Errorf("resolve: no tier required")
	}
	// The tier is validated before the budget is consulted, though a limited
	// budget stops the resolution either way. A typo'd tier name is a mistake in
	// the module that no amount of waiting fixes, and checking the budget first
	// hides it for as long as the account is limited: the job defers, reports a
	// rate limit, and comes back to defer again. This costs a map lookup on data
	// already in hand.
	enrolled, ok := enrol.Tier(reqs.Tier)
	if !ok {
		return nil, &UnknownTierError{Tier: reqs.Tier, Known: enrol.Tiers()}
	}

	if budget.Limited {
		return nil, &LimitedError{ResetsAt: budget.ResetsAt}
	}

	var (
		candidates Candidates
		rejected   []Rejection
		// catalogued counts the tier's models the catalogue actually knows.
		// Only they were checked against anything, so only they can support a
		// claim about what the tier does and does not support.
		catalogued int
		// met records which required capabilities at least one enrolled model
		// has. What is left over after the walk is the set no model in the
		// tier supports, and that is the error the acceptance criterion asks
		// for by name.
		met = make(map[Capability]bool, len(reqs.Capabilities))
	)
	for _, ref := range enrolled {
		m, known := cat.Lookup(ref)
		if !known {
			rejected = append(rejected, Rejection{ref, "not in the catalogue"})
			continue
		}
		catalogued++
		for _, c := range reqs.Capabilities {
			if m.Has(c) {
				met[c] = true
			}
		}
		if why := reject(m, reqs); why != "" {
			rejected = append(rejected, Rejection{ref, why})
			continue
		}
		candidates = append(candidates, ref)
	}

	if len(candidates) == 0 {
		// Missing is only claimed about models that exist. A tier whose models
		// the catalogue has all lost supports nothing, trivially, and reporting
		// every required capability as unsupported would send the operator to
		// enrol a capability when what happened is that their tier evaporated.
		var missing []Capability
		if catalogued > 0 {
			for _, c := range reqs.Capabilities {
				if !met[c] {
					missing = append(missing, c)
				}
			}
		}
		return nil, &NoCandidateError{
			Tier:       reqs.Tier,
			Missing:    missing,
			Rejected:   rejected,
			Catalogued: catalogued,
		}
	}
	return candidates, nil
}

// reject reports why a model fails the requirements, or "" if it does not.
//
// Capabilities first, then capacity, then price: a model that cannot do the
// work at all is a more useful thing to say than that it is also dear.
func reject(m Model, reqs Requirements) string {
	for _, c := range reqs.Capabilities {
		if !m.Has(c) {
			return fmt.Sprintf("no %s", c)
		}
	}
	if reqs.MinContext > 0 && m.Limit.Context < reqs.MinContext {
		return fmt.Sprintf("context %d, needs %d", m.Limit.Context, reqs.MinContext)
	}
	if over := overCeiling(m.Price, reqs.Ceiling); over != "" {
		return over
	}
	return ""
}

// overCeiling reports which price field breaches the ceiling, or "".
//
// An unpriced model breaches any ceiling. That is the whole reason Price.Known
// exists: a missing cost block is upstream saying it does not know, and reading
// it as zero would admit a model under a ceiling on the strength of a gap in a
// document.
func overCeiling(p Price, ceiling Ceiling) string {
	if ceiling == (Ceiling{}) {
		return ""
	}
	if !p.Known {
		return "unpriced, and a ceiling is required"
	}
	for _, f := range []struct {
		name                  string
		have, limit, headline float64
	}{
		{"input", p.Input, ceiling.Input, p.Headline.Input},
		{"output", p.Output, ceiling.Output, p.Headline.Output},
		{"cache read", p.CacheRead, ceiling.CacheRead, p.Headline.CacheRead},
	} {
		if f.limit > 0 && f.have > f.limit {
			if f.headline < f.have {
				// The number that excluded the model is not the number on
				// models.dev's page, and an operator who cannot reconcile the
				// two has no way to tell a repriced model from a band they did
				// not know was being read. Say where it came from.
				return fmt.Sprintf("%s %g over ceiling %g (long-context band; headline %g)", f.name, f.have, f.limit, f.headline)
			}
			return fmt.Sprintf("%s %g over ceiling %g", f.name, f.have, f.limit)
		}
	}
	return ""
}

// Attempt returns the model to use for attempt n, zero-based, bounded.
//
// This is ADR 0001 §10 as a function. A transient failure - a throttle, a
// provider hiccup, a pay-as-you-go balance running out - retries at the *next*
// enrolled model in the *same* tier: same tier, so the quality floor is never
// silently crossed, and next rather than same, because retrying the model that
// just failed is how a bad few minutes at one provider becomes an hour of them.
//
// bound is a parameter and comes from configuration; zero or negative means the
// only bound is the length of the list. Running past the bound is not a failure
// to hand back to a human but a job to defer, so the caller gets an
// ExhaustedError and decides when to come back. Choose defers it for the tier
// wait and says why, and the pool tells the operator about a job whose tier
// has run out --tier-notify-after times without a model answering in between
// (notify.Notifier.TierExhausted) - not about one that ran out once and came
// back.
func (c Candidates) Attempt(n, bound int) (Ref, error) {
	limit := len(c)
	if bound > 0 && bound < limit {
		limit = bound
	}
	if n < 0 {
		return Ref{}, fmt.Errorf("attempt %d is negative", n)
	}
	if n >= limit {
		return Ref{}, &ExhaustedError{Tried: limit, Enrolled: len(c)}
	}
	return c[n], nil
}

// LimitedError is returned when observed budget state says the account is rate
// limited. Not a failure of the resolution: there is nothing wrong with the
// requirements or the enrolment, and the job defers to ResetsAt.
type LimitedError struct{ ResetsAt time.Time }

func (e *LimitedError) Error() string {
	if e.ResetsAt.IsZero() {
		return "budget is rate limited"
	}
	return fmt.Sprintf("budget is rate limited until %s", e.ResetsAt.Format(time.RFC3339))
}

// UnknownTierError is a job kind requiring a tier nobody enrolled anything in.
// A configuration mistake, and distinguishable from a tier whose models all
// failed: one is a typo in the module, the other is a real shortage.
type UnknownTierError struct {
	Tier  Tier
	Known []Tier
}

func (e *UnknownTierError) Error() string {
	known := make([]string, len(e.Known))
	for i, t := range e.Known {
		known[i] = string(t)
	}
	sort.Strings(known)
	if len(known) == 0 {
		return fmt.Sprintf("no tier %q: nothing is enrolled", e.Tier)
	}
	return fmt.Sprintf("no tier %q: enrolled tiers are %s", e.Tier, strings.Join(known, ", "))
}

// NoCandidateError is a tier that exists and in which nothing qualified.
//
// It carries both halves on purpose. Missing names the capabilities no enrolled
// model in the tier has, which is the sentence the operator needs - enrol
// something that can do this. Rejected is the per-model detail, which is the
// sentence they need when Missing is empty and the shortage is price or
// context.
//
// What it never is: a downgraded run. There is no quiet fallback to a lower
// tier and no dropping of a requirement to find a match; a job whose tier
// cannot serve it stops and says so.
type NoCandidateError struct {
	Tier     Tier
	Missing  []Capability
	Rejected []Rejection

	// Catalogued is how many of the tier's enrolled models the catalogue knew.
	// Zero is a tier that has evaporated upstream rather than one that fell
	// short, and Missing is empty in that case: nothing was checked against
	// anything, so there is no claim to make about what the tier supports.
	Catalogued int
}

// Rejection is one enrolled model that did not qualify, and why.
//
// Exported because the why is the useful half. "not in the catalogue" against a
// model an operator enrolled last week is a different morning's work from
// "input 4 over ceiling 3", and a caller that can only print the error string
// cannot tell a notification which it is.
type Rejection struct {
	Ref Ref
	Why string
}

func (e *NoCandidateError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "no enrolled model in tier %q can serve this job", e.Tier)
	if e.Catalogued == 0 && len(e.Rejected) > 0 {
		b.WriteString(": the catalogue carries none of them")
	}
	if len(e.Missing) > 0 {
		names := make([]string, len(e.Missing))
		for i, c := range e.Missing {
			names[i] = string(c)
		}
		fmt.Fprintf(&b, ": none supports %s", strings.Join(names, ", "))
	}
	for _, r := range e.Rejected {
		fmt.Fprintf(&b, "\n  %s: %s", r.Ref, r.Why)
	}
	return b.String()
}

// ExhaustedError is every candidate in the tier having been tried.
//
// The job is deferred rather than handed back: a tier is exhausted by a
// provider having a bad few minutes far more often than by anything needing a
// human, and generating work for the operator on the common case is how a
// notification channel stops being read. The operator is told only when the
// tier stays exhausted, a configured number of times in a row (ADR 0001 §10).
type ExhaustedError struct {
	Tried    int
	Enrolled int
}

func (e *ExhaustedError) Error() string {
	if e.Tried < e.Enrolled {
		return fmt.Sprintf("tier exhausted: %d attempts used, the bound, of %d enrolled models", e.Tried, e.Enrolled)
	}
	return fmt.Sprintf("tier exhausted: all %d enrolled models tried", e.Enrolled)
}
