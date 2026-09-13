package intake_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
)

const agent = "afk-bot"

// tracker is a fixture tracker: pull requests, their comments, and the
// reactions on those comments.
type tracker struct {
	prs       []int
	comments  map[int][]github.Comment
	reactions map[int64][]github.Reaction
	broken    map[int]error

	// asked is the comments whose reactions were read.
	asked []int64
}

func (tr *tracker) OpenPullRequests(context.Context) ([]github.PullRequest, error) {
	var prs []github.PullRequest
	for _, n := range tr.prs {
		prs = append(prs, github.PullRequest{Number: n, State: "open"})
	}
	return prs, nil
}

func (tr *tracker) Comments(_ context.Context, n int) ([]github.Comment, error) {
	if err := tr.broken[n]; err != nil {
		return nil, err
	}
	return tr.comments[n], nil
}

func (tr *tracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	tr.asked = append(tr.asked, id)
	return tr.reactions[id], nil
}

func comment(id int64, login, association, body string) github.Comment {
	return github.Comment{ID: id, Login: login, Association: association, Body: body}
}

var now = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

func intakeFor(t *testing.T, s store.Store, tr *tracker) *intake.Intake {
	t.Helper()
	return &intake.Intake{
		Tracker:  tr,
		Store:    s,
		Commands: []intake.Command{{Word: "/review", Kind: store.KindReview, Start: "start"}},
		Login:    agent,
		Holder:   "intake-test",
		LeaseTTL: time.Minute,
		Clock:    func() time.Time { return now },
	}
}

func pass(t *testing.T, in *intake.Intake) []store.Job {
	t.Helper()
	made, err := in.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	return made
}

func ids(jobs []store.Job) []string {
	var out []string
	for _, j := range jobs {
		out = append(out, j.ID)
	}
	return out
}

func job(t *testing.T, s store.Store, id string) store.Job {
	t.Helper()
	j, err := s.Job(context.Background(), id)
	if err != nil {
		t.Fatalf("Job(%s): %v", id, err)
	}
	return j
}

// rest puts a job in a state with nothing scheduled and no lease, the way a
// transition that finished or handed back leaves it.
func rest(t *testing.T, s store.Store, id, state string, attempts int) {
	t.Helper()
	ctx := context.Background()
	if _, ok, err := s.Acquire(ctx, id, "a-transition", now, time.Minute); err != nil || !ok {
		t.Fatalf("Acquire(%s) = %v, %v", id, ok, err)
	}
	if err := s.Commit(ctx, store.Commit{JobID: id, Holder: "a-transition", State: state, Attempts: attempts, Release: true}); err != nil {
		t.Fatalf("Commit(%s): %v", id, err)
	}
}

func TestAReviewCommandMakesAReviewJobDue(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{prs: []int{12}, comments: map[int][]github.Comment{
		12: {comment(1, "alice", "OWNER", "/review")},
	}}
	in := intakeFor(t, s, tr)

	made := pass(t, in)
	if got := ids(made); len(got) != 1 || got[0] != "review-pr-12" {
		t.Fatalf("made due %v, want [review-pr-12]", got)
	}
	j := job(t, s, "review-pr-12")
	if j.State != "start" || !j.NextRunAt.Equal(now) || j.Lease != nil {
		t.Errorf("job = %+v, want state start, due now, no lease", j)
	}

	// The same comment on the next pass is the same command, not a second one.
	if again := pass(t, in); len(again) != 0 {
		t.Errorf("the next pass made %v due again", ids(again))
	}
	if jobs, _ := s.Jobs(context.Background()); len(jobs) != 1 {
		t.Errorf("%d jobs in the store, want 1", len(jobs))
	}
}

