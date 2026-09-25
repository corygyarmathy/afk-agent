package implement_test

import (
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
}

func mustHead(t *testing.T, f *fixture) string {
	t.Helper()
	head, err := run(f.workspace(), "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// Running out of CI rounds hands back on the pull request, and only there,
// with what CI said - and never hands off.
func TestRunningOutOfCIRoundsHandsBackOnThePullRequest(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"), commit("a"), commit("b"))
	f.tr.checks = func(string, int) []github.CheckRun {
		return []github.CheckRun{{Name: "test", Status: "completed", Conclusion: "failure", Text: "--- FAIL: TestReserve"}}
	}

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 1+f.deps.CIRounds {
		t.Errorf("the model was asked %d times, want the first run and %d rounds", len(f.model.asked), f.deps.CIRounds)
	}
	pr := 101
	posted := f.tr.byAgent()
	if len(posted) != 1 || f.tr.commentedOn[0] != pr {
		t.Fatalf("comments %+v on %v, want one hand-back on #%d", posted, f.tr.commentedOn, pr)
	}
	for _, want := range []string{"before handing this pull request off", "2 rounds", "--- FAIL: TestReserve", "stays open"} {
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
