package revise_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// setupReplying is a claimed revision whose pull request's head moves with the
// remote, as GitHub's does, so the review job it asks for reviews the head it
// pushed.
func setupReplying(t *testing.T) *revFixture {
	t.Helper()
	f := setupRevision(t)
	f.tr.live = f.remote
	f.deps.SizeSignal = 1000
	return f
}

// finish runs the revision and the review job it asks for, each whenever it is
// due, until both are at rest, and returns where the revision stopped. The
// review job goes first, as a pool with a worker free would run it while the
// revision waits.
func (f *revFixture) finish() store.Job {
	f.t.Helper()
	ctx := context.Background()
	reviewID := store.ID(store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12})
	for range 60 {
		ran := false
		for _, id := range []string{reviewID, jobID()} {
			job, err := f.store.Job(ctx, id)
			if errors.Is(err, store.ErrNoJob) {
				continue
			}
			if err != nil {
				f.t.Fatal(err)
			}
			next, ok := f.reg.Next(job.Kind, job.State)
			if !ok || job.NextRunAt.IsZero() {
				continue
			}
			f.runner.Run(ctx, next.Name, id)
			ran = true
			break
		}
		if !ran {
			return f.now()
		}
	}
	f.t.Fatalf("the revision never came to rest; it is in %q", f.now().State)
	return store.Job{}
}

