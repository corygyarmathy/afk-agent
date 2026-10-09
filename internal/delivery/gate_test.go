package delivery_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/delivery"
	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/statefile"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// The stub kind's own states. Spelled as no real kind spells a state, so a
// machine that moved to a real kind's state by its literal fails here.
const (
	drafting     = "stub-drafting"
	handedBack   = "stub-handed-back"
	gone         = "stub-gone"
	lost         = "stub-lost"
	asked        = "stub-asked"
	stubGateName = "stub-gate"
)

// stubProgress is the stub kind's progress: delivery's, and what the stub
// supplies its members from.
type stubProgress struct {
	delivery.Progress

	// Anchor is the head the stub's work must keep, or none.
	Anchor string `json:"anchor,omitempty"`

	// Questions is what the stub's session left to hand back in place of
	// commits, or nothing.
	Questions string `json:"questions,omitempty"`
}

// stub is a kind that delivers, as small as the machine allows: each member
// records that it was reached, and goes to a state of the stub's own.
type stub struct {
	reason, output string
	handBacks      int
}

func (s *stub) kind(anchored bool) delivery.Kind[stubProgress, *stubProgress] {
	k := delivery.Kind[stubProgress, *stubProgress]{
		Job:     store.KindRevise,
		Session: drafting,
		HandBack: func(_ context.Context, in transition.In, _ stubProgress, reason, output string) (transition.Result, error) {
			s.reason, s.output = reason, output
			s.handBacks++
			return transition.Result{State: handedBack}, nil
		},
		Gone: func(_ context.Context, in transition.In) (transition.Result, error) {
			return transition.Result{State: gone, RunAt: in.Now}, nil
		},
		Lost: func(_ context.Context, in transition.In) (transition.Result, error) {
			return transition.Result{State: lost}, nil
		},
		Nothing: func(p stubProgress, fix bool) string {
			if fix {
				return "stub: no fix since " + p.Pushed
			}
			return "stub: nothing before the push"
		},
		Gaps: func(_ context.Context, in transition.In, p stubProgress) (transition.Result, bool, error) {
			if p.Questions == "" {
				return transition.Result{}, false, nil
			}
			s.reason = p.Questions
			return transition.Result{State: asked}, true, nil
		},
	}
	if anchored {
		k.Anchor = func(p stubProgress) string { return p.Anchor }
		k.Rewrote = func(p stubProgress) string { return "stub: rewrote " + p.Anchor }
	}
	return k
}

