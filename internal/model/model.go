// Package model is model choice: a pure resolver over enrolled models
// (ADR 0001 §9).
//
// Three things meet here and are deliberately kept apart:
//
//   - The catalogue. What a model can do and what it costs, fetched from
//     models.dev. Facts about the world, true whether or not this agent has
//     any opinion about them.
//   - The enrolment. Which models a human has admitted to which tier
//     (CONTEXT.md: enrolled model). Eligibility is never inferred: a model
//     absent from the enrolment is not a candidate however capable the
//     catalogue says it is, so a newly published model cannot become eligible
//     on its own.
//   - The resolver. A pure function from a job kind's requirements, the
//     catalogue and observed budget state to an ordered candidate list.
//
// Pure because a resolver that needs the network to be tested will not be
// tested. Nothing in this package reaches out except Source, which is the
// fetch-and-cache seam and is kept at arm's length from Resolve for exactly
// that reason.
package model

import (
	"fmt"
	"sort"
	"strings"
)

// Ref names one model at one provider. Both halves are needed: model ids are
// unique within a provider and not across them, and the same model is often
// reachable through several providers at different prices and under different
// subscriptions.
type Ref struct {
	Provider string
	Model    string
}

// String renders a ref the way the enrolment spells it: `provider/model`.
func (r Ref) String() string { return r.Provider + "/" + r.Model }

// ParseRef reads a ref from `provider/model`.
//
// Model ids may themselves contain a slash - models.dev carries entries such
// as `qwen/qwen3.5-plus` - so the split is on the first separator and the
// remainder is the model id, whatever is in it.
func ParseRef(s string) (Ref, error) {
	provider, model, ok := strings.Cut(s, "/")
	if !ok || provider == "" || model == "" {
		return Ref{}, fmt.Errorf("%q is not provider/model", s)
	}
	return Ref{Provider: provider, Model: model}, nil
}

// Capability is one thing a job kind may require of a model.
//
// Named rather than expressed as a struct of booleans so that an unmet
// requirement can be reported in the words the requirement was written in: the
// acceptance criterion for this resolver is an error that names the capability,
// and "no enrolled model in tier %q supports %s" needs the capability to have a
// name at the point the error is built.
type Capability string

const (
	// CapAttachment is handing the model a file - the question the issue asks
	// as "does this job ever hand over an image?".
	CapAttachment Capability = "attachment"

	// CapToolCall is calling tools. Every transition that does real work needs
	// it; it is declared rather than assumed because a model without it fails
	// in a way that looks like a bad answer rather than like a missing
	// feature.
	CapToolCall Capability = "tool_call"

	// CapReasoning is an explicit reasoning mode.
	CapReasoning Capability = "reasoning"

	// CapStructuredOutput is schema-constrained output.
	CapStructuredOutput Capability = "structured_output"
)

// Modality capabilities are spelled `input:image`, `output:text`, and are
// matched against the model's declared modalities. They are open rather than a
// fixed set of constants because models.dev grows modality values - text,
// image, pdf, audio and video are in the catalogue today - and a value this
// package has not heard of must read as "not supported" rather than fail to
// parse.
const (
	inputPrefix  = "input:"
	outputPrefix = "output:"
)

// InputModality is the capability of accepting this input modality.
func InputModality(m string) Capability { return Capability(inputPrefix + m) }

// OutputModality is the capability of producing this output modality.
func OutputModality(m string) Capability { return Capability(outputPrefix + m) }

// Price is what a model costs per million tokens, as the catalogue advertises
// it.
//
// Advertised, not spent. The agent does not meter its own spend and holds no
// opinion about the account's ceiling (ADR 0001 §11, §12): usage is observed at
// the provider, and a locally kept ledger answers the wrong question. These
// numbers exist so that a job kind can decline to send work to a model dearer
// than it is worth, which is a question about a published price list and not
// about a balance.
type Price struct {
	Input     float64
	Output    float64
	CacheRead float64

	// Headline is the base band: the prices upstream lists first, and the ones
	// an operator reading models.dev will see. Carried only so that a rejection
	// can be reconciled with the document it came from - a ceiling is never
	// checked against it. When a model publishes one band, it equals the
	// fields above.
	Headline Band

	// Known is false for a model the catalogue prices nothing for. Several
	// hundred entries carry no cost block at all, and an absent price must not
	// read as free: a ceiling can only be honoured against a number, so an
	// unpriced model fails a ceiling rather than passing it silently.
	Known bool
}

