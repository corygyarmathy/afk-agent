package implement_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

const reviewJob = "review-pr-101"

// greenPR drives an issue to a pull request whose CI is green, and returns the
// fixture with the review asked for.
func greenPR(t *testing.T) *fixture {
	t.Helper()
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	f.tr.checks = green
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Reviewing {
		t.Fatalf("job in %q, want %q", j.State, implement.Reviewing)
	}
	return f
}

func (f *fixture) reviewJob() store.Job {
	f.t.Helper()
	j, err := f.store.Job(context.Background(), reviewJob)
	if err != nil {
		f.t.Fatalf("the review job: %v", err)
	}
	return j
}

// postReview is the review job's work landing on the pull request.
func (f *fixture) postReview() {
	f.tr.mu.Lock()
	defer f.tr.mu.Unlock()
	f.tr.comments = append(f.tr.comments, github.Comment{ID: 900, Login: agent, Body: review.Marker(f.pushed()) + "\nLooks sound."})
}

// restReviewJob puts the review job at rest, the way a review that came to
// nothing leaves it.
func (f *fixture) restReviewJob() {
	f.t.Helper()
	ctx := context.Background()
	if _, ok, err := f.store.Acquire(ctx, reviewJob, "review", f.at, time.Minute); err != nil || !ok {
		f.t.Fatalf("Acquire = %v, %v", ok, err)
	}
	if err := f.store.Commit(ctx, store.Commit{JobID: reviewJob, Holder: "review", State: review.Start, Release: true}); err != nil {
		f.t.Fatal(err)
	}
}

// A green head makes the pull request's review job due - never a /review
// comment - and the hand-off waits for the review, then goes on the pull
// request.
func TestAGreenHeadIsReviewedThenHandedOff(t *testing.T) {
	f := greenPR(t)

	rj := f.reviewJob()
	if rj.State != review.Start || !rj.NextRunAt.Equal(now) {
		t.Errorf("review job = %+v, want it due now in start", rj)
	}
	if len(f.tr.byAgent()) != 0 {
		t.Errorf("the agent commented %+v; a review is asked for by the job, never by a comment", f.tr.byAgent())
	}
	if len(f.tr.labels) != 0 {
		t.Error("handed off before the review")
	}

	f.postReview()
	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if strings.Join(f.tr.labels, ",") != "needs-review" || f.tr.labelledOn[0] != 101 {
		t.Errorf("labels %v on %v, want the hand-off label on #101", f.tr.labels, f.tr.labelledOn)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
	if _, err := run(f.workspace(), "git", "status"); err == nil {
		t.Error("the workspace was left behind")
	}
}

// A review on its way is waited for; a review job that came to nothing is
// asked again, a bounded number of times.
func TestAReviewThatNeverComesIsAskedForAgainThenStops(t *testing.T) {
	f := greenPR(t)

	// Queued: nothing is asked again.
	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	for i := range f.deps.Bound - 1 {
		f.restReviewJob()
		f.at = f.at.Add(f.deps.CIWait)
		if errs := f.drive(); len(errs) != 0 {
			t.Fatalf("round %d: errors: %v", i, errs)
		}
		if rj := f.reviewJob(); rj.NextRunAt.IsZero() {
			t.Fatalf("round %d: the review job was not asked for again", i)
		}
	}

	f.restReviewJob()
	f.at = f.at.Add(f.deps.CIWait)
	errs := f.drive()
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "never took effect") {
		t.Errorf("errors %v, want the review asked for %d times and no more", errs, f.deps.Bound)
	}
	if len(f.tr.labels) != 0 {
		t.Error("handed off with no review")
	}
}

// Someone else's push after green CI is theirs, as it is while CI runs: the
// review job reviews the new head, so a review of the agent's never comes, and
// the agent hands back rather than ask for one until its rounds run out.
func TestAPushByAnyoneElseWhileAwaitingTheReviewHandsBack(t *testing.T) {
	f := greenPR(t)
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
	f.restReviewJob()

	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	f.handedBackOnThePR("Someone else pushed")
}

// A review job that failed and parked is the operator's to look at. Asking
// again would throw its state away and pay for the model again, so the agent
// hands back instead, and leaves the review job where it parked.
func TestAParkedReviewJobIsHandedBackNotStartedOver(t *testing.T) {
	f := greenPR(t)
	ctx := context.Background()
	if _, ok, err := f.store.Acquire(ctx, reviewJob, "review", f.at, time.Minute); err != nil || !ok {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	if err := f.store.Commit(ctx, store.Commit{JobID: reviewJob, Holder: "review", State: review.Posting, Attempts: 3, Release: true}); err != nil {
		t.Fatal(err)
	}

	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	f.handedBackOnThePR("review job", review.Posting)
	if rj := f.reviewJob(); rj.State != review.Posting || rj.Attempts != 3 || !rj.NextRunAt.IsZero() {
		t.Errorf("review job = %+v, want it left parked in %q", rj, review.Posting)
	}
}

// Label names are case-insensitive on GitHub, and the repository's spelling
// is the one read back.
func TestTheHandOffLabelIsReadBackInAnyCase(t *testing.T) {
	f := greenPR(t)
	f.tr.repoLabels = []string{"Needs-Review"}
	f.postReview()
	f.at = f.at.Add(f.deps.CIWait)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.tr.labels) != 1 {
		t.Errorf("labels %v, want the hand-off applied once", f.tr.labels)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
}

// The pull request's description carries the marker the review job reads.
// Spelled in two packages, so held to one spelling here.
func TestThePullRequestMarkerIsTheOneReviewReads(t *testing.T) {
	if got := implement.PRMarker(7); got != "<!-- afk:implement issue=7 -->" {
		t.Errorf("PRMarker(7) = %q; review's implementMarker reads <!-- afk:implement issue=N -->", got)
	}
}
