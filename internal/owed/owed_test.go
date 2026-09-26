package owed_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

const agent = "afk-bot"

var now = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// tracker is issue 7 and pull request 12 on a fixture tracker. Each write can
// be made to fail, or to be lost: reported done and never landed, which is
// what a process killed between the commit and the effect leaves behind.
type tracker struct {
	comments  map[int][]github.Comment
	reactions map[int64][]github.Reaction
	prEyes    map[int][]github.Reaction
	labels    map[int][]string
	deleted   map[int64]bool
	nextID    int64

	// writes counts every write that landed, by what it was.
	writes map[string]int

	// fail names the writes that error the next time they are made, and
	// lose how many times each vanishes: "comment", "react", "react-pr",
	// "label".
	fail map[string]bool
	lose map[string]int
}

func newTracker() *tracker {
	return &tracker{
		comments:  map[int][]github.Comment{},
		reactions: map[int64][]github.Reaction{},
		prEyes:    map[int][]github.Reaction{},
		labels:    map[int][]string{},
		deleted:   map[int64]bool{},
		nextID:    1000,
		writes:    map[string]int{},
		fail:      map[string]bool{},
		lose:      map[string]int{},
	}
}

// write reports whether a write of this kind lands, and the error it reports.
func (tr *tracker) write(what string) (bool, error) {
	if tr.fail[what] {
		delete(tr.fail, what)
		return false, fmt.Errorf("%s: 502 Bad Gateway", what)
	}
	if tr.lose[what] > 0 {
		tr.lose[what]--
		return false, nil
	}
	tr.writes[what]++
	return true, nil
}

func (tr *tracker) Issue(_ context.Context, n int) (github.Issue, error) {
	return github.Issue{Number: n, State: "open", Labels: tr.labels[n]}, nil
}

func (tr *tracker) Comments(_ context.Context, n int) ([]github.Comment, error) {
	return append([]github.Comment(nil), tr.comments[n]...), nil
}

func (tr *tracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	if tr.deleted[id] {
		return nil, &github.StatusError{Method: "GET", Code: 404, Status: "404 Not Found"}
	}
	return tr.reactions[id], nil
}

func (tr *tracker) IssueReactions(_ context.Context, n int) ([]github.Reaction, error) {
	return tr.prEyes[n], nil
}

func (tr *tracker) Comment(_ context.Context, n int, body string) (github.Comment, error) {
	tr.nextID++
	c := github.Comment{ID: tr.nextID, Login: agent, Body: body}
	lands, err := tr.write("comment")
	if lands {
		tr.comments[n] = append(tr.comments[n], c)
	}
	return c, err
}

func (tr *tracker) React(_ context.Context, id int64, content string) error {
	lands, err := tr.write("react")
	if lands && !intake.Claimed(tr.reactions[id], agent) {
		tr.reactions[id] = append(tr.reactions[id], github.Reaction{Login: agent, Content: content})
	}
	return err
}

func (tr *tracker) ReactToIssue(_ context.Context, n int, content string) error {
	lands, err := tr.write("react-pr")
	if lands && !intake.Claimed(tr.prEyes[n], agent) {
		tr.prEyes[n] = append(tr.prEyes[n], github.Reaction{Login: agent, Content: content})
	}
	return err
}

func (tr *tracker) Label(_ context.Context, n int, label string) error {
	lands, err := tr.write("label")
	if lands {
		tr.labels[n] = append(tr.labels[n], label)
	}
	return err
}

// said is the comments on n carrying marker.
func (tr *tracker) said(n int, marker string) int {
	count := 0
	for _, c := range tr.comments[n] {
		if strings.Contains(c.Body, marker) {
			count++
		}
	}
	return count
}

func command(id int64) github.Comment {
	return github.Comment{ID: id, Login: "alice", Association: "OWNER", Body: "/review"}
}

const marker = "<!-- afk:hand-back issue=7 key=abc -->"

