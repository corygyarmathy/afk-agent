package implement_test

import (
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/implement"
)

// A session whose last turn was over the threshold is not continued: the gate
// retry is a fresh session, given the issue and the failure, on the branch's
// commits (#192).
func TestAnOutgrownSessionIsNotContinuedForAGateRetry(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.FreshAt = 1000
	f.model.lastInput = 1001
	f.model.then(commit("attempt"), commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	if len(f.model.asked) != 2 {
		t.Fatalf("the model was asked %d times, want 2", len(f.model.asked))
	}
	retry := f.model.asked[1]
	if retry.Session != "" {
		t.Errorf("the retry continued session %q, want a fresh one", retry.Session)
	}
	for _, want := range []string{"implement` skill", ".git/afk-issue.md", "an earlier session's commits are on this branch", ".git/afk-gate.log"} {
		if !strings.Contains(retry.Prompt, want) {
			t.Errorf("the fresh session's prompt does not say %q:\n%s", want, retry.Prompt)
		}
	}
	if !strings.Contains(f.model.logs[1], "FAIL: no ok") {
		t.Errorf("the gate's output was not left for the fresh session: %q", f.model.logs[1])
	}
	if n, _ := run(f.workspace(), "git", "rev-list", "--count", "origin/main..HEAD"); n != "2" {
		t.Errorf("%s commits on the branch, want both sessions' on the one branch", n)
	}
	f.loggedFresh("ses_1", "1001", "1000")
}

// A fix is a continuation too, and the same rule holds for it.
func TestAnOutgrownSessionIsNotContinuedForAFix(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.FreshAt = 1000
	f.model.lastInput = 1001
	f.model.then(commit("ok"), commit("fix"))
	f.tr.checks = red()

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Reviewing {
		t.Fatalf("job in %q, want %q", j.State, implement.Reviewing)
	}
	fix := f.model.asked[1]
	if fix.Session != "" || !strings.Contains(fix.Prompt, "CI failed") {
		t.Errorf("request = %+v, want a fresh session told CI failed", fix)
	}
	if !strings.Contains(f.model.logs[1], "--- FAIL: TestReserve") {
		t.Errorf("CI's output was not left for the fresh session: %q", f.model.logs[1])
	}
	if n, _ := run(f.remote, "git", "rev-list", "--count", "main..afk/7-1"); n != "2" {
		t.Errorf("%s commits on the pushed branch, want the fix on top of the first", n)
	}
	f.loggedFresh("ses_1", "1001", "1000")
}

// A cut is a continuation too: a fresh session is given the cut, and the
// piece it cuts opens as the session's would have.
func TestAnOutgrownSessionIsNotContinuedForACut(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.SizeSignal = 10
	f.deps.FreshAt = 1000
	f.model.lastInput = 1001
	f.model.then(big, cut)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 2 {
		t.Fatalf("%d model runs, want the work and one cut", len(f.model.asked))
	}
	again := f.model.asked[1]
	if again.Session != "" {
		t.Errorf("the cut continued session %q, want a fresh one", again.Session)
	}
	for _, want := range []string{"implement` skill", ".git/afk-issue.md", "12 changed lines", "first coherent piece", "`afk/7-1-whole`", ".git/afk-remainder.md"} {
		if !strings.Contains(again.Prompt, want) {
			t.Errorf("the fresh session's prompt does not say %q:\n%s", want, again.Prompt)
		}
	}
	if len(f.tr.opened) != 1 || f.tr.opened[0].Title != "Reserve a job: the tests first" {
		t.Errorf("opened %+v, want the piece under the session's title", f.tr.opened)
	}
	f.loggedFresh("ses_1", "1001", "1000")
}

// At the threshold, and with no threshold at all, the session is continued.
func TestASessionNotOverTheThresholdIsContinued(t *testing.T) {
	for _, tc := range []struct {
		name               string
		freshAt, lastInput int
	}{
		{"at the threshold", 1000, 1000},
		{"no threshold", 0, 1 << 30},
		{"size unknown", 1000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.deps.FreshAt = tc.freshAt
			f.model.lastInput = tc.lastInput
			f.model.then(commit("attempt"), commit("ok"))

			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			if len(f.model.asked) != 2 {
				t.Fatalf("the model was asked %d times, want 2", len(f.model.asked))
			}
			if retry := f.model.asked[1]; retry.Session != "ses_1" {
				t.Errorf("the retry ran in session %q, want the first run's, ses_1", retry.Session)
			}
		})
	}
}

// The threshold is read against the last session to finish, not the first: a
// fresh session that came in under it is continued.
func TestAFreshSessionIsContinuedWhileItIsUnderTheThreshold(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.FreshAt = 1000
	f.deps.Attempts = 3
	f.model.lastInput = 1001
	f.model.then(commit("a"), func(dir string) error {
		f.model.lastInput = 10
		return commit("b")(dir)
	}, commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 3 {
		t.Fatalf("the model was asked %d times, want 3", len(f.model.asked))
	}
	if got := []string{f.model.asked[1].Session, f.model.asked[2].Session}; got[0] != "" || got[1] != "ses_2" {
		t.Errorf("the retries ran in sessions %q, want a fresh one and then that one, ses_2", got)
	}
}

// loggedFresh fails the test unless the log says once that session was not
// continued, with its size and the threshold.
func (f *fixture) loggedFresh(session string, want ...string) {
	f.t.Helper()
	var lines []string
	for _, l := range f.logged {
		if strings.Contains(l, "fresh session") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		f.t.Fatalf("logged %d fresh sessions, want 1:\n%s", len(lines), strings.Join(f.logged, "\n"))
	}
	for _, w := range append([]string{"implement-issue-7:", session}, want...) {
		if !strings.Contains(lines[0], w) {
			f.t.Errorf("the log line does not say %q: %s", w, lines[0])
		}
	}
}
