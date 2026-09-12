// Package budget is budget observation and admission control (ADR 0001 §11,
// §12).
//
// One account-wide dollar budget across three independent windows, read from
// the provider's own endpoint. Two properties of that shape decide everything
// here:
//
//   - There is no per-model dimension. When the budget is spent, every model is
//     spent at once, so there is nothing to fail over to and nothing cheaper to
//     prefer. The only question this package answers is whether new work may
//     start, and if not, when to come back.
//   - The windows are independent. Limited is an OR across all three: on
//     2026-09-11 the monthly window read `rate-limited` at 100% while rolling
//     and weekly both read `ok`, and a check on one window would have said
//     everything was fine.
//
// The budget is observed, never estimated. Nothing here keeps a ledger of the
// agent's own spend: the endpoint sees interactive use of the same account as
// well, which is the whole reason it is the source of truth. Nothing here is
// persisted either - a budget observation is re-derivable from the provider by
// definition, and a column holding one is the decay TestNoRederivableColumns
// exists to catch.
package budget

import (
	"fmt"
	"time"
)

// Endpoint is where usage comes from.
//
// Undocumented, and added by anomalyco/opencode#16513. A constant rather than a
// parameter for the same reason model.Endpoint is: it is the identity of the
// document this package knows how to read, and pointing it elsewhere would mean
// a different schema. Tests never reach it - Observer takes its fetch as a
// field.
const Endpoint = "https://opencode.ai/zen/go/v1/usage"

// Status is what the provider says about one window.
//
// A string rather than a bool, and compared against the one value that means
// limited rather than against the one that means fine. A status this build has
// not heard of must read as "not limited": the safe direction here is the
// opposite of the resolver's, because refusing to work on a word we do not
// recognise stops the agent on a provider's copy-edit, while continuing costs
// at most a transient failure that is already handled as one (ADR 0001 §12).
type Status string

const (
	StatusOK          Status = "ok"
	StatusRateLimited Status = "rate-limited"
)

// Window is one usage window, as the endpoint reports it.
type Window struct {
	// Name is the window's key in the response: rolling, weekly, monthly.
	Name string

	Status Status

	// Percent is how much of the window is spent, 0-100.
	Percent float64

	// ResetsAt is when the window reopens, absolute. Subscription-anniversary
	// based rather than calendar, which is why it is read from the response
	// rather than computed from the clock.
	ResetsAt time.Time
}

// Limited reports whether this window is rate limited.
func (w Window) Limited() bool { return w.Status == StatusRateLimited }

// String renders a window for a log line.
func (w Window) String() string {
	s := fmt.Sprintf("%s %.0f%% (%s)", w.Name, w.Percent, w.Status)
	if !w.ResetsAt.IsZero() {
		s += ", resets " + w.ResetsAt.Format(time.RFC3339)
	}
	return s
}

// State is one observation of the budget: every window the endpoint reported,
// in the order it reported them.
//
// A value, and the answers below are derived from it rather than stored
// alongside it. An observation that carried its own precomputed Limited could
// disagree with its windows, and the OR across windows is the one thing about
// this document that must not be got wrong.
type State struct {
	Windows []Window

	// ObservedAt is when this state was read. Zero is the zero State, which is
	// "nothing has been observed" rather than "everything is fine" - see
	// Known.
	ObservedAt time.Time
}

// Known reports whether this state is an observation at all.
func (s State) Known() bool { return !s.ObservedAt.IsZero() }

// Limited reports whether any window is rate limited.
func (s State) Limited() bool {
	for _, w := range s.Windows {
		if w.Limited() {
			return true
		}
	}
	return false
}

// ResetsAt is when the last limited window reopens, or the zero time if nothing
// is limited.
//
// The latest of them, not the earliest. Limited is an OR, so the account stays
// limited until every window that is limited has reset; coming back when the
// first one reopens is coming back to the same answer.
//
// It can be zero while Limited is true, if the provider reported a limit with
// no timestamp. That is a real case and the caller must handle it: deferring to
// a zero time parks a job, which needs an operator to undo. See Admit.
func (s State) ResetsAt() time.Time {
	var latest time.Time
	for _, w := range s.Windows {
		if w.Limited() && w.ResetsAt.After(latest) {
			latest = w.ResetsAt
		}
	}
	return latest
}

// Peak is the window nearest its limit, which is the one worth naming in a log
// line or a notification. The zero Window if nothing was observed.
func (s State) Peak() Window {
	var peak Window
	for _, w := range s.Windows {
		// A limited window outranks an unlimited one whatever the percents
		// say: percent is a number about a window, and being limited is the
		// answer about the account.
		switch {
		case w.Limited() && !peak.Limited(), w.Limited() == peak.Limited() && w.Percent > peak.Percent:
			peak = w
		}
	}
	return peak
}

// Window returns the named window.
func (s State) Window(name string) (Window, bool) {
	for _, w := range s.Windows {
		if w.Name == name {
			return w, true
		}
	}
	return Window{}, false
}

// String renders a state for a log line.
func (s State) String() string {
	if !s.Known() {
		return "budget not observed"
	}
	out := ""
	for i, w := range s.Windows {
		if i > 0 {
			out += "; "
		}
		out += w.String()
	}
	return out
}