// everything is one item of each kind.
func everything() []owed.Item {
	return []owed.Item{
		owed.Claim(command(1)),
		owed.ClaimPullRequest(12),
		owed.Reply("already-comment-1", 12, command(1), "Already reviewed."),
		owed.Comment("hand-back-issue-7-abc", 7, marker, marker+"\nI stopped."),
		owed.Label("hand-back-label-issue-7-abc", 7, "needs-decision"),
	}
}

type fixture struct {
	t     *testing.T
	tr    *tracker
	store store.Store
	run   *transition.Runner
	job   store.Job
}

// setup is a job in start whose decision owes items and goes on to "next",
// due, once they are all on the tracker. The read-back runs from "owing", and
// a lost record sends the job to "lost".
func setup(t *testing.T, bound int, items ...owed.Item) *fixture {
	t.Helper()
	tr := newTracker()
	s := storetest.Open(t)
	b := &owed.Book{Tracker: tr, Store: s, Login: agent, Rounds: bound, Dir: t.TempDir()}
	reg := transition.MustRegistry(
		transition.Transition{Name: "decide", Kind: store.KindReview, From: "start", Run: func(ctx context.Context, in transition.In) (transition.Result, error) {
			return b.Owe(ctx, in, "owing", owed.Record{Next: "next", Due: true, Items: items})
		}},
		transition.Transition{Name: "settle", Kind: store.KindReview, From: "owing", Run: func(ctx context.Context, in transition.In) (transition.Result, error) {
			return b.Settle(ctx, in, transition.Result{State: "lost"})
		}},
	)
	job := storetest.Seed(t, s, store.KindReview, 12, "start")
	return &fixture{t: t, tr: tr, store: s, job: job,
		run: &transition.Runner{Store: s, Registry: reg, Holder: "test", LeaseTTL: time.Minute, Clock: func() time.Time { return now }}}
}