// The repository is public and a command spends budget: only accounts with
// write access issue one, and the agent never issues one to itself.
func TestOnlyAWriterWhoIsNotTheAgentIssuesACommand(t *testing.T) {
	for _, tc := range []struct {
		name, login, association, body string
		want                           bool
	}{
		{"an owner", "alice", "OWNER", "/review", true},
		{"a member", "bob", "MEMBER", "/review", true},
		{"a collaborator", "carol", "COLLABORATOR", "/review", true},
		{"a command with words after it", "alice", "OWNER", "/review please look at the store\nthanks", true},
		{"a contributor", "dave", "CONTRIBUTOR", "/review", false},
		{"a first-time contributor", "erin", "FIRST_TIME_CONTRIBUTOR", "/review", false},
		{"no association", "mallory", "NONE", "/review", false},
		{"the agent itself", agent, "COLLABORATOR", "/review", false},
		{"the agent itself, spelled differently", "AFK-Bot", "OWNER", "/review", false},
		{"a mention that is not a command", "alice", "OWNER", "could someone /review this", false},
		{"a longer word", "alice", "OWNER", "/reviewer", false},
		{"a command after the first line", "alice", "OWNER", "thanks\n/review", false},
		{"an empty comment", "alice", "OWNER", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			tr := &tracker{prs: []int{12}, comments: map[int][]github.Comment{
				12: {comment(1, tc.login, tc.association, tc.body)},
			}}
			made := pass(t, intakeFor(t, s, tr))
			if got := len(made) == 1; got != tc.want {
				t.Errorf("made due %v, want a job: %v", ids(made), tc.want)
			}
			// Reactions are an API call per comment, so only a command
			// costs one.
			if !tc.want && len(tr.asked) != 0 {
				t.Errorf("read the reactions of a comment that is not a command")
			}
		})
	}
}

// Answered is the agent's own claim, and nobody else's.
func TestACommandTheAgentClaimedIsAnswered(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reactions []github.Reaction
		want      bool
	}{
		{"claimed by the agent", []github.Reaction{{Login: agent, Content: intake.Claim}}, false},
		{"claimed by the agent, spelled differently", []github.Reaction{{Login: "AFK-BOT", Content: intake.Claim}}, false},
		{"the same reaction from someone else", []github.Reaction{{Login: "alice", Content: intake.Claim}}, true},
		{"a different reaction from the agent", []github.Reaction{{Login: agent, Content: "+1"}}, true},
		{"no reactions", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			tr := &tracker{
				prs:       []int{12},
				comments:  map[int][]github.Comment{12: {comment(1, "alice", "OWNER", "/review")}},
				reactions: map[int64][]github.Reaction{1: tc.reactions},
			}
			made := pass(t, intakeFor(t, s, tr))
			if got := len(made) == 1; got != tc.want {
				t.Errorf("made due %v, want a job: %v", ids(made), tc.want)
			}
		})
	}
}

// A second command on a pull request whose job came to rest makes that job due
// again - the same job, since its id is its kind and its subject - and starts
// it over.
func TestANewCommandStartsAJobAtRestOver(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{
		prs:       []int{12},
		comments:  map[int][]github.Comment{12: {comment(1, "alice", "OWNER", "/review")}},
		reactions: map[int64][]github.Reaction{},
	}
	in := intakeFor(t, s, tr)
	pass(t, in)

	// The first command is answered and its job has come to rest, having
	// failed twice on the way.
	tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	rest(t, s, "review-pr-12", "reviewed", 2)

	tr.comments[12] = append(tr.comments[12], comment(2, "alice", "OWNER", "/review"))
	made := pass(t, in)
	if got := ids(made); len(got) != 1 || got[0] != "review-pr-12" {
		t.Fatalf("made due %v, want [review-pr-12]", got)
	}
	j := job(t, s, "review-pr-12")
	if j.State != "start" || j.Attempts != 0 || !j.NextRunAt.Equal(now) || j.Lease != nil {
		t.Errorf("job = %+v, want it started over: state start, no attempts, due now, no lease", j)
	}
}

// A job that came to rest without claiming its command - a failure ahead of
// the claim, then a park - is not made due on every pass. That would be a retry
// loop nobody configured, and it would run at the poll interval.
func TestACommandArmsItsJobOnce(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{prs: []int{12}, comments: map[int][]github.Comment{
		12: {comment(1, "alice", "OWNER", "/review")},
	}}
	in := intakeFor(t, s, tr)
	pass(t, in)

	rest(t, s, "review-pr-12", "start", 3)

	for range 3 {
		if made := pass(t, in); len(made) != 0 {
			t.Fatalf("an unclaimed command made %v due again", ids(made))
		}
	}
	if j := job(t, s, "review-pr-12"); !j.NextRunAt.IsZero() || j.Attempts != 3 {
		t.Errorf("job = %+v, want it left at rest for a human", j)
	}
}