// fixture is a stub job in gating, with a workspace cloned from a bare remote
// on its own branch, and the gate a command that fails while the workspace
// has a file named broken committed.
type fixture struct {
	t      *testing.T
	stub   *stub
	params *delivery.Params
	store  store.Store
	job    store.Job
	ws     string
	runs   string
	logs   []string
	base   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "afk", "GIT_AUTHOR_EMAIL": "afk@example.invalid",
		"GIT_COMMITTER_NAME": "afk", "GIT_COMMITTER_EMAIL": "afk@example.invalid",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	f := &fixture{t: t, stub: &stub{}, store: storetest.Open(t), runs: filepath.Join(dir, "gate.runs")}
	f.job = storetest.Seed(t, f.store, store.KindRevise, 7, delivery.Gating)
	f.params = &delivery.Params{
		Store:    f.store,
		StateDir: filepath.Join(dir, "state"),
		Gate:     fmt.Sprintf("echo ran >> %s && test ! -e broken", f.runs),
		Attempts: 2,
		Log:      func(msg string) { f.logs = append(f.logs, msg) },
	}
	f.ws = filepath.Join(f.params.StateDir, "workspaces", f.job.ID)
	remote := filepath.Join(dir, "remote.git")
	f.git(dir, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	f.git(dir, "clone", "--quiet", remote, f.ws)
	f.commit("README")
	f.git(f.ws, "checkout", "--quiet", "-b", "stub-7")
	f.base = f.head()
	f.save(stubProgress{Progress: delivery.Progress{Progress: work.Progress{Nonce: "n", Branch: "stub-7", Base: f.base, Into: "main"}}})
	return f
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit is the session committing a file named name.
func (f *fixture) commit(name string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.ws, name), []byte(name+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(f.ws, "add", name)
	f.git(f.ws, "commit", "--quiet", "-m", "add "+name)
	return f.head()
}

func (f *fixture) head() string { return f.git(f.ws, "rev-parse", "HEAD") }

func (f *fixture) progressPath() string {
	return filepath.Join(f.params.StateDir, "progress", f.job.ID+".json")
}

func (f *fixture) save(p stubProgress) {
	f.t.Helper()
	if err := statefile.Save(f.progressPath(), p); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) progress() stubProgress {
	f.t.Helper()
	var p stubProgress
	if err := statefile.Load(f.progressPath(), &p); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// change is the saved progress with change made to it.
func (f *fixture) change(change func(p *stubProgress)) {
	f.t.Helper()
	p := f.progress()
	change(&p)
	f.save(p)
}

// gate runs the stub kind's gate, as registered, from the job's persisted
// state: the state it moved to.
func (f *fixture) gate(anchored bool) string {
	f.t.Helper()
	m := delivery.Machine[stubProgress, *stubProgress]{Kind: f.stub.kind(anchored), Params: func() *delivery.Params { return f.params }}
	r := &transition.Runner{Store: f.store, Registry: transition.MustRegistry(delivery.Must(m.Gate(stubGateName))), Holder: "test", LeaseTTL: time.Minute}
	out, err := r.Run(context.Background(), stubGateName, f.job.ID)
	if err != nil {
		f.t.Fatalf("%s: %v", stubGateName, err)
	}
	return out.To
}

// gated is how many times the gate command ran.
func (f *fixture) gated() int {
	b, err := os.ReadFile(f.runs)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Count(string(b), "ran")
}

// correcting puts a correction of the review of reviewed under way, the work
// last pushed at pushed.
func (f *fixture) correcting(reviewed, pushed string) {
	f.change(func(p *stubProgress) {
		p.Pushed = pushed
		p.Correction = &correction.Correction{Reviewed: reviewed, Review: 1, Given: true}
	})
}

// Behaviour 20.
func TestWorkTheGatePassesGoesOnToThePush(t *testing.T) {
	f := newFixture(t)
	f.commit("work")
	f.change(func(p *stubProgress) { p.Failure, p.Why = "an old failure", "old" })
	if got := f.gate(false); got != delivery.Pushing {
		t.Fatalf("moved to %q, want %q", got, delivery.Pushing)
	}
	if p := f.progress(); p.Failure != "" || p.Why != "" {
		t.Errorf("a gate that passed left its last failure: %q, %q", p.Why, p.Failure)
	}
}

// Behaviour 21.
func TestAFailedGateWithAttemptsLeftGoesBackToTheSession(t *testing.T) {
	f := newFixture(t)
	f.commit("broken")
	if got := f.gate(false); got != drafting {
		t.Fatalf("moved to %q, want the stub's session state %q", got, drafting)
	}
	p := f.progress()
	if p.Attempts != 1 || !strings.Contains(p.Why, "failed on the work") {
		t.Errorf("the failure was not recorded: attempts %d, why %q", p.Attempts, p.Why)
	}
	if f.stub.handBacks != 0 {
		t.Error("a gate with attempts left handed back")
	}
}

// Behaviour 22.
func TestAGateFailedOnItsLastAttemptHandsBackWithItsOutput(t *testing.T) {
	f := newFixture(t)
	f.commit("broken")
	f.change(func(p *stubProgress) { p.Attempts = 1 })
	if got := f.gate(false); got != handedBack {
		t.Fatalf("moved to %q, want %q", got, handedBack)
	}
	if !strings.HasPrefix(f.stub.reason, "The local gate still failed after 2 attempts. The local gate, `") {
		t.Errorf("hand-back said %q", f.stub.reason)
	}
}

// Behaviour 23.
func TestASessionThatLeftItsBranchHandsBackNamingBoth(t *testing.T) {
	f := newFixture(t)
	f.git(f.ws, "checkout", "--quiet", "-b", "elsewhere")
	f.commit("work")
	if got := f.gate(false); got != handedBack {
		t.Fatalf("moved to %q, want %q", got, handedBack)
	}
	if want := "The session left `stub-7` for `elsewhere`, and the prompt said not to change branches."; f.stub.reason != want {
		t.Errorf("hand-back said %q, want %q", f.stub.reason, want)
	}
}

// Behaviour 24, as Check has it: uncommitted changes are a failed attempt,
// the gate unrun, and handed back with what was left on the last of them.
func TestUncommittedChangesAreAFailureTheGateDoesNotRun(t *testing.T) {
	f := newFixture(t)
	f.commit("work")
	if err := os.WriteFile(filepath.Join(f.ws, "work"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := f.gate(false); got != drafting {
		t.Fatalf("moved to %q, want %q", got, drafting)
	}
	if p := f.progress(); p.Attempts != 1 || !strings.Contains(p.Failure, "M work") {
		t.Errorf("recorded attempts %d, failure %q", p.Attempts, p.Failure)
	}
	if f.gated() != 0 {
		t.Error("the gate ran on uncommitted changes")
	}

	f = newFixture(t)
	f.commit("work")
	if err := os.WriteFile(filepath.Join(f.ws, "work"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.change(func(p *stubProgress) { p.Attempts = 1 })
	if got := f.gate(false); got != handedBack {
		t.Fatalf("on the last attempt moved to %q, want %q", got, handedBack)
	}
	if !strings.Contains(f.stub.reason, "left changes to tracked files uncommitted") || !strings.Contains(f.stub.output, "M work") {
		t.Errorf("hand-back said %q with %q", f.stub.reason, f.stub.output)
	}
	if f.gated() != 0 {
		t.Error("the gate ran on uncommitted changes")
	}
}

// Behaviour 25.
func TestACorrectionThatCommittedNothingFails(t *testing.T) {
	f := newFixture(t)
	reviewed := f.commit("work")
	f.correcting(reviewed, reviewed)
	if got := f.gate(false); got != delivery.Reviewing {
		t.Fatalf("moved to %q, want %q: failed, and already at the head the review read", got, delivery.Reviewing)
	}
	want := fmt.Sprintf("The session committed nothing on top of `%s`, the head the review read, so nothing was corrected.", git.Short(reviewed))
	if c := f.progress().Correction; c.Failed != want {
		t.Errorf("the correction failed for %q, want %q", c.Failed, want)
	}
	if f.stub.handBacks != 0 {
		t.Error("a correction's own stop handed back")
	}
	if len(f.logs) != 1 || !strings.Contains(f.logs[0], want) {
		t.Errorf("logged %q", f.logs)
	}
}

// Behaviour 26, before the push and as a fix, with an anchor and without:
// the last push equal to the anchor is before the push.
func TestASessionThatCommittedNothingHandsBackInTheKindsWords(t *testing.T) {
	for _, tc := range []struct {
		name     string
		anchored bool
		pushed   bool
		want     string
	}{
		{"no anchor, nothing pushed", false, false, "stub: nothing before the push"},
		{"no anchor, after a push", false, true, "stub: no fix since "},
		{"at the anchor", true, false, "stub: nothing before the push"},
		{"past the anchor", true, true, "stub: no fix since "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.change(func(p *stubProgress) {
				if tc.anchored {
					p.Anchor, p.Pushed = f.base, f.base
				}
				if tc.pushed {
					p.Pushed = f.commit("work")
				}
			})
			if got := f.gate(tc.anchored); got != handedBack {
				t.Fatalf("moved to %q, want %q", got, handedBack)
			}
			if !strings.HasPrefix(f.stub.reason, tc.want) {
				t.Errorf("hand-back said %q, want %q", f.stub.reason, tc.want)
			}
		})
	}
}

// Behaviour 27: and only there, before the push and out of a correction.
func TestWhatASessionLeftInPlaceOfCommitsIsHandedBackInstead(t *testing.T) {
	f := newFixture(t)
	f.change(func(p *stubProgress) { p.Questions = "why?" })
	if got := f.gate(false); got != asked || f.stub.reason != "why?" {
		t.Fatalf("moved to %q saying %q, want %q", got, f.stub.reason, asked)
	}

	f = newFixture(t)
	at := f.commit("work")
	f.change(func(p *stubProgress) { p.Questions, p.Pushed = "why?", at })
	if got := f.gate(false); got != handedBack || !strings.HasPrefix(f.stub.reason, "stub: no fix since ") {
		t.Errorf("a fix that committed nothing moved to %q saying %q, want the words for it", got, f.stub.reason)
	}

	f = newFixture(t)
	at = f.commit("work")
	f.correcting(at, at)
	f.change(func(p *stubProgress) { p.Questions = "why?" })
	if got := f.gate(false); got != delivery.Reviewing {
		t.Errorf("a correction that committed nothing moved to %q, want it failed", got)
	}
}

// Behaviour 28.
func TestACorrectionThatRewroteTheReviewedHeadFailsBeforeTheGate(t *testing.T) {
	f := newFixture(t)
	reviewed := f.commit("work")
	f.correcting(reviewed, reviewed)
	f.git(f.ws, "reset", "--quiet", "--hard", f.base)
	f.commit("other")
	if got := f.gate(false); got != delivery.Reviewing {
		t.Fatalf("moved to %q, want %q", got, delivery.Reviewing)
	}
	want := "The correction rewrote `" + git.Short(reviewed) + "`, the head the review read, which a correction never does."
	p := f.progress()
	if p.Correction.Failed != want {
		t.Errorf("the correction failed for %q, want %q", p.Correction.Failed, want)
	}
	if f.gated() != 0 || p.Attempts != 0 {
		t.Errorf("the gate ran %d times, %d attempts counted", f.gated(), p.Attempts)
	}
	if f.head() != reviewed {
		t.Errorf("the workspace is at %s, not back at the head the review read", f.head())
	}
}

// Behaviour 29, and the kind with no anchor checks none.
func TestRewritingTheAnchorHandsBackBeforeTheGate(t *testing.T) {
	f := newFixture(t)
	anchor := f.commit("work")
	f.change(func(p *stubProgress) { p.Anchor, p.Pushed = anchor, anchor })
	f.git(f.ws, "reset", "--quiet", "--hard", f.base)
	f.commit("other")
	if got := f.gate(true); got != handedBack || f.stub.reason != "stub: rewrote "+anchor {
		t.Fatalf("moved to %q saying %q", got, f.stub.reason)
	}
	if p := f.progress(); f.gated() != 0 || p.Attempts != 0 {
		t.Errorf("the gate ran %d times, %d attempts counted", f.gated(), p.Attempts)
	}

	f = newFixture(t)
	f.commit("work")
	f.git(f.ws, "commit", "--quiet", "--amend", "-m", "rewritten")
	if got := f.gate(false); got != delivery.Pushing {
		t.Errorf("with no anchor, rewritten work moved to %q, want %q", got, delivery.Pushing)
	}
}

// Behaviour 30.
func TestProgressOrWorkspaceGoneIsTheKindsToSay(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(f.progressPath()); err != nil {
		t.Fatal(err)
	}
	if got := f.gate(false); got != gone {
		t.Errorf("progress gone: moved to %q, want %q", got, gone)
	}

	f = newFixture(t)
	if err := os.RemoveAll(f.ws); err != nil {
		t.Fatal(err)
	}
	if got := f.gate(false); got != gone {
		t.Errorf("workspace gone: moved to %q, want %q", got, gone)
	}
}

// Behaviour 31, back at the head the review read and not yet.
func TestAFailedCorrectionMetAgainGoesWhereItGoes(t *testing.T) {
	for _, tc := range []struct {
		name string
		back bool
		want string
	}{
		{"back at the reviewed head", true, delivery.Reviewing},
		{"not yet pushed back", false, delivery.Pushing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			reviewed := f.commit("work")
			pushed := f.commit("broken")
			if tc.back {
				pushed = reviewed
			}
			f.correcting(reviewed, pushed)
			f.change(func(p *stubProgress) { p.Correction.Failed = "earlier" })
			if got := f.gate(false); got != tc.want {
				t.Fatalf("moved to %q, want %q", got, tc.want)
			}
			if f.gated() != 0 {
				t.Error("the gate ran for a correction that already failed")
			}
		})
	}
}

// The stop rule, past the push: a correction that runs out of attempts fails,
// and goes back to the head the review read to push it back.
func TestACorrectionOutOfAttemptsFailsAndIsPushedBack(t *testing.T) {
	f := newFixture(t)
	reviewed := f.commit("work")
	pushed := f.commit("more")
	f.correcting(reviewed, pushed)
	f.commit("broken")
	f.change(func(p *stubProgress) { p.Attempts = 1 })
	if got := f.gate(false); got != delivery.Pushing {
		t.Fatalf("moved to %q, want %q", got, delivery.Pushing)
	}
	p := f.progress()
	if !strings.HasPrefix(p.Correction.Failed, "The local gate still failed after 2 attempts.") || p.Failure != "" {
		t.Errorf("the correction failed for %q, leaving failure %q", p.Correction.Failed, p.Failure)
	}
	if f.head() != reviewed || f.stub.handBacks != 0 {
		t.Errorf("at %s with %d hand-backs", f.head(), f.stub.handBacks)
	}
}

// Checked at wiring: a kind missing what the gate reaches is refused, naming
// it, and a kind with no anchor needs no words for one.
func TestAKindMissingWhatTheGateNeedsIsRefusedNamingIt(t *testing.T) {
	k := (&stub{}).kind(false)
	params := func() *delivery.Params { return nil }
	if _, err := (delivery.Machine[stubProgress, *stubProgress]{Kind: k, Params: params}).Gate(stubGateName); err != nil {
		t.Fatalf("the stub kind was refused: %v", err)
	}

	k.HandBack, k.Lost = nil, nil
	_, err := delivery.Machine[stubProgress, *stubProgress]{Kind: k, Params: params}.Gate(stubGateName)
	if err == nil || !strings.Contains(err.Error(), "HandBack") || !strings.Contains(err.Error(), "Lost") || !strings.Contains(err.Error(), stubGateName) {
		t.Errorf("refused with %v, want it to name the gate, HandBack and Lost", err)
	}

	k = (&stub{}).kind(true)
	k.Rewrote = nil
	if _, err := (delivery.Machine[stubProgress, *stubProgress]{Kind: k, Params: params}).Gate(stubGateName); err == nil || !strings.Contains(err.Error(), "Rewrote") {
		t.Errorf("an anchor with no words for it was refused with %v", err)
	}

	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "Rewrote") {
			t.Errorf("Must did not stop on it: %v", r)
		}
	}()
	delivery.Must(delivery.Machine[stubProgress, *stubProgress]{Kind: k, Params: params}.Gate(stubGateName))
}

// The registry check (transition.Registry.Unmovable), failing: the stub kind
// can reach a state nothing runs from.
func TestAStateNoTransitionRunsFromIsCaught(t *testing.T) {
	m := delivery.Machine[stubProgress, *stubProgress]{Kind: (&stub{}).kind(false), Params: func() *delivery.Params { return nil }}
	stay := func(name, from string) transition.Transition {
		return transition.Transition{Name: name, Kind: store.KindRevise, From: from, Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			return transition.Result{State: from}, nil
		}}
	}
	reg := transition.MustRegistry(delivery.Must(m.Gate(stubGateName)), stay("stub-draft", drafting))
	if none := reg.Unmovable(store.KindRevise, []string{delivery.Gating, drafting}); len(none) != 0 {
		t.Errorf("states with transitions reported as having none: %q", none)
	}
	if none := reg.Unmovable(store.KindRevise, []string{delivery.Gating, drafting, handedBack}); len(none) != 1 || none[0] != handedBack {
		t.Errorf("Unmovable = %q, want only %q", none, handedBack)
	}
}
