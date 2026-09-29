package revise_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

const (
	agent   = "afk-bot"
	repo    = "o/n"
	handOff = "ready-for-review"
	head    = "abc123"
)

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// tracker is issue 7 and pull request 12 on a fixture tracker: what intake
// lists, and what the revise kind reads and writes.
type tracker struct {
	pr        github.PullRequest
	comments  map[int][]github.Comment
	reactions map[int64][]github.Reaction
	issues    map[int]github.Issue
	nextID    int64

	// editFails is the error every edit of the pull request's description
	// fails with, or nil.
	editFails error

	// writes counts every write, by what it was.
	writes map[string]int

	// checks is the check runs on a commit, by the time they are asked
	// about: green on every head, unless a test says otherwise. required is
	// the checks the base branch's rules require.
	checks   func(sha string, call int) []github.CheckRun
	required []string
	asks     int
}

func newTracker() *tracker {
	return &tracker{
		pr: github.PullRequest{
			Number: 12, State: "open", HeadSHA: head, HeadRef: "feature", HeadRepo: repo, BaseRef: "main",
			Login: "alice", Labels: []string{handOff, "bug"},
		},
		comments:  map[int][]github.Comment{},
		reactions: map[int64][]github.Reaction{},
		issues:    map[int]github.Issue{},
		nextID:    1000,
		writes:    map[string]int{},
	}
}

func (tr *tracker) CheckRuns(_ context.Context, sha string) ([]github.CheckRun, error) {
	tr.asks++
	if tr.checks == nil {
		return green(sha, tr.asks), nil
	}
	return tr.checks(sha, tr.asks), nil
}

func (tr *tracker) RequiredChecks(context.Context, string) ([]string, error) {
	return tr.required, nil
}

// green is every check passed.
func green(string, int) []github.CheckRun {
	return []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}, {Name: "lint", Status: "completed", Conclusion: "skipped"}}
}

// say posts a comment on subject n, as someone other than the agent.
func (tr *tracker) say(n int, c github.Comment) {
	tr.comments[n] = append(tr.comments[n], c)
}

func (tr *tracker) OpenIssues(context.Context) ([]github.Issue, error) {
	out := []github.Issue{{Number: 7, State: "open", DependenciesRead: true}}
	if tr.pr.State == "open" {
		out = append(out, github.Issue{Number: 12, State: "open", PullRequest: true, Author: tr.pr.Login, Labels: tr.pr.Labels})
	}
	return out, nil
}

func (tr *tracker) PullRequest(_ context.Context, n int) (github.PullRequest, error) {
	if n != 12 {
		return github.PullRequest{}, &github.StatusError{Code: 404, Status: "404 Not Found"}
	}
	pr := tr.pr
	pr.Labels = append([]string(nil), tr.pr.Labels...)
	return pr, nil
}

func (tr *tracker) Issue(_ context.Context, n int) (github.Issue, error) {
	if n == 12 {
		return github.Issue{Number: 12, State: tr.pr.State, PullRequest: true, Labels: tr.pr.Labels}, nil
	}
	if is, ok := tr.issues[n]; ok {
		return is, nil
	}
	return github.Issue{Number: n, State: "open"}, nil
}

func (tr *tracker) Comments(_ context.Context, n int) ([]github.Comment, error) {
	return append([]github.Comment(nil), tr.comments[n]...), nil
}

func (tr *tracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	return tr.reactions[id], nil
}

func (tr *tracker) IssueReactions(context.Context, int) ([]github.Reaction, error) {
	return nil, nil
}

func (tr *tracker) Comment(_ context.Context, n int, body string) (github.Comment, error) {
	tr.writes["comment"]++
	tr.nextID++
	c := github.Comment{ID: tr.nextID, Login: agent, Body: body}
	tr.comments[n] = append(tr.comments[n], c)
	return c, nil
}

func (tr *tracker) React(_ context.Context, id int64, content string) error {
	tr.writes["react"]++
	if !intake.Claimed(tr.reactions[id], agent) {
		tr.reactions[id] = append(tr.reactions[id], github.Reaction{Login: agent, Content: content})
	}
	return nil
}

func (tr *tracker) ReactToIssue(context.Context, int, string) error {
	return errors.New("the revise kind never claims a pull request's description")
}

