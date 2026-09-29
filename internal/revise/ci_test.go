package revise_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// redOn is a failing run on the heads it names, and green on every other.
func redOn(heads ...*string) func(sha string, call int) []github.CheckRun {
	return func(sha string, _ int) []github.CheckRun {
		for _, h := range heads {
			if *h == "" || *h == sha {
				*h = sha
				return []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "failure", URL: "https://ci.example/run/1", Title: "1 test failed", Text: "--- FAIL: TestBar"},
				}
			}
		}
		return green(sha, 0)
	}
}

// once runs the transition out of the job's state, due or not.
func (f *revFixture) once() store.Job {
	f.t.Helper()
	job := f.now()
	next, ok := f.reg.Next(job.Kind, job.State)
	if !ok {
		f.t.Fatalf("no transition out of %q", job.State)
	}
	f.runner.Run(context.Background(), next.Name, jobID())
	return f.now()
}

// gateLog is what the workspace's gate log said to the last session.
func (f *revFixture) gateLog() string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.deps.StateDir, "workspaces", jobID(), ".git", "afk-gate.log"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// A head still running is looked at again after the CI wait, having said
// nothing, and a green one moves the revision on.
func TestARevisionsCIIsWaitedOnAndGreenMovesOn(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."))
	f.tr.checks = func(sha string, call int) []github.CheckRun {
		if call == 1 {
			return []github.CheckRun{{Name: "build", Status: "in_progress"}}
		}
		return green(sha, call)
	}

	f.step(revise.Watching)
	if job := f.once(); job.State != revise.Watching || !job.NextRunAt.Equal(now.Add(f.deps.CIWait)) {
		t.Fatalf("job = %+v, want it watching again after the CI wait", job)
	}
	if f.handBack() != "" || f.handedBack() {
		t.Error("a pending head said something")
	}

	f.at = now.Add(f.deps.CIWait)
	if job := f.once(); job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Replying)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want once: green needs no fix", len(f.model.asked))
	}
}

// A required check that has not registered yet holds back green.
func TestARevisionWaitsForEveryRequiredCheck(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done."))
	f.tr.required = []string{"build", "gate"}

	f.step(revise.Watching)
	if job := f.once(); job.State != revise.Watching {
		t.Fatalf("the job is in %q, want watching: `gate` is required and has not run", job.State)
	}
}

// A red run goes back to the revision's session with what CI said, and the
// fix lands on top of the revision's push - through the gate, the denylist and
// the lease - and is watched in its turn.
func TestARedRunGoesBackToTheRevisionsSession(t *testing.T) {
	f := setupRevision(t)
	f.model.then(
		reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."),
		reviseOn("fix.txt", "## Points\n\n- \"Rename Foo\" done, and fixed."),
	)
	var first string
	f.tr.checks = redOn(&first)

	job := f.drive()
	if job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s\n%s", job.State, revise.Replying, f.handBack())
	}
	if len(f.model.asked) != 2 {
		t.Fatalf("the model was asked %d times, want 2", len(f.model.asked))
	}
	fix := f.model.asked[1]
	if fix.Session != "ses_1" || !strings.Contains(fix.Prompt, "CI failed") || !strings.Contains(fix.Prompt, "`test`") {
		t.Errorf("request = %+v, want the revision's session told CI failed on test", fix)
	}
	for _, want := range []string{"## test: failure", "https://ci.example/run/1", "1 test failed", "--- FAIL: TestBar"} {
		if !strings.Contains(f.gateLog(), want) {
			t.Errorf("the session was not given %q:\n%s", want, f.gateLog())
		}
	}
	at := f.remoteHead()
	if at == first {
		t.Fatal("the fix was not pushed")
	}
	if !f.ancestor(first, at) || !f.ancestor(f.head, at) {
		t.Errorf("the fix %s is not on top of the revision's push %s and the send-back's head %s", git.Short(at), git.Short(first), git.Short(f.head))
	}
	if _, reply := f.progress(); !strings.Contains(reply, "and fixed") {
		t.Errorf("the reply kept is %q, want the fix's", reply)
	}
	if len(f.logs) != 1 || !strings.Contains(f.logs[0], "CI caught what the local gate passed") || !strings.Contains(f.logs[0], "`test` failure") {
		t.Errorf("logs = %q, want one catch naming `test`", f.logs)
	}
}

