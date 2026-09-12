package budget

import (
	"fmt"
	"time"
)

// Decision is what an observed budget says about starting new work.
//
// Three answers rather than two, because "do not start" divides into two cases
// that must be acted on differently, and collapsing them is exactly what the
// acceptance criterion for this rules out.
type Decision int

const (
	// Start admits new work. The budget is fine, or nothing is known about it.
	Start Decision = iota

	// Wait stops new work from starting and says nothing about when to come
	// back. Approaching a limit is this.
	//
	// Neither a park nor a defer (CONTEXT.md). The job the caller was holding
	// is released and stays due, so nothing is waiting for an operator and
	// nothing has been scheduled on the provider's word; the caller simply
	// stops taking jobs and looks again on its own poll interval. That is the
	// right shape for a rolling window, whose percent falls as old usage ages
	// out of it rather than at a timestamp anyone can name.
	Wait

	// Defer stops new work from starting and carries the absolute timestamp it
	// resumes at. Being limited is this, and it is the glossary's defer
	// (CONTEXT.md): the provider has said when the window reopens, and a job
	// scheduled to that time is a queue that goes quiet rather than one that
	// re-asks every poll interval and suppresses the answer.
	Defer
)

func (d Decision) String() string {
	switch d {
	case Start:
		return "start"
	case Wait:
		return "wait"
	case Defer:
		return "defer"
	}
	return fmt.Sprintf("decision(%d)", int(d))
}

// Admission is the answer to "may a new job start now", and is what a caller
// acts on.
type Admission struct {
	Decision Decision

	// Until is when work resumes, and is set only for Defer. Never zero when
	// the decision is Defer, and never in the past at the moment it was
	// decided: a Defer to either would park the job it is applied to, and a
	// parked job waits for an operator rather than for time.
	Until time.Time

	// Window is the window that decided it, for a log line or a notification.
	// The zero Window when nothing did.
	Window Window
}

// Starts reports whether new work may begin.
func (a Admission) Starts() bool { return a.Decision == Start }

// String renders an admission for a log line.
//
// The decision spells itself, so that this does not become a second switch over
// Decision that a fourth answer could be added to without.
func (a Admission) String() string {
	switch {
	case a.Decision == Defer:
		return fmt.Sprintf("%s until %s: %s", a.Decision, a.Until.Format(time.RFC3339), a.Window)
	case a.Window.Name == "":
		return a.Decision.String()
	default:
		return fmt.Sprintf("%s: %s", a.Decision, a.Window)
	}
}

// Admit decides whether new work may start, given a threshold percentage that
// counts as approaching a limit.
//
// Pure, and the whole policy: an Observer adds the fetch and the last good
// answer, and adds nothing to the reasoning below. threshold is a parameter and
// comes from configuration; zero or negative is no threshold, which means the
// only thing that stops work is an actual limit.
//
// An unobserved budget admits. That is the fail-open direction, and it is the
// one ADR 0001 §12 already accepts: a budget that cannot be read is not a
// budget that is spent, and the cost of being wrong is a transient failure at
// the provider, which is handled as one. Failing closed would let an outage of
// an undocumented endpoint stop the agent entirely.
func (s State) Admit(threshold float64, now time.Time) Admission {
	if !s.Known() {
		return Admission{Decision: Start}
	}

	if s.Limited() {
		// The window named is the one that reopens last, because that is the
		// window the timestamp came from. Naming the peak instead would print a
		// window whose own reset is not the time being deferred to, and "defer
		// until T: <window that reopens well before T>" is a log line that
		// reads as a bug in the agent rather than as the OR working.
		last := s.LastToReset()
		if last.ResetsAt.After(now) {
			return Admission{Decision: Defer, Until: last.ResetsAt, Window: last}
		}
		// Limited, but with no future timestamp to come back at: the provider
		// reported no resetsAt, or the one it reported has passed and this
		// observation has not caught up. Waiting re-asks at the caller's own
		// interval, which costs a poll; deferring would push the job to a zero
		// or past time, and NextRunAt in the past is a job that is due
		// immediately while a zero one is a job nothing ever picks up again.
		//
		// Nothing reopens, so the window worth naming is the worst one.
		return Admission{Decision: Wait, Window: s.Peak()}
	}

	if threshold > 0 {
		// Approaching is a Wait rather than a Defer even though the window
		// carries a reset time. A rolling window's percent falls as old usage
		// ages out of it, so it can drop below the threshold well before it
		// resets, and deferring to the reset would stand the agent down for
		// hours it did not need to lose.
		//
		// Nothing is limited at this point, so Peak is simply the window
		// nearest its limit by percent: if any window is at or above the
		// threshold then that one is, and it is the one to name.
		if peak := s.Peak(); peak.Percent >= threshold {
			return Admission{Decision: Wait, Window: peak}
		}
	}
	return Admission{Decision: Start}
}
