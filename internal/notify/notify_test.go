package notify_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/corygyarmathy/afk-agent/internal/budget"
	"github.com/corygyarmathy/afk-agent/internal/notify"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// message is one publish, as the seam saw it.
type message struct{ title, tag, body string }

// recorder stands in for the ntfy server. The tests here run offline
// (AGENTS.md), so this is what Post is for.
type recorder struct {
	mu   sync.Mutex
	msgs []message
	err  error         // what the next publish returns
	slow time.Duration // how long a publish takes
}

func (r *recorder) post(_ context.Context, title, tag, body string) error {
	r.mu.Lock()
	err, slow := r.err, r.slow
	r.mu.Unlock()

	// Outside the lock, so a slow server is slow the way a real one is:
	// concurrent publishes overlap rather than queuing behind each other.
	time.Sleep(slow)

	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		return err
	}
	r.msgs = append(r.msgs, message{title, tag, body})
	return nil
}

func (r *recorder) all() []message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]message(nil), r.msgs...)
}

func (r *recorder) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func notifier(r *recorder) *notify.Notifier {
	return &notify.Notifier{URL: "https://ntfy.example/afk-agent", Post: r.post}
}

func parked(state string, attempts int) store.Job {
	return store.Job{
		ID:       "review-pr-12",
		Kind:     store.KindReview,
		Subject:  store.Subject{Type: store.SubjectPR, Number: 12},
		State:    state,
		Attempts: attempts,
	}
}

func window(name, status string, percent float64, resets string) budget.Window {
	w := budget.Window{Name: name, Status: budget.Status(status), Percent: percent}
	if resets != "" {
		at, err := time.Parse(time.RFC3339, resets)
		if err != nil {
			panic(err)
		}
		w.ResetsAt = at
	}
	return w
}

// The acceptance criterion: repeated occurrences of the same condition do not
// re-notify on every poll. Both conditions are re-observed constantly - a
// limited window by every worker on every pass, a parked job by anything that
// reads the queue - so reporting each observation would be a notification a
// second for as long as the condition lasted.
func TestTheSameConditionIsReportedOnce(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	n := notifier(r)

	w := window("monthly", "rate-limited", 100, "2026-09-27T00:00:00Z")
	job := parked("await-ci", 3)
	for range 5 {
		if err := n.Exhausted(ctx, w); err != nil {
			t.Fatal(err)
		}
		if err := n.Parked(ctx, job, errors.New("the build did not finish")); err != nil {
			t.Fatal(err)
		}
	}

	if got := r.all(); len(got) != 2 {
		t.Fatalf("%d notifications for two conditions observed five times each, want 2: %+v", len(got), got)
	}
}

// Suppression is per occurrence, not per condition. A window that is spent
// again after it reset is news, and so is a job that parked again after an
// operator freed it - a key that held for the life of the process would make
// the second one silent forever.
func TestANewOccurrenceIsReportedAgain(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	n := notifier(r)

	first := window("monthly", "rate-limited", 100, "2026-09-27T00:00:00Z")
	next := window("monthly", "rate-limited", 100, "2026-10-27T00:00:00Z")
	for _, w := range []budget.Window{first, first, next, next} {
		if err := n.Exhausted(ctx, w); err != nil {
			t.Fatal(err)
		}
	}

	// A transition that moves resets the attempt count, so an operator who
	// freed this job and watched it fail again produces a different key.
	for _, job := range []store.Job{parked("await-ci", 3), parked("await-ci", 3), parked("await-ci", 1)} {
		if err := n.Parked(ctx, job, errors.New("still failing")); err != nil {
			t.Fatal(err)
		}
	}

	if got := r.all(); len(got) != 4 {
		t.Fatalf("%d notifications, want 4 - two windows and two parks: %+v", len(got), got)
	}
}

// A publish that failed reported nothing, so it must not suppress the next
// attempt. The alternative turns an ntfy that is down into an agent that is
// silent, and silence is what this channel says when nothing is wrong.
func TestAFailedPublishDoesNotSuppressTheNext(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	r.fail(errors.New("connection refused"))
	n := notifier(r)

	w := window("monthly", "rate-limited", 100, "2026-09-27T00:00:00Z")
	err := n.Exhausted(ctx, w)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Exhausted = %v, want the publish failure reported", err)
	}
	if n := len(r.all()); n != 0 {
		t.Fatalf("%d notifications recorded by a failing server", n)
	}

	r.fail(nil)
	if err := n.Exhausted(ctx, w); err != nil {
		t.Fatal(err)
	}
	if got := r.all(); len(got) != 1 {
		t.Fatalf("%d notifications after the server came back, want 1: %+v", len(got), got)
	}
}

// The token is never in what this package reports. A publish failure names the
// endpoint and the status, which is fetch.Post's contract, and the error this
// package wraps it in adds the title and nothing else.
func TestAPublishFailureNamesNeitherTheTokenNorTheURLsCredentials(t *testing.T) {
	r := &recorder{}
	r.fail(errors.New("https://ntfy.example/afk-agent: 401 Unauthorized"))
	n := notifier(r)
	n.Token = "the-secret"

	err := n.Exhausted(context.Background(), window("weekly", "rate-limited", 100, ""))
	if err == nil {
		t.Fatal("Exhausted = nil, want the 401 reported")
	}
	if strings.Contains(err.Error(), "the-secret") {
		t.Fatalf("the error carries the token: %v", err)
	}
}

