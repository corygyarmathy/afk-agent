package dispatch_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
	"github.com/corygyarmathy/afk-agent/internal/dispatch"
	"github.com/corygyarmathy/afk-agent/internal/notify"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

func openStore(t *testing.T) store.Store {
	return storetest.Open(t)
}

func pool(t *testing.T, capacity map[string]int) *transition.Pool {
	t.Helper()
	p, err := transition.NewPool(capacity)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

// dispatcher is a pool configured the way a test wants it: fast, so a test that
// waits on it finishes, and small, so what it proves is about the limits rather
// than about timing.
func dispatcher(t *testing.T, s store.Store, reg *transition.Registry, p *transition.Pool, workers int) *dispatch.Dispatcher {
	t.Helper()
	return &dispatch.Dispatcher{
		Runner: transition.Runner{
			Store:    s,
			Registry: reg,
			Holder:   "test",
			LeaseTTL: time.Minute,
		},
		Pool:      p,
		Workers:   workers,
		Poll:      time.Millisecond,
		TokenWait: 5 * time.Second,
		Log:       func(msg string) { t.Log(msg) },
	}
}

// runUntil starts the dispatcher and stops it when done closes or the deadline
// passes, so a failing test fails rather than hanging.
func runUntil(t *testing.T, d *dispatch.Dispatcher, done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.Run(ctx)
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Error("the dispatcher did not finish the work in time")
	}
	cancel()
	wg.Wait()
}

// Worker parallelism and token capacity are two limits, not one. Eight workers
// with a single heavy-build permit is the configuration that says so: the light
// transitions overlap freely, and the heavy ones never do.
func TestWorkerParallelismAndTokenCapacityAreSeparateLimits(t *testing.T) {
	s := openStore(t)

	const (
		workers = 8
		heavy   = 6
		light   = 6
	)
	var (
		heavyNow, heavyPeak atomic.Int64
		lightNow, lightPeak atomic.Int64
		remaining           atomic.Int64
	)
	remaining.Store(heavy + light)
	done := make(chan struct{})

	finish := func() {
		if remaining.Add(-1) == 0 {
			close(done)
		}
	}
	track := func(now, peak *atomic.Int64) int64 {
		n := now.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		return n
	}

	// The reviews meet rather than sleep. Each one waits until a second is
	// running alongside it, so that a pool which lets them overlap always gets
	// the chance to, however a busy host schedules the workers - and a pool which
	// runs them one at a time fails at the deadline instead of passing by luck.
	// The deadline is shared, so that pool fails once rather than once a review.
	overlapped := make(chan struct{})
	var overlap sync.Once
	meet, gaveUp := context.WithTimeout(context.Background(), 10*time.Second)
	defer gaveUp()

	reg := transition.MustRegistry(
		transition.Transition{
			Name: "build", Kind: store.KindImplement, From: "start",
			Tokens: []string{"heavy-build"},
			Run: func(context.Context, transition.In) (transition.Result, error) {
				defer finish()
				track(&heavyNow, &heavyPeak)
				// Long enough that a second build let in beside it would still
				// be running. The capacity is an upper bound, so a build that
				// happens to run alone cannot make this test fail.
				time.Sleep(20 * time.Millisecond)
				heavyNow.Add(-1)
				return transition.Result{State: "built"}, nil
			},
		},
		transition.Transition{
			Name: "review", Kind: store.KindReview, From: "start",
			Run: func(ctx context.Context, _ transition.In) (transition.Result, error) {
				defer finish()
				if track(&lightNow, &lightPeak) >= 2 {
					overlap.Do(func() { close(overlapped) })
				}
				select {
				case <-overlapped:
				case <-meet.Done():
				case <-ctx.Done():
				}
				lightNow.Add(-1)
				return transition.Result{State: "reviewed"}, nil
			},
		},
	)

	for i := range heavy {
		storetest.Seed(t, s, store.KindImplement, 100+i, "start")
	}
	for i := range light {
		storetest.Seed(t, s, store.KindReview, 200+i, "start")
	}

	runUntil(t, dispatcher(t, s, reg, pool(t, map[string]int{"heavy-build": 1}), workers), done)

	if got := heavyPeak.Load(); got != 1 {
		t.Errorf("%d builds at once; the heavy-build token's capacity is 1", got)
	}
	if got := lightPeak.Load(); got < 2 {
		t.Errorf("%d reviews at once; a transition holding no token should not be limited to one", got)
	}
}