// A fix may rewrite the revision's own commits under the lease, but never the
// head the send-back was written against.
func TestAFixMayRewriteTheRevisionsOwnCommits(t *testing.T) {
	f := setupRevision(t)
	f.model.then(
		reviseOn("bar.txt", "## Points\n\n- done."),
		func(dir string) error {
			if err := os.WriteFile(filepath.Join(dir, "bar.txt"), []byte("fixed\n"), 0o644); err != nil {
				return err
			}
			if _, err := run(dir, "git", "add", "bar.txt"); err != nil {
				return err
			}
			_, err := run(dir, "git", "commit", "--quiet", "--amend", "--no-edit")
			return err
		},
	)
	var first string
	f.tr.checks = redOn(&first)

	if job := f.drive(); job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s\n%s", job.State, revise.Replying, f.handBack())
	}
	at := f.remoteHead()
	if at == first || f.ancestor(first, at) {
		t.Errorf("the remote is at %s, want the amended revision in place of %s", git.Short(at), git.Short(first))
	}
	if !f.ancestor(f.head, at) {
		t.Errorf("the send-back's head %s is not an ancestor of %s", git.Short(f.head), git.Short(at))
	}
	if n, _ := run(f.remote, "git", "rev-list", "--count", f.head+"..feature"); n != "1" {
		t.Errorf("%s commits on top of the send-back's head, want the one amended revision", n)
	}
}

// A fix that rewrites the head the send-back was written against hands back,
// as the revision's first session would.
func TestAFixThatRewritesTheReadHeadHandsBack(t *testing.T) {
	f := setupRevision(t)
	f.model.then(
		reviseOn("bar.txt", "## Points\n\n- done."),
		func(dir string) error {
			if _, err := run(dir, "git", "reset", "--quiet", "--hard", "HEAD~2"); err != nil {
				return err
			}
			return commitOn("fix.txt")(dir)
		},
	)
	var first string
	f.tr.checks = redOn(&first)

	if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest after a hand-back", job.State)
	}
	if at := f.remoteHead(); at != first {
		t.Errorf("the remote is at %s, want the revision's push %s left alone", git.Short(at), git.Short(first))
	}
	if body := f.handBack(); !strings.Contains(body, "rewrote `"+git.Short(f.head)+"`") {
		t.Errorf("the hand-back does not say the read head was rewritten:\n%s", body)
	}
}

// A fix's push goes through the denylist, as the revision's first did.
func TestAFixsPushGoesThroughTheDenylist(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done."), commitOn("flake.lock"))
	var first string
	f.tr.checks = redOn(&first)

	if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest after a hand-back", job.State)
	}
	if at := f.remoteHead(); at != first {
		t.Errorf("the remote is at %s, want the revision's push %s: a denied path was pushed", git.Short(at), git.Short(first))
	}
	if body := f.handBack(); !strings.Contains(body, "`flake.lock`") {
		t.Errorf("the hand-back does not name the denied path:\n%s", body)
	}
}