func (tr *tracker) Label(_ context.Context, _ int, label string) error {
	tr.writes["label"]++
	for _, l := range tr.pr.Labels {
		if l == label {
			return nil
		}
	}
	tr.pr.Labels = append(tr.pr.Labels, label)
	return nil
}

func (tr *tracker) Unlabel(_ context.Context, n int, label string) error {
	tr.writes["unlabel"]++
	var kept []string
	for _, l := range tr.pr.Labels {
		if l != label {
			kept = append(kept, l)
		}
	}
	tr.pr.Labels = kept
	return nil
}

func (tr *tracker) EditPullRequest(_ context.Context, n int, body string) error {
	tr.writes["edit"]++
	if tr.editFails != nil {
		return tr.editFails
	}
	if n != 12 {
		return &github.StatusError{Code: 404, Status: "404 Not Found"}
	}
	tr.pr.Body = body
	return nil
}

// answers is the agent's replies to comment id.
func (tr *tracker) answers(id int64) []string {
	var out []string
	for _, c := range tr.comments[12] {
		if c.Login == agent && strings.Contains(c.Body, owed.ReplyMarker(id)) {
			out = append(out, c.Body)
		}
	}
	return out
}

func (tr *tracker) claimed(id int64) bool {
	return intake.Claimed(tr.reactions[id], agent)
}

func (tr *tracker) labelled() bool {
	for _, l := range tr.pr.Labels {
		if l == handOff {
			return true
		}
	}
	return false
}

// send is a /revise from the operator.
func send(id int64, body string) github.Comment {
	return github.Comment{ID: id, Login: "cory", Association: "OWNER", Body: body}
}

// answer is the agent's comment answering command id as a revision, as its
// reply and its hand-back carry it.
func answer(id, answered int64) github.Comment {
	return github.Comment{ID: id, Login: agent, Body: owed.RevisionMarker(answered) + "\nDone."}
}

type fixture struct {
	t     *testing.T
	tr    *tracker
	store store.Store
	deps  *revise.Deps
	reg   *transition.Registry
	run   *transition.Runner
	in    *intake.Intake
}

func setup(t *testing.T) *fixture {
	t.Helper()
	tr := newTracker()
	s := storetest.Open(t)
	d := &revise.Deps{Tracker: tr, Store: s, Login: agent, Repo: repo, Rounds: 3, HandOffLabel: handOff, StateDir: t.TempDir()}
	reg := transition.MustRegistry(revise.Transitions(d)...)
	clock := func() time.Time { return now }
	return &fixture{
		t: t, tr: tr, store: s, deps: d, reg: reg,
		run: &transition.Runner{Store: s, Registry: reg, Holder: "test", LeaseTTL: time.Minute, Clock: clock},
		in: &intake.Intake{
			Tracker: tr, Store: s, Login: agent, Holder: "intake", LeaseTTL: time.Minute, Clock: clock,
			// The entry #149 adds to the registry `afk work` answers.
			Commands: []intake.Command{{Word: revise.Word, On: store.SubjectPR, Kind: store.KindRevise, Start: revise.Start}},
		},
	}
}

