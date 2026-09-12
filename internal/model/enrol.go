package model

import (
	"encoding/json"
	"fmt"
	"io"
)

// Tier is a quality level a human assigns to enrolled models, and which a job
// kind requires (CONTEXT.md: tier).
//
// An opaque string here, exactly as a job's state is opaque to the store. How
// many tiers there are and what they are called is a parameter and belongs to
// the NixOS module that configures this agent (docs/agents/domain.md): reversing
// "two tiers" sends you to a text editor rather than back to an ADR's
// Alternatives section, so it is not a decision this package gets to hold.
//
// The consequence is worth stating, because it is the point: a tier has no
// ordering here and the resolver never falls from one tier to another. A tier
// is a floor, and a floor you can step through is not one (ADR 0001 §10).
type Tier string

// Enrolment is which models a human has admitted to which tier
// (CONTEXT.md: enrolled model).
//
// The whole of eligibility. Capability and price are fetched; this is not, and
// there is no path by which a model reaches a candidate list without appearing
// here first. That asymmetry is the design: models.dev gains entries daily, and
// none of them may start doing this agent's work because upstream published
// them.
//
// Order within a tier is the order the human wrote, and it is the resolver's
// preference order. Deliberately not derived - not cheapest-first, not
// largest-context-first. A derived order would silently re-rank the tier when
// upstream changed a price, which is the same class of surprise as a model
// becoming eligible on its own.
type Enrolment struct {
	tiers map[Tier][]Ref
	order []Tier
}

// NewEnrolment builds an enrolment from ordered tiers.
func NewEnrolment(tiers ...TierEnrolment) (*Enrolment, error) {
	e := &Enrolment{tiers: make(map[Tier][]Ref, len(tiers))}
	for _, t := range tiers {
		if t.Name == "" {
			return nil, fmt.Errorf("enrolment: a tier has no name")
		}
		if _, dup := e.tiers[t.Name]; dup {
			return nil, fmt.Errorf("enrolment: tier %q declared twice", t.Name)
		}
		if len(t.Models) == 0 {
			// An empty tier is a tier every job requiring it fails against,
			// one job at a time, at 04:00. Refusing it at load is the same
			// bargain the transition registry makes: a malformed configuration
			// is a startup failure rather than a surprise later.
			return nil, fmt.Errorf("enrolment: tier %q has no models", t.Name)
		}
		seen := make(map[Ref]bool, len(t.Models))
		for _, r := range t.Models {
			if r.Provider == "" || r.Model == "" {
				return nil, fmt.Errorf("enrolment: tier %q has an incomplete model reference", t.Name)
			}
			if seen[r] {
				return nil, fmt.Errorf("enrolment: tier %q enrols %s twice", t.Name, r)
			}
			seen[r] = true
		}
		e.tiers[t.Name] = append([]Ref(nil), t.Models...)
		e.order = append(e.order, t.Name)
	}
	return e, nil
}

// TierEnrolment is one tier and the models enrolled in it, in preference order.
type TierEnrolment struct {
	Name   Tier
	Models []Ref
}

// Tier returns the models enrolled in a tier, in preference order.
//
// A tier nobody enrolled anything in is absent rather than empty, so the
// resolver can tell "this tier does not exist" from "nothing in it qualified".
// They are different mistakes and deserve different errors.
func (e *Enrolment) Tier(t Tier) ([]Ref, bool) {
	if e == nil {
		return nil, false
	}
	refs, ok := e.tiers[t]
	return refs, ok
}

// Tiers returns the tier names, in the order they were declared.
func (e *Enrolment) Tiers() []Tier {
	if e == nil {
		return nil
	}
	return append([]Tier(nil), e.order...)
}

// wireEnrolment is the on-disk enrolment, written by the NixOS module.
//
// JSON, and an array of tiers rather than an object keyed by tier name, so that
// the declared order survives the round trip and `afk` can list tiers the way
// the operator wrote them. A mini-format of our own was the alternative and is
// worse: the module already has builtins.toJSON, and a format nobody can
// generate without a parser is a format that gets generated wrongly.
type wireEnrolment struct {
	Tiers []struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
	} `json:"tiers"`
}

// DecodeEnrolment reads an enrolment.
//
// This is where a human's decision enters the binary, and it is strict on
// purpose: an unreadable enrolment must stop the agent rather than degrade it.
// The degradation that matters - a stale catalogue - is handled in Source, and
// works precisely because the enrolment underneath it is known good.
func DecodeEnrolment(r io.Reader) (*Enrolment, error) {
	var doc wireEnrolment
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode enrolment: %w", err)
	}
	if len(doc.Tiers) == 0 {
		return nil, fmt.Errorf("decode enrolment: no tiers")
	}

	tiers := make([]TierEnrolment, 0, len(doc.Tiers))
	for _, t := range doc.Tiers {
		refs := make([]Ref, 0, len(t.Models))
		for _, s := range t.Models {
			ref, err := ParseRef(s)
			if err != nil {
				return nil, fmt.Errorf("decode enrolment: tier %q: %w", t.Name, err)
			}
			refs = append(refs, ref)
		}
		tiers = append(tiers, TierEnrolment{Name: Tier(t.Name), Models: refs})
	}
	return NewEnrolment(tiers...)
}
