package model

import (
	"encoding/json"
	"fmt"
	"io"
)

// This file is the models.dev wire format, and it is the only place that knows
// it. Everything above works in the types in model.go, so a change upstream is
// a change to the decode here rather than a change that reaches the resolver.
//
// The document is keyed provider -> models -> model, and is large: the live
// response carries over seven thousand models across two hundred providers,
// most of which this agent will never be enrolled against. It is decoded whole
// anyway rather than filtered during the decode, because the filter is the
// enrolment and the enrolment is configuration - deciding here which providers
// are worth keeping would put a second, invisible eligibility rule underneath
// the one a human wrote.

// wireProvider is one provider's entry.
type wireProvider struct {
	Models map[string]wireModel `json:"models"`
}

// wireModel is one model's entry. Fields not listed here are read past; see
// Model for why the projection is narrow.
type wireModel struct {
	Name             string `json:"name"`
	Attachment       bool   `json:"attachment"`
	ToolCall         bool   `json:"tool_call"`
	Reasoning        bool   `json:"reasoning"`
	StructuredOutput bool   `json:"structured_output"`
	Modalities       struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`

	// Cost is a pointer because its absence is meaningful: a few hundred
	// entries carry no cost block, and that is "unpriced" rather than "free".
	Cost *wireCost `json:"cost"`
}

// wireCost is a model's price, which is not one number.
//
// The base fields are the headline band. `tiers` and `context_over_200k` are
// dearer bands that apply above a context threshold; upstream currently emits
// both, the second being the older spelling of the same fact. Both are read,
// and the bands are reduced field by field to the worst of them - see
// Model.Price.
type wireCost struct {
	wireBand
	Tiers []struct {
		wireBand
	} `json:"tiers"`
	ContextOver200k *wireBand `json:"context_over_200k"`
}

type wireBand struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	CacheRead float64 `json:"cache_read"`
}

// price reduces the bands to the dearest per field.
func (c *wireCost) price() Price {
	if c == nil {
		return Price{}
	}
	p := Price{
		Input:     c.Input,
		Output:    c.Output,
		CacheRead: c.CacheRead,
		Known:     true,
	}
	worst := func(b wireBand) {
		p.Input = max(p.Input, b.Input)
		p.Output = max(p.Output, b.Output)
		p.CacheRead = max(p.CacheRead, b.CacheRead)
	}
	for _, t := range c.Tiers {
		worst(t.wireBand)
	}
	if c.ContextOver200k != nil {
		worst(*c.ContextOver200k)
	}
	return p
}

// DecodeCatalogue reads a models.dev document.
//
// It streams rather than reading the whole body into memory first: the live
// document is several megabytes, and there is no reason for the agent's resident
// size to track upstream's model count.
func DecodeCatalogue(r io.Reader) (*Catalogue, error) {
	var doc map[string]wireProvider
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode catalogue: %w", err)
	}
	if len(doc) == 0 {
		// An empty document parses cleanly and would degrade the resolver to
		// an empty candidate list without anything looking wrong. Refusing it
		// here is what lets Source fall back to the last good copy instead
		// (ADR 0001 §9's acceptance: a stale catalogue beats no catalogue).
		return nil, fmt.Errorf("decode catalogue: no providers")
	}

	c := &Catalogue{models: map[Ref]Model{}}
	for providerID, p := range doc {
		for modelID, wm := range p.Models {
			ref := Ref{Provider: providerID, Model: modelID}
			c.models[ref] = Model{
				Ref:              ref,
				Name:             wm.Name,
				Attachment:       wm.Attachment,
				ToolCall:         wm.ToolCall,
				Reasoning:        wm.Reasoning,
				StructuredOutput: wm.StructuredOutput,
				InputModalities:  wm.Modalities.Input,
				OutputModalities: wm.Modalities.Output,
				Limit:            Limit{Context: wm.Limit.Context, Output: wm.Limit.Output},
				Price:            wm.Cost.price(),
			}
		}
	}
	if len(c.models) == 0 {
		return nil, fmt.Errorf("decode catalogue: no models")
	}
	return c, nil
}
