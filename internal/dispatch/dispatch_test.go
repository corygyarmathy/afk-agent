package dispatch_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
	"github.com/corygyarmathy/afk-agent/internal/dispatch"
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
	track := func(now, peak *atomic.Int64) func() {
		n := now.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		// Long enough that anything running alongside it is still running.
		time.Sleep(20 * time.Millisecond)
		now.Add(-1)
		return finish
	}

	reg := transition.MustRegistry(
		transition.Transition{
			Name: "build", Kind: store.KindImplement, From: "start",
			Tokens: []string{"heavy-build"},
			Run: func(context.Context, transition.In) (transition.Result, error) {
				defer track(&heavyNow, &heavyPeak)()
				return transition.Result{State: "built"}, nil
			},
		},
		transition.Transition{
			Name: "review", Kind: store.KindReview, From: "start",
			Run: func(context.Context, transition.In) (transition.Result, error) {
				defer track(&lightNow, &lightPeak)()
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
