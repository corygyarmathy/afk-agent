package budget

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// This file is the usage endpoint's wire format, and it is the only place that
// knows it. Everything else works in the types in budget.go.
//
// The document is small and undocumented:
//
//	{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"<ISO8601>"},
//	          "weekly":{...},"monthly":{...}}}
//
// Unknown fields are read past rather than refused, which is the opposite of
// what model.DecodeEnrolment does with the file a human wrote. An enrolment
// that has gained a field is a mistake to report; a fetched document that has
// is upstream shipping, and refusing it would turn any addition to an
// undocumented response into an agent that stops observing its budget.

// wireUsage is the response.
type wireUsage struct {
	// A map rather than three named fields, so that a window this build has
	// never heard of is still part of the OR. Limited is an OR across every
	// window (ADR 0001 §11), and a fourth window decoded into no field at all
	// would be a limit the agent works straight through - which is the failure
	// this criterion was written about, one release later.
	Usage map[string]wireWindow `json:"usage"`
}

type wireWindow struct {
	Status   string  `json:"status"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt"`
}

// order is the order the windows are reported in, narrowest first. Everything
// upstream adds sorts after them, so a new window has a stable place without
// this list having to know about it.
var order = []string{"rolling", "weekly", "monthly"}

// Decode reads a usage document.
//
// A response with no usage object is an error rather than an empty state. It
// is the shape an error page, a redirect to a login form, or a renamed field
// arrives in, and an empty State reads as "no window is limited" - which would
// admit every job on the strength of a document that said nothing.
func Decode(r io.Reader, observedAt time.Time) (State, error) {
	var doc wireUsage
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return State{}, fmt.Errorf("decode usage: %w", err)
	}
	if len(doc.Usage) == 0 {
		return State{}, fmt.Errorf("decode usage: no windows")
	}

	names := make([]string, 0, len(doc.Usage))
	for name := range doc.Usage {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		ri, rj := rank(names[i]), rank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})

	state := State{ObservedAt: observedAt, Windows: make([]Window, 0, len(names))}
	for _, name := range names {
		w := doc.Usage[name]
		resets, err := parseTime(w.ResetsAt)
		if err != nil {
			// An unreadable timestamp fails the whole decode rather than
			// becoming a zero one. The Observer's fallback is the last good
			// observation, which is a better answer than a limited window with
			// no reset time: that one can only be waited out a poll at a time.
			return State{}, fmt.Errorf("decode usage: window %q: %w", name, err)
		}
		state.Windows = append(state.Windows, Window{
			Name:     name,
			Status:   Status(w.Status),
			Percent:  w.Percent,
			ResetsAt: resets,
		})
	}
	return state, nil
}

// rank orders a known window, and puts anything else after all of them.
func rank(name string) int {
	for i, n := range order {
		if n == name {
			return i
		}
	}
	return len(order)
}

// parseTime reads an ISO 8601 timestamp. Absent is the zero time and is not an
// error: a window that is not limited has nothing the agent needs to wait for,
// and the endpoint need not have said when it rolls.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an RFC 3339 timestamp", s)
	}
	return t, nil
}
