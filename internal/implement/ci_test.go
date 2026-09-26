package implement_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/implement"
)

func green(string, int) []github.CheckRun {
	return []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}, {Name: "lint", Status: "completed", Conclusion: "skipped"}}
}

// red is a failing run on the first head it is asked about, and green on
// every head after.
func red() func(sha string, call int) []github.CheckRun {
	var first string
	return func(sha string, _ int) []github.CheckRun {
		if first == "" {
			first = sha
		}
		if sha != first {
			return green(sha, 0)
		}
		return []github.CheckRun{
			{Name: "build", Status: "completed", Conclusion: "success"},
			{Name: "test", Status: "completed", Conclusion: "failure", URL: "https://ci.example/run/1", Title: "1 test failed", Text: "--- FAIL: TestReserve"},
		}
	}
}

// pushed is the head of the pull request's branch on the remote.
func (f *fixture) pushed() string {
	f.t.Helper()
	at, err := run(f.remote, "git", "rev-parse", "refs/heads/afk/7-1")
	if err != nil {
		f.t.Fatal(err)
	}
	return at
}

// A green head goes on to the review, and one still running is looked at
// again after the CI wait, having done nothing else.
func TestCIIsWaitedOnAndAGreenHeadGoesOn(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	f.tr.checks = func(sha string, call int) []github.CheckRun {
		if call == 1 {
			return []github.CheckRun{{Name: "build", Status: "in_progress"}}
		}
		return green(sha, call)
	}

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching || !j.NextRunAt.Equal(now.Add(f.deps.CIWait)) {
		t.Fatalf("job = %+v, want it watching again after the CI wait", j)
	}
	if len(f.tr.byAgent()) != 0 || len(f.tr.labels) != 0 {
		t.Error("a pending head said something")
	}

	f.at = now.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Reviewing {
		t.Errorf("job in %q, want %q", j.State, implement.Reviewing)
	}
	f.caught()
}

// A red run goes back to the session that wrote the commit, with what CI
// said, and the fix lands on the same branch - through the gate, the denylist
// and the push - and is watched in its turn.
func TestARedRunGoesBackToTheSessionThatWroteIt(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"), commit("fix"))
	f.tr.checks = red()

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Reviewing {
		t.Fatalf("job in %q, want %q", j.State, implement.Reviewing)
	}
	if len(f.model.asked) != 2 {
		t.Fatalf("the model was asked %d times, want 2", len(f.model.asked))
	}
	fix := f.model.asked[1]
	if fix.Session != "ses_1" || !strings.Contains(fix.Prompt, "CI failed") || !strings.Contains(fix.Prompt, "`test`") {
		t.Errorf("request = %+v, want the first session told CI failed on test", fix)
	}
	for _, want := range []string{"## test: failure", "https://ci.example/run/1", "1 test failed", "--- FAIL: TestReserve"} {
		if !strings.Contains(f.model.logs[1], want) {
			t.Errorf("the session was not given %q:\n%s", want, f.model.logs[1])
		}
	}
	if n, _ := run(f.remote, "git", "rev-list", "--count", "main..afk/7-1"); n != "2" {
		t.Errorf("%s commits on the pushed branch, want the fix on top of the first", n)
	}
	if f.pushed() != mustHead(t, f) {
		t.Error("the fix was not pushed")
	}
	if len(f.tr.opened) != 1 {
		t.Errorf("%d pull requests, want 1", len(f.tr.opened))
	}
	f.caught("fixes so far 0:")
}

// caught fails the test unless the log has one line of what CI caught per
// want, in order, each naming the failing check and saying want.
func (f *fixture) caught(want ...string) {
	f.t.Helper()
	var lines []string
	for _, l := range f.logged {
		if strings.Contains(l, "CI caught") {
			lines = append(lines, l)
		}
	}
	if len(lines) != len(want) {
		f.t.Fatalf("logged %d catches, want %d:\n%s", len(lines), len(want), strings.Join(f.logged, "\n"))
	}
	for i, l := range lines {
		for _, w := range []string{"implement-issue-7:", "pull request #101", "`test` failure", want[i]} {
			if !strings.Contains(l, w) {
				f.t.Errorf("catch %d does not say %q: %s", i, w, l)
			}
		}
	}
}

