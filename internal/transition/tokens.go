package transition

import (
	"context"
	"fmt"
	"sort"
)

// HeavyBuild is the resource token a transition that builds or tests holds
// (CONTEXT.md: resource token). The name is vocabulary, and the capacity is a
// parameter: `--token heavy-build=<n>`.
const HeavyBuild = "heavy-build"

// Pool issues resource tokens: named, capacity-limited permits a transition
// must hold to run (CONTEXT.md: resource token).
//
// A token expresses a host constraint rather than a logical one. Most
// transitions are network-bound and declare none; the few that build or test
// can exhaust a host that has no swap, and hold a token that says so
// (ADR 0001 §8). It is a separate limit from worker parallelism, not a
// re-spelling of it: eight workers with one heavy-build permit is a sensible
// configuration, and a single global concurrency number cannot express it.
//
// Capacities are configuration, so the pool is constructed with them rather
// than knowing any (AGENTS.md: no hard-coded parameters). A pool with no
// capacities is valid and serves every transition that declares no tokens.
type Pool struct {
	permits map[string]chan struct{}
}

// NewPool builds a pool from a name-to-capacity map.
func NewPool(capacity map[string]int) (*Pool, error) {
	p := &Pool{permits: make(map[string]chan struct{}, len(capacity))}
	for name, n := range capacity {
		if name == "" {
			return nil, fmt.Errorf("resource token with no name")
		}
		if n < 1 {
			return nil, fmt.Errorf("resource token %q has capacity %d: a token nothing can ever hold blocks its transitions forever", name, n)
		}
		p.permits[name] = make(chan struct{}, n)
	}
	return p, nil
}

// Acquire holds every named token, blocking until all of them are free or ctx
// is done, and returns the function that gives them back.
//
// Tokens are taken in name order. Two transitions asking for the heavy-build
// and the network token in opposite orders would otherwise be able to hold one
// each and wait on the other, and a deadlock in the dispatcher is a stall that
// looks exactly like an idle agent.
//
// An unknown name is an error rather than an unlimited permit: a mistyped token
// is a transition that was meant to be limited and would silently not be.
func (p *Pool) Acquire(ctx context.Context, names []string) (release func(), err error) {
	if len(names) == 0 {
		return func() {}, nil
	}

	ordered := append([]string(nil), names...)
	sort.Strings(ordered)

	held := make([]chan struct{}, 0, len(ordered))
	giveBack := func() {
		for _, c := range held {
			<-c
		}
	}
	for _, name := range ordered {
		c, ok := p.permits[name]
		if !ok {
			giveBack()
			return nil, fmt.Errorf("unknown resource token %q: it has no configured capacity", name)
		}
		select {
		case c <- struct{}{}:
			held = append(held, c)
		case <-ctx.Done():
			giveBack()
			return nil, fmt.Errorf("waiting for resource token %q: %w", name, ctx.Err())
		}
	}
	return giveBack, nil
}

// Known reports whether every named token has a configured capacity. The
// dispatcher checks its registry against its pool at startup, so a transition
// declaring a token nobody configured is a refusal to start rather than a job
// that fails the first time it is picked up.
func (p *Pool) Known(names []string) error {
	for _, name := range names {
		if _, ok := p.permits[name]; !ok {
			return fmt.Errorf("unknown resource token %q: it has no configured capacity", name)
		}
	}
	return nil
}
