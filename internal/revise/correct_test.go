package revise_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/revise"
)

// reviewMarker is the hidden line any review carries, and the head it read.
var reviewMarker = regexp.MustCompile(`<!-- afk:review head=([0-9a-f]+) -->`)

// finder is a fake model for the review job whose report has a Correctness
// finding, with its reproduction, and an Approach one.
type finder struct{}

func (finder) Run(context.Context, opencode.Request) (opencode.Reply, error) {
	return opencode.Reply{Text: "## Correctness\n\n1. **blocker** — `bar.txt:1`: the name is wrong.\n\n<details><summary>Reproduction</summary>\n\n```sh\ngrep -q Bar bar.txt\n```\n\n```\nexit 1\n```\n\n</details>\n\n## Approach\n\n2. **consider** — a sketch.\n"}, nil
}

// correctOn is a turn that commits name with a trailer naming what it
// corrects.
func correctOn(name, trailer string) func(dir string) error {
	return func(dir string) error {
		if err := commitOn(name)(dir); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "--amend", "-m", "correct "+name+"\n\n"+trailer)
		return err
	}
}

// On the agent's own pull request, the review of a revision's delta drives a
// correction the way implement's review does. The reply is posted once, for
// the head the review read, and not edited; the review is edited to say what
// corrected it; nothing is reviewed again; and the pull request is handed off.
func TestARevisionsOwnFindingsAreCorrectedBeforeTheHandOff(t *testing.T) {
	f := setupReplying(t)
	f.tr.pr.Login = agent
	f.review.Model = finder{}
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done.\n"), correctOn("regression", "Corrects: advisory 1"))

	if job := f.finish(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the revision is in %q, want at rest\n%s", job.State, f.handBack())
	}
	if len(f.model.asked) != 2 {
		t.Fatalf("the model was asked %d times, want the revision and its correction", len(f.model.asked))
	}
	if c := f.model.asked[1]; c.Session != "ses_1" || !strings.Contains(c.Prompt, ".git/afk-findings.md") {
		t.Errorf("the correction ran in %q with:\n%s\nwant the revision's session, given the findings", c.Session, c.Prompt)
	}

	at := f.remoteHead()
	reply, n := f.reply()
	if n != 1 {
		t.Fatalf("%d replies, want one", n)
	}
	var reviews []string
	for _, c := range f.tr.comments[12] {
		if c.Login == agent && reviewMarker.MatchString(c.Body) {
			reviews = append(reviews, c.Body)
		}
	}
	if len(reviews) != 1 {
		t.Fatalf("%d reviews, want one: one review run per hand-off", len(reviews))
	}
	body := reviews[0]
	reviewed := reviewMarker.FindStringSubmatch(body)[1]
	if reviewed == at {
		t.Fatal("the correction was never pushed")
	}
	if !strings.Contains(reply.Body, "revision-reply pr=12 head="+reviewed) {
		t.Errorf("the reply is not the one for the reviewed head %s:\n%s", reviewed, reply.Body)
	}
	if parent, _ := run(f.remote, "git", "rev-parse", at+"^"); parent != reviewed {
		t.Errorf("the correction is on %s, want it on the reviewed head %s", parent, reviewed)
	}
	for _, want := range []string{
		review.Marker(reviewed),
		"corrected to <code>" + git.Short(at) + "</code>",
		"1. Corrected in <a href=\"https://github.com/" + repo + "/commit/" + at + "\">",
		"2. **consider** — a sketch.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the review does not have %q:\n%s", want, body)
		}
	}
	if !f.tr.labelled() {
		t.Error("the hand-off label is not back on the pull request")
	}
	if f.handedBack() {
		t.Errorf("the revision was handed back:\n%s", f.handBack())
	}
}

