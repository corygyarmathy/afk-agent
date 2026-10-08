package implement_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/review"
)

// reviewID is the id of the advisory review the fixture posts.
const reviewID = 900

// postFindings is the review job's review of the pushed head landing on the
// pull request, as the agent posts one, with a Standards finding (1), a
// Correctness finding with its reproduction (2) and an Approach finding (3).
// The review job is then at rest, so that a second ask would show.
func (f *fixture) postFindings() string {
	f.t.Helper()
	head := f.pushed()
	body := "<details>\n<summary>Advisory review of <code>" + git.Short(head) + "</code>. Open it after your own reading.</summary>\n\n" +
		review.Marker(head) + "\nThis review does not gate or block merging.\n\n" +
		"## Standards\n\n1. **should-fix** — `ok:1`: breaks the rule in AGENTS.md.\n\n" +
		"## Spec\n\nNone.\n\n" +
		"## Correctness\n\n2. **blocker** — `ok:1`: the file is empty.\n\n<details><summary>Reproduction</summary>\n\n```sh\ntest -s ok\n```\n\n```\nexit 1\n```\n\n</details>\n\n" +
		"## Approach\n\n3. **consider** — a sketch.\n\n</details>\n"
	f.tr.mu.Lock()
	f.tr.comments = append(f.tr.comments, github.Comment{ID: reviewID, Login: agent, Body: body})
	f.tr.mu.Unlock()
	f.restReviewJob()
	return head
}

// theReview is the advisory review as it is on the tracker now.
func (f *fixture) theReview() string {
	f.t.Helper()
	f.tr.mu.Lock()
	defer f.tr.mu.Unlock()
	for _, c := range f.tr.comments {
		if c.ID == reviewID {
			return c.Body
		}
	}
	f.t.Fatal("the review is gone")
	return ""
}

// correcting is a turn that commits a change naming the findings it corrects,
// and keeps what the session was given.
func correcting(given *string, name string, trailers ...string) func(dir string) error {
	return func(dir string) error {
		if b, err := os.ReadFile(filepath.Join(dir, ".git", correction.File)); err == nil && given != nil {
			*given = string(b)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			return err
		}
		if _, err := run(dir, "git", "add", name); err != nil {
			return err
		}
		msg := "correct " + name
		if len(trailers) > 0 {
			msg += "\n\n" + strings.Join(trailers, "\n")
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", msg)
		return err
	}
}

// handedOffWithNoHandBack checks for the hand-off label on #101 and nothing
// said but the review, and a job at rest.
func (f *fixture) handedOffWithNoHandBack() {
	f.t.Helper()
	if strings.Join(f.tr.labels, ",") != "needs-review" || f.tr.labelledOn[0] != 101 {
		f.t.Errorf("labels %v on %v, want only the hand-off label on #101", f.tr.labels, f.tr.labelledOn)
	}
	if posted := f.tr.byAgent(); len(posted) != 1 || posted[0].ID != reviewID {
		f.t.Errorf("the agent said %+v, want nothing but the review", posted)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		f.t.Errorf("job = %+v, want it at rest", j)
	}
	if rj := f.reviewJob(); !rj.NextRunAt.IsZero() {
		f.t.Errorf("review job = %+v, want it never asked again: one review run per hand-off", rj)
	}
}

// A review with a Correctness or Standards finding sends the work back to the
// session that wrote it, continued, with the findings. Its correction goes
// through the gate, the push and CI, is never reviewed again, and the review is
// edited: the finding a commit names collapses to a link to that commit, the
// one none names stays as advice, and the summary names both heads. Then the
// pull request is handed off.
func TestAReviewsFindingsAreCorrectedBeforeTheHandOff(t *testing.T) {
	f := greenPR(t)
	reviewed := f.postFindings()
	var given string
	f.model.then(correcting(&given, "regression", "Corrects: advisory 2"))

	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	if len(f.model.asked) != 2 {
		t.Fatalf("the model was asked %d times, want the work and its correction", len(f.model.asked))
	}
	fix := f.model.asked[1]
	if fix.Session != "ses_1" || !strings.Contains(fix.Prompt, ".git/afk-findings.md") || !strings.Contains(fix.Prompt, "Corrects: advisory 3") {
		t.Errorf("the correction ran in %q with:\n%s\nwant the session that wrote the branch, given the findings", fix.Session, fix.Prompt)
	}
	for _, want := range []string{"advisory 1 (Standards)", "advisory 2 (Correctness)", "test -s ok"} {
		if !strings.Contains(given, want) {
			t.Errorf("the findings file does not have %q:\n%s", want, given)
		}
	}
	if strings.Contains(given, "advisory 3") {
		t.Errorf("the findings file has the Approach finding:\n%s", given)
	}

	head := f.pushed()
	if head == reviewed {
		t.Fatal("the correction was never pushed")
	}
	if parent, _ := run(f.remote, "git", "rev-parse", head+"^"); parent != reviewed {
		t.Errorf("the correction is on %s, want it on the reviewed head %s", parent, reviewed)
	}
	body := f.theReview()
	for _, want := range []string{
		review.Marker(reviewed),
		"corrected to <code>" + git.Short(head) + "</code>",
		"<details><summary>2. Corrected in <code>" + git.Short(head) + "</code>.</summary>",
		"_Correction attempted: no commit corrected this, so it is advice._",
		"3. **consider** — a sketch.\n\n</details>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the review does not have %q:\n%s", want, body)
		}
	}
	f.handedOffWithNoHandBack()
}