// reply is the revision's reply to the pull request, and how many there are.
func (f *revFixture) reply() (github.Comment, int) {
	f.t.Helper()
	var found []github.Comment
	for _, c := range f.tr.comments[12] {
		if c.Login == agent && strings.Contains(c.Body, "<!-- afk:revision-reply ") {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		return github.Comment{}, 0
	}
	return found[0], len(found)
}

// reviews is the agent's reviews of head on the pull request.
func (f *revFixture) reviews(head string) []github.Comment {
	var out []github.Comment
	for _, c := range f.tr.comments[12] {
		if c.Login == agent && strings.Contains(c.Body, review.Marker(head)) {
			out = append(out, c)
		}
	}
	return out
}

// A revision ends with its reply, then a review of the new head that claims
// the reply with a 👀 and links it, then the hand-off label again, in that
// order.
func TestARevisionIsRepliedToThenReviewedThenHandedOff(t *testing.T) {
	f := setupReplying(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done in deadbee.\n"))

	if job := f.finish(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the revision is in %q, want at rest in %s\n%s", job.State, revise.Start, f.handBack())
	}
	at := f.remoteHead()
	reply, n := f.reply()
	if n != 1 {
		t.Fatalf("%d replies, want one", n)
	}
	if !strings.Contains(reply.Body, owed.RevisionReplyMarker(12, at)) || !strings.Contains(reply.Body, owed.RevisionMarker(1)) {
		t.Errorf("the reply does not carry the markers for the head %s and for command 1:\n%s", at, reply.Body)
	}
	reviews := f.reviews(at)
	if len(reviews) != 1 {
		t.Fatalf("%d reviews of %s, want one", len(reviews), at)
	}
	link := fmt.Sprintf("https://github.com/%s/pull/12#issuecomment-%d", repo, reply.ID)
	if !strings.Contains(reviews[0].Body, link) {
		t.Errorf("the review does not link the reply it claimed (%s):\n%s", link, reviews[0].Body)
	}
	if !f.tr.claimed(reply.ID) || f.tr.reacts[reply.ID] != 1 {
		t.Errorf("the reply has %d reactions landed, and claimed is %v: want one claim", f.tr.reacts[reply.ID], f.tr.claimed(reply.ID))
	}
	if !f.tr.labelled() {
		t.Error("the hand-off label is not back on the pull request")
	}
	if f.handedBack() || f.handBack() != "" {
		t.Errorf("the revision was handed back:\n%s", f.handBack())
	}

	// In order: the reply, the review's claim on it, the review, the label.
	want := []string{
		fmt.Sprintf("comment %d", reply.ID),
		fmt.Sprintf("react %d", reply.ID),
		fmt.Sprintf("comment %d", reviews[0].ID),
		"label " + handOff,
	}
	var got []string
	for _, e := range f.tr.events {
		if slices.Contains(want, e) {
			got = append(got, e)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("the tracker saw %v, want %v in that order", got, want)
	}
}

// The reply is Go's compare link from the head the operator read to the one
// the revision left, and then the session's sections: in the prompt's order,
// with empty ones and anything else left out.
func TestTheReplyIsTheCompareLinkAndTheSessionsSections(t *testing.T) {
	f := setupReplying(t)
	f.model.then(reviseOn("bar.txt", "I did everything you asked.\n\n"+
		"## Not verified\n\n- The Windows path.\n\n"+
		"## Suggested follow-ups\n\n\n"+
		"## Points\n\n- \"Rename Foo\" done in deadbee.\n\n"+
		"## Summary\n\nAll tests pass.\n"))

	f.finish()
	at := f.remoteHead()
	reply, _ := f.reply()
	if want := fmt.Sprintf("https://github.com/%s/compare/%s...%s", repo, f.head, at); !strings.Contains(reply.Body, want) {
		t.Errorf("the reply has no compare link %s:\n%s", want, reply.Body)
	}
	points, unverified := strings.Index(reply.Body, "## Points"), strings.Index(reply.Body, "## Not verified")
	if points < 0 || unverified < points {
		t.Errorf("the reply's sections are not Points then Not verified:\n%s", reply.Body)
	}
	for _, not := range []string{"Suggested follow-ups", "I did everything", "## Summary", "All tests pass", "size signal"} {
		if strings.Contains(reply.Body, not) {
			t.Errorf("the reply says %q:\n%s", not, reply.Body)
		}
	}
}

// Over the size signal, the reply says so in one line. It is a note: the
// revision is still reviewed and handed off.
func TestTheReplySaysWhenThePullRequestIsOverTheSizeSignal(t *testing.T) {
	f := setupReplying(t)
	f.deps.SizeSignal = 1
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done.\n"))

	if job := f.finish(); job.State != revise.Start {
		t.Fatalf("the revision is in %q, want %s", job.State, revise.Start)
	}
	reply, _ := f.reply()
	if !strings.Contains(reply.Body, "over the size signal of 1.") {
		t.Errorf("the reply does not say the pull request is over the size signal:\n%s", reply.Body)
	}
	if !f.tr.labelled() {
		t.Error("a pull request over the size signal was not handed off")
	}
}

// A review that fails after the reply is posted hands back on the pull
// request, and the reply is not posted again. Here the review job's post never
// appears, and the review job's own hand-back is the pull request's.
func TestAReviewThatFailsAfterTheReplyHandsBack(t *testing.T) {
	f := setupReplying(t)
	f.tr.drop = "<!-- afk:review head="
	f.model.then(reviseOn("bar.txt", "## Points\n\n- done.\n"))

	if job := f.finish(); job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the revision is in %q, want at rest in %s", job.State, revise.Start)
	}
	if _, n := f.reply(); n != 1 {
		t.Errorf("%d replies, want the one posted before the review", n)
	}
	if !f.handedBack() {
		t.Error("the hand-back label is not on the pull request")
	}
	handedBack := false
	for _, c := range f.tr.comments[12] {
		if c.Login == agent && strings.Contains(c.Body, review.HandBackMarker(f.remoteHead())) {
			handedBack = true
		}
	}
	if !handedBack {
		t.Error("the review's hand-back is not on the pull request")
	}
	if f.tr.labelled() {
		t.Error("the pull request was handed off without a review")
	}
}

// Someone else's push after the reply is posted hands the revision back, and
// the hand-back links the reply rather than say its points again.
func TestAPushAfterTheReplyHandsBackLinkingIt(t *testing.T) {
	f := setupReplying(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done in deadbee.\n"))

	f.step(revise.Reviewing)
	if err := pushAs(t.TempDir(), f.remote, "other.txt", "other\n"); err != nil {
		t.Fatal(err)
	}
	if job := f.finish(); job.State != revise.Start {
		t.Fatalf("the revision is in %q, want %s", job.State, revise.Start)
	}
	reply, n := f.reply()
	if n != 1 {
		t.Fatalf("%d replies, want one", n)
	}
	hb := f.handBack()
	if !strings.Contains(hb, "Someone else pushed") || !strings.Contains(hb, fmt.Sprintf("#issuecomment-%d", reply.ID)) {
		t.Errorf("the hand-back does not say someone pushed, and link the reply:\n%s", hb)
	}
	if strings.Contains(hb, "Rename Foo") {
		t.Errorf("the hand-back says the reply's points again:\n%s", hb)
	}
	if !f.handedBack() || f.tr.labelled() {
		t.Errorf("labels are %v, want handed back and not handed off", f.tr.pr.Labels)
	}
}

func TestSectionsAreThePromptsInOrder(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"none", "Done.", ""},
		{"ordered", "## Not verified\n\nx\n\n## Points\n\n- a\n", "## Points\n\n- a\n\n## Not verified\n\nx"},
		{"empty left out", "## Points\n\n- a\n\n## Suggested follow-ups\n\n", "## Points\n\n- a"},
		{"others left out", "Hello.\n## Verdict\n\nGreat.\n## Points\n- a\n", "## Points\n\n- a"},
		{"case and space", "##  points \n- a\n", "## Points\n\n- a"},
		{"fenced heading", "## Points\n\n```\n## Verdict\n```\n", "## Points\n\n```\n## Verdict\n```"},
		{"twice is one", "## Points\n- a\n## Not verified\nx\n## Points\n- b\n", "## Points\n\n- a\n- b\n\n## Not verified\n\nx"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := revise.Sections(c.in); got != c.want {
				t.Errorf("Sections(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