func mustHead(t *testing.T, f *fixture) string {
	t.Helper()
	head, err := run(f.workspace(), "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// A red run is one fix however many times its decision is made. A
// commit lost after the decision - a kill, or a lost lease - replays it, and
// the replay counts nothing new.
func TestARedRunReplayedIsCountedOnce(t *testing.T) {
	f := watched(t)
	f.deps.CIFixes = 1
	f.tr.checks = red()

	for i := range 2 {
		// The replay: the decision is made again from watching, with the
		// progress the first one saved.
		f.setState(implement.Watching)
		if _, err := f.run.Run(context.Background(), "implement-watch", f.job.ID); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if j := f.now(); j.State != implement.Implementing {
			t.Fatalf("run %d: job in %q, want %q: the one round is not spent yet", i, j.State, implement.Implementing)
		}
	}
	if len(f.tr.byAgent()) != 0 {
		t.Errorf("the agent said %+v, want nothing", f.tr.byAgent())
	}
}

// Running out of fixes hands back on the pull request, and only there,
// with what CI said - and never hands off.
func TestRunningOutOfFixesHandsBackOnThePullRequest(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"), commit("a"), commit("b"))
	f.tr.checks = func(string, int) []github.CheckRun {
		return []github.CheckRun{{Name: "test", Status: "completed", Conclusion: "failure", Text: "--- FAIL: TestReserve"}}
	}

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 1+f.deps.CIFixes {
		t.Errorf("the model was asked %d times, want the first run and %d fixes", len(f.model.asked), f.deps.CIFixes)
	}
	pr := 101
	posted := f.tr.byAgent()
	if len(posted) != 1 || f.tr.commentedOn[0] != pr {
		t.Fatalf("comments %+v on %v, want one hand-back on #%d", posted, f.tr.commentedOn, pr)
	}
	for _, want := range []string{"before handing this pull request off", "2 fixes", "--- FAIL: TestReserve", "stays open"} {
		if !strings.Contains(posted[0].Body, want) {
			t.Errorf("the hand-back does not say %q:\n%s", want, posted[0].Body)
		}
	}
	if len(f.tr.labelledOn) != 1 || f.tr.labelledOn[0] != pr || f.tr.labels[0] != "needs-decision" {
		t.Errorf("labels %v on %v, want the hand-back label on #%d only", f.tr.labels, f.tr.labelledOn, pr)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
	// Every red reading is a catch, the one that ran out of fixes too.
	f.caught("fixes so far 0:", "fixes so far 1:", "fixes so far 2:")
}

// A head whose checks never finish is handed back once the ceiling passes.
func TestCIThatNeverFinishesHandsBackAtTheCeiling(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))

	for f.at.Before(now.Add(f.deps.CICeiling + f.deps.CIWait)) {
		if errs := f.drive(); len(errs) != 0 {
			t.Fatalf("errors: %v", errs)
		}
		if f.now().NextRunAt.IsZero() {
			break
		}
		f.at = f.at.Add(f.deps.CIWait)
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 || f.tr.commentedOn[0] != 101 || !strings.Contains(posted[0].Body, "had not finished") {
		t.Fatalf("comments %+v on %v, want one hand-back on the pull request saying CI never finished", posted, f.tr.commentedOn)
	}
	if f.at.Before(now.Add(f.deps.CICeiling)) {
		t.Errorf("handed back at %s, before the ceiling", f.at.Sub(now))
	}
	f.caught()
}

// A required check that has not registered yet is waited for, however green
// the runs already there are, and the head goes on once it has run.
func TestARequiredCheckNotYetRegisteredIsWaitedFor(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	f.tr.required = []string{"build", "gate"}
	f.tr.checks = func(sha string, call int) []github.CheckRun {
		if call == 1 {
			return green(sha, call)
		}
		return append(green(sha, call), github.CheckRun{Name: "gate", Status: "completed", Conclusion: "success"})
	}

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching || !j.NextRunAt.Equal(now.Add(f.deps.CIWait)) {
		t.Fatalf("job = %+v, want it watching again after the CI wait", j)
	}

	f.at = now.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Reviewing {
		t.Errorf("job in %q, want %q", j.State, implement.Reviewing)
	}
}

// A required check that never registers is handed back at the ceiling, like
// one that never finishes, and the hand-back names it.
func TestARequiredCheckThatNeverRegistersHandsBackAtTheCeiling(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	f.tr.required = []string{"build", "gate"}
	f.tr.checks = green

	for f.at.Before(now.Add(f.deps.CICeiling + f.deps.CIWait)) {
		if errs := f.drive(); len(errs) != 0 {
			t.Fatalf("errors: %v", errs)
		}
		if f.now().NextRunAt.IsZero() {
			break
		}
		f.at = f.at.Add(f.deps.CIWait)
	}
	if f.at.Before(now.Add(f.deps.CICeiling)) {
		t.Errorf("handed back at %s, before the ceiling", f.at.Sub(now))
	}
	f.handedBackOnThePR("had not finished", "`gate`, required on")
}

// A head whose runs have all finished, and one failed, is red, whether or not
// every required check has registered: it goes back for a fix rather than
// waiting out the ceiling for a check that may never start. The fix's head is
// watched in its turn, required checks and all.
func TestARedRunGoesBackForAFixBeforeEveryRequiredCheckRegisters(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"), commit("fix"))
	f.tr.required = []string{"build", "gate"}
	first := red()
	f.tr.checks = func(sha string, call int) []github.CheckRun {
		runs := first(sha, call)
		if runs[len(runs)-1].Conclusion == "failure" {
			return runs
		}
		return append(runs, github.CheckRun{Name: "gate", Status: "completed", Conclusion: "success"})
	}

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 2 {
		t.Fatalf("the model was asked %d times, want 2: a fix for the red head", len(f.model.asked))
	}
	if j := f.now(); j.State != implement.Reviewing {
		t.Errorf("job in %q, want %q", j.State, implement.Reviewing)
	}
	f.caught("fixes so far 0:")
}

// A pull request a human closed while CI ran is their decision: the job rests
// and says nothing.
func TestAPullRequestClosedWhileWatchingIsLeftAlone(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	f.tr.prs = nil
	f.at = f.at.Add(time.Hour)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() || len(f.tr.byAgent()) != 0 {
		t.Errorf("job = %+v with comments %+v, want it at rest and silent", j, f.tr.byAgent())
	}
}

// A watch whose record was wiped hands the agent's pull request back rather
// than fixing it on a branch of its own.
func TestAWatchThatLostItsRecordHandsBack(t *testing.T) {
	f := setup(t, newTracker())
	f.tr.prs = []github.PullRequest{agentPR(40, "afk/7-1")}
	f.setState(implement.Watching)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 || f.tr.commentedOn[0] != 40 || !strings.Contains(posted[0].Body, "lost its record") {
		t.Fatalf("comments %+v on %v, want one hand-back on #40", posted, f.tr.commentedOn)
	}
}

// watched drives the first run to a pushed head that CI has not finished on,
// with the pull request open as #101.
func watched(t *testing.T) *fixture {
	t.Helper()
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	return f
}

// handedBackOnThePR checks for one hand-back, on #101 and nowhere else,
// saying each of want, and a job at rest.
func (f *fixture) handedBackOnThePR(want ...string) {
	f.t.Helper()
	posted := f.tr.byAgent()
	if len(posted) != 1 || f.tr.commentedOn[0] != 101 {
		f.t.Fatalf("comments %+v on %v, want one hand-back on #101", posted, f.tr.commentedOn)
	}
	for _, w := range append([]string{"before handing this pull request off", "stays open"}, want...) {
		if !strings.Contains(posted[0].Body, w) {
			f.t.Errorf("the hand-back does not say %q:\n%s", w, posted[0].Body)
		}
	}
	if strings.Contains(posted[0].Body, "Nothing was pushed") {
		f.t.Errorf("the hand-back on the pull request says nothing was pushed:\n%s", posted[0].Body)
	}
	if len(f.tr.labelledOn) != 1 || f.tr.labelledOn[0] != 101 || f.tr.labels[0] != "needs-decision" {
		f.t.Errorf("labels %v on %v, want the hand-back label on #101 only", f.tr.labels, f.tr.labelledOn)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		f.t.Errorf("job = %+v, want it at rest", j)
	}
}

// A fix goes through the same gate and denylist as the first push, and
// what stops it there is handed back on the pull request, not on the issue:
// by then the work is the pull request's. Nothing more reaches the remote.
func TestAFixThatIsStoppedBeforeItsPushHandsBackOnThePullRequest(t *testing.T) {
	unok := func(dir string) error {
		if _, err := run(dir, "git", "rm", "--quiet", "ok"); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "drop ok")
		return err
	}
	denied := func(dir string) error {
		if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
			return err
		}
		return commit(".github/workflows/ci.yml")(dir)
	}
	for _, tc := range []struct {
		name  string
		turns []func(string) error
		want  string
	}{
		{"the gate runs out", []func(string) error{unok, commit("a"), commit("b")}, "3 attempts"},
		{"the denylist", []func(string) error{denied}, "`.github/workflows/ci.yml`"},
		{"no new commit", nil, "nothing since"},
		{"another branch", []func(string) error{func(dir string) error {
			_, err := run(dir, "git", "switch", "--quiet", "--create", "elsewhere")
			return err
		}}, "left `afk/7-1`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.model.then(commit("ok"))
			f.model.then(tc.turns...)
			f.tr.checks = red()

			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			first, _ := run(f.remote, "git", "rev-parse", "main")
			if n, _ := run(f.remote, "git", "rev-list", "--count", first+"..afk/7-1"); n != "1" {
				t.Errorf("%s commits on the pushed branch, want only the first push", n)
			}
			f.handedBackOnThePR(tc.want)
		})
	}
}