// A correction whose gate is still red at its last attempt is not a hand-back:
// the branch stays at the head the review read, the review says the
// correction failed and leaves its findings as advice, and the pull request is
// handed off.
func TestACorrectionTheGateRefusesLeavesTheFindingsAsAdvice(t *testing.T) {
	f := greenPR(t)
	reviewed := f.postFindings()
	f.model.then(func(dir string) error {
		if _, err := run(dir, "git", "rm", "--quiet", "ok"); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "drop ok\n\nCorrects: advisory 2")
		return err
	})

	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	if len(f.model.asked) != 1+f.deps.Attempts {
		t.Errorf("the model was asked %d times, want the work and the correction's %d attempts", len(f.model.asked), f.deps.Attempts)
	}
	if at := f.pushed(); at != reviewed {
		t.Errorf("the branch is at %s, want it left at the reviewed head %s", at, reviewed)
	}
	body := f.theReview()
	if !strings.Contains(body, "attempted and failed: The local gate still failed after 3 attempts.") || strings.Count(body, "_Correction attempted, failed: this is advice._") != 2 {
		t.Errorf("the review does not say the correction failed:\n%s", body)
	}
	if strings.Contains(body, "corrected to") {
		t.Errorf("a failed correction says it corrected:\n%s", body)
	}
	f.handedOffWithNoHandBack()
	if !logged(f, "the correction of the review of `"+git.Short(reviewed)+"` failed") {
		t.Errorf("logged %q, want the failed correction", f.logged)
	}
}

// A correction CI is still red on after its fixes is pushed back to the head
// the review read, under the lease, and handed off with its findings as
// advice. That head is not watched again: CI was green on it before the review.
func TestACorrectionCIRefusesIsPushedBackToTheReviewedHead(t *testing.T) {
	f := greenPR(t)
	reviewed := f.postFindings()
	f.tr.checks = func(sha string, call int) []github.CheckRun {
		if sha == reviewed {
			return green(sha, call)
		}
		return []github.CheckRun{{Name: "test", Status: "completed", Conclusion: "failure"}}
	}
	f.model.then(correcting(nil, "regression", "Corrects: advisory 2"), commit("fix1"), commit("fix2"))

	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	if len(f.model.asked) != 2+f.deps.CIFixes {
		t.Errorf("the model was asked %d times, want the work, the correction and its %d fixes", len(f.model.asked), f.deps.CIFixes)
	}
	if at := f.pushed(); at != reviewed {
		t.Errorf("the branch is at %s, want it pushed back to the reviewed head %s", at, reviewed)
	}
	body := f.theReview()
	if !strings.Contains(body, "attempted and failed: CI still failed after 2 fixes.") {
		t.Errorf("the review does not say why the correction failed:\n%s", body)
	}
	f.handedOffWithNoHandBack()
}