// A fix's push is leased on the revision's own push, as the revision's first
// was on the send-back's head: someone else's push during the fix is theirs,
// and the fix is replayed onto it rather than pushed over it (#134).
func TestAFixsPushIsLeasedOnTheRevisionsPush(t *testing.T) {
	f := setupRevision(t)
	var theirs string
	f.model.then(
		reviseOn("bar.txt", "## Points\n\n- done."),
		func(dir string) error {
			if err := f.someoneElsePushes("other.txt"); err != nil {
				return err
			}
			theirs = f.remoteHead()
			return commitOn("fix.txt")(dir)
		},
	)
	var first string
	f.tr.checks = redOn(&first)

	if job := f.drive(); job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s\n%s", job.State, revise.Replying, f.handBack())
	}
	at := f.remoteHead()
	if parent, err := run(f.remote, "git", "rev-parse", at+"^"); err != nil || parent != theirs {
		t.Errorf("the pushed head's parent is %s, want their push %s: only the fix goes on top", git.Short(parent), git.Short(theirs))
	}
	if !f.ancestor(first, at) {
		t.Errorf("the revision's push %s is not an ancestor of %s", git.Short(first), git.Short(at))
	}
}

// A workspace lost after the revision pushed is handed back when CI sends a
// red run to it, rather than made afresh from the send-back's head: the points
// would be done over, and the lease would refuse the push over the revision's
// own.
func TestAWorkspaceLostAfterThePushHandsBack(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."))
	var first string
	f.tr.checks = redOn(&first)

	f.step(revise.Watching)
	if job := f.once(); job.State != revise.Revising {
		t.Fatalf("the job is in %q, want %s: CI was red", job.State, revise.Revising)
	}
	if err := os.RemoveAll(filepath.Join(f.deps.StateDir, "workspaces", jobID())); err != nil {
		t.Fatal(err)
	}

	if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest after a hand-back", job.State)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want once: nothing is done over", len(f.model.asked))
	}
	if at := f.remoteHead(); at != first {
		t.Errorf("the remote is at %s, want the revision's push %s", git.Short(at), git.Short(first))
	}
	body := f.handBack()
	for _, want := range []string{"lost the revision's workspace after it pushed `" + git.Short(first) + "`", "\"Rename Foo\" done."} {
		if !strings.Contains(body, want) {
			t.Errorf("the hand-back does not say %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Someone else") {
		t.Errorf("the hand-back blames someone else for the agent's own push:\n%s", body)
	}
}

// A progress file lost after the revision pushed, with its send-back kept, is
// handed back when CI sends a red run to it, rather than made afresh from the
// send-back's head: without the progress the lease is gone, and the revision's
// own push would be taken for someone else's (#160).
func TestAProgressLostAfterThePushHandsBack(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."))
	var first string
	f.tr.checks = redOn(&first)

	f.step(revise.Watching)
	if job := f.once(); job.State != revise.Revising {
		t.Fatalf("the job is in %q, want %s: CI was red", job.State, revise.Revising)
	}
	if err := os.Remove(filepath.Join(f.deps.StateDir, "progress", jobID()+".json")); err != nil {
		t.Fatal(err)
	}

	if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest after a hand-back", job.State)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want once: nothing is done over", len(f.model.asked))
	}
	if at := f.remoteHead(); at != first {
		t.Errorf("the remote is at %s, want the revision's push %s", git.Short(at), git.Short(first))
	}
	body := f.handBack()
	for _, want := range []string{"lost its record of the revision after it pushed `" + git.Short(first) + "`", owed.RevisionMarker(1)} {
		if !strings.Contains(body, want) {
			t.Errorf("the hand-back does not say %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Someone else") {
		t.Errorf("the hand-back blames someone else for the agent's own push:\n%s", body)
	}
}

// A fix that commits nothing is handed back, and says it was the fix that
// added nothing.
func TestAFixThatCommitsNothingHandsBack(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done."), func(string) error { return nil })
	var first string
	f.tr.checks = redOn(&first)

	if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest after a hand-back", job.State)
	}
	if body := f.handBack(); !strings.Contains(body, "the revision's last push") {
		t.Errorf("the hand-back does not say the fix added nothing to the revision's push:\n%s", body)
	}
}

