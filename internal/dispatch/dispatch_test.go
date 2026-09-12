package dispatch_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
