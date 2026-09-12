package transition_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/transition"
)

func TestNewPoolRejectsACapacityNothingCouldHold(t *testing.T) {
	for _, capacity := range []map[string]int{
		{"heavy-build": 0},
		{"heavy-build": -1},
		{"": 1},
	} {
		if _, err := transition.NewPool(capacity); err == nil {
			t.Errorf("NewPool(%v) = nil error; want a refusal", capacity)
		}
	}
}

func TestATokenAdmitsOnlyItsCapacity(t *testing.T) {
	ctx := context.Background()
	pool, err := transition.NewPool(map[string]int{"heavy-build": 2})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	var held, peak atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := pool.Acquire(ctx, []string{"heavy-build"})
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			defer release()
			n := held.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			held.Add(-1)
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > 2 {
		t.Errorf("%d holders at once; the capacity is 2", got)
	}
	if got := peak.Load(); got < 2 {
		t.Errorf("%d holders at once; want the capacity to actually be used", got)
	}
}

// A mistyped token must be a failure, not an unlimited permit: the transition
// that declared it meant to be limited, and would silently not be.
func TestAnUnknownTokenIsRefused(t *testing.T) {
	pool, err := transition.NewPool(map[string]int{"heavy-build": 1})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	if _, err := pool.Acquire(context.Background(), []string{"heavey-build"}); err == nil {
		t.Error("Acquire of an unknown token = nil error; want a refusal")
	} else if !strings.Contains(err.Error(), "no configured capacity") {
		t.Errorf("Acquire error = %q, want it to say the token has no capacity", err)
	}
	if err := pool.Known([]string{"heavy-build", "nonesuch"}); err == nil {
		t.Error("Known = nil error; want it to name the unconfigured token")
	}
}

// An unknown token in the middle of a list must not leave the ones before it
// held: a pool that leaks a permit on a typo stalls the agent rather than
// failing it.
func TestARefusedAcquireHoldsNothing(t *testing.T) {
	ctx := context.Background()
	pool, err := transition.NewPool(map[string]int{"a": 1})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	if _, err := pool.Acquire(ctx, []string{"a", "z"}); err == nil {
		t.Fatal("Acquire = nil error; want a refusal for z")
	}
	release, err := pool.Acquire(ctx, []string{"a"})
	if err != nil {
		t.Fatalf("the permit for a was leaked: %v", err)
	}
	release()
}

func TestAcquireGivesUpWhenTheContextIsDone(t *testing.T) {
	pool, err := transition.NewPool(map[string]int{"heavy-build": 1})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	release, err := pool.Acquire(context.Background(), []string{"heavy-build"})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := pool.Acquire(ctx, []string{"heavy-build"}); err == nil {
		t.Error("Acquire = nil error; want it to give up when the context is done")
	}
}

// Tokens are taken in name order, so two transitions declaring the same pair in
// opposite orders cannot each hold one and wait on the other. A deadlock in the
// dispatcher looks exactly like an idle agent, which is the worst way for this
// to fail.
func TestTokensAreTakenInAConsistentOrder(t *testing.T) {
	ctx := context.Background()
	pool, err := transition.NewPool(map[string]int{"a": 1, "b": 1})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for _, names := range [][]string{{"a", "b"}, {"b", "a"}} {
			for range 20 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					release, err := pool.Acquire(ctx, names)
					if err != nil {
						t.Errorf("Acquire: %v", err)
						return
					}
					release()
				}()
			}
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlocked: two token orders held each other")
	}
}

func TestNoTokensIsNotAWait(t *testing.T) {
	pool, err := transition.NewPool(nil)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	release, err := pool.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()
}
