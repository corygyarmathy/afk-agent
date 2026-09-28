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
// Until is the window's resetsAt as the operator read it from `afk budget`. A
// waiver covers the period whose reset it names and no other: it applies only
// while the window's observed resetsAt is within resetDrift of Until, and it
// lapses when Until passes. Matching the period rather than only the clock is
// what keeps a waiver from spending the balance through the next period too - a
// mistyped month, or next month's reset given early, would otherwise waive every
// period up to it.
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

// resetDrift is how far apart two readings of one period's resetsAt can be.
//
// Not a parameter: it is the endpoint's arithmetic and `afk budget`'s format,
// not a choice. The endpoint computes resetsAt as now plus a whole number of
// seconds rounded up, so the same period reads up to a second later on one
// observation than another; and the RFC 3339 the operator copies drops the
// fraction, which is up to a second earlier again.
const resetDrift = 2 * time.Second

// Covers reports whether wa waives w at now: w is the window wa names, wa has
// not lapsed, and w's observed reset is the one wa names.
//
// A window observed with no resetsAt is never covered. There is no period to
// match, and spending the balance on a guess is the wrong way to fail.
func (wa Waiver) Covers(w Window, now time.Time) bool {
	return wa.Window == w.Name && now.Before(wa.Until) && wa.Matches(w)
}

// Matches reports whether w's observed reset is the one wa names, whatever the
// clock says. `afk budget` shows it, so a waiver can be checked against the
// period it is meant for before that period is spent.
func (wa Waiver) Matches(w Window) bool {
	if w.ResetsAt.IsZero() {
		return false
	}
	d := w.ResetsAt.Sub(wa.Until)
	return -resetDrift <= d && d <= resetDrift
}

// Waivers is a configured set of waivers, in the order they were given.
type Waivers []Waiver

// Waived reports the waiver covering w at now, if one is configured.
func (ws Waivers) Waived(w Window, now time.Time) (Waiver, bool) {
	for _, wa := range ws {
		if wa.Covers(w, now) {
			return wa, true
		}
	}
	return Waiver{}, false
}

// Waive is s with the windows ws covers at now left out, and the waivers that
// covered a limited window.
//
// It is the one place a waiver is applied. Admission and resolution each read
// the result with their own reasoning unchanged, so the two cannot disagree
// about which windows a waiver leaves out. Every covered window goes, limited
// or not: a waived window is out of the threshold check as well as the limit
// (ADR 0001 §11). Only the waivers for a limited window come back, because
// those are the ones that let something through.
func (s State) Waive(ws Waivers, now time.Time) (State, []Waiver) {
	if len(ws) == 0 {
		return s, nil
	}
	out := State{ObservedAt: s.ObservedAt, Windows: make([]Window, 0, len(s.Windows))}
	var waived []Waiver
	for _, w := range s.Windows {
		wa, ok := ws.Waived(w, now)
		if !ok {
			out.Windows = append(out.Windows, w)
			continue
		}
		if w.Limited() {
			waived = append(waived, wa)
		}
	}
	return out, waived
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