// Fixes are bounded as implement's are: out of them, the revision is handed
// back on the pull request with the branch pushed, what CI said, and the
// points done so far.
func TestARevisionOutOfFixesHandsBackWithThePointsDone(t *testing.T) {
	f := setupRevision(t)
	f.model.then(
		reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."),
		commitOn("fix1.txt"),
		commitOn("fix2.txt"),
	)
	f.tr.checks = func(string, int) []github.CheckRun {
		return []github.CheckRun{{Name: "test", Status: "completed", Conclusion: "failure", Text: "--- FAIL: TestBar"}}
	}

	if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest after a hand-back", job.State)
	}
	if len(f.model.asked) != 1+f.deps.CIFixes {
		t.Errorf("the model was asked %d times, want %d: the revision and one run per fix", len(f.model.asked), 1+f.deps.CIFixes)
	}
	if !f.handedBack() {
		t.Error("no hand-back label on the pull request")
	}
	body := f.handBack()
	for _, want := range []string{"CI still failed after 2 fixes.", "\"Rename Foo\" done.", "--- FAIL: TestBar"} {
		if !strings.Contains(body, want) {
			t.Errorf("the hand-back does not say %q:\n%s", want, body)
		}
	}
	if _, err := run(f.remote, "git", "cat-file", "-e", "feature:fix2.txt"); err != nil {
		t.Error("the last fix is not on the pushed branch")
	}
	if len(f.logs) != 3 {
		t.Errorf("%d catches logged, want one per red head: %q", len(f.logs), f.logs)
	}
}

// A head whose checks never finish is handed back once the ceiling passes.
func TestARevisionPastTheCeilingHandsBack(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."))
	f.tr.checks = func(string, int) []github.CheckRun {
		return []github.CheckRun{{Name: "build", Status: "queued"}}
	}

	f.step(revise.Watching)
	f.once()
	f.at = now.Add(f.deps.CICeiling)
	if job := f.once(); job.State != revise.HandingBack {
		t.Fatalf("the job is in %q, want %s", job.State, revise.HandingBack)
	}
	f.drive()
	body := f.handBack()
	for _, want := range []string{"CI had not finished", "\"Rename Foo\" done."} {
		if !strings.Contains(body, want) {
			t.Errorf("the hand-back does not say %q:\n%s", want, body)
		}
	}
}

// Someone else's push while CI runs is theirs: the revision hands back rather
// than fix on top of a head it does not own.
func TestAPushBySomeoneElseWhileCIRunsHandsBack(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done."))
	f.step(revise.Watching)
	theirs := f.humanPushes("other.txt")

	if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest after a hand-back", job.State)
	}
	if at := f.remoteHead(); at != theirs {
		t.Errorf("the remote is at %s, want their push %s", git.Short(at), git.Short(theirs))
	}
	if body := f.handBack(); !strings.Contains(body, "Someone else pushed to `feature` while CI ran") {
		t.Errorf("the hand-back does not say someone else pushed:\n%s", body)
	}
}

// A pull request closed while CI runs is the operator's decision: the job
// rests, and says nothing.
func TestAClosedPullRequestWhileCIRunsRests(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done."))
	f.step(revise.Watching)
	f.tr.pr.State = "closed"

	if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest", job.State)
	}
	if f.handBack() != "" || f.handedBack() {
		t.Error("a closed pull request was handed back")
	}
	if _, err := os.Stat(filepath.Join(f.deps.StateDir, "workspaces", jobID())); !os.IsNotExist(err) {
		t.Errorf("the workspace is still there: %v", err)
	}
}

// A closed pull request's rest that was cleared but not committed - killed
// between the two - rests on the next run too, rather than read the progress
// it cleared as a lost revision to hand back.
func TestAClosedPullRequestClearedButNotCommittedRests(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done."))
	f.step(revise.Watching)
	f.tr.pr.State = "closed"
	if err := os.Remove(filepath.Join(f.deps.StateDir, "progress", jobID()+".json")); err != nil {
		t.Fatal(err)
	}

	if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest", job.State)
	}
	if f.handBack() != "" || f.handedBack() {
		t.Error("a closed pull request was handed back")
	}
}
