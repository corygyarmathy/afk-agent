package intake_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
)

const agent = "afk-bot"

// tracker is a fixture tracker: open issues and pull requests, their
// comments, and the reactions on those comments.
type tracker struct {
	issues    []int
	prs       []int
	comments  map[int][]github.Comment
	reactions map[int64][]github.Reaction
	broken    map[int]error

	// refused is the comments whose reactions cannot be read.
	refused map[int64]error

	// updated is when each subject last changed. say moves it; a subject not
	// in it was last changed at since.
	updated map[int]time.Time

	// asked is the comments whose reactions were read, and read the subjects
	// whose comments were.
	asked []int64
	read  []int
}

// since is when every subject in a fixture was opened.
var since = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func (tr *tracker) OpenIssues(context.Context) ([]github.Issue, error) {
	var out []github.Issue
	for _, n := range tr.issues {
		out = append(out, github.Issue{Number: n, State: "open", UpdatedAt: tr.updatedAt(n)})
	}
	for _, n := range tr.prs {
		out = append(out, github.Issue{Number: n, State: "open", PullRequest: true, UpdatedAt: tr.updatedAt(n)})
	}
	return out, nil
}

func (tr *tracker) updatedAt(n int) time.Time {
	if at, ok := tr.updated[n]; ok {
		return at
	}
	return since
}

// say posts a comment on subject n, which moves the subject's updated_at the
// way a comment on GitHub does.
func (tr *tracker) say(n int, c github.Comment) {
	if tr.comments == nil {
		tr.comments = map[int][]github.Comment{}
	}
	if tr.updated == nil {
		tr.updated = map[int]time.Time{}
	}
	tr.comments[n] = append(tr.comments[n], c)
	tr.updated[n] = tr.updatedAt(n).Add(time.Minute)
}

func (tr *tracker) Comments(_ context.Context, n int) ([]github.Comment, error) {
	tr.read = append(tr.read, n)
	if err := tr.broken[n]; err != nil {
		return nil, err
	}
	return tr.comments[n], nil
}

func (tr *tracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	tr.asked = append(tr.asked, id)
	if err := tr.refused[id]; err != nil {
		return nil, err
	}
	return tr.reactions[id], nil
}

func comment(id int64, login, association, body string) github.Comment {
	return github.Comment{ID: id, Login: login, Association: association, Body: body}
}

var now = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

