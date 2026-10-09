// Package delivery is what `implement` and `revise` share of a delivery: a
// model session's commits written on a pull request's branch, gated, pushed,
// and carried through CI and the advisory review, with its correction, to a
// hand-off or a hand-back (#210).
//
// It sits above the steps a delivery is made of - the workspace, the gate, the
// push, the CI watch (package work), the correction (package correction) and
// the hand-off (package handoff) - and below the two kinds that deliver. It
// cannot live in work: correction and handoff import work.
//
// It holds the progress and the parameters both kinds take. Each kind embeds
// them, and keeps what is its own beside them.
package delivery

import (
	"context"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/spend"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// Progress is how far a delivery has got: the workspace's progress, and the
// correction of the advisory review's findings. A kind embeds it in its own
// progress, which adds what that kind alone needs.
//
// The JSON is the fields of both, as the kind's own: a progress file written
// before this type existed, with `correction` as its last key, loads the same,
// and one written now loads in that code too.
type Progress struct {
	work.Progress

	// Correction is the correction of the advisory review's findings, from
	// the review that asks for one until the hand-off (#193). Nil for work
	// whose review asked for none, or that has not been reviewed.
	Correction *correction.Correction `json:"correction,omitempty"`
}

// Model runs one model. opencode.Command is one.
type Model interface {
	Run(ctx context.Context, req opencode.Request) (opencode.Reply, error)
}

// Params is what both kinds that deliver take. Built once, by the command
// surface, and embedded in each kind's dependencies; a kind's own parameters
// stay on the kind.
type Params struct {
	Model Model

	// Store is read, never written: which round of an effect is next, and
	// where the pull request's review job is. The job's own state is the
	// runner's to write.
	Store store.Store

	// Login is the agent's own account: whose reaction is a claim, whose
	// comment is an answer, and whose pull requests are the agent's.
	Login string

	// Repo is the repository, as owner/name.
	Repo string

	// Remote is the repository a workspace is cloned from and the work is
	// pushed to, and the App's installation token every git process that
	// reaches it carries.
	Remote git.Remote

	// Resolve is the ordered candidate list for the implement tier, as of
	// now (ADR 0001 §9): a revision runs on it too, since it is implementing
	// work on a branch. A *model.LimitedError defers the job to the reset.
	Resolve func(ctx context.Context) (model.Candidates, error)

	// Price is the catalogue's price for a model, for the spend footer of a
	// run opencode reported no cost for (#22). Nil prices none of them.
	Price spend.Prices

	// Bound is how many candidates a run tries before the tier counts as
	// exhausted, and TierWait how long an exhausted tier defers. Parameters.
	Bound    int
	TierWait time.Duration

	// FreshAt is the last-turn input tokens over which a session is not
	// continued, and a fresh one takes over; zero continues every session
	// (work.Tier). A parameter.
	FreshAt int

	// Rounds is how many times an effect that is read back - a push, the
	// pull request, the review asked for, a label, what is owed - is made
	// before it counts as never taking effect. A parameter.
	Rounds int

	// Gate is the local gate: a shell command run in the workspace, which
	// passes by exiting zero, and Attempts how many times it may fail before
	// the work is handed back. Parameters.
	Gate     string
	Attempts int

	// HandBackLabel is the label a hand-back applies, and HandOffLabel the
	// one the hand-off does. Parameters.
	HandBackLabel string
	HandOffLabel  string

	// CIWait is how long a head whose checks are not finished waits before
	// it is looked at again, CICeiling how long after its push they may
	// take before the work is handed back, and CIFixes how many times a
	// red run is sent back to the session. Parameters.
	CIWait    time.Duration
	CICeiling time.Duration
	CIFixes   int

	// Denylist is the paths the agent may never push, as globs (package
	// work). A parameter.
	Denylist []string

	// Sensitive is the paths the operator named as deserving closer
	// reading, by label (package sensitive). A pull request that touches
	// one says so in its description. A parameter; empty, nothing is said.
	Sensitive []sensitive.Path

	// SizeSignal is the changed non-test lines a pull request may have
	// before it needs a decision (package size). What over it means is the
	// kind's. A parameter.
	SizeSignal int

	// AskReview makes the pull request's review job due now, under a lease
	// of its own: the one store write an effect makes, and it is to another
	// job. handoff.Asker makes one.
	AskReview func(ctx context.Context, pr store.Subject, now time.Time) error

	// StateDir is where workspaces and their progress live - beside the
	// store, never in it (ADR 0001 §5).
	StateDir string

	// Log receives a line for what nothing else keeps and nobody need act
	// on: a candidate's run that failed transiently (#98), a run CI failed
	// on a head the local gate passed (dotfiles ADR 0007 §8), and what each
	// kind adds. Nil is silent.
	Log func(msg string)
}