// A job already queued, or in the hands of a live process, is left alone. That
// run will meet the command.
func TestAJobAlreadyQueuedOrHeldIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	commented := func() *tracker {
		return &tracker{prs: []int{12}, comments: map[int][]github.Comment{
			12: {comment(1, "alice", "OWNER", "/review")},
		}}
	}

	t.Run("queued", func(t *testing.T) {
		s := storetest.Open(t)
		later := now.Add(time.Hour)
		if _, err := s.Ensure(ctx, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "waiting", later); err != nil {
			t.Fatal(err)
		}
		if made := pass(t, intakeFor(t, s, commented())); len(made) != 0 {
			t.Errorf("made %v due", ids(made))
		}
		if j := job(t, s, "review-pr-12"); j.State != "waiting" || !j.NextRunAt.Equal(later) {
			t.Errorf("job = %+v, want it untouched", j)
		}
	})

	t.Run("held", func(t *testing.T) {
		s := storetest.Open(t)
		seeded := storetest.Seed(t, s, store.KindReview, 12, "start")
		rest(t, s, seeded.ID, "start", 0)
		if _, ok, err := s.Acquire(ctx, seeded.ID, "a-live-transition", now, time.Hour); err != nil || !ok {
			t.Fatalf("Acquire = %v, %v", ok, err)
		}
		if made := pass(t, intakeFor(t, s, commented())); len(made) != 0 {
			t.Errorf("made %v due", ids(made))
		}
		if j := job(t, s, seeded.ID); j.Lease == nil || j.Lease.Holder != "a-live-transition" || !j.NextRunAt.IsZero() {
			t.Errorf("job = %+v, want it untouched and still held", j)
		}
	})
}

// Wiping the store loses dedup history and never the queue: a pass over the
// same tracker re-derives the same jobs, and a command the agent claimed stays
// answered because the claim is on the tracker.
func TestWipingTheStoreReDerivesTheSameJobs(t *testing.T) {
	tr := &tracker{
		prs: []int{3, 12, 40},
		comments: map[int][]github.Comment{
			3:  {comment(1, "alice", "OWNER", "/review")},
			12: {comment(2, "bob", "MEMBER", "/review"), comment(3, "mallory", "NONE", "/review")},
			40: {comment(4, "alice", "OWNER", "/review")},
		},
		reactions: map[int64][]github.Reaction{4: {{Login: agent, Content: intake.Claim}}},
	}

	before := ids(pass(t, intakeFor(t, storetest.Open(t), tr)))
	after := ids(pass(t, intakeFor(t, storetest.Open(t), tr)))

	want := "[review-pr-3 review-pr-12]"
	if got := strings.Join([]string{"[", strings.Join(before, " "), "]"}, ""); got != want {
		t.Errorf("before the wipe: %s, want %s", got, want)
	}
	if strings.Join(before, " ") != strings.Join(after, " ") {
		t.Errorf("after the wipe: %v, want the same jobs as before: %v", after, before)
	}
}

// A pull request that cannot be read does not stop the others.
func TestAPullRequestThatCannotBeReadDoesNotStopTheRest(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{
		prs:      []int{1, 2},
		comments: map[int][]github.Comment{2: {comment(1, "alice", "OWNER", "/review")}},
		broken:   map[int]error{1: errors.New("502 Bad Gateway")},
	}

	made, err := intakeFor(t, s, tr).Pass(context.Background())
	if got := ids(made); len(got) != 1 || got[0] != "review-pr-2" {
		t.Errorf("made due %v, want [review-pr-2]", got)
	}
	if err == nil || !strings.Contains(err.Error(), "pull request 1") || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want it to name pull request 1 and what went wrong", err)
	}
}

func TestAnIntakeThatCannotWorkIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*intake.Intake)
		want  string
	}{
		{"no login", func(in *intake.Intake) { in.Login = "" }, "own login"},
		{"no lease", func(in *intake.Intake) { in.LeaseTTL = 0 }, "lease"},
		{"a command that is not a word", func(in *intake.Intake) { in.Commands[0].Word = "review" }, "starting with /"},
		{"a command for no kind", func(in *intake.Intake) { in.Commands[0].Kind = "deploy" }, "unknown job kind"},
		{"a command with no start", func(in *intake.Intake) { in.Commands[0].Start = "" }, "no start state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &tracker{prs: []int{12}}
			in := intakeFor(t, storetest.Open(t), tr)
			tc.spoil(in)
			if _, err := in.Pass(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