func intakeFor(t *testing.T, s store.Store, tr *tracker) *intake.Intake {
	t.Helper()
	return &intake.Intake{
		Tracker: tr,
		Store:   s,
		Commands: []intake.Command{
			{Word: "/review", On: store.SubjectPR, Kind: store.KindReview, Start: "start"},
			{Word: "/implement", On: store.SubjectIssue, Kind: store.KindImplement, Start: "start"},
		},
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
	j := storetest.Job(t, s, "review-pr-12")
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

// A command is issued on an issue or on a pull request, and each word is on
// one of them: /implement on the issue it implements, /review on the pull
// request it reviews. Both are read in the same pass.
func TestACommandIsReadOnTheKindOfSubjectItIsIssuedOn(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{
		issues: []int{7, 8},
		prs:    []int{12, 13},
		comments: map[int][]github.Comment{
			7:  {comment(1, "alice", "OWNER", "/implement")},
			8:  {comment(2, "alice", "OWNER", "/review")},
			12: {comment(3, "alice", "OWNER", "/review")},
			13: {comment(4, "alice", "OWNER", "/implement")},
		},
	}

	made := pass(t, intakeFor(t, s, tr))
	if got, want := strings.Join(ids(made), " "), "implement-issue-7 review-pr-12"; got != want {
		t.Errorf("made due [%s], want [%s]", got, want)
	}
	// The word on the wrong kind of subject is not a command, so its
	// reactions are not worth a request.
	for _, id := range tr.asked {
		if id == 2 || id == 4 {
			t.Errorf("read the reactions of comment %d, a command on the wrong kind of subject", id)
		}
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
	storetest.Rest(t, s, "review-pr-12", "reviewed", 2, now)

	tr.say(12, comment(2, "alice", "OWNER", "/review"))
	made := pass(t, in)
	if got := ids(made); len(got) != 1 || got[0] != "review-pr-12" {
		t.Fatalf("made due %v, want [review-pr-12]", got)
	}
	j := storetest.Job(t, s, "review-pr-12")
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

	storetest.Rest(t, s, "review-pr-12", "start", 3, now)

	for range 3 {
		if made := pass(t, in); len(made) != 0 {
			t.Fatalf("an unclaimed command made %v due again", ids(made))
		}
	}
	if j := storetest.Job(t, s, "review-pr-12"); !j.NextRunAt.IsZero() || j.Attempts != 3 {
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
		if j := storetest.Job(t, s, "review-pr-12"); j.State != "waiting" || !j.NextRunAt.Equal(later) {
			t.Errorf("job = %+v, want it untouched", j)
		}
	})

	t.Run("held", func(t *testing.T) {
		s := storetest.Open(t)
		seeded := storetest.Seed(t, s, store.KindReview, 12, "start")
		storetest.Rest(t, s, seeded.ID, "start", 0, now)
		if _, ok, err := s.Acquire(ctx, seeded.ID, "a-live-transition", now, time.Hour); err != nil || !ok {
			t.Fatalf("Acquire = %v, %v", ok, err)
		}
		if made := pass(t, intakeFor(t, s, commented())); len(made) != 0 {
			t.Errorf("made %v due", ids(made))
		}
		if j := storetest.Job(t, s, seeded.ID); j.Lease == nil || j.Lease.Holder != "a-live-transition" || !j.NextRunAt.IsZero() {
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

// A subject that cannot be read does not stop the others.
func TestASubjectThatCannotBeReadDoesNotStopTheRest(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{
		issues:   []int{3},
		prs:      []int{1, 2},
		comments: map[int][]github.Comment{2: {comment(1, "alice", "OWNER", "/review")}},
		broken:   map[int]error{1: errors.New("502 Bad Gateway"), 3: errors.New("503 Service Unavailable")},
	}

	made, err := intakeFor(t, s, tr).Pass(context.Background())
	if got := ids(made); len(got) != 1 || got[0] != "review-pr-2" {
		t.Errorf("made due %v, want [review-pr-2]", got)
	}
	if err == nil || !strings.Contains(err.Error(), "pull request 1") || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want it to name pull request 1 and what went wrong", err)
	}
	if err == nil || !strings.Contains(err.Error(), "issue 3") || !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v, want it to name issue 3 and what went wrong", err)
	}
}

// A pass reads the comments of a subject that changed since passes settled it,
// and no other: a comments request per open subject per poll is a rate limit
// the backlog grows into. A new comment moves the subject, so it is read.
func TestAPassReadsOnlyTheSubjectsThatChanged(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{
		issues: []int{3},
		prs:    []int{12, 13},
		comments: map[int][]github.Comment{
			3:  {comment(1, "alice", "OWNER", "a question")},
			12: {comment(2, "alice", "OWNER", "/review")},
		},
	}
	in := intakeFor(t, s, tr)
	pass(t, in)
	if got := fmt.Sprint(tr.read); got != "[3 12 13]" {
		t.Fatalf("the first pass read %s, want every open subject", got)
	}
	// One read does not settle a subject, so the second pass reads them all
	// again.
	tr.read = nil
	pass(t, in)
	if got := fmt.Sprint(tr.read); got != "[3 12 13]" {
		t.Fatalf("the second pass read %s, want every open subject", got)
	}

	tr.read, tr.asked = nil, nil
	if made := pass(t, in); len(made) != 0 {
		t.Errorf("made %v due", ids(made))
	}
	if len(tr.read) != 0 || len(tr.asked) != 0 {
		t.Errorf("a pass over subjects that had not changed read comments on %v and reactions on %v", tr.read, tr.asked)
	}

	tr.say(3, comment(3, "alice", "OWNER", "/implement"))
	tr.read = nil
	made := pass(t, in)
	if got := fmt.Sprint(tr.read); got != "[3]" {
		t.Errorf("read %s, want [3], the subject with the new comment", got)
	}
	if got := ids(made); len(got) != 1 || got[0] != "implement-issue-3" {
		t.Errorf("made due %v, want [implement-issue-3]", got)
	}
}

// A new intake - a restart - has read nothing, so it reads everything, and a
// command issued while nothing was running is not missed.
func TestANewIntakeReadsEverySubject(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{prs: []int{12, 13}}
	pass(t, intakeFor(t, s, tr))

	// Commented on without moving updated_at, which only a restart finds.
	tr.comments = map[int][]github.Comment{13: {comment(1, "alice", "OWNER", "/review")}}
	tr.read = nil
	made := pass(t, intakeFor(t, s, tr))
	if got := fmt.Sprint(tr.read); got != "[12 13]" {
		t.Errorf("a new intake read %s, want every open subject", got)
	}
	if got := ids(made); len(got) != 1 || got[0] != "review-pr-13" {
		t.Errorf("made due %v, want [review-pr-13]", got)
	}
}

// A subject whose comments could not be read is read again on the next pass,
// and on every pass until they can.
func TestASubjectThatFailedIsReadAgain(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{
		prs:      []int{1, 2},
		comments: map[int][]github.Comment{2: {comment(1, "alice", "OWNER", "/review")}},
		broken:   map[int]error{1: errors.New("502 Bad Gateway")},
	}
	in := intakeFor(t, s, tr)
	if _, err := in.Pass(context.Background()); err == nil {
		t.Fatal("a pass with a broken subject returned no error")
	}

	// The second pass settles 2, and reads 1 again.
	if _, err := in.Pass(context.Background()); err == nil {
		t.Fatal("the next pass returned no error")
	}
	tr.read = nil
	if _, err := in.Pass(context.Background()); err == nil {
		t.Fatal("the next pass returned no error")
	}
	if got := fmt.Sprint(tr.read); got != "[1]" {
		t.Errorf("read %s, want [1], the subject that failed", got)
	}

	delete(tr.broken, 1)
	tr.read = nil
	pass(t, in)
	pass(t, in)
	if got := fmt.Sprint(tr.read); got != "[1 1]" {
		t.Errorf("read %s once it could be read, want [1 1], a read and the one that settles it", got)
	}
	tr.read = nil
	pass(t, in)
	if len(tr.read) != 0 {
		t.Errorf("read %v after it was settled, want nothing", tr.read)
	}
}

// A subject whose comments were read but whose command's reactions were not is
// not settled either: the command is neither armed nor answered.
func TestASubjectWhoseReactionsFailedIsReadAgain(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{
		prs:      []int{12},
		comments: map[int][]github.Comment{12: {comment(1, "alice", "OWNER", "/review")}},
		refused:  map[int64]error{1: errors.New("502 Bad Gateway")},
	}
	in := intakeFor(t, s, tr)
	for range 2 {
		if _, err := in.Pass(context.Background()); err == nil {
			t.Fatal("a pass whose reactions read failed returned no error")
		}
	}

	delete(tr.refused, 1)
	tr.read = nil
	if got := ids(pass(t, in)); len(got) != 1 || got[0] != "review-pr-12" {
		t.Errorf("made due %v once the reactions could be read, want [review-pr-12]", got)
	}
	if got := fmt.Sprint(tr.read); got != "[12]" {
		t.Errorf("read %s, want [12]", got)
	}
}

// A command whose job was already queued or held is left to that run, and a
// later pass arms it if the run did not answer it. The subject may not have
// changed in between - the claim is a reaction, not a comment - so it is read
// again until its command is armed or answered.
func TestACommandLeftToARunIsReadAgainUntilSettled(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	seeded := storetest.Seed(t, s, store.KindReview, 12, "start")
	storetest.Rest(t, s, seeded.ID, "start", 0, now)
	if _, ok, err := s.Acquire(ctx, seeded.ID, "a-live-transition", now, time.Hour); err != nil || !ok {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	tr := &tracker{prs: []int{12}, comments: map[int][]github.Comment{
		12: {comment(1, "alice", "OWNER", "/review")},
	}}
	in := intakeFor(t, s, tr)
	if made := pass(t, in); len(made) != 0 {
		t.Fatalf("made %v due while the job was held", ids(made))
	}

	// The run parks before it claims the command, and the subject has not
	// changed.
	if err := s.Release(ctx, seeded.ID, "a-live-transition"); err != nil {
		t.Fatal(err)
	}
	tr.read = nil
	made := pass(t, in)
	if got := fmt.Sprint(tr.read); got != "[12]" {
		t.Errorf("read %s, want [12], a subject with a command nobody armed or answered", got)
	}
	if got := ids(made); len(got) != 1 || got[0] != seeded.ID {
		t.Errorf("made due %v, want [%s]", got, seeded.ID)
	}

	tr.read = nil
	pass(t, in)
	pass(t, in)
	if got := fmt.Sprint(tr.read); got != "[12]" {
		t.Errorf("read %s once its command was armed, want [12], the read that settles it", got)
	}
}

// A read can miss a comment the listing's updated_at already covers: one
// posted after the read but in the same second, which is updated_at's
// resolution, or one a comments read lagging the listing did not have yet.
// Either way updated_at does not move again, and the next pass reads the
// subject anyway, because one read at an updated_at does not settle it.
func TestACommentTheReadMissedIsReadOnTheNextPass(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{prs: []int{12}}
	in := intakeFor(t, s, tr)
	pass(t, in)
	pass(t, in)

	// The subject changes, and the pass that lists the change reads it
	// without the comment.
	tr.updated = map[int]time.Time{12: since.Add(time.Minute)}
	pass(t, in)
	tr.comments = map[int][]github.Comment{12: {comment(1, "alice", "OWNER", "/review")}}

	tr.read = nil
	if got := ids(pass(t, in)); len(got) != 1 || got[0] != "review-pr-12" {
		t.Errorf("made due %v, want [review-pr-12]", got)
	}
	if got := fmt.Sprint(tr.read); got != "[12]" {
		t.Errorf("read %s, want [12]", got)
	}
}

// A zero updated_at says nothing about whether a subject changed, so a subject
// listed with one is read on every pass.
func TestASubjectWithNoUpdatedAtIsReadEveryPass(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{prs: []int{12}, updated: map[int]time.Time{12: {}}}
	in := intakeFor(t, s, tr)
	for range 3 {
		pass(t, in)
	}
	if got := fmt.Sprint(tr.read); got != "[12 12 12]" {
		t.Errorf("read %s over three passes, want [12 12 12]", got)
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
		{"a command on no subject", func(in *intake.Intake) { in.Commands[0].On = "" }, "unknown subject type"},
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
