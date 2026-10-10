package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/statefile"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// The states a delivery shares between the kinds, which each kind's own
// states alias. Only the state a kind's session runs in, and the one after
// its push, are named by the kind.
const (
	Start       = "start"
	Gating      = "gating"
	Pushing     = "pushing"
	Watching    = "watching"
	Reviewing   = "reviewing"
	HandingOff  = "handing-off"
	HandingBack = "handing-back"
	Deferred    = "deferred"
)

// Delivery is how the machine reaches the part of a kind's progress that is
// this package's: promoted from the Progress the kind's embeds.
func (p *Progress) Delivery() *Progress { return p }

// Shared is a pointer to a kind's progress T, which embeds Progress.
type Shared[T any] interface {
	*T
	Delivery() *Progress
}

// Kind is a kind that delivers, as the machine needs it: what that kind does
// differently, and nothing else. A nil member is the shared behaviour where
// there is one, and a member a registered transition needs and there is none
// is refused when the transition is built (Machine.Gate).
type Kind[T any, P Shared[T]] struct {
	// Job is the kind's job kind, and Session the state its session runs
	// in.
	Job     store.Kind
	Session string

	// HandBack returns the work to a human, saying reason, with output
	// after it: where, and in what words, are the kind's.
	HandBack func(ctx context.Context, in transition.In, p T, reason, output string) (transition.Result, error)

	// Gone is what the progress or the workspace being gone at the gate
	// means. Lost is what it means once a correction has to fail with the
	// workspace gone, which is after the push.
	Gone func(ctx context.Context, in transition.In) (transition.Result, error)
	Lost func(ctx context.Context, in transition.In) (transition.Result, error)

	// Nothing is the kind's words for a session that committed nothing, out
	// of a correction: before the push, or as a fix since it.
	Nothing func(p T, fix bool) string

	// Anchor is the head the work must keep in its history out of a
	// correction, and Rewrote the kind's words for a session that rewrote
	// it. Nil keeps none and checks none; with an anchor, Rewrote is
	// required.
	Anchor  func(p T) string
	Rewrote func(p T) string

	// Gaps is a session that committed nothing before the push, out of a
	// correction, and left something to hand back in place of the words for
	// it: ok, and where the job went. Nil is nothing left.
	Gaps func(ctx context.Context, in transition.In, p T) (r transition.Result, ok bool, err error)
}

// Machine is the transitions a delivery's kinds share, for kind k. Params is
// called as a transition runs and never before, so that a registry built only
// to name the transitions has nothing to reach.
type Machine[T any, P Shared[T]] struct {
	Kind   Kind[T, P]
	Params func() *Params
}

// States is every state a job of the kind can be in that the machine names:
// the shared states, and the state the kind's session runs in. A kind's own
// list adds only the states it names itself, so a state the machine comes to
// move a job to is one the registry check reads with no kind listing it.
func (m Machine[T, P]) States() []string {
	return []string{Start, m.Kind.Session, Gating, Deferred, Pushing, Watching, Reviewing, HandingOff, HandingBack}
}

// Must is a transition the machine built, for a kind whose members are a
// literal: one missing is a programming error, and stops the agent at start-up
// as a malformed registry does (transition.MustRegistry).
func Must(t transition.Transition, err error) transition.Transition {
	if err != nil {
		panic(err)
	}
	return t
}

// Gate is the local gate as the kind's transition name: the agent's own
// reading of the work, which the session's word does not replace. It holds
// the heavy-build token: the gate builds.
func (m Machine[T, P]) Gate(name string) (transition.Transition, error) {
	k := m.Kind
	var missing []string
	need := func(member string, have bool) {
		if !have {
			missing = append(missing, member)
		}
	}
	need("Job", k.Job != "")
	need("Session", k.Session != "")
	need("Params", m.Params != nil)
	need("HandBack", k.HandBack != nil)
	need("Gone", k.Gone != nil)
	need("Lost", k.Lost != nil)
	need("Nothing", k.Nothing != nil)
	// Only an anchor has words for its rewriting.
	need("Rewrote", k.Anchor == nil || k.Rewrote != nil)
	return transition.Transition{Name: name, Kind: k.Job, From: Gating, Tokens: []string{transition.HeavyBuild}, Run: m.gate}, missed(name, missing)
}

// missed is the error for a transition named name built for a kind missing
// the members named, or nil when it misses none.
func missed(name string, missing []string) error {
	if len(missing) > 0 {
		return fmt.Errorf("delivery: %s needs the kind's %v", name, missing)
	}
	return nil
}