// Two workers must not both get the same job. The store makes that true
// (selecting and leasing are one statement); this is the dispatcher holding up
// its end of it.
func TestEachJobIsRunOnce(t *testing.T) {
	s := openStore(t)

	const jobs = 20
	var runs sync.Map
	var remaining atomic.Int64
	remaining.Store(jobs)
	done := make(chan struct{})

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			n, _ := runs.LoadOrStore(in.Job.ID, new(atomic.Int64))
			n.(*atomic.Int64).Add(1)
			if remaining.Add(-1) == 0 {
				close(done)
			}
			return transition.Result{State: "reviewed"}, nil
		},
	})

	for i := range jobs {
		storetest.Seed(t, s, store.KindReview, 300+i, "start")
	}

	runUntil(t, dispatcher(t, s, reg, pool(t, nil), 8), done)

	runs.Range(func(id, n any) bool {
		if got := n.(*atomic.Int64).Load(); got != 1 {
			t.Errorf("%s ran %d times, want 1", id, got)
		}
		return true
	})
	for _, j := range jobsIn(t, s) {
		if j.State != "reviewed" {
			t.Errorf("%s is in state %q, want reviewed", j.ID, j.State)
		}
		if j.Lease != nil {
			t.Errorf("%s is still leased: %+v", j.ID, j.Lease)
		}
	}
}

// A job due in a state nothing can move it out of would otherwise be picked up,
// put back and picked up again forever, which burns a worker on a job that is
// going nowhere. It is parked instead, where an operator can see it.
func TestAJobWithNoTransitionOutOfItsStateIsParked(t *testing.T) {
	s := openStore(t)
	job := storetest.Seed(t, s, store.KindReview, 12, "a-state-from-a-newer-binary")

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{State: "reviewed"}, nil
		},
	})

	done := make(chan struct{})
	d := dispatcher(t, s, reg, pool(t, nil), 1)
	var once sync.Once
	d.Log = func(msg string) {
		t.Log(msg)
		if strings.Contains(msg, "parking") {
			once.Do(func() { close(done) })
		}
	}
	runUntil(t, d, done)

	got, err := s.Job(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if !got.NextRunAt.IsZero() {
		t.Errorf("NextRunAt = %s; want nothing scheduled", got.NextRunAt)
	}
	if got.Lease != nil {
		t.Errorf("lease = %+v; want it released", got.Lease)
	}
}