// A revision of a pull request someone else wrote is advised, never
// corrected: its review's findings are the operator's.
func TestARevisionOfAnotherAuthorsPullRequestIsNotCorrected(t *testing.T) {
	f := setupReplying(t)
	f.review.Model = finder{}
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done.\n"))

	if job := f.finish(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the revision is in %q, want at rest\n%s", job.State, f.handBack())
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want only the revision", len(f.model.asked))
	}
	at := f.remoteHead()
	reviews := f.reviews(at)
	if len(reviews) != 1 || strings.Contains(reviews[0].Body, "afk:correction") {
		t.Errorf("reviews of %s: %+v, want one, never edited", at, reviews)
	}
	if !f.tr.labelled() {
		t.Error("the hand-off label is not back on the pull request")
	}
}

// A correction the gate refuses to the last attempt fails, and is not a
// hand-back: the branch stays at the head the review read, the review says the
// correction failed, and the pull request is handed off.
func TestARevisionsFailedCorrectionLeavesTheFindingsAsAdvice(t *testing.T) {
	f := setupReplying(t)
	f.tr.pr.Login = agent
	f.review.Model = finder{}
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done.\n"), func(dir string) error {
		if _, err := run(dir, "git", "rm", "--quiet", "ok"); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "drop ok\n\nCorrects: advisory 1")
		return err
	})

	if job := f.finish(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the revision is in %q, want at rest\n%s", job.State, f.handBack())
	}
	at := f.remoteHead()
	reviews := f.reviews(at)
	if len(reviews) != 1 {
		t.Fatalf("%d reviews of %s, want the branch left at the one reviewed", len(reviews), at)
	}
	if body := reviews[0].Body; !strings.Contains(body, "attempted and failed: The local gate still failed after 3 attempts.") || !strings.Contains(body, "_Correction attempted, failed: this is advice._") {
		t.Errorf("the review does not say the correction failed:\n%s", body)
	}
	if _, n := f.reply(); n != 1 {
		t.Errorf("%d replies, want one", n)
	}
	if !f.tr.labelled() {
		t.Error("the hand-off label is not back on the pull request")
	}
	if f.handedBack() {
		t.Errorf("the revision was handed back:\n%s", f.handBack())
	}
}

// A correction CI is still red on after its fixes is pushed back to the head
// the review read, under the lease, and is not a hand-back: that head is not
// watched again, the review says the correction failed, the reply is left as
// it was posted, and the pull request is handed off.
func TestARevisionsCorrectionCIRefusesIsPushedBackToTheReviewedHead(t *testing.T) {
	f := setupReplying(t)
	f.tr.pr.Login = agent
	f.review.Model = finder{}
	var reviewed string
	f.tr.checks = func(sha string, call int) []github.CheckRun {
		if reviewed == "" {
			reviewed = sha
		}
		if sha == reviewed {
			return green(sha, call)
		}
		return []github.CheckRun{{Name: "test", Status: "completed", Conclusion: "failure", Text: "--- FAIL: TestBar"}}
	}
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done.\n"), correctOn("regression", "Corrects: advisory 1"), commitOn("fix1"), commitOn("fix2"))

	if job := f.finish(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the revision is in %q, want at rest\n%s", job.State, f.handBack())
	}
	if len(f.model.asked) != 2+f.deps.CIFixes {
		t.Errorf("the model was asked %d times, want the revision, the correction and its %d fixes", len(f.model.asked), f.deps.CIFixes)
	}
	if at := f.remoteHead(); at != reviewed {
		t.Errorf("the branch is at %s, want it pushed back to the reviewed head %s", at, reviewed)
	}
	reviews := f.reviews(reviewed)
	if len(reviews) != 1 {
		t.Fatalf("%d reviews of %s, want one", len(reviews), reviewed)
	}
	if body := reviews[0].Body; !strings.Contains(body, "attempted and failed: CI still failed after 2 fixes.") || !strings.Contains(body, "_Correction attempted, failed: this is advice._") {
		t.Errorf("the review does not say the correction failed:\n%s", body)
	}
	if _, n := f.reply(); n != 1 {
		t.Errorf("%d replies, want one", n)
	}
	if !f.tr.labelled() {
		t.Error("the hand-off label is not back on the pull request")
	}
	if f.handedBack() {
		t.Errorf("the revision was handed back:\n%s", f.handBack())
	}
}