// Band is a price per million tokens, as one of a model's advertised bands.
type Band struct {
	Input     float64
	Output    float64
	CacheRead float64
}

// Limit is the model's context and output window, in tokens.
type Limit struct {
	Context int
	Output  int
}

// Model is one model's entry in the catalogue.
//
// A projection of models.dev rather than a transcription of it: the fields a
// requirement can be expressed against, and nothing else. Descriptions,
// release dates, knowledge cutoffs and provider transport details are read past
// deliberately - a field carried here is a field something could start
// depending on.
type Model struct {
	Ref  Ref
	Name string

	Attachment       bool
	ToolCall         bool
	Reasoning        bool
	StructuredOutput bool

	InputModalities  []string
	OutputModalities []string

	Limit Limit

	// Price is the worst advertised band, not the headline one.
	//
	// Several models price by context length - grok-4.6 doubles above 200k
	// tokens, qwen3.7-plus triples above 256k - and which band a request lands
	// in is not knowable when the model is chosen. Taking the dearest band is
	// the only reading under which a declared ceiling is a ceiling: the
	// alternative quotes a price the run can exceed, which is a ceiling that
	// does not hold on precisely the long-context runs it was written for.
	Price Price
}

// Has reports whether the model satisfies a capability.
//
// An unrecognised capability is not satisfied. That is the safe direction: a
// requirement this build does not understand excludes every model and produces
// the unmet-capability error, rather than being quietly dropped from the
// filter and admitting a model that cannot do the work.
func (m Model) Has(c Capability) bool {
	switch c {
	case CapAttachment:
		return m.Attachment
	case CapToolCall:
		return m.ToolCall
	case CapReasoning:
		return m.Reasoning
	case CapStructuredOutput:
		return m.StructuredOutput
	}
	if v, ok := strings.CutPrefix(string(c), inputPrefix); ok {
		return contains(m.InputModalities, v)
	}
	if v, ok := strings.CutPrefix(string(c), outputPrefix); ok {
		return contains(m.OutputModalities, v)
	}
	return false
}

func contains(vs []string, v string) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}

// Catalogue is what the models this agent might use can do and what they cost.
//
// Immutable once built. It is a snapshot of a fetched document, and a caller
// holding one is entitled to assume it does not change underneath a resolution.
type Catalogue struct {
	models map[Ref]Model
}

// NewCatalogue builds a catalogue from models, for a test or a caller that has
// its entries already.
func NewCatalogue(models ...Model) *Catalogue {
	c := &Catalogue{models: make(map[Ref]Model, len(models))}
	for _, m := range models {
		c.models[m.Ref] = m
	}
	return c
}

// Lookup returns the catalogue's entry for a ref.
//
// Absent is a real answer and not an error: a model can be enrolled before the
// catalogue knows about it, or stay enrolled after it is withdrawn, and both
// are the operator's business rather than a reason for the resolver to fail.
func (c *Catalogue) Lookup(r Ref) (Model, bool) {
	if c == nil {
		return Model{}, false
	}
	m, ok := c.models[r]
	return m, ok
}

// Len is how many models the catalogue holds.
func (c *Catalogue) Len() int {
	if c == nil {
		return 0
	}
	return len(c.models)
}

// Refs returns every ref in the catalogue, ordered, for tests and for an
// operator asking what is there.
func (c *Catalogue) Refs() []Ref {
	if c == nil {
		return nil
	}
	refs := make([]Ref, 0, len(c.models))
	for r := range c.models {
		refs = append(refs, r)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Provider != refs[j].Provider {
			return refs[i].Provider < refs[j].Provider
		}
		return refs[i].Model < refs[j].Model
	})
	return refs
}