// pass runs intake once, and returns the jobs it made due.
func (f *fixture) pass() []store.Job {
	f.t.Helper()
	made, err := f.in.Pass(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return made
}

// drive runs the pull request's revise job until it comes to rest or reaches
// revising, which #146 takes on, and returns where it stopped.
func (f *fixture) drive() store.Job {
	f.t.Helper()
	ctx := context.Background()
	id := store.ID(store.KindRevise, store.Subject{Type: store.SubjectPR, Number: 12})
	for range 10 {
		job, err := f.store.Job(ctx, id)
		if err != nil {
			f.t.Fatal(err)
		}
		if job.State == revise.Revising {
			return job
		}
		next, ok := f.reg.Next(job.Kind, job.State)
		if job.NextRunAt.IsZero() || !ok {
			return job
		}
		if _, err := f.run.Run(ctx, next.Name, id); err != nil {
			f.t.Fatalf("%s: %v", next.Name, err)
		}
	}
	f.t.Fatal("the job never came to rest")
	return store.Job{}
}

// A /revise with points from a writer, on an open pull request, arms one
// revise job. The job claims it, takes the hand-off label off, and moves on
// to the work with the points and the head they were written against.
func TestASendBackIsClaimedAndMovesOnToTheWork(t *testing.T) {
	f := setup(t)
	f.tr.say(12, send(1, "/revise\nRename Foo to Bar.\n\nadvisory 3, but keep the test"))

	made := f.pass()
	if len(made) != 1 || made[0].Kind != store.KindRevise || made[0].Subject.Number != 12 {
		t.Fatalf("intake made %v, want one revise job for pull request 12", made)
	}
	job := f.drive()
	if job.State != revise.Revising || job.NextRunAt.IsZero() {
		t.Errorf("the job is in %q (due %v), want revising and due", job.State, !job.NextRunAt.IsZero())
	}
	if !f.tr.claimed(1) {
		t.Error("the command is not claimed")
	}
	if got := strings.Join(f.tr.pr.Labels, ","); got != "bug" {
		t.Errorf("labels are %q, want only the hand-off label taken off", got)
	}
	if f.tr.writes["comment"] != 0 {
		t.Errorf("%d comments, want none: the reply is the revision's", f.tr.writes["comment"])
	}
	sb, err := f.deps.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := revise.SendBack{Head: head, Ref: "feature", Points: []revise.Point{{Comment: 1, Text: "Rename Foo to Bar.\n\nadvisory 3, but keep the test"}}}
	if fmt.Sprint(sb) != fmt.Sprint(want) {
		t.Errorf("send-back = %+v, want %+v", sb, want)
	}

	// Claimed, the command is answered: the next pass arms nothing.
	if made := f.pass(); len(made) != 0 {
		t.Errorf("a second pass made %v, want nothing", made)
	}
}

// A /revise from someone without write access, from the agent itself, or on
// an issue arms nothing.
func TestOnlyAWritersRevisionOnAPullRequestArmsAJob(t *testing.T) {
	f := setup(t)
	f.tr.say(12, github.Comment{ID: 1, Login: "mallory", Association: "CONTRIBUTOR", Body: "/revise do it"})
	f.tr.say(12, github.Comment{ID: 2, Login: "stranger", Association: "NONE", Body: "/revise do it"})
	f.tr.say(12, github.Comment{ID: 3, Login: agent, Association: "OWNER", Body: "/revise do it"})
	f.tr.say(7, send(4, "/revise do it"))

	if made := f.pass(); len(made) != 0 {
		t.Errorf("intake made %v, want nothing", made)
	}
	if n := len(f.tr.reactions); n != 0 {
		t.Errorf("%d comments reacted to, want none", n)
	}
}

// Several /revise with points before the agent got to them are one send-back,
// in the order they were written, and each is claimed.
func TestUnansweredSendBacksAreOneSendBackInOrder(t *testing.T) {
	f := setup(t)
	f.tr.say(12, send(1, "/revise Rename Foo."))
	f.tr.say(12, github.Comment{ID: 2, Login: "cory", Association: "OWNER", Body: "Looking again."})
	f.tr.say(12, send(3, "/revise And drop the flag."))
	f.pass()

	job := f.drive()
	if job.State != revise.Revising {
		t.Fatalf("the job is in %q, want revising", job.State)
	}
	if !f.tr.claimed(1) || !f.tr.claimed(3) {
		t.Error("not every command is claimed")
	}
	sb, err := f.deps.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sb.Points) != fmt.Sprint([]revise.Point{{Comment: 1, Text: "Rename Foo."}, {Comment: 3, Text: "And drop the flag."}}) {
		t.Errorf("points = %+v, want both commands' in order", sb.Points)
	}
	if f.tr.writes["unlabel"] != 1 {
		t.Errorf("the label was taken off %d times, want once", f.tr.writes["unlabel"])
	}
}