func (m Machine[T, P]) gate(ctx context.Context, in transition.In) (transition.Result, error) {
	d := m.Params()
	w := d.work()
	p, err := Load[T, P](w, in.Job.ID)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !w.Exists(in.Job.ID)) {
		return m.Kind.Gone(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}
	if r, ok := m.over(in, p); ok {
		// Failed here, and the move that followed lost.
		return r, nil
	}
	s := P(&p).Delivery()

	// Before the gate runs, and before an attempt is counted: a session that
	// rewrote the head the work must keep has broken the one history rule
	// there is, and a retry to make the gate pass is not what would put that
	// right. A workspace on another branch is Check's to say.
	ws := w.Dir(in.Job.ID)
	if kept := m.kept(p); kept != "" {
		if branch, err := work.BranchOf(ctx, ws); err == nil && branch == s.Branch {
			if ok, err := work.Ancestor(ctx, ws, kept, "HEAD"); err != nil {
				return transition.Result{}, err
			} else if !ok {
				return m.stop(ctx, in, p, m.rewrote(p), "")
			}
		}
	}

	r, err := w.Check(ctx, in.Job.ID, &s.Progress, d.Gate, d.Attempts)
	if err != nil {
		return transition.Result{}, err
	}
	switch r.State {
	case work.GatePassed:
		if err := w.Save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
		return transition.Result{State: Pushing, RunAt: in.Now}, nil
	case work.GateSwitched:
		return m.stop(ctx, in, p, fmt.Sprintf("The session left `%s` for `%s`, and the prompt said not to change branches.", s.Branch, r.Branch), "")
	case work.GateEmpty:
		if s.Correction.Running() {
			return m.stop(ctx, in, p, fmt.Sprintf("The session committed nothing on top of `%s`, the head the review read, so nothing was corrected.", git.Short(s.Pushed)), "")
		}
		fix := s.Pushed != m.anchor(p)
		if !fix && m.Kind.Gaps != nil {
			if r, ok, err := m.Kind.Gaps(ctx, in, p); err != nil || ok {
				return r, err
			}
		}
		return m.stop(ctx, in, p, m.Kind.Nothing(p, fix), "")
	case work.GateExhausted:
		return m.stop(ctx, in, p, fmt.Sprintf("The local gate still failed after %d attempts. %s", s.Attempts, s.Why), r.Output)
	case work.GateDirty:
		return m.stop(ctx, in, p, s.Why, r.Output)
	default: // GateFailed
		if err := w.Save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
		return transition.Result{State: m.Kind.Session, RunAt: in.Now}, nil
	}
}

// anchor is the kind's anchor for p, or none.
func (m Machine[T, P]) anchor(p T) string {
	if m.Kind.Anchor == nil {
		return ""
	}
	return m.Kind.Anchor(p)
}

// kept is the head the work must keep in its history: while a correction
// runs, the head its review read, and otherwise the kind's anchor. None checks
// nothing.
func (m Machine[T, P]) kept(p T) string {
	if c := P(&p).Delivery().Correction; c.Running() {
		return c.Reviewed
	}
	return m.anchor(p)
}

// rewrote is why the work stopped on rewriting the head it must keep.
func (m Machine[T, P]) rewrote(p T) string {
	if c := P(&p).Delivery().Correction; c.Running() {
		return "The correction rewrote `" + git.Short(c.Reviewed) + "`, the head the review read, which a correction never does."
	}
	return m.Kind.Rewrote(p)
}

// stop is the work stopped by something it did: a correction under way fails,
// and anything else is handed back through the kind. Which of the two is the
// machine's to decide; where a hand-back goes, and what it says, the kind's.
func (m Machine[T, P]) stop(ctx context.Context, in transition.In, p T, reason, output string) (transition.Result, error) {
	if P(&p).Delivery().Correction.Running() {
		return m.failCorrection(ctx, in, p, reason)
	}
	return m.Kind.HandBack(ctx, in, p, reason, output)
}

// failCorrection is a correction that cannot be finished: the gate red at its
// last attempt, CI red past its fixes, nothing committed, or anything else the
// correction's own work stopped on. It is not a hand-back. The workspace goes
// back to the head the review read, which is pushed back under the lease if the
// correction had pushed past it, and the pull request is handed off with the
// findings left as advice, marked as a correction that failed. Someone else's
// push is theirs, and still hands back.
//
// Logged, because the review says why in a sentence and the output that said
// it goes nowhere else.
func (m Machine[T, P]) failCorrection(ctx context.Context, in transition.In, p T, why string) (transition.Result, error) {
	d := m.Params()
	w := d.work()
	if !w.Exists(in.Job.ID) {
		return m.Kind.Lost(ctx, in)
	}
	s := P(&p).Delivery()
	c := s.Correction
	if err := c.Fail(ctx, d.Store, w.Dir(in.Job.ID), s.Branch, why); err != nil {
		return transition.Result{}, err
	}
	d.logf("%s: the correction of the review of `%s` failed, so the pull request goes back to it: %s", in.Job.ID, git.Short(c.Reviewed), why)
	s.Failure, s.Why = "", ""
	if err := w.Save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	r, _ := m.over(in, p)
	return r, nil
}

// over is a failed correction met again in a state that failed it, which has
// nothing left to do for it: ok, and where the job goes instead.
func (m Machine[T, P]) over(in transition.In, p T) (transition.Result, bool) {
	s := P(&p).Delivery()
	step := s.Correction.Over(s.Pushed)
	return transition.Result{State: m.step(step), RunAt: in.Now}, step != correction.Making
}

// step is the state a correction's step s goes on in.
func (m Machine[T, P]) step(s correction.Step) string {
	switch s {
	case correction.Session:
		return m.Kind.Session
	case correction.PushBack:
		return Pushing
	}
	return Reviewing
}

// Load is a job's progress, of a kind whose progress is T. A progress file
// that is there and does not describe a workspace is an error, and one that is
// not there is os.ErrNotExist.
func Load[T any, P Shared[T]](w work.Workspace, jobID string) (T, error) {
	var p T
	err := statefile.Load(w.ProgressPath(jobID), &p)
	if errors.Is(err, os.ErrNotExist) {
		return *new(T), err
	}
	if err != nil {
		return *new(T), fmt.Errorf("progress of %s: %w", jobID, err)
	}
	if !P(&p).Delivery().Complete() {
		return *new(T), fmt.Errorf("progress of %s is incomplete", jobID)
	}
	return p, nil
}

// work is the workspace machinery, as the parameters place it.
func (d *Params) work() work.Workspace {
	return work.Workspace{StateDir: d.StateDir, Remote: d.Remote}
}

// logf is one line to Log, if there is one.
func (d *Params) logf(format string, a ...any) {
	if d.Log != nil {
		d.Log(fmt.Sprintf(format, a...))
	}
}