// drive runs the job until it leaves the read-back, and returns the errors
// the runs reported.
func (f *fixture) drive() []error {
	f.t.Helper()
	var errs []error
	for range 10 {
		job, err := f.store.Job(context.Background(), f.job.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		name := map[string]string{"start": "decide", "owing": "settle"}[job.State]
		if name == "" || job.NextRunAt.IsZero() {
			return errs
		}
		if _, err := f.run.Run(context.Background(), name, f.job.ID); err != nil {
			errs = append(errs, err)
		}
	}
	f.t.Fatal("the job never left the read-back")
	return nil
}

func (f *fixture) state() string {
	f.t.Helper()
	job, err := f.store.Job(context.Background(), f.job.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return job.State
}

// everythingOnce asserts each of everything() is on the tracker exactly once.
func (f *fixture) everythingOnce() {
	f.t.Helper()
	tr := f.tr
	if !intake.Claimed(tr.reactions[1], agent) || len(tr.reactions[1]) != 1 {
		f.t.Errorf("the command's reactions are %v, want the one claim", tr.reactions[1])
	}
	if !intake.Claimed(tr.prEyes[12], agent) || len(tr.prEyes[12]) != 1 {
		f.t.Errorf("the pull request's reactions are %v, want the one claim", tr.prEyes[12])
	}
	if n := tr.said(12, "afk:reply comment=1"); n != 1 {
		f.t.Errorf("%d replies to the command, want 1", n)
	}
	if n := tr.said(7, marker); n != 1 {
		f.t.Errorf("%d hand-backs on the issue, want 1", n)
	}
	if l := strings.Join(tr.labels[7], ","); l != "needs-decision" {
		f.t.Errorf("labels on the issue are %q, want the hand-back label once", l)
	}
	if f.state() != "next" {
		f.t.Errorf("the job is in %q, want it moved on to next", f.state())
	}
}

// Everything owed is made once, read back, and the job moves on.
func TestWhatIsOwedIsMadeOnceAndTheJobMovesOn(t *testing.T) {
	f := setup(t, 3, everything()...)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatal(errs)
	}
	f.everythingOnce()
	// Two comments: the reply, and the hand-back.
	want := map[string]int{"react": 1, "react-pr": 1, "comment": 2, "label": 1}
	if fmt.Sprint(f.tr.writes) != fmt.Sprint(want) {
		t.Errorf("writes = %v, want %v", f.tr.writes, want)
	}
}

// Anything lost between the commit and the effect - a kill, the way a replay
// sees it - is read back as missing and made again under the next round's key.
// What did land is not made twice.
func TestAnythingLostIsMadeAgain(t *testing.T) {
	for _, what := range []string{"react", "react-pr", "comment", "label"} {
		t.Run(what, func(t *testing.T) {
			f := setup(t, 3, everything()...)
			f.tr.lose[what] = 1
			if errs := f.drive(); len(errs) != 0 {
				t.Fatal(errs)
			}
			f.everythingOnce()
		})
	}
	t.Run("everything", func(t *testing.T) {
		f := setup(t, 3, everything()...)
		for _, what := range []string{"react", "react-pr", "comment", "label"} {
			f.tr.lose[what] = 1
		}
		if errs := f.drive(); len(errs) != 0 {
			t.Fatal(errs)
		}
		f.everythingOnce()
	})
}

// An effect that errors stops the runner, so every item after it is never
// tried. Each is read back on its own, and all of them made (#58): a
// hand-back whose comment failed still gets its label.
func TestAFailedEffectDoesNotLoseTheOnesAfterIt(t *testing.T) {
	f := setup(t, 3,
		owed.Comment("hand-back-issue-7-abc", 7, marker, marker+"\nI stopped."),
		owed.Label("hand-back-label-issue-7-abc", 7, "needs-decision"),
	)
	f.tr.fail["comment"] = true
	errs := f.drive()
	if len(errs) != 1 {
		t.Errorf("errors = %v, want the one failed comment", errs)
	}
	if n := f.tr.said(7, marker); n != 1 {
		t.Errorf("%d hand-backs on the issue, want 1", n)
	}
	if l := strings.Join(f.tr.labels[7], ","); l != "needs-decision" {
		t.Errorf("labels on the issue are %q, want the hand-back label once", l)
	}
	if f.state() != "next" {
		t.Errorf("the job is in %q, want next", f.state())
	}
}

// A comment is read back before it is posted again: one that landed while its
// round reported an error is not posted twice.
func TestACommentThatLandedIsNotPostedAgain(t *testing.T) {
	f := setup(t, 3, owed.Comment("hand-back-issue-7-abc", 7, marker, marker+"\nI stopped."))
	f.tr.comments[7] = []github.Comment{{ID: 1, Login: agent, Body: marker + "\nI stopped."}}
	if errs := f.drive(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if n := f.tr.said(7, marker); n != 1 || f.tr.writes["comment"] != 0 {
		t.Errorf("%d hand-backs after %d posts, want the one already there", n, f.tr.writes["comment"])
	}
}

// A marker in someone else's comment is not the agent's comment.
func TestOnlyTheAgentsCommentCounts(t *testing.T) {
	f := setup(t, 3, owed.Comment("hand-back-issue-7-abc", 7, marker, marker+"\nI stopped."))
	f.tr.comments[7] = []github.Comment{{ID: 1, Login: "mallory", Body: marker}}
	if errs := f.drive(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if f.tr.writes["comment"] != 1 {
		t.Errorf("%d posts, want the agent's own hand-back posted", f.tr.writes["comment"])
	}
}

// A command deleted before its claim was read back has nothing left to claim,
// and waits for nothing.
func TestADeletedCommandIsNotWaitedFor(t *testing.T) {
	f := setup(t, 3, owed.Claim(command(1)))
	f.tr.lose["react"] = 1
	f.tr.deleted[1] = true
	if errs := f.drive(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if f.state() != "next" {
		t.Errorf("the job is in %q, want next", f.state())
	}
}

// Something that never appears is made as many times as the rounds allow, and
// then the read-back is an error: the job fails where someone will see it. The
// attempt after that - a retry, or an operator freeing the parked job - has
// rounds of its own, and makes it again.
func TestSomethingThatNeverAppearsRunsOutAndAFreedJobTriesAgain(t *testing.T) {
	f := setup(t, 2, owed.Label("hand-back-label-issue-7-abc", 7, "needs-decision"))
	f.tr.lose["label"] = 10
	errs := f.drive()
	if len(errs) == 0 || !strings.Contains(errs[len(errs)-1].Error(), "never took effect") {
		t.Fatalf("errors = %v, want the label to run out of rounds", errs)
	}
	if f.state() != "owing" {
		t.Errorf("the job is in %q, want it left in the read-back", f.state())
	}
	if n := 10 - f.tr.lose["label"]; n != 2 {
		t.Errorf("the label was applied %d times, want the 2 rounds", n)
	}

	// Freed: due again, in the state it parked in, with whatever stopped
	// the label put right.
	f.tr.lose["label"] = 0
	ctx := context.Background()
	if _, ok, err := f.store.Acquire(ctx, f.job.ID, "operator", now, time.Minute); err != nil || !ok {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	if err := f.store.Commit(ctx, store.Commit{JobID: f.job.ID, Holder: "operator", State: "owing", NextRunAt: now, Release: true}); err != nil {
		t.Fatal(err)
	}
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors = %v, want the freed job to make the label again", errs)
	}
	if l := strings.Join(f.tr.labels[7], ","); l != "needs-decision" || f.state() != "next" {
		t.Errorf("labels %q and the job in %q, want the label and the job moved on", l, f.state())
	}
}

// A decision that owes nothing goes straight where it decided.
func TestOwingNothingMovesStraightOn(t *testing.T) {
	f := setup(t, 3)
	out, err := f.run.Run(context.Background(), "decide", f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.To != "next" {
		t.Errorf("the decision moved to %q, want next", out.To)
	}
}

// A record lost with the state directory goes where the kind says.
func TestALostRecordGoesWhereTheKindSays(t *testing.T) {
	f := setup(t, 3, owed.Claim(command(1)))
	if _, err := f.run.Run(context.Background(), "decide", f.job.ID); err != nil {
		t.Fatal(err)
	}
	// A second book on an empty directory: the first one's record is gone.
	s, tr := f.store, f.tr
	b := &owed.Book{Tracker: tr, Store: s, Login: agent, Rounds: 3, Dir: t.TempDir()}
	job, err := s.Job(context.Background(), f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Settle(context.Background(), transition.In{Job: job, Now: now}, transition.Result{State: "lost"})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != "lost" || len(res.Effects) != 0 {
		t.Errorf("result = %+v, want the kind's lost result", res)
	}
}

// Only the agent's unclaimed commands for the word are unanswered.
func TestUnansweredIsTheAgentsUnclaimedCommands(t *testing.T) {
	tr := newTracker()
	tr.reactions[3] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	tr.reactions[6] = []github.Reaction{{Login: "alice", Content: intake.Claim}}
	b := &owed.Book{Tracker: tr, Login: agent}
	got, err := b.Unanswered(context.Background(), []github.Comment{
		command(1),
		{ID: 2, Login: "mallory", Association: "NONE", Body: "/review"},
		command(3),
		{ID: 4, Login: agent, Association: "OWNER", Body: "/review"},
		{ID: 5, Login: "alice", Association: "OWNER", Body: "/implement"},
		command(6),
	}, "/review")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if fmt.Sprint(ids) != "[1 6]" {
		t.Errorf("unanswered = %v, want [1 6]", ids)
	}
}

// A malformed item is refused when it is owed, not when it is read back.
func TestAMalformedItemIsRefused(t *testing.T) {
	f := setup(t, 3, owed.Comment("hand-back-issue-7-abc", 7, marker, "no marker here"))
	_, err := f.run.Run(context.Background(), "decide", f.job.ID)
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("err = %v, want the comment without its marker refused", err)
	}
	if f.state() != "start" {
		t.Errorf("the job is in %q, want it left in start", f.state())
	}
}