// Someone else's push to the branch while CI runs is theirs: the agent hands
// the pull request back rather than fix a run their push cancelled, and never
// asks the model.
func TestAPushByAnyoneElseWhileWatchingHandsBack(t *testing.T) {
	f := watched(t)
	human := filepath.Join(t.TempDir(), "human")
	if _, err := run("", "git", "clone", "--quiet", "--branch", "afk/7-1", f.remote, human); err != nil {
		t.Fatal(err)
	}
	if err := commit("review-fix")(human); err != nil {
		t.Fatal(err)
	}
	if _, err := run(human, "git", "push", "--quiet", "origin", "afk/7-1"); err != nil {
		t.Fatal(err)
	}
	f.tr.checks = func(string, int) []github.CheckRun {
		return []github.CheckRun{{Name: "test", Status: "completed", Conclusion: "cancelled"}}
	}

	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want only the first run", len(f.model.asked))
	}
	f.handedBackOnThePR("Someone else pushed")
}

// A run waiting for a human's approval is not something a session can fix:
// it is handed back, and the model is not asked.
func TestARunWaitingForApprovalHandsBack(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	f.tr.checks = func(string, int) []github.CheckRun {
		return []github.CheckRun{
			{Name: "build", Status: "completed", Conclusion: "success"},
			{Name: "deploy", Status: "completed", Conclusion: "action_required"},
		}
	}

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want only the first run", len(f.model.asked))
	}
	f.handedBackOnThePR("waiting for approval", "`deploy`")
	// Nothing the gate could have caught: the run never ran.
	f.caught()
}