func TestRunRefusesAMisconfiguredDispatcher(t *testing.T) {
	s := openStore(t)
	reg := transition.MustRegistry(transition.Transition{
		Name: "build", Kind: store.KindImplement, From: "start",
		Tokens: []string{"heavy-build"},
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{State: "built"}, nil
		},
	})

	tests := []struct {
		name string
		mut  func(*dispatch.Dispatcher)
		want string
	}{
		{"no workers", func(d *dispatch.Dispatcher) { d.Workers = 0 }, "0 workers"},
		{"no holder", func(d *dispatch.Dispatcher) { d.Holder = "" }, "holder"},
		{"no lease", func(d *dispatch.Dispatcher) { d.LeaseTTL = 0 }, "lease"},
		{"no poll interval", func(d *dispatch.Dispatcher) { d.Poll = 0 }, "poll"},
		{"no token wait", func(d *dispatch.Dispatcher) { d.TokenWait = 0 }, "token wait"},
		{
			// A worker still queuing for a permit after its lease has lapsed
			// is a worker about to commit a job somebody else now holds.
			name: "a token wait that outlives the lease",
			mut:  func(d *dispatch.Dispatcher) { d.TokenWait = d.LeaseTTL },
			want: "not shorter than the lease",
		},
		{
			// The one worth catching at startup: it would otherwise run fine
			// until the day that transition is first dispatched.
			name: "a token nobody configured",
			mut:  func(d *dispatch.Dispatcher) { d.Pool = pool(t, nil) },
			want: `unknown resource token "heavy-build"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := dispatcher(t, s, reg, pool(t, map[string]int{"heavy-build": 1}), 1)
			tt.mut(d)
			err := d.Run(context.Background())
			if err == nil {
				t.Fatalf("Run = nil error; want one mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Run error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// Stopping the pool costs at most one transition per worker: a worker between
// transitions holds nothing, and every lease is given back.
func TestStoppingLeavesNothingHeld(t *testing.T) {
	s := openStore(t)
	for i := range 5 {
		storetest.Seed(t, s, store.KindReview, 400+i, "start")
	}

	var remaining atomic.Int64
	remaining.Store(5)
	done := make(chan struct{})
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			if remaining.Add(-1) == 0 {
				close(done)
			}
			return transition.Result{State: "reviewed"}, nil
		},
	})

	runUntil(t, dispatcher(t, s, reg, pool(t, nil), 4), done)

	for _, j := range jobsIn(t, s) {
		if j.Lease != nil {
			t.Errorf("%s is still leased after the pool stopped: %+v", j.ID, j.Lease)
		}
	}
}

func jobsIn(t *testing.T, s store.Store) []store.Job {
	t.Helper()
	jobs, err := s.Jobs(context.Background())
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	return jobs
}

// usage is a usage endpoint under the test's control: what it answers can be
// changed while the pool is running, which is what the criteria about starting
// and not interrupting are actually about.
type usage struct {
	mu  sync.Mutex
	doc string
}

func (u *usage) set(doc string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.doc = doc
}

func (u *usage) fetch(context.Context) (io.ReadCloser, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return io.NopCloser(strings.NewReader(u.doc)), nil
}

const (
	usageOK          = `{"usage":{"rolling":{"status":"ok","percent":5,"resetsAt":"2126-01-01T00:00:00Z"}}}`
	usageApproaching = `{"usage":{"rolling":{"status":"ok","percent":95,"resetsAt":"2126-01-01T00:00:00Z"}}}`
	usageLimited     = `{"usage":{"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2126-01-01T00:00:00Z"}}}`
)

// reopens is the resetsAt the documents above carry, and the timestamp a
// deferred job must land on. Far enough away that no test's clock reaches it.
func reopens(t *testing.T) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, "2126-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// observer watches u, refreshing on every dispatch so a test can change the
// answer and see the pool follow it.
func observer(u *usage, threshold float64) *budget.Observer {
	return &budget.Observer{Fetch: u.fetch, MaxAge: time.Nanosecond, Threshold: threshold}
}

// queue puts n jobs in the store, all due. Called again with a larger n it adds
// the missing ones and returns them all: Ensure is idempotent, so a test can
// make one job due now and another later.
func queue(t *testing.T, s store.Store, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := range n {
		job, err := s.Ensure(context.Background(), store.KindReview, store.Subject{Type: store.SubjectPR, Number: i + 1}, "start", time.Now().Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
	}
	return ids
}

// until polls the store until cond holds, so a test asserts on what the pool
// did rather than on how long it took to do it.
func until(t *testing.T, s store.Store, id string, what string, cond func(store.Job) bool) store.Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		job, err := s.Job(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if cond(job) {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s did not %s; it is in state %q, attempts %d, due %s", id, id, what, job.State, job.Attempts, job.NextRunAt)
		}
		time.Sleep(time.Millisecond)
	}
}

// The acceptance criterion: being limited defers work until resetsAt, an
// absolute timestamp, rather than polling and suppressing. The job comes back
// on its own at the timestamp the provider gave, and an operator reading the
// queue sees that rather than a job that looks due now and never runs.
func TestALimitedBudgetDefersDueJobsToTheResetTimestamp(t *testing.T) {
	s := openStore(t)
	u := &usage{doc: usageLimited}

	var ran int32
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			atomicAdd(&ran)
			return transition.Result{State: "done"}, nil
		},
	})

	ids := queue(t, s, 2)
	d := dispatcher(t, s, reg, pool(t, nil), 1)
	d.Budget = observer(u, 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, id := range ids {
			until(t, s, id, "defer", func(j store.Job) bool { return j.NextRunAt.After(time.Now()) })
		}
	}()
	runUntil(t, d, done)

	for _, id := range ids {
		job, err := s.Job(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if want := reopens(t); !job.NextRunAt.Equal(want) {
			t.Fatalf("%s is due %s, want the window's own %s", id, job.NextRunAt, want)
		}
		if job.State != "start" {
			t.Fatalf("%s moved to %q; a deferral is a scheduling change and nothing else", id, job.State)
		}
		// A deferral is not an attempt at the work. Counting one would spend
		// the retry bound on the account being busy, and a long enough window
		// would park every job in the queue for a human to find.
		if job.Attempts != 0 {
			t.Fatalf("%s is at %d attempts; being rate limited is not an attempt", id, job.Attempts)
		}
	}
	if n := atomicLoad(&ran); n != 0 {
		t.Fatalf("%d transitions ran under a limited budget", n)
	}
}

// The acceptance criterion: approaching a limit stops new jobs starting without
// interrupting jobs in flight.
func TestApproachingALimitStopsNewJobsAndLeavesWorkInFlightAlone(t *testing.T) {
	s := openStore(t)
	u := &usage{doc: usageOK}

	var (
		started = make(chan string, 8)
		release = make(chan struct{})
		ran     int32
	)
	// Released however the test ends, so a failure reports itself rather than
	// leaving two workers parked inside a transition and timing the run out.
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(ctx context.Context, in transition.In) (transition.Result, error) {
			atomicAdd(&ran)
			started <- in.Job.ID
			<-release
			return transition.Result{State: "done"}, nil
		},
	})

	// Two workers, so that the one held back is a different worker from the one
	// in flight: the criterion is about the pool, not about a single worker
	// declining to loop.
	d := dispatcher(t, s, reg, pool(t, nil), 2)
	d.Budget = observer(u, 90)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.Run(ctx)
	}()

	// One job is in flight and blocked inside its transition.
	inFlight := queue(t, s, 1)[0]
	select {
	case id := <-started:
		if id != inFlight {
			t.Fatalf("%s started, want %s", id, inFlight)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("nothing started under an ok budget")
	}

	// The account now approaches its limit, and a second job becomes due. The
	// free worker must not start it.
	u.set(usageApproaching)
	held := queue(t, s, 2)[1]
	select {
	case id := <-started:
		t.Fatalf("%s started while the budget was approaching its limit", id)
	case <-time.After(500 * time.Millisecond):
	}

	// And the job already in flight is not interrupted by any of that: it
	// finishes and commits.
	unblock()
	until(t, s, inFlight, "finish", func(j store.Job) bool { return j.State == "done" })

	// The one that was held back is still due rather than deferred or parked:
	// approaching is a wait, and it resumes on its own when usage falls.
	job, err := s.Job(context.Background(), held)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "start" || job.NextRunAt.After(time.Now()) {
		t.Fatalf("%s is in state %q due %s, want it still due in its starting state", held, job.State, job.NextRunAt)
	}
	if job.Attempts != 0 {
		t.Fatalf("%s is at %d attempts; being held back is not an attempt", held, job.Attempts)
	}
	if n := atomicLoad(&ran); n != 1 {
		t.Fatalf("%d transitions ran, want only the one that was already in flight", n)
	}

	cancel()
	wg.Wait()
}

// An unconfigured budget is no admission control, which is the shape a
// deployment that has not set one up has and is safe: work runs into the
// provider's limits and they arrive as transient failures (ADR 0001 §12).
func TestNoBudgetIsNoAdmissionControl(t *testing.T) {
	s := openStore(t)
	var ran int32
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			atomicAdd(&ran)
			return transition.Result{State: "done"}, nil
		},
	})

	ids := queue(t, s, 1)
	d := dispatcher(t, s, reg, pool(t, nil), 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		until(t, s, ids[0], "finish", func(j store.Job) bool { return j.State == "done" })
	}()
	runUntil(t, d, done)

	if n := atomicLoad(&ran); n != 1 {
		t.Fatalf("%d transitions ran with no budget configured, want 1", n)
	}
}

// An observer with no maximum age asks the usage endpoint once per job
// dispatched. Refused at startup rather than on the first busy morning.
func TestADispatcherRefusesAnObserverWithNoMaxAge(t *testing.T) {
	s := openStore(t)
	d := dispatcher(t, s, transition.MustRegistry(), pool(t, nil), 1)
	d.Budget = &budget.Observer{Fetch: (&usage{doc: usageOK}).fetch}

	if err := d.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "maximum age") {
		t.Fatalf("Run: %v, want a refusal naming the maximum age", err)
	}
}

// atomicAdd and atomicLoad keep the counters above readable; the transitions
// that increment them run on several workers at once.
func atomicAdd(n *int32)        { atomic.AddInt32(n, 1) }
func atomicLoad(n *int32) int32 { return atomic.LoadInt32(n) }

// published stands in for the ntfy server, so that a test asserts on what
// reached the operator rather than on what was logged. The tests here run
// offline (AGENTS.md); notify.Notifier's Post field is the seam.
type published struct {
	mu   sync.Mutex
	sent []string
}

func (p *published) post(_ context.Context, title, _, body string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, title+"\n"+body)
	return nil
}

func (p *published) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sent...)
}

// silent is the assertion the happy path has to pass. It is called after the
// dispatcher has stopped, so there is nothing still in flight that could
// publish after it looked.
func (p *published) silent(t *testing.T) {
	t.Helper()
	if got := p.all(); len(got) != 0 {
		t.Fatalf("%d notifications, want none:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
}

// notifier wires a dispatcher to p.
func notifier(p *published) *notify.Notifier {
	return &notify.Notifier{URL: "https://ntfy.example/afk-agent", Post: p.post, TierAfter: 1}
}

// failing is a transition that never succeeds, which is how a test reaches the
// failure paths without a network or a model.
func failing(cause error) *transition.Registry {
	return transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{}, cause
		},
	})
}

// The first condition ADR 0001 §13 reserves the channel for: a failure that has
// come to rest and needs a human. With no backoff policy a failed job parks on
// its first attempt, which is what "waits for an operator" is in the store.
func TestAFailureThatParkedReachesTheOperator(t *testing.T) {
	s := openStore(t)
	ids := queue(t, s, 1)
	p := &published{}

	d := dispatcher(t, s, failing(errors.New("the build did not finish")), pool(t, nil), 1)
	d.Notify = notifier(p)

	done := make(chan struct{})
	go func() {
		defer close(done)
		until(t, s, ids[0], "park", func(j store.Job) bool { return j.Attempts > 0 && j.NextRunAt.IsZero() })
	}()
	runUntil(t, d, done)

	got := p.all()
	if len(got) != 1 {
		t.Fatalf("%d notifications for one parked job, want 1:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
	for _, want := range []string{ids[0], "the build did not finish"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the notification does not mention %q:\n%s", want, got[0])
		}
	}
}

// A failure the backoff rescheduled is retrying, and retrying is not yet
// anyone's problem. Notifying here would report every transient failure at the
// provider, which is the noise §13 exists to keep off the channel.
func TestAFailureThatIsStillRetryingDoesNotNotify(t *testing.T) {
	s := openStore(t)
	ids := queue(t, s, 1)
	p := &published{}

	d := dispatcher(t, s, failing(errors.New("a transient failure")), pool(t, nil), 1)
	d.Notify = notifier(p)
	d.Backoff = func(int) (time.Time, bool) { return time.Now().Add(time.Hour), true }

	done := make(chan struct{})
	go func() {
		defer close(done)
		until(t, s, ids[0], "be rescheduled", func(j store.Job) bool {
			return j.Attempts > 0 && j.NextRunAt.After(time.Now())
		})
	}()
	runUntil(t, d, done)

	p.silent(t)
}

// The acceptance criterion: the happy path is silent. A successful run produces
// no notification of any kind.
func TestTheHappyPathIsSilent(t *testing.T) {
	s := openStore(t)
	ids := queue(t, s, 3)
	p := &published{}

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{State: "done"}, nil
		},
	})
	d := dispatcher(t, s, reg, pool(t, nil), 2)
	d.Notify = notifier(p)
	d.Budget = observer(&usage{doc: usageOK}, 80)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, id := range ids {
			until(t, s, id, "finish", func(j store.Job) bool { return j.State == "done" })
		}
	}()
	runUntil(t, d, done)

	p.silent(t)
}

// The acceptance criterion: a hand-back does not notify. It is a state, not an
// interrupt.
//
// A transition that comes to rest without failing is what a hand-back is in the
// store - the job keeps its state and nothing is scheduled - so the condition
// cannot be "the job parked". It is "the job parked because something failed".
func TestAJobThatCameToRestWithoutFailingDoesNotNotify(t *testing.T) {
	s := openStore(t)
	ids := queue(t, s, 1)
	p := &published{}

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			// No RunAt: the job rests where it is, waiting for a human.
			return transition.Result{State: "handed-back"}, nil
		},
	})
	d := dispatcher(t, s, reg, pool(t, nil), 1)
	d.Notify = notifier(p)

	done := make(chan struct{})
	go func() {
		defer close(done)
		until(t, s, ids[0], "hand back", func(j store.Job) bool { return j.State == "handed-back" })
	}()
	runUntil(t, d, done)

	p.silent(t)
}

// A job in a state no transition leads out of is parked by the dispatcher
// rather than by the runner, and it is as much the operator's to look at: a
// terminal state left scheduled, or a state written by a binary that knew a
// transition this one does not.
func TestAJobNothingCanMoveReachesTheOperator(t *testing.T) {
	s := openStore(t)
	job := storetest.Seed(t, s, store.KindReview, 12, "a-state-from-a-newer-binary")
	p := &published{}

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{State: "reviewed"}, nil
		},
	})
	d := dispatcher(t, s, reg, pool(t, nil), 1)
	d.Notify = notifier(p)

	done := make(chan struct{})
	go func() {
		defer close(done)
		until(t, s, job.ID, "park", func(j store.Job) bool { return j.NextRunAt.IsZero() && j.Lease == nil })
	}()
	runUntil(t, d, done)

	got := p.all()
	if len(got) != 1 {
		t.Fatalf("%d notifications for one job nothing can move, want 1:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
	if !strings.Contains(got[0], "a-state-from-a-newer-binary") {
		t.Errorf("the notification does not name the state it is stuck in:\n%s", got[0])
	}
}

// The second condition, and the acceptance criterion that repeated occurrences
// do not re-notify on every poll. A limited window is re-observed by every
// worker on every pass for as long as it lasts; the operator hears about it
// once.
func TestAnExhaustedBudgetReachesTheOperatorOnce(t *testing.T) {
	s := openStore(t)
	ids := queue(t, s, 3)
	p := &published{}

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{State: "done"}, nil
		},
	})
	d := dispatcher(t, s, reg, pool(t, nil), 2)
	d.Notify = notifier(p)
	d.Budget = observer(&usage{doc: usageLimited}, 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, id := range ids {
			until(t, s, id, "defer", func(j store.Job) bool { return j.NextRunAt.After(time.Now()) })
		}
	}()
	runUntil(t, d, done)

	got := p.all()
	if len(got) != 1 {
		t.Fatalf("%d notifications for one limited window observed by two workers across three jobs, want 1:\n%s",
			len(got), strings.Join(got, "\n---\n"))
	}
	for _, want := range []string{"monthly", "2126-01-01T00:00:00Z"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the notification does not mention %q:\n%s", want, got[0])
		}
	}
}

// Approaching a limit stops new jobs starting and says nothing. The queue
// standing down while a rolling window drains is admission control working, and
// a notification for it would train the operator to ignore the channel that
// carries the other two.
func TestApproachingALimitIsSilent(t *testing.T) {
	s := openStore(t)
	queue(t, s, 1)
	p := &published{}

	var ran int32
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			atomicAdd(&ran)
			return transition.Result{State: "done"}, nil
		},
	})
	d := dispatcher(t, s, reg, pool(t, nil), 1)
	d.Notify = notifier(p)
	d.Budget = observer(&usage{doc: usageApproaching}, 80)

	// The store does not change when admission waits - the job is given back
	// untouched - so the log line is what says the pool has seen it.
	done := make(chan struct{})
	var once sync.Once
	d.Log = func(msg string) {
		t.Log(msg)
		if strings.Contains(msg, "wait") {
			once.Do(func() { close(done) })
		}
	}
	runUntil(t, d, done)

	p.silent(t)
	if n := atomicLoad(&ran); n != 0 {
		t.Fatalf("%d transitions ran while approaching a limit", n)
	}
}

// No notifier is no notification, which is the shape of an unconfigured channel
// and must not be a dispatcher that panics on the first park.
func TestNoNotifierIsNoNotification(t *testing.T) {
	s := openStore(t)
	ids := queue(t, s, 1)

	d := dispatcher(t, s, failing(errors.New("boom")), pool(t, nil), 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		until(t, s, ids[0], "park", func(j store.Job) bool { return j.Attempts > 0 && j.NextRunAt.IsZero() })
	}()
	runUntil(t, d, done)
}

// The one case that reads as a hand-back and notifies anyway: a transition that
// parked deliberately and whose outward effect then failed. The state moved and
// the job is resting, but the comment that was to tell the human never posted -
// so the tracker says nothing, and this channel is all that is left.
//
// It is the boundary of the condition above rather than a second condition:
// TestAJobThatCameToRestWithoutFailingDoesNotNotify is the same park with an
// effect that worked, and it is silent.
func TestAHandBackWhoseEffectFailedReachesTheOperator(t *testing.T) {
	s := openStore(t)
	ids := queue(t, s, 1)
	p := &published{}

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			// No RunAt: the job rests where it is, waiting for a human who is
			// told by the effect below - which does not happen.
			return transition.Result{
				State: "handed-back",
				Effects: []transition.Effect{{Key: "say-so", Do: func(context.Context) error {
					return errors.New("posting the hand-back comment: 502 Bad Gateway")
				}}},
			}, nil
		},
	})
	d := dispatcher(t, s, reg, pool(t, nil), 1)
	d.Notify = notifier(p)

	done := make(chan struct{})
	go func() {
		defer close(done)
		until(t, s, ids[0], "hand back", func(j store.Job) bool { return j.State == "handed-back" })
	}()
	runUntil(t, d, done)

	got := p.all()
	if len(got) != 1 {
		t.Fatalf("%d notifications for a hand-back nobody was told about, want 1:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
	if !strings.Contains(got[0], "502 Bad Gateway") {
		t.Errorf("the notification does not say what failed:\n%s", got[0])
	}
}

// tier is a job kind whose model tier behaves as script says, one letter per
// run of the model: x the tier is exhausted and the job defers, s a candidate
// failed transiently and the job stays for the next, g a model answered and
// the job moves past it - to a state that leads back to the model, the way a
// gate that failed does - p the job parks where it is, for runTier to
// reschedule the way an operator would, and d a model answered and the job is
// done. X is x with a tier wait longer than the test, so the job rests
// deferred until runTierAcrossRestarts reschedules it.
//
// It has the shape review and implement have: a state the model runs from, a
// state an exhausted tier waits in, and a resume from one to the other that
// starts the tier again (#76).
func tier(script string) *transition.Registry {
	var (
		mu   sync.Mutex
		next int
	)
	now := func() time.Time { return time.Now().Add(-time.Second) }
	step := func(state string) transition.Transition {
		return transition.Transition{
			Name: state, Kind: store.KindReview, From: state,
			Run: func(context.Context, transition.In) (transition.Result, error) {
				return transition.Result{State: "running", RunAt: now()}, nil
			},
		}
	}
	return transition.MustRegistry(
		step("start"), step("deferred"), step("gated"),
		transition.Transition{
			Name: "run", Kind: store.KindReview, From: "running",
			Run: func(context.Context, transition.In) (transition.Result, error) {
				mu.Lock()
				defer mu.Unlock()
				c := byte('d')
				if next < len(script) {
					c = script[next]
				}
				next++
				switch c {
				case 'x':
					return transition.Result{State: "deferred", RunAt: now(), Exhausted: errors.New("tier exhausted: all 2 enrolled models tried")}, nil
				case 'X':
					return transition.Result{State: "deferred", RunAt: time.Now().Add(time.Hour), Exhausted: errors.New("tier exhausted: all 2 enrolled models tried")}, nil
				case 's':
					return transition.Result{State: "running", RunAt: now()}, nil
				case 'g':
					return transition.Result{State: "gated", RunAt: now()}, nil
				case 'p':
					return transition.Result{State: "running"}, nil
				}
				return transition.Result{State: "done"}, nil
			},
		},
	)
}

// runTier runs one job through script with a notifier that tells an exhausted
// tier after two exhaustions, and returns what was published. A job that
// parks is rescheduled where it rests, as an operator's requeue would.
func runTier(t *testing.T, script string) []string {
	t.Helper()
	s := openStore(t)
	ids := queue(t, s, 1)
	p := &published{}

	d := dispatcher(t, s, tier(script), pool(t, nil), 1)
	d.Notify = notifier(p)
	d.Notify.TierAfter = 2

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			job := until(t, s, ids[0], "finish or park", func(j store.Job) bool {
				return j.State == "done" || (j.NextRunAt.IsZero() && j.Lease == nil)
			})
			if job.State == "done" {
				return
			}
			requeue(t, s, job)
		}
	}()
	runUntil(t, d, done)
	return p.all()
}

// runTierAcrossRestarts is runTier with a restart of the pool wherever the
// job waits out a tier (X): each process has a notifier of its own, as a
// restarted `afk work` does, and the store is all they share. The job is
// rescheduled between them, as the tier wait running out would, and after a
// park, as runTier does. It returns what each process published.
func runTierAcrossRestarts(t *testing.T, script string) [][]string {
	t.Helper()
	s := openStore(t)
	ids := queue(t, s, 1)
	reg := tier(script)

	var told [][]string
	for {
		p := &published{}
		d := dispatcher(t, s, reg, pool(t, nil), 1)
		d.Notify = notifier(p)
		d.Notify.TierAfter = 2

		var job store.Job
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				job = until(t, s, ids[0], "finish, park or wait out the tier", func(j store.Job) bool {
					return j.State == "done" || (j.Lease == nil && (j.NextRunAt.IsZero() || j.NextRunAt.After(time.Now().Add(time.Minute))))
				})
				if !job.NextRunAt.IsZero() || job.State == "done" {
					return
				}
				requeue(t, s, job)
			}
		}()
		runUntil(t, d, done)
		told = append(told, p.all())
		if job.State == "done" {
			return told
		}
		requeue(t, s, job)
	}
}

// requeue schedules a parked job where it rests.
func requeue(t *testing.T, s store.Store, job store.Job) {
	t.Helper()
	ctx := context.Background()
	if _, ok, err := s.Acquire(ctx, job.ID, "operator", time.Now(), time.Minute); err != nil || !ok {
		t.Fatalf("acquire %s to requeue it: ok %v, %v", job.ID, ok, err)
	}
	err := s.Commit(ctx, store.Commit{
		JobID: job.ID, Holder: "operator",
		State: job.State, Attempts: job.Attempts, Stays: job.Stays,
		NextRunAt: time.Now().Add(-time.Second), Release: true,
	})
	if err != nil {
		t.Fatalf("requeue %s: %v", job.ID, err)
	}
}

// The acceptance criterion of #76: a tier that stays exhausted across resumes
// reaches the operator, and once. Each resume clears the stays and starts the
// tier again, so the episode is what spans them; a candidate failing on the
// way is inside it rather than an end to it.
func TestATierThatStaysExhaustedReachesTheOperatorOnce(t *testing.T) {
	got := runTier(t, "xsxxxsxx")
	if len(got) != 1 {
		t.Fatalf("%d notifications for one episode of an exhausted tier, want 1:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
	for _, want := range []string{"review-pr-1", "all 2 enrolled models tried"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the notification does not mention %q:\n%s", want, got[0])
		}
	}
}

// A tier that recovers on resume is a bad few minutes at a provider, and
// ADR 0001 §10 says that must not generate work for the operator.
func TestATierThatRecoversOnResumeIsSilent(t *testing.T) {
	if got := runTier(t, "xsd"); len(got) != 0 {
		t.Fatalf("%d notifications for a tier that came back, want none:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
}

// An episode ends when the job gets past the model. Exhaustions either side of
// that are two episodes, neither long enough alone to be told - and a tier that
// runs out again for long enough afterwards is told again.
func TestAnEpisodeEndsWhenTheJobGetsPastTheModel(t *testing.T) {
	if got := runTier(t, "xgxgxd"); len(got) != 0 {
		t.Fatalf("%d notifications for three short episodes, want none:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
	if got := runTier(t, "xxgxxd"); len(got) != 2 {
		t.Fatalf("%d notifications for two long episodes, want 2:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
}

// A park ends an episode: the job is the operator's now, and what they do
// with it is not the tier's. Exhaustions after a requeue are a new episode,
// counted from nothing and told again - rather than added to one that was
// already told, and so never told at all.
func TestAParkEndsAnEpisode(t *testing.T) {
	if got := runTier(t, "xpxd"); len(got) != 0 {
		t.Fatalf("%d notifications for two short episodes either side of a park, want none:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
	if got := runTier(t, "xxpxxd"); len(got) != 2 {
		t.Fatalf("%d notifications for two long episodes either side of a park, want 2:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
}

// The acceptance criterion of #91: an episode is kept in the store, so a
// restart carries on counting it. A tier that ran out once before the restart
// and once after is told by the process that saw the second, where a count
// kept in memory would start again and a pool that restarts more often than
// the tier recovers would never tell anyone.
func TestAnEpisodeSurvivesARestart(t *testing.T) {
	got := runTierAcrossRestarts(t, "XXd")
	if n := len(got); n != 3 {
		t.Fatalf("%d processes, want 3", n)
	}
	if len(got[0]) != 0 || len(got[1]) != 1 || len(got[2]) != 0 {
		t.Fatalf("notifications by process = %d, %d, %d; want 0, 1, 0: the second exhaustion is the episode's second, whichever process sees it", len(got[0]), len(got[1]), len(got[2]))
	}
}

// A restart carries having told as well as the count: an episode already told
// is not told again by the next process to see its tier run out, so a pool in
// a crash loop tells an episode once rather than once per restart.
func TestAToldEpisodeIsNotToldAgainAfterARestart(t *testing.T) {
	got := runTierAcrossRestarts(t, "xXXd")
	if n := len(got); n != 3 {
		t.Fatalf("%d processes, want 3", n)
	}
	if len(got[0]) != 1 || len(got[1]) != 0 || len(got[2]) != 0 {
		t.Fatalf("notifications by process = %d, %d, %d; want 1, 0, 0", len(got[0]), len(got[1]), len(got[2]))
	}
}

// A park still ends an episode that a restart carried over: exhaustions after
// the requeue are counted from nothing.
func TestAParkEndsAnEpisodeCarriedOverARestart(t *testing.T) {
	got := runTierAcrossRestarts(t, "XpXd")
	var n int
	for _, told := range got {
		n += len(told)
	}
	if n != 0 {
		t.Fatalf("%d notifications for two short episodes either side of a park, want none: %q", n, got)
	}
}

// A notifier with no count is a half-made channel, refused at startup rather
// than read as "tell on the zeroth exhaustion".
func TestADispatcherRefusesANotifierWithNoExhaustionCount(t *testing.T) {
	s := openStore(t)
	d := dispatcher(t, s, transition.MustRegistry(), pool(t, nil), 1)
	d.Notify = &notify.Notifier{URL: "https://ntfy.example/afk-agent", Post: (&published{}).post}

	err := d.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exhaust") {
		t.Fatalf("Run = %v, want a refusal naming the missing count", err)
	}
}

// Intake runs on every poll, beside the workers rather than inside one.
func TestIntakeRunsOnEveryPoll(t *testing.T) {
	s := openStore(t)
	d := dispatcher(t, s, transition.MustRegistry(), pool(t, nil), 1)

	var calls int32
	done := make(chan struct{})
	d.Intake = func(context.Context) error {
		if atomic.AddInt32(&calls, 1) == 3 {
			close(done)
		}
		return nil
	}
	runUntil(t, d, done)
}

// A tracker that cannot be read is a log line and another try, not a pool that
// stops.
func TestAnIntakeThatFailsIsLoggedAndTriedAgain(t *testing.T) {
	s := openStore(t)
	d := dispatcher(t, s, transition.MustRegistry(), pool(t, nil), 1)

	var (
		mu    sync.Mutex
		lines []string
		calls int32
	)
	d.Log = func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, msg)
	}
	done := make(chan struct{})
	d.Intake = func(context.Context) error {
		if atomic.AddInt32(&calls, 1) == 2 {
			close(done)
		}
		return errors.New("listing open pull requests: 502 Bad Gateway")
	}
	runUntil(t, d, done)

	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 || !strings.Contains(lines[0], "intake: listing open pull requests: 502 Bad Gateway") {
		t.Errorf("log = %q, want the intake failure", lines)
	}
}

// What intake makes due is what the workers run.
func TestAJobIntakeMadeDueIsRun(t *testing.T) {
	s := openStore(t)
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{State: "reviewed"}, nil
		},
	})
	d := dispatcher(t, s, reg, pool(t, nil), 1)
	ensured := make(chan struct{})
	var once sync.Once
	d.Intake = func(ctx context.Context) error {
		_, err := s.Ensure(ctx, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", time.Now())
		once.Do(func() { close(ensured) })
		return err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The job does not exist until intake has run once.
		<-ensured
		until(t, s, "review-pr-12", "get reviewed", func(j store.Job) bool { return j.State == "reviewed" })
	}()
	runUntil(t, d, done)
}