// Someone else's push while a correction's CI runs is theirs, as it is for
// any push: the pull request is handed back, not handed off, and the review
// is left as it was posted.
func TestAPushByAnyoneElseDuringACorrectionHandsBack(t *testing.T) {
	f := greenPR(t)
	reviewed := f.postFindings()
	f.tr.checks = func(sha string, call int) []github.CheckRun {
		if sha == reviewed {
			return green(sha, call)
		}
		return []github.CheckRun{{Name: "test", Status: "in_progress"}}
	}
	f.model.then(correcting(nil, "regression", "Corrects: advisory 2"))
	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want the correction's CI watched", j.State)
	}

	human := filepath.Join(t.TempDir(), "human")
	if _, err := run("", "git", "clone", "--quiet", "--branch", "afk/7-1", f.remote, human); err != nil {
		t.Fatal(err)
	}
	if err := commit("theirs")(human); err != nil {
		t.Fatal(err)
	}
	if _, err := run(human, "git", "push", "--quiet", "origin", "afk/7-1"); err != nil {
		t.Fatal(err)
	}
	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	var handBacks []github.Comment
	for _, c := range f.tr.byAgent() {
		if c.ID != reviewID {
			handBacks = append(handBacks, c)
		}
	}
	if len(handBacks) != 1 || !strings.Contains(handBacks[0].Body, "Someone else pushed") {
		t.Errorf("the agent said %+v, want one hand-back for their push", handBacks)
	}
	if strings.Join(f.tr.labels, ",") != "needs-decision" {
		t.Errorf("labels %v, want the hand-back label and no hand-off", f.tr.labels)
	}
	if strings.Contains(f.theReview(), "afk:correction") {
		t.Errorf("the review was edited for a correction that was handed back:\n%s", f.theReview())
	}
}

// An edit of the review that never lands is logged and holds nothing up: the
// pull request is at the corrected head CI checked, and the review is advice.
func TestAReviewEditThatNeverLandsStillHandsOff(t *testing.T) {
	f := greenPR(t)
	f.postFindings()
	f.tr.failCommentEdits = f.deps.Rounds
	f.model.then(correcting(nil, "regression", "Corrects: advisory 2"))

	f.at = f.at.Add(f.deps.CIWait)
	errs := f.drive()
	if len(errs) != f.deps.Rounds {
		t.Fatalf("errors: %v, want one for each failed edit", errs)
	}
	for _, err := range errs {
		if !errors.Is(err, errCommentEdit) {
			t.Fatalf("errors: %v, want only the failed edits", errs)
		}
	}
	if f.tr.commentEdits != 0 || strings.Contains(f.theReview(), "afk:correction") {
		t.Errorf("the review was edited %d times:\n%s", f.tr.commentEdits, f.theReview())
	}
	if !logged(f, "edited 2 times for its correction and never showed it") {
		t.Errorf("logged %q, want the edit given up on", f.logged)
	}
	f.handedOffWithNoHandBack()
}

// A review with no Correctness or Standards finding is handed off as it is,
// and nothing goes back to the session.
func TestAReviewWithNothingToCorrectIsHandedOffAsItIs(t *testing.T) {
	f := greenPR(t)
	head := f.pushed()
	f.tr.mu.Lock()
	f.tr.comments = append(f.tr.comments, github.Comment{ID: reviewID, Login: agent, Body: review.Marker(head) + "\n\n## Approach\n\n1. **should-fix** — a sketch.\n"})
	f.tr.mu.Unlock()
	f.restReviewJob()

	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want only the work", len(f.model.asked))
	}
	if f.tr.commentEdits != 0 {
		t.Error("the review was edited")
	}
	f.handedOffWithNoHandBack()
}

// logged reports whether a line the deps logged says want.
func logged(f *fixture, want string) bool {
	for _, l := range f.logged {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}
