package budget

import (
	"fmt"
	"strings"
	"time"
)

// Waiver is the operator's permission for work to carry on through one spent
// window, until that window next resets (ADR 0001 §11, CONTEXT.md: waiver). It
// spends the pay-as-you-go balance, and the agent never grants or extends one:
// carrying on through a spent window is a decision to spend money, and it is
// the operator's.
//
// Until is the window's resetsAt as the operator read it from `afk budget`, and
// the waiver lapses when it passes. Nothing compares Until against the window's
// resetsAt: the endpoint moves that by up to a second between observations, so
// a comparison on the timestamp would miss the period the operator meant. What
// makes a waiver cover the current period and not the next is that the operator
// wrote this period's reset, and the next one is about a month later.
type Waiver struct {
	// Window is the window's name as the endpoint reports it: rolling, weekly,
	// monthly, or any window upstream adds. Which names may be waived at all
	// is the module's to restrict, not this package's.
	Window string

	// Until is when the waiver lapses.
	Until time.Time
}

// String renders a waiver for a log line.
func (w Waiver) String() string {
	return fmt.Sprintf("%s until %s", w.Window, w.Until.Format(time.RFC3339))
}

// Waivers is a configured set of waivers, in the order they were given.
type Waivers []Waiver

// Waived reports the waiver covering w, if one is configured and has not
// lapsed.
//
// The window is matched by name and the waiver by now. A waiver whose Until has
// passed waives nothing, so the next time the window is spent work defers
// again - which is what "it lapses when that window resets" is.
func (ws Waivers) Waived(w Window, now time.Time) (Waiver, bool) {
	for _, wa := range ws {
		if wa.Window == w.Name && now.Before(wa.Until) {
			return wa, true
		}
	}
	return Waiver{}, false
}

// skipWaived is the predicate the walks that leave a waived window out use.
func skipWaived(waivers Waivers, now time.Time) func(Window) bool {
	return func(w Window) bool {
		_, ok := waivers.Waived(w, now)
		return ok
	}
}

// Live returns the waivers that have not lapsed, for `afk budget` to show what
// is in force whatever the windows currently read.
func (ws Waivers) Live(now time.Time) Waivers {
	var out Waivers
	for _, wa := range ws {
		if now.Before(wa.Until) {
			out = append(out, wa)
		}
	}
	return out
}

// String renders the waivers for a log line.
func (ws Waivers) String() string {
	parts := make([]string, 0, len(ws))
	for _, wa := range ws {
		parts = append(parts, wa.String())
	}
	return strings.Join(parts, "; ")
}
