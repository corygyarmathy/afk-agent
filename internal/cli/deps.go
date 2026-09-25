package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// reviewDeps builds what the review's transitions reach, from the parameters and
// the command's tracker, which it shares with intake (ADR 0005 §5).
//
// A variable for the reason catalogue is: the tests in this package put fixture
// transitions in front of the command surface, and those reach nothing.
var reviewDeps = func(ctx context.Context, p params, st store.Store, tr *tracker) (*review.Deps, error) {
	if tr == nil {
		return nil, usagef("a review needs --repo (or set AFK_REPO)")
	}
	m, err := p.model()
	if err != nil {
		return nil, err
	}
	stateDir, resolve, err := resolver(p, m)
	if err != nil {
		return nil, err
	}
	login, err := tr.Login(ctx)
	if err != nil {
		return nil, err
	}
	return &review.Deps{
		Tracker:  tr.client,
		Model:    opencode.Command{Path: m.opencode},
		Store:    st,
		Checkout: review.Git{Remote: cloneURL(tr)}.Checkout,
		Resolve:  resolve,
		Bound:    m.attempts,
		TierWait: m.tierWait,
		Login:    login,
		StateDir: stateDir,
	}, nil
}

// implementDeps builds what the implement kind's transitions reach, from the
// parameters and the command's tracker.
//
// A variable for the reason reviewDeps is.
var implementDeps = func(ctx context.Context, p params, st store.Store, tr *tracker) (*implement.Deps, error) {
	if tr == nil {
		return nil, usagef("implement needs --repo (or set AFK_REPO)")
	}
	ip, err := p.implement()
	if err != nil {
		return nil, err
	}
	m, err := p.implementModel()
	if err != nil {
		return nil, err
	}
	stateDir, resolve, err := resolver(p, m)
	if err != nil {
		return nil, err
	}
	login, err := tr.Login(ctx)
	if err != nil {
		return nil, err
	}
	return &implement.Deps{
		Tracker:       tr.client,
		Model:         opencode.Command{Path: m.opencode},
		Login:         login,
		BranchPrefix:  ip.branchPrefix,
		Remote:        cloneURL(tr),
		Resolve:       resolve,
		Bound:         m.attempts,
		TierWait:      m.tierWait,
		Gate:          ip.gate,
		Attempts:      ip.attempts,
		HandBackLabel: ip.handBackLabel,
		Denylist:      ip.denylist,
		CIWait:        ip.ciWait,
		CICeiling:     ip.ciCeiling,
		CIRounds:      ip.ciRounds,
		// The installation token, minted and cached by the App the tracker
		// uses: the push is the agent on the tracker like any other request
		// (ADR 0005).
		Token:    tr.app.Token,
		Store:    st,
		StateDir: stateDir,
	}, nil
}

// cloneURL is where the tracker's repository is fetched from. No credentials
// travel with it: reading a public repository needs none.
func cloneURL(tr *tracker) string {
	return "https://github.com/" + tr.client.Repo + ".git"
}

// resolver is the state directory, and the candidate list for one job kind's
// model choice as of each call to it.
func resolver(p params, m modelParams) (string, func(context.Context) (model.Candidates, error), error) {
	observer, err := p.budget()
	if err != nil {
		return "", nil, err
	}
	path, err := p.storePath()
	if err != nil {
		return "", nil, err
	}

	// Beside the store and not in it (ADR 0001 §5): the catalogue is
	// re-derivable from models.dev, and workspaces and replies waiting to be
	// posted are re-derivable by running the model again.
	stateDir := filepath.Dir(path)
	source := &model.Source{Path: filepath.Join(stateDir, "models.json"), MaxAge: m.catalogueAge}
	reqs := model.Requirements{Tier: m.tier, Capabilities: m.needs}

	// Read on every resolution rather than once: the enrolment is a human's
	// file and the catalogue has a cache of its own, so an edit to either is
	// seen by the next run without a restart.
	resolve := func(ctx context.Context) (model.Candidates, error) {
		enrol, err := readEnrolment(m.enrolment)
		if err != nil {
			return nil, err
		}
		cat, _, err := source.Load(ctx)
		if err != nil {
			return nil, err
		}
		var budget model.Budget
		if observer != nil {
			// A budget that cannot be read is not one that is spent, and
			// Observe returns the last good observation alongside the error
			// (ADR 0001 §12).
			state, _ := observer.Observe(ctx)
			budget = state.Resolver()
		}
		return model.Resolve(reqs, cat, enrol, budget)
	}
	return stateDir, resolve, nil
}

// kindDeps builds the dependencies of one job kind, and leaves the rest nil:
// a hand-run needs only its own kind's parameters.
func kindDeps(ctx context.Context, kind store.Kind, p params, st store.Store, tr *tracker) (*deps, error) {
	var (
		d   deps
		err error
	)
	switch kind {
	case store.KindReview:
		d.review, err = reviewDeps(ctx, p, st, tr)
	case store.KindImplement:
		d.implement, err = implementDeps(ctx, p, st, tr)
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func readEnrolment(path string) (*model.Enrolment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("--enrolment: %w", err)
	}
	defer f.Close()
	e, err := model.DecodeEnrolment(f)
	if err != nil {
		return nil, fmt.Errorf("--enrolment: %s: %w", path, err)
	}
	return e, nil
}