// What cannot be revised is claimed and answered with one reply each, and
// nothing else happens: no label off, no work.
func TestWhatCannotBeRevisedGetsOneReply(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(*tracker)
		body    string
		says    string
	}{
		"no points":         {body: "/revise   ", says: "nothing here to revise"},
		"no points, a line": {body: "/revise\n\n", says: "nothing here to revise"},
		"a fork":            {prepare: func(tr *tracker) { tr.pr.HeadRepo = "someone/fork" }, body: "/revise Rename Foo.", says: "not in o/n"},
		"a deleted fork":    {prepare: func(tr *tracker) { tr.pr.HeadRepo = "" }, body: "/revise Rename Foo.", says: "not in o/n"},
		"in flight": {
			prepare: func(tr *tracker) {
				// An earlier send-back, claimed, answered only after
				// this one was written.
				tr.say(12, send(1, "/revise First."))
				tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
			},
			body: "/revise Second.", says: "in flight",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			if tc.prepare != nil {
				tc.prepare(f.tr)
			}
			f.tr.say(12, send(5, tc.body))
			if name == "in flight" {
				f.tr.say(12, answer(6, 1))
			}
			f.pass()

			job := f.drive()
			if job.State != revise.Start || !job.NextRunAt.IsZero() {
				t.Errorf("the job is in %q (due %v), want at rest in start", job.State, !job.NextRunAt.IsZero())
			}
			if !f.tr.claimed(5) {
				t.Error("the command is not claimed")
			}
			answers := f.tr.answers(5)
			if len(answers) != 1 || !strings.Contains(answers[0], tc.says) {
				t.Errorf("answers = %q, want one saying %q", answers, tc.says)
			}
			if !f.tr.labelled() || f.tr.writes["unlabel"] != 0 {
				t.Error("the hand-off label came off, with nothing to revise")
			}
			if _, err := f.deps.Load(job.ID); err == nil {
				t.Error("a send-back was handed to the work")
			}

			// Answered once: armed again by hand, it says nothing more.
			a := transition.Armer{Store: f.store, Holder: "operator", LeaseTTL: time.Minute}
			if _, _, err := a.Restart(context.Background(), store.KindRevise, job.Subject, revise.Start, now, "by-hand"); err != nil {
				t.Fatal(err)
			}
			f.drive()
			if n := len(f.tr.answers(5)); n != 1 {
				t.Errorf("%d answers after running again, want 1", n)
			}
		})
	}
}

// A /revise written after the revision before it was answered is a send-back
// like any other; so is one after a revision that stopped without an answer.
func TestASendBackAfterTheLastRevisionIsNotInFlight(t *testing.T) {
	for name, answered := range map[string]bool{"answered": true, "parked": false} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.tr.say(12, send(1, "/revise First."))
			f.tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
			if answered {
				f.tr.say(12, answer(2, 1))
			}
			f.tr.say(12, send(3, "/revise Second."))
			f.pass()

			job := f.drive()
			if job.State != revise.Revising {
				t.Errorf("the job is in %q, want revising", job.State)
			}
			if n := len(f.tr.answers(3)); n != 0 {
				t.Errorf("%d answers to the send-back, want none yet", n)
			}
		})
	}
}

// Refused and done in one claim: the commands written during the flight are
// refused, and the ones after it are the send-back.
func TestInFlightAndAfterAreToldApart(t *testing.T) {
	f := setup(t)
	f.tr.say(12, send(1, "/revise First."))
	f.tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f.tr.say(12, send(2, "/revise During."))
	f.tr.say(12, answer(3, 1))
	f.tr.say(12, send(4, "/revise After."))
	f.pass()

	job := f.drive()
	if job.State != revise.Revising {
		t.Fatalf("the job is in %q, want revising", job.State)
	}
	if a := f.tr.answers(2); len(a) != 1 || !strings.Contains(a[0], "in flight") {
		t.Errorf("answers to the command during the flight = %q, want one refusal", a)
	}
	sb, err := f.deps.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sb.Points) != fmt.Sprint([]revise.Point{{Comment: 4, Text: "After."}}) {
		t.Errorf("points = %+v, want only the command after the flight", sb.Points)
	}
}

// A revision's reply and its hand-back both carry its marker. A command
// written between them is in flight too: the hand-back came after it, so the
// head it was written against has moved.
func TestACommandBetweenTheReplyAndTheHandBackIsInFlight(t *testing.T) {
	f := setup(t)
	f.tr.say(12, send(1, "/revise First."))
	f.tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f.tr.say(12, answer(2, 1)) // the revision's reply, before the command
	f.tr.say(12, send(3, "/revise Second."))
	f.tr.say(12, answer(4, 1)) // the revision's hand-back, after it
	f.pass()

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q (due %v), want at rest in start", job.State, !job.NextRunAt.IsZero())
	}
	if a := f.tr.answers(3); len(a) != 1 || !strings.Contains(a[0], "in flight") {
		t.Errorf("answers to the command between the reply and the hand-back = %q, want one refusal", a)
	}
}

