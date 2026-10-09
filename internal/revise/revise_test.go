package revise_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/github/githubtest"
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
	base    = "fed456"
)

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// newTracker is issue 7 and pull request 12 on a fixture tracker: what intake
// lists, and what the revise kind reads and writes. Its checks are green on
// every head, unless a test says otherwise, and it refuses a claim on a pull
// request's description, which the revise kind never makes.
func newTracker() *githubtest.Tracker {
	tr := githubtest.New(agent)
	tr.Issues[7] = &github.Issue{Number: 7, State: "open", DependenciesRead: true}
	tr.PullRequests[12] = &github.PullRequest{
		Number: 12, State: "open", HeadSHA: head, HeadRef: "feature", HeadRepo: repo, BaseRef: "main", BaseSHA: base,
		Login: "alice", Labels: []string{handOff, "bug"},
	}
	tr.Checks = green
	tr.Fail = func(c githubtest.Call) error {
		if c.Method == "ReactToIssue" {
			return errors.New("the revise kind never claims a pull request's description")
		}
		return nil
	}
	return tr
}

// green is every check passed.
func green(string, int) []github.CheckRun {
	return []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}, {Name: "lint", Status: "completed", Conclusion: "skipped"}}
}

// fail makes every call of method on tr fail with err, without landing, and
// leaves what else tr fails as it was.
func fail(tr *githubtest.Tracker, method string, err error) {
	before := tr.Fail
	tr.Fail = func(c githubtest.Call) error {
		if c.Method == method {
			return err
		}
		return before(c)
	}
}

// drop makes a comment of tr's carrying marker land and never be seen. An
// empty marker drops nothing.
func drop(tr *githubtest.Tracker, marker string) {
	tr.Drop = func(c githubtest.Call) bool {
		return marker != "" && c.Method == "Comment" && strings.Contains(c.Text, marker)
	}
}

// answersTo is the agent's replies to comment id.
func answersTo(tr *githubtest.Tracker, id int64) []string {
	var out []string
	for _, c := range tr.CommentsOn[12] {
		if c.Login == agent && strings.Contains(c.Body, owed.ReplyMarker(id)) {
			out = append(out, c.Body)
		}
	}
	return out
}

func claimed(tr *githubtest.Tracker, id int64) bool {
	return intake.Claimed(tr.ReactionsOn[id], agent)
}

func labelled(tr *githubtest.Tracker) bool {
	return github.HasLabel(tr.PullRequests[12].Labels, handOff)
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
	tr    *githubtest.Tracker
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
			Commands: []intake.Command{{Word: revise.Word, On: store.SubjectPR, Kind: store.KindRevise, Start: revise.Start, ByReview: true}},
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
	f.tr.Say(12, send(1, "/revise\nRename Foo to Bar.\n\nadvisory 3, but keep the test"))

	made := f.pass()
	if len(made) != 1 || made[0].Kind != store.KindRevise || made[0].Subject.Number != 12 {
		t.Fatalf("intake made %v, want one revise job for pull request 12", made)
	}
	job := f.drive()
	if job.State != revise.Revising || job.NextRunAt.IsZero() {
		t.Errorf("the job is in %q (due %v), want revising and due", job.State, !job.NextRunAt.IsZero())
	}
	if !claimed(f.tr, 1) {
		t.Error("the command is not claimed")
	}
	if got := strings.Join(f.tr.PullRequests[12].Labels, ","); got != "bug" {
		t.Errorf("labels are %q, want only the hand-off label taken off", got)
	}
	if f.tr.Writes["Comment"] != 0 {
		t.Errorf("%d comments, want none: the reply is the revision's", f.tr.Writes["Comment"])
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
	f.tr.Say(12, github.Comment{ID: 1, Login: "mallory", Association: "CONTRIBUTOR", Body: "/revise do it"})
	f.tr.Say(12, github.Comment{ID: 2, Login: "stranger", Association: "NONE", Body: "/revise do it"})
	f.tr.Say(12, github.Comment{ID: 3, Login: agent, Association: "OWNER", Body: "/revise do it"})
	f.tr.Say(7, send(4, "/revise do it"))

	if made := f.pass(); len(made) != 0 {
		t.Errorf("intake made %v, want nothing", made)
	}
	if n := len(f.tr.ReactionsOn); n != 0 {
		t.Errorf("%d comments reacted to, want none", n)
	}
}

// Several /revise with points before the agent got to them are one send-back,
// in the order they were written, and each is claimed.
func TestUnansweredSendBacksAreOneSendBackInOrder(t *testing.T) {
	f := setup(t)
	f.tr.Say(12, send(1, "/revise Rename Foo."))
	f.tr.Say(12, github.Comment{ID: 2, Login: "cory", Association: "OWNER", Body: "Looking again."})
	f.tr.Say(12, send(3, "/revise And drop the flag."))
	f.pass()

	job := f.drive()
	if job.State != revise.Revising {
		t.Fatalf("the job is in %q, want revising", job.State)
	}
	if !claimed(f.tr, 1) || !claimed(f.tr, 3) {
		t.Error("not every command is claimed")
	}
	sb, err := f.deps.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sb.Points) != fmt.Sprint([]revise.Point{{Comment: 1, Text: "Rename Foo."}, {Comment: 3, Text: "And drop the flag."}}) {
		t.Errorf("points = %+v, want both commands' in order", sb.Points)
	}
	if f.tr.Writes["Unlabel"] != 1 {
		t.Errorf("the label was taken off %d times, want once", f.tr.Writes["Unlabel"])
	}
}

