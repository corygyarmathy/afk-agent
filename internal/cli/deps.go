package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
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
	ep, err := p.effects()
	if err != nil {
		return nil, err
	}
	rp, err := p.review()
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
		Tracker:       tr.client,
		Model:         opencode.Command{Path: m.opencode, Timeout: m.timeout},
		Store:         st,
		Checkout:      review.Git{Remote: remote(tr, stateDir)}.Checkout,
		Resolve:       resolve,
		Bound:         m.attempts,
		TierWait:      m.tierWait,
		Rounds:        ep.rounds,
		HandBackLabel: ep.handBackLabel,
		Floor:         rp.floor,
		FoldCut:       rp.foldCut,
		Repo:          tr.client.Repo,
		Login:         login,
		StateDir:      stateDir,
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
	ep, err := p.effects()
	if err != nil {
		return nil, err
	}
	stateDir, resolve, err := resolver(p, m)
	if err != nil {
		return nil, err
	}
	lease, err := p.leaseTTL()
	if err != nil {
		return nil, err
	}
	login, err := tr.Login(ctx)
	if err != nil {
		return nil, err
	}
	return &implement.Deps{
		Tracker:         tr.client,
		Model:           opencode.Command{Path: m.opencode, Timeout: m.timeout},
		Login:           login,
		BranchPrefix:    ip.branchPrefix,
		Remote:          remote(tr, stateDir),
		Resolve:         resolve,
		Bound:           m.attempts,
		TierWait:        m.tierWait,
		Rounds:          ep.rounds,
		Gate:            ip.gate,
		Attempts:        ip.attempts,
		HandBackLabel:   ep.handBackLabel,
		HandOffLabel:    ip.handOffLabel,
		Denylist:        ip.denylist,
		Sensitive:       ip.sensitive,
		CIWait:          ip.ciWait,
		CICeiling:       ip.ciCeiling,
		CIFixes:         ip.ciFixes,
		SizeSignal:      ip.sizeSignal,
		Repo:            tr.client.Repo,
		ReviewProcedure: ip.reviewProcedure,
		Store:           st,
		// A holder of its own: it leases the review job, never this one.
		AskReview: implement.ReviewAsker(transition.Armer{Store: st, Holder: holder() + "/ask-review", LeaseTTL: lease}),
		StateDir:  stateDir,
	}, nil
}

// reviseDeps builds what the revise kind's transitions reach, from the
// parameters and the command's tracker.
//
// A variable for the reason reviewDeps is.
var reviseDeps = func(ctx context.Context, p params, st store.Store, tr *tracker) (*revise.Deps, error) {
	if tr == nil {
		return nil, usagef("revise needs --repo (or set AFK_REPO)")
	}
	// The revision's gate and its bound, the paths it may not push, and the
	// tier it runs on are the implement kind's: one local gate, one denylist
	// and one tier serve both.
	ip, err := p.implement()
	if err != nil {
		return nil, err
	}
	m, err := p.implementModel()
	if err != nil {
		return nil, err
	}
	ep, err := p.effects()
	if err != nil {
		return nil, err
	}
	handOff, err := required(p.handOffLabel, "hand-off-label", "AFK_HAND_OFF_LABEL")
	if err != nil {
		return nil, err
	}
	rp, err := p.replayBound()
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
	return &revise.Deps{
		Tracker:       tr.client,
		Model:         opencode.Command{Path: m.opencode, Timeout: m.timeout},
		Store:         st,
		Login:         login,
		Repo:          tr.client.Repo,
		Remote:        remote(tr, stateDir),
		Resolve:       resolve,
		Bound:         m.attempts,
		TierWait:      m.tierWait,
		Rounds:        ep.rounds,
		Gate:          ip.gate,
		Attempts:      ip.attempts,
		Denylist:      ip.denylist,
		Replays:       rp,
		HandOffLabel:  handOff,
		HandBackLabel: ep.handBackLabel,
		// Beside the store, as every other kind's state directory is.
		StateDir: stateDir,
	}, nil
}

// remote is the tracker's repository as git reaches it, with the installation
// token minted and cached by the App the tracker uses: a clone, a fetch or a
// push is the agent on the tracker like any other request (ADR 0005), and a
// private repository is read as a public one is. It is never reached from a
// job's workspace under stateDir, whose configuration is the model's to write.
func remote(tr *tracker, stateDir string) git.Remote {
	return git.Remote{
		URL: "https://github.com/" + tr.client.Repo + ".git", Token: tr.app.Token, Refused: tr.app.Refused,
		Untrusted: []string{work.Workspace{StateDir: stateDir}.Workspaces()},
	}
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
			budget = observer.Resolver(state)
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
	case store.KindRevise:
		d.revise, err = reviseDeps(ctx, p, st, tr)
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