// The two conditions are told apart by their tags, which is how a phone
// distinguishes them without the message being read.
func TestEachConditionCarriesItsOwnTag(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	n := notifier(r)

	if err := n.Parked(ctx, parked("await-ci", 2), errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if err := n.Exhausted(ctx, window("rolling", "rate-limited", 100, "2026-09-11T18:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if _, err := n.TierExhausted(ctx, parked("deferred", 0), store.Episode{Since: time.Now(), Times: 1}, errors.New("tier exhausted")); err != nil {
		t.Fatal(err)
	}

	got := r.all()
	if len(got) != 3 {
		t.Fatalf("%d notifications, want 3: %+v", len(got), got)
	}
	tags := map[string]bool{}
	for _, m := range got {
		if m.tag == "" {
			t.Fatalf("a notification with no tag: %+v", m)
		}
		if tags[m.tag] {
			t.Fatalf("two conditions carry the tag %q", m.tag)
		}
		tags[m.tag] = true
	}
}

// An exhausted tier is told once per episode, and only once the episode has
// gone on for TierAfter exhaustions (#76). Fewer is a bad few minutes at a
// provider, which ADR 0001 §10 keeps off the channel; more is the same episode
// the operator has already heard about, re-observed on every tier wait.
func TestAnExhaustedTierIsToldOncePerEpisodeAfterTheCount(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	n := notifier(r)
	n.TierAfter = 3

	job := parked("deferred", 0)
	since := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cause := errors.New("tier exhausted: all 2 enrolled models tried")
	for times := 1; times <= 5; times++ {
		if _, err := n.TierExhausted(ctx, job, store.Episode{Since: since, Times: times}, cause); err != nil {
			t.Fatal(err)
		}
		want := 0
		if times >= 3 {
			want = 1
		}
		if got := r.all(); len(got) != want {
			t.Fatalf("after %d exhaustions, %d notifications, want %d: %+v", times, len(got), want, got)
		}
	}

	// A later episode of the same job is a new occurrence: the job got past
	// the model in between, and its tier running out again is news.
	later := store.Episode{Since: since.Add(24 * time.Hour), Times: 3}
	if _, err := n.TierExhausted(ctx, job, later, cause); err != nil {
		t.Fatal(err)
	}
	if got := r.all(); len(got) != 2 {
		t.Fatalf("%d notifications after a second episode, want 2: %+v", len(got), got)
	}
}

// What TierExhausted reports is what the pool records, so a restarted process
// does not tell the episode again (#91): told once it is published, not while
// it is short of the count or its publish failed, and an episode already told
// is not published again by a notifier that has never seen it.
func TestAnExhaustedTierReportsWhetherItWasTold(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	n := notifier(r)
	n.TierAfter = 2

	job := parked("deferred", 0)
	ep := store.Episode{Since: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), Times: 1}
	if told, err := n.TierExhausted(ctx, job, ep, nil); err != nil || told {
		t.Fatalf("short of the count = %v, %v; want not told", told, err)
	}
	ep.Times = 2
	r.fail(errors.New("ntfy is down"))
	if told, err := n.TierExhausted(ctx, job, ep, nil); err == nil || told {
		t.Fatalf("a failed publish = %v, %v; want not told, and the error", told, err)
	}
	r.fail(nil)
	if told, err := n.TierExhausted(ctx, job, ep, nil); err != nil || !told {
		t.Fatalf("a publish = %v, %v; want told", told, err)
	}

	ep.Times, ep.Told = 3, true
	restarted := notifier(r)
	restarted.TierAfter = 2
	if told, err := restarted.TierExhausted(ctx, job, ep, nil); err != nil || !told {
		t.Fatalf("an episode already told = %v, %v; want told", told, err)
	}
	if got := r.all(); len(got) != 1 {
		t.Fatalf("%d notifications, want 1: %+v", len(got), got)
	}
}

// An episode is the same occurrence whichever location its start is read in:
// the pool counts it from its clock and reads it back from the store in UTC,
// and one episode is told once across the two.
func TestAnEpisodeIsOneOccurrenceInAnyLocation(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	n := notifier(r)
	n.TierAfter = 1

	job := parked("deferred", 0)
	since := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{since.In(time.FixedZone("AEST", 10*60*60)), since} {
		if _, err := n.TierExhausted(ctx, job, store.Episode{Since: at, Times: 1}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.all(); len(got) != 1 {
		t.Fatalf("%d notifications for one episode read in two locations, want 1: %+v", len(got), got)
	}
}

// What an exhausted tier's message has to carry: which job, on what, how long
// it has been going on, what the tier said, and that nothing will stop it
// on its own. It names the bound on a run among the causes, because a bound
// too short for the work fails every candidate the same way, and is the one
// cause the operator set.
func TestAnExhaustedTierSaysWhichJobAndWhatTheTierSaid(t *testing.T) {
	r := &recorder{}
	n := notifier(r)
	n.TierAfter = 2

	job := parked("deferred", 0)
	job.NextRunAt = time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC)
	// Since in the pool's own location, as the process that started the
	// episode has it: the message says it in UTC, as a restarted process
	// reading it back from the store would.
	since := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC).In(time.FixedZone("AEST", 10*60*60))
	ep := store.Episode{Since: since, Times: 2}
	if _, err := n.TierExhausted(context.Background(), job, ep, errors.New("tier exhausted: all 2 enrolled models tried")); err != nil {
		t.Fatal(err)
	}

	got := r.all()
	if len(got) != 1 {
		t.Fatalf("%d notifications, want 1", len(got))
	}
	for _, want := range []string{"review-pr-12", "pr #12", "2 times", "2026-09-26T12:00:00Z", "all 2 enrolled models tried", "2026-09-26T14:00:00Z", "--model-timeout"} {
		if !strings.Contains(got[0].title+"\n"+got[0].body, want) {
			t.Errorf("the notification does not mention %q:\n%s\n%s", want, got[0].title, got[0].body)
		}
	}
}

// A park's message says which job, which state, and what failed - the three
// things an operator needs before they can decide anything. The subject is
// there too, because a job id is this agent's name for the work and the tracker
// number is the operator's.
func TestAParkSaysWhichJobAndWhatFailed(t *testing.T) {
	r := &recorder{}
	n := notifier(r)

	err := n.Parked(context.Background(), parked("await-ci", 3), errors.New("the build did not finish"))
	if err != nil {
		t.Fatal(err)
	}

	got := r.all()[0]
	if !strings.Contains(got.title, "review-pr-12") {
		t.Errorf("title = %q, want the job named in it", got.title)
	}
	for _, want := range []string{"review-pr-12", "await-ci", "3 attempt", "pr #12", "the build did not finish"} {
		if !strings.Contains(got.body, want) {
			t.Errorf("body does not mention %q:\n%s", want, got.body)
		}
	}
}

// An exhausted window's message says which window, and when work resumes. The
// operator's next question after "the budget is spent" is "until when", and a
// notification that does not answer it sends them to `afk budget` for a number
// the notification already had.
func TestAnExhaustedBudgetSaysWhichWindowAndWhenItReopens(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	n := notifier(r)

	if err := n.Exhausted(ctx, window("monthly", "rate-limited", 100, "2026-09-27T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	got := r.all()[0]
	if !strings.Contains(got.title, "monthly") {
		t.Errorf("title = %q, want the window named in it", got.title)
	}
	for _, want := range []string{"monthly", "rate-limited", "2026-09-27T00:00:00Z"} {
		if !strings.Contains(got.body, want) {
			t.Errorf("body does not mention %q:\n%s", want, got.body)
		}
	}

	// A limit the provider reported with no timestamp is a real case: there is
	// nothing to defer to, and the message must not imply there is.
	if err := n.Exhausted(ctx, window("weekly", "rate-limited", 100, "")); err != nil {
		t.Fatal(err)
	}
	second := r.all()[1]
	if strings.Contains(second.body, "deferred until") {
		t.Errorf("a window with no reset time promises a resumption:\n%s", second.body)
	}
}

// ntfy carries the title in a header and refuses a body over 4 KB. Neither is
// this agent's to get wrong: a job id or a state name is a string a transition
// chose, and a park's cause is an error of whatever length the transition
// returned.
func TestAMessageFitsWhatTheServerAccepts(t *testing.T) {
	r := &recorder{}
	n := notifier(r)

	job := parked("a state\nwith a newline", 1)
	job.ID = "review\r\npr-12"
	if err := n.Parked(context.Background(), job, errors.New(strings.Repeat("é", 4000))); err != nil {
		t.Fatal(err)
	}

	got := r.all()[0]
	if strings.ContainsAny(got.title, "\r\n") {
		t.Errorf("title carries a line ending: %q", got.title)
	}
	if len(got.body) > 4096 {
		t.Errorf("body is %d bytes, more than the server accepts", len(got.body))
	}
	if !utf8.ValidString(got.body) {
		t.Error("the truncated body is not valid UTF-8")
	}
}

// The pool observes both conditions from several workers at once, so the
// suppression that makes them report once has to hold under that.
//
// The server is made slow on purpose. A worker is inside the publish for as
// long as the network takes, and that window is exactly where a notifier that
// checked and then recorded would let every other worker through: with a fast
// stub the first caller finishes before the rest have started, and the test
// passes whether or not the code is right.
func TestConcurrentObservationsReportOnce(t *testing.T) {
	ctx := context.Background()
	r := &recorder{slow: 50 * time.Millisecond}
	n := notifier(r)
	w := window("monthly", "rate-limited", 100, "2026-09-27T00:00:00Z")

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 20 {
				if err := n.Exhausted(ctx, w); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := r.all(); len(got) != 1 {
		t.Fatalf("%d notifications from 8 workers observing one window, want 1: %s", len(got), fmt.Sprint(got))
	}
}