// What cannot be revised is claimed and answered with one reply each, and
// nothing else happens: no label off, no work.
func TestWhatCannotBeRevisedGetsOneReply(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(*githubtest.Tracker)
		body    string
		says    string
	}{
		"no points":         {body: "/revise   ", says: "nothing here to revise"},
		"no points, a line": {body: "/revise\n\n", says: "nothing here to revise"},
		"a fork":            {prepare: func(tr *githubtest.Tracker) { tr.PullRequests[12].HeadRepo = "someone/fork" }, body: "/revise Rename Foo.", says: "not in o/n"},
		"a deleted fork":    {prepare: func(tr *githubtest.Tracker) { tr.PullRequests[12].HeadRepo = "" }, body: "/revise Rename Foo.", says: "not in o/n"},
		"in flight": {
			prepare: func(tr *githubtest.Tracker) {
				// An earlier send-back, claimed, answered only after
				// this one was written.
				tr.Say(12, send(1, "/revise First."))
				tr.ReactionsOn[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
			},
			body: "/revise Second.", says: "in flight",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			if tc.prepare != nil {
				tc.prepare(f.tr)
			}
			f.tr.Say(12, send(5, tc.body))
			if name == "in flight" {
				f.tr.Say(12, answer(6, 1))
			}
			f.pass()

			job := f.drive()
			if job.State != revise.Start || !job.NextRunAt.IsZero() {
				t.Errorf("the job is in %q (due %v), want at rest in start", job.State, !job.NextRunAt.IsZero())
			}
			if !claimed(f.tr, 5) {
				t.Error("the command is not claimed")
			}
			answers := answersTo(f.tr, 5)
			if len(answers) != 1 || !strings.Contains(answers[0], tc.says) {
				t.Errorf("answers = %q, want one saying %q", answers, tc.says)
			}
			if !labelled(f.tr) || f.tr.Writes["Unlabel"] != 0 {
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
			if n := len(answersTo(f.tr, 5)); n != 1 {
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
			f.tr.Say(12, send(1, "/revise First."))
			f.tr.ReactionsOn[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
			if answered {
				f.tr.Say(12, answer(2, 1))
			}
			f.tr.Say(12, send(3, "/revise Second."))
			f.pass()

			job := f.drive()
			if job.State != revise.Revising {
				t.Errorf("the job is in %q, want revising", job.State)
			}
			if n := len(answersTo(f.tr, 3)); n != 0 {
				t.Errorf("%d answers to the send-back, want none yet", n)
			}
		})
	}
}

// Refused and done in one claim: the commands written during the flight are
// refused, and the ones after it are the send-back.
func TestInFlightAndAfterAreToldApart(t *testing.T) {
	f := setup(t)
	f.tr.Say(12, send(1, "/revise First."))
	f.tr.ReactionsOn[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f.tr.Say(12, send(2, "/revise During."))
	f.tr.Say(12, answer(3, 1))
	f.tr.Say(12, send(4, "/revise After."))
	f.pass()

	job := f.drive()
	if job.State != revise.Revising {
		t.Fatalf("the job is in %q, want revising", job.State)
	}
	if a := answersTo(f.tr, 2); len(a) != 1 || !strings.Contains(a[0], "in flight") {
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
	f.tr.Say(12, send(1, "/revise First."))
	f.tr.ReactionsOn[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f.tr.Say(12, answer(2, 1)) // the revision's reply, before the command
	f.tr.Say(12, send(3, "/revise Second."))
	f.tr.Say(12, answer(4, 1)) // the revision's hand-back, after it
	f.pass()

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q (due %v), want at rest in start", job.State, !job.NextRunAt.IsZero())
	}
	if a := answersTo(f.tr, 3); len(a) != 1 || !strings.Contains(a[0], "in flight") {
		t.Errorf("answers to the command between the reply and the hand-back = %q, want one refusal", a)
	}
}

// A refusal's reply carries a different marker from a revision's answer, so a
// command written before a refusal's reply lands is not read as in flight: no
// revision was in flight, only a reply in the post.
func TestARefusalReplyIsNotARevisionAnswer(t *testing.T) {
	f := setup(t)
	f.tr.Say(12, send(1, "/revise")) // refused earlier: no points
	f.tr.ReactionsOn[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f.tr.Say(12, send(3, "/revise Rename Foo."))
	// The earlier command's refusal reply is posted only after the command
	// below was written, the way a killed claim's read-back replays it.
	f.tr.Say(12, github.Comment{ID: 4, Login: agent, Body: owed.ReplyMarker(1) + "\nThere is nothing here to revise."})
	f.pass()

	job := f.drive()
	if job.State != revise.Revising {
		t.Errorf("the job is in %q, want revising", job.State)
	}
	if a := answersTo(f.tr, 3); len(a) != 0 {
		t.Errorf("answers to the command after a refusal = %q, want none", a)
	}
}

// A closed pull request's commands are claimed, and nothing else happens.
func TestAClosedPullRequestsCommandsAreOnlyClaimed(t *testing.T) {
	f := setup(t)
	f.tr.Say(12, send(1, "/revise Rename Foo."))
	f.tr.Say(12, send(2, "/revise"))
	f.tr.PullRequests[12].State = "closed"

	// Intake lists open subjects only, so this is the job run by hand.
	a := transition.Armer{Store: f.store, Holder: "operator", LeaseTTL: time.Minute}
	if _, _, err := a.Restart(context.Background(), store.KindRevise, store.Subject{Type: store.SubjectPR, Number: 12}, revise.Start, now, "by-hand"); err != nil {
		t.Fatal(err)
	}
	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Errorf("the job is in %q, want at rest in start", job.State)
	}
	if !claimed(f.tr, 1) || !claimed(f.tr, 2) {
		t.Error("not every command is claimed")
	}
	if f.tr.Writes["Comment"] != 0 || f.tr.Writes["Unlabel"] != 0 {
		t.Errorf("writes = %v, want only the claims", f.tr.Writes)
	}
}

// A hand-off label applied after the claim read the pull request is still
// taken off before the job moves on: the removal is owed whatever the read
// showed, and the read-back is what catches the label appearing in between.
func TestALabelAppliedAfterTheClaimIsStillTakenOff(t *testing.T) {
	f := setup(t)
	f.tr.PullRequests[12].Labels = []string{"bug"}
	f.tr.Say(12, send(1, "/revise Rename Foo."))
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
	f.tr.PullRequests[12].Labels = []string{"bug", handOff}
	if _, err := f.run.Run(ctx, "revise-claimed", id); err != nil {
		t.Fatal(err)
	}
	if labelled(f.tr) {
		t.Error("the hand-off label is still on, want it taken off")
	}
}

// A pull request with no hand-off label on it is still carried through the
// removal: a label that is not there is not an error, and the removal being
// owed whatever the pull request read showed is what lets the read-back catch
// one applied after that read.
func TestNoHandOffLabelIsTakenOffHarmlessly(t *testing.T) {
	f := setup(t)
	f.tr.PullRequests[12].Labels = []string{"bug"}
	f.tr.Say(12, send(1, "/revise Rename Foo."))
	f.pass()

	if job := f.drive(); job.State != revise.Revising {
		t.Errorf("the job is in %q, want revising", job.State)
	}
	if got := strings.Join(f.tr.PullRequests[12].Labels, ","); got != "bug" {
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