// A run that failed beside one waiting for approval is still a catch, though
// the approval is what hands the work back.
func TestAFailureBesideAnApprovalWaitIsStillACatch(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	f.tr.checks = func(string, int) []github.CheckRun {
		return []github.CheckRun{
			{Name: "test", Status: "completed", Conclusion: "failure"},
			{Name: "deploy", Status: "completed", Conclusion: "action_required"},
		}
	}

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	f.handedBackOnThePR("waiting for approval", "`deploy`")
	f.caught("fixes so far 0:")
}

// A head with a failed run and another that never finishes waits out the
// ceiling like any unfinished head, and the hand-back quotes the failure.
func TestTheCeilingHandBackQuotesWhatHadAlreadyFailed(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	f.tr.checks = func(string, int) []github.CheckRun {
		return []github.CheckRun{
			{Name: "test", Status: "completed", Conclusion: "failure", Text: "--- FAIL: TestReserve"},
			{Name: "slow", Status: "queued"},
		}
	}

	for {
		if errs := f.drive(); len(errs) != 0 {
			t.Fatalf("errors: %v", errs)
		}
		if f.now().NextRunAt.IsZero() {
			break
		}
		f.at = f.at.Add(f.deps.CIWait)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want only the first run", len(f.model.asked))
	}
	f.handedBackOnThePR("had not finished", "--- FAIL: TestReserve")
	// The failure is a catch, though CI never finished around it.
	f.caught("fixes so far 0:")
}

// A workspace lost during a fix is not a new start: the branch is
// pushed and the pull request open, so a new branch would be a second pull
// request. It is handed back on the one there is.
func TestAWorkspaceLostInAFixHandsBackOnThePullRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		lose string
	}{
		{"the workspace", "workspaces"},
		{"the whole state directory", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.model.then(commit("ok"), func(dir string) error {
				if err := commit("fix")(dir); err != nil {
					return err
				}
				return os.RemoveAll(filepath.Join(f.deps.StateDir, tc.lose))
			})
			f.tr.checks = red()

			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			if b := f.remoteBranches(); b != "afk/7-1\nmain" {
				t.Errorf("the remote has %q, want only the one branch beside main", b)
			}
			if len(f.tr.opened) != 1 {
				t.Errorf("%d pull requests, want 1", len(f.tr.opened))
			}
			f.handedBackOnThePR("lost its record")
		})
	}
}
