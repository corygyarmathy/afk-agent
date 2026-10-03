// Package spend is what one job's own model runs cost, by model, and the
// footer that reports it on what the job writes (#22).
//
// It is reporting, not metering. It informs a human reading the pull request;
// it is never an input to admission, to the resolver, or to a retry or a
// deferral (ADR 0001 §11), and it is never reconciled against a budget
// observation - the two count different things on purpose. Nothing that
// decides imports this package, and a test says so.
//
// What it counts is what opencode reports for each run: the run's cost at the
// models' list prices and its tokens, with its sub-agents' when they were read.
// A run that failed transiently is counted as far as it got, because it was
// paid for. A record lost with the state directory is a footer missing, which
// reads as missing rather than as zero.
package spend

import (
	"fmt"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
)

// Open and Close are the hidden lines a footer is written between, which is
// how a description's footer is found again to bring it up to date.
const (
	Open  = "<!-- afk:spend -->"
	Close = "<!-- /afk:spend -->"
)

// Spent is what a job's runs have spent so far: a line for each model, in the
// order the job first used them.
type Spent struct {
	Lines []Line `json:"lines,omitempty"`
}

// Line is what the runs on one model spent.
type Line struct {
	// Model is the ref as enrolled, `provider/model`. A sub-agent runs on
	// it unless its agent is configured with a model of its own (opencode
	// 1.18.31), so its spend is counted here either way.
	Model string `json:"model"`

	Input      int `json:"input,omitempty"`
	Output     int `json:"output,omitempty"`
	Reasoning  int `json:"reasoning,omitempty"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`

	// Cost is in dollars, at list price, as opencode reports it.
	Cost float64 `json:"cost,omitempty"`

	// SubAgents is how many sub-agents' sessions the runs started, and
	// Unread how many of those could not be read: Cost is a floor when it is
	// not zero.
	SubAgents int `json:"sub_agents,omitempty"`
	Unread    int `json:"unread,omitempty"`
}

// Add counts one run of ref, whether it succeeded or failed. A run that
// reported nothing - no tokens, no cost, no sub-agent - adds no line: missing
// data reads as missing.
func (s *Spent) Add(ref model.Ref, r opencode.Reply) {
	t := r.Tokens
	if t == (opencode.Tokens{}) && r.Cost == 0 && r.SubAgents == 0 {
		return
	}
	l := s.line(ref.String())
	l.Input += t.Input
	l.Output += t.Output
	l.Reasoning += t.Reasoning
	l.CacheRead += t.CacheRead
	l.CacheWrite += t.CacheWrite
	l.Cost += r.Cost
	l.SubAgents += r.SubAgents
	l.Unread += r.Unread
}

func (s *Spent) line(ref string) *Line {
	for i := range s.Lines {
		if s.Lines[i].Model == ref {
			return &s.Lines[i]
		}
	}
	s.Lines = append(s.Lines, Line{Model: ref})
	return &s.Lines[len(s.Lines)-1]
}

// Footer is the spend as a footer for what the job writes, between Open and
// Close, or nothing when no run reported any.
//
// It says what it is - an estimate at list price of this job's own spend - so
// that it cannot be read as a bill, or as the account's spend. A model opencode
// has no price for shows its tokens and no figure rather than a zero.
func (s Spent) Footer() string {
	if len(s.Lines) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n<sub>What this job's own model runs cost, estimated at list price as opencode reports it: not a bill, and not the account's spend.", Open)
	for _, l := range s.Lines {
		fmt.Fprintf(&b, "<br>\n%s", l)
	}
	fmt.Fprintf(&b, "</sub>\n%s", Close)
	return b.String()
}

// Held is the footer for a body that brings it up to date later, With: the
// footer, or its hidden lines with nothing between them while no run has
// reported any spend. Either way the agent's lines are there, after anything a
// session wrote, so With finds them and not lines a session typed.
func (s Spent) Held() string {
	if f := s.Footer(); f != "" {
		return f
	}
	return Open + "\n" + Close
}

// String is the line as the footer gives it: the model, its tokens in and out,
// and its cost.
func (l Line) String() string {
	who := l.Model
	if l.SubAgents > 0 {
		who += " and its sub-agents"
	}
	in := fmt.Sprintf("%s in", count(l.Input+l.CacheRead+l.CacheWrite))
	if l.CacheRead > 0 {
		in += fmt.Sprintf(" (%s cached)", count(l.CacheRead))
	}
	out := fmt.Sprintf("%s out", count(l.Output+l.Reasoning))

	cost := "no listed price"
	switch {
	case l.Cost > 0 && l.Unread > 0:
		cost = fmt.Sprintf("≥ $%.4f", l.Cost)
	case l.Cost > 0:
		cost = fmt.Sprintf("$%.4f", l.Cost)
	}
	switch l.Unread {
	case 0:
	case 1:
		cost += ", with 1 sub-agent's cost unread"
	default:
		cost += fmt.Sprintf(", with %d sub-agents' cost unread", l.Unread)
	}
	return fmt.Sprintf("%s · %s · %s · %s", who, in, out, cost)
}

// count is a token count as a reader skims it: 812, 34.5k, 1.2M.
func count(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 999950: // and not 1000.0k
		return trim(fmt.Sprintf("%.1f", float64(n)/1e3)) + "k"
	}
	return trim(fmt.Sprintf("%.1f", float64(n)/1e6)) + "M"
}

func trim(s string) string { return strings.TrimSuffix(s, ".0") }

// With is body with its footer brought to footer: the one between the last
// Open and the Close after it replaced, or footer appended when body has none.
// An empty footer leaves the hidden lines and nothing between them, as Held
// does. The last, because a session's part of a description comes before the
// footer and may say anything, an Open included.
func With(body, footer string) string {
	if i := strings.LastIndex(body, Open); i >= 0 {
		if j := strings.Index(body[i:], Close); j >= 0 {
			if footer == "" {
				footer = Open + "\n" + Close
			}
			return body[:i] + footer + body[i+j+len(Close):]
		}
	}
	if footer == "" {
		return body
	}
	return strings.TrimRight(body, "\n") + "\n\n" + footer + "\n"
}

// Strip is body without its footer, for a reader that is to be unaware of
// what the work cost: the advisory review, which reviews the work and not its
// price. A body with no footer is as it was.
func Strip(body string) string {
	i := strings.LastIndex(body, Open)
	if i < 0 {
		return body
	}
	j := strings.Index(body[i:], Close)
	if j < 0 {
		return body
	}
	return strings.TrimRight(body[:i], "\n") + "\n" + strings.TrimLeft(body[i+j+len(Close):], "\n")
}
