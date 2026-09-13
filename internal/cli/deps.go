package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// reviewDeps builds what the review's transitions reach, from the parameters.
//
// A variable for the reason catalogue is: the tests in this package put fixture
// transitions in front of the command surface, and those reach nothing.
var reviewDeps = func(ctx context.Context, p params, st store.Store) (*review.Deps, error) {
	client, err := p.tracker()
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, usagef("a review needs --repo (or set AFK_REPO)")
	}
	m, err := p.model()
	if err != nil {
		return nil, err
	}
	observer, err := p.budget()
	if err != nil {
		return nil, err
	}
	path, err := p.storePath()
	if err != nil {
		return nil, err
	}
	login, err := client.Login(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the agent's own login: %w", err)
	}

	// Beside the store and not in it (ADR 0001 §5): the catalogue is
	// re-derivable from models.dev, and workspaces and replies waiting to be
	// posted are re-derivable by running the model again.
	stateDir := filepath.Dir(path)
	source := &model.Source{Path: filepath.Join(stateDir, "models.json"), MaxAge: m.catalogueAge}
	reqs := model.Requirements{Tier: m.tier, Capabilities: m.needs}

	// Read on every resolution rather than once: the enrolment is a human's
	// file and the catalogue has a cache of its own, so an edit to either is
	// seen by the next review without a restart.
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

	return &review.Deps{
		Tracker:  client,
		Model:    opencode.Command{Path: m.opencode},
		Store:    st,
		Checkout: review.Git{Remote: "https://github.com/" + client.Repo + ".git"}.Checkout,
		Resolve:  resolve,
		Bound:    m.attempts,
		TierWait: m.tierWait,
		Login:    login,
		StateDir: stateDir,
	}, nil
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