// A refusal's reply carries a different marker from a revision's answer, so a
// command written before a refusal's reply lands is not read as in flight: no
// revision was in flight, only a reply in the post.
func TestARefusalReplyIsNotARevisionAnswer(t *testing.T) {
	f := setup(t)
	f.tr.say(12, send(1, "/revise")) // refused earlier: no points
	f.tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f.tr.say(12, send(3, "/revise Rename Foo."))
	// The earlier command's refusal reply is posted only after the command
	// below was written, the way a killed claim's read-back replays it.
	f.tr.say(12, github.Comment{ID: 4, Login: agent, Body: owed.ReplyMarker(1) + "\nThere is nothing here to revise."})
	f.pass()

	job := f.drive()
	if job.State != revise.Revising {
		t.Errorf("the job is in %q, want revising", job.State)
	}
	if a := f.tr.answers(3); len(a) != 0 {
		t.Errorf("answers to the command after a refusal = %q, want none", a)
	}
}

// A closed pull request's commands are claimed, and nothing else happens.
func TestAClosedPullRequestsCommandsAreOnlyClaimed(t *testing.T) {
	f := setup(t)
	f.tr.say(12, send(1, "/revise Rename Foo."))
	f.tr.say(12, send(2, "/revise"))
	f.tr.pr.State = "closed"

	// Intake lists open subjects only, so this is the job run by hand.
	a := transition.Armer{Store: f.store, Holder: "operator", LeaseTTL: time.Minute}
	if _, _, err := a.Restart(context.Background(), store.KindRevise, store.Subject{Type: store.SubjectPR, Number: 12}, revise.Start, now, "by-hand"); err != nil {
		t.Fatal(err)
	}
	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Errorf("the job is in %q, want at rest in start", job.State)
	}
	if !f.tr.claimed(1) || !f.tr.claimed(2) {
		t.Error("not every command is claimed")
	}
	if f.tr.writes["comment"] != 0 || f.tr.writes["unlabel"] != 0 {
		t.Errorf("writes = %v, want only the claims", f.tr.writes)
	}
}

// A hand-off label applied after the claim read the pull request is still
// taken off before the job moves on: the removal is owed whatever the read
// showed, and the read-back is what catches the label appearing in between.
func TestALabelAppliedAfterTheClaimIsStillTakenOff(t *testing.T) {
	f := setup(t)
	f.tr.pr.Labels = []string{"bug"}
	f.tr.say(12, send(1, "/revise Rename Foo."))
	subject := store.Subject{Type: store.SubjectPR, Number: 12}
	if _, err := f.store.Ensure(context.Background(), store.KindRevise, subject, revise.Start, now); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := store.ID(store.KindRevise, subject)
	if _, err := f.run.Run(ctx, "revise", id); err != nil {
		t.Fatal(err)
	}

	// The label appears while the claim's effects are in the post.
	f.tr.pr.Labels = []string{"bug", handOff}
	if _, err := f.run.Run(ctx, "revise-claimed", id); err != nil {
		t.Fatal(err)
	}
	if f.tr.labelled() {
		t.Error("the hand-off label is still on, want it taken off")
	}
}

// A pull request with no hand-off label on it is still carried through the
// removal: a label that is not there is not an error, and the removal being
// owed whatever the pull request read showed is what lets the read-back catch
// one applied after that read.
func TestNoHandOffLabelIsTakenOffHarmlessly(t *testing.T) {
	f := setup(t)
	f.tr.pr.Labels = []string{"bug"}
	f.tr.say(12, send(1, "/revise Rename Foo."))
	f.pass()

	if job := f.drive(); job.State != revise.Revising {
		t.Errorf("the job is in %q, want revising", job.State)
	}
	if got := strings.Join(f.tr.pr.Labels, ","); got != "bug" {
		t.Errorf("labels are %q, want only bug", got)
	}
}

func TestPointsAreTheBodyAfterTheWord(t *testing.T) {
	for body, want := range map[string]string{
		"/revise":                       "",
		"  /revise  \n\n ":              "",
		"/revise Rename Foo.":           "Rename Foo.",
		"/revise\nRename Foo.\n- and X": "Rename Foo.\n- and X",
	} {
		if got := revise.Points(body); got != want {
			t.Errorf("Points(%q) = %q, want %q", body, got, want)
		}
	}
}
