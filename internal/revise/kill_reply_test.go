//go:build unix

package revise_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/handoff"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// A process killed at any point from the revision's push to its hand-off, then
// run again, still gives one push, one answer to the command, one claim on the
// reply, one review and the hand-off label: every effect is read back from the
// tracker before the job moves on from it, and a lost one is made again under
// the next key, never twice under one.
//
// Done by actually killing a process, for the reason the other kill tests
// give. The tracker is a file, so what the killed process did to the pull
// request survives it the way GitHub would, and the review job the revision
// asks for runs in the same processes, as the pool runs both.
//
// A state is the commit to it, before its effect: `replying` loses the reply,
// `reviewing` the review job's arming, `claiming` the review's claim on the
// reply, `verifying` the review, and `handing-off` the label. An `after-`
// point is the effect landed on the tracker, before the process knows it has.
// The review kind has a `reviewing` state too; the revision commits to it
// first.
func TestKillingARevisionStillAnswersOnce(t *testing.T) {
	for _, at := range []string{
		"watching", "replying", "after-reply", "reviewing", "claiming", "after-reply-claim",
		"verifying", "after-review", "handing-off", "after-label",
	} {
		t.Run(at, func(t *testing.T) {
			dir := t.TempDir()
			remote, head, count := revisionRemoteCounting(t, dir)
			state := filepath.Join(dir, "state")
			if err := os.MkdirAll(state, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := seedReviseJob(t, state, head); err != nil {
				t.Fatal(err)
			}
			dt := &diskTracker{path: filepath.Join(dir, "tracker.json"), remote: remote}
			if err := dt.save(snapshot{Labels: []string{"bug"}, NextID: 1000}); err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(dir, "ready")

			doomed := answerHelper(dir, remote, at, ready)
			doomed.Stdout, doomed.Stderr = os.Stderr, os.Stderr
			if err := doomed.Start(); err != nil {
				t.Fatalf("start the helper: %v", err)
			}
			t.Cleanup(func() { doomed.Process.Kill() })
			waitForFile(t, ready)

			if err := doomed.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatalf("kill the helper: %v", err)
			}
			if err := doomed.Wait(); err == nil {
				t.Fatal("the helper exited cleanly; it was supposed to be killed")
			}

			finish := answerHelper(dir, remote, "", "")
			if out, err := finish.CombinedOutput(); err != nil {
				t.Fatalf("finishing the revision after the kill: %v\n%s", err, out)
			}

			if n := pushCount(t, count); n != 1 {
				t.Errorf("%d pushes landed, want exactly one", n)
			}
			after := remoteFeatureHead(t, remote)
			snap, err := dt.load()
			if err != nil {
				t.Fatal(err)
			}
			var answers, replies, reviews []github.Comment
			for _, c := range snap.Comments {
				if strings.Contains(c.Body, owed.RevisionMarker(1)) {
					answers = append(answers, c)
				}
				if strings.Contains(c.Body, owed.RevisionReplyMarker(12, after)) {
					replies = append(replies, c)
				}
				if strings.Contains(c.Body, review.Marker(after)) {
					reviews = append(reviews, c)
				}
			}
			if len(answers) != 1 || len(replies) != 1 {
				t.Fatalf("%d answers to the command and %d replies, want the one reply\n%v", len(answers), len(replies), snap.Comments)
			}
			if n := snap.Reacts[replies[0].ID]; n != 1 {
				t.Errorf("%d claims landed on the reply, want one", n)
			}
			if len(reviews) != 1 {
				t.Errorf("%d reviews of %s, want one", len(reviews), git.Short(after))
			}
			if !github.HasLabel(snap.Labels, handOff) || github.HasLabel(snap.Labels, "needs-decision") {
				t.Errorf("the labels are %v, want handed off and not handed back", snap.Labels)
			}
			if got := finalState(t, state); got != revise.Start {
				t.Errorf("the revision is in %q, want at rest in %s", got, revise.Start)
			}
		})
	}
}

// answerHelper is the command for a helper process. An empty at is the process
// that finishes; a non-empty one dies there.
func answerHelper(dir, remote, at, ready string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperAnswers$")
	cmd.Env = append(os.Environ(), envRevDir+"="+dir, envRevRemote+"="+remote, envRevAt+"="+at)
	if at != "" {
		cmd.Env = append(cmd.Env, "AFK_REV_KILL_READY="+ready)
	}
	return cmd
}

// TestHelperAnswers is not a test. It is the process a kill test kills, and
// then the one that finishes the revision and the review it asks for.
func TestHelperAnswers(t *testing.T) {
	dir := os.Getenv(envRevDir)
	if dir == "" {
		t.Skip("run by the kill test, not directly")
	}
	state := filepath.Join(dir, "state")
	remote := os.Getenv(envRevRemote)
	killAt := os.Getenv(envRevAt)
	ready := os.Getenv("AFK_REV_KILL_READY")

	s, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var st store.Store = s
	if killAt != "" && !strings.HasPrefix(killAt, "after-") {
		st = &killStore{Store: s, at: killAt, ready: ready}
	}
	dt := &diskTracker{path: filepath.Join(dir, "tracker.json"), remote: remote, killAt: killAt, ready: ready}

	resolve := func(context.Context) (model.Candidates, error) { return model.Candidates{refFirst}, nil }
	d := &revise.Deps{
		Tracker:       dt,
		Model:         &reviser{turns: []func(string) error{reviseOn("bar.txt", finishedReply)}},
		Store:         st,
		Login:         agent,
		Repo:          repo,
		Remote:        git.Remote{URL: remote, Untrusted: []string{filepath.Join(state, "workspaces")}},
		Resolve:       resolve,
		Bound:         1,
		TierWait:      time.Hour,
		Rounds:        3,
		Gate:          "echo checking; test -f ok || { echo 'FAIL: no ok' >&2; exit 1; }",
		Attempts:      3,
		Denylist:      []string{"flake.lock"},
		CIWait:        10 * time.Minute,
		CICeiling:     2 * time.Hour,
		CIFixes:       2,
		Replays:       1,
		SizeSignal:    1000,
		HandOffLabel:  handOff,
		HandBackLabel: "needs-decision",
		AskReview:     handoff.Asker(transition.Armer{Store: st, Holder: "helper-ask-" + killAt, LeaseTTL: time.Minute}),
		StateDir:      state,
	}
	rd := &review.Deps{
		Tracker: dt, Model: advisor{}, Store: st,
		Checkout: func(ctx context.Context, dir string, n int) (string, error) {
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
				return "", err
			}
			pr, err := dt.PullRequest(ctx, n)
			return pr.HeadSHA, err
		},
		Resolve: resolve, Bound: 1, Rounds: 3, TierWait: time.Hour,
		Repo: repo, Login: agent, HandBackLabel: "needs-decision", StateDir: state,
	}
	reg := transition.MustRegistry(append(revise.Transitions(d), review.Transitions(rd)...)...)

	clock := time.Now()
	if killAt == "" {
		// Past the killed process's leases, rather than waiting them out.
		clock = clock.Add(time.Hour)
	}
	r := &transition.Runner{Store: st, Registry: reg, Holder: "helper-" + killAt, LeaseTTL: time.Minute, Clock: func() time.Time { return clock }}

	// The review job first whenever it is due, as a pool with a worker free
	// would run it while the revision waits on it.
	ctx := context.Background()
	ids := []string{store.ID(store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}), jobID()}
	for range 80 {
		ran := false
		for _, id := range ids {
			job, err := s.Job(ctx, id)
			if errors.Is(err, store.ErrNoJob) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			next, ok := reg.Next(job.Kind, job.State)
			if !ok || job.NextRunAt.IsZero() {
				continue
			}
			if job.NextRunAt.After(clock) {
				clock = job.NextRunAt
			}
			r.Run(ctx, next.Name, id)
			ran = true
			break
		}
		if !ran {
			return
		}
	}
	t.Fatalf("the revision never finished; it is in %q", jobState(t, s, jobID()))
}

// diskTracker is the fixture tracker with pull request 12's comments,
// reactions and labels in a file, so that what a killed process did to it
// outlives the process. Its head is the remote's, as GitHub's moves with a
// push.
type diskTracker struct {
	path   string
	remote string

	// killAt is the `after-` point this process dies at, having told the
	// parent it is there: once the write it names has landed.
	killAt string
	ready  string
}

type snapshot struct {
	Labels    []string
	Comments  []github.Comment
	Reactions map[int64][]github.Reaction
	NextID    int64

	// Reacts is how many reactions landed on each comment.
	Reacts map[int64]int
}

func (dt *diskTracker) load() (snapshot, error) {
	b, err := os.ReadFile(dt.path)
	if err != nil {
		return snapshot{}, err
	}
	var s snapshot
	err = json.Unmarshal(b, &s)
	return s, err
}

func (dt *diskTracker) save(s snapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := dt.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dt.path)
}

// with runs do on the fixture tracker as the file has it, and writes back what
// it changed.
func (dt *diskTracker) with(do func(tr *tracker) error) error {
	s, err := dt.load()
	if err != nil {
		return err
	}
	tr := newTracker()
	tr.live = dt.remote
	tr.pr.Labels = s.Labels
	tr.comments[12] = s.Comments
	if s.Reactions != nil {
		tr.reactions = s.Reactions
	}
	if s.Reacts != nil {
		tr.reacts = s.Reacts
	}
	tr.nextID = s.NextID
	if err := do(tr); err != nil {
		return err
	}
	return dt.save(snapshot{Labels: tr.pr.Labels, Comments: tr.comments[12], Reactions: tr.reactions, NextID: tr.nextID, Reacts: tr.reacts})
}

// landed dies if point is where this process is to die.
func (dt *diskTracker) landed(point string) {
	if dt.killAt != point {
		return
	}
	os.WriteFile(dt.ready, nil, 0o644)
	select {}
}

func (dt *diskTracker) PullRequest(ctx context.Context, n int) (pr github.PullRequest, err error) {
	err = dt.with(func(tr *tracker) (err error) { pr, err = tr.PullRequest(ctx, n); return err })
	return pr, err
}

func (dt *diskTracker) Issue(ctx context.Context, n int) (is github.Issue, err error) {
	err = dt.with(func(tr *tracker) (err error) { is, err = tr.Issue(ctx, n); return err })
	return is, err
}

func (dt *diskTracker) Comments(ctx context.Context, n int) (cs []github.Comment, err error) {
	err = dt.with(func(tr *tracker) (err error) { cs, err = tr.Comments(ctx, n); return err })
	return cs, err
}

func (dt *diskTracker) Reactions(ctx context.Context, id int64) (rs []github.Reaction, err error) {
	err = dt.with(func(tr *tracker) (err error) { rs, err = tr.Reactions(ctx, id); return err })
	return rs, err
}

func (dt *diskTracker) IssueReactions(context.Context, int) ([]github.Reaction, error) {
	return nil, nil
}

func (dt *diskTracker) Comment(ctx context.Context, n int, body string) (c github.Comment, err error) {
	if err = dt.with(func(tr *tracker) (err error) { c, err = tr.Comment(ctx, n, body); return err }); err != nil {
		return c, err
	}
	switch {
	case strings.Contains(body, "<!-- afk:revision-reply "):
		dt.landed("after-reply")
	case strings.Contains(body, "<!-- afk:review head="):
		dt.landed("after-review")
	}
	return c, nil
}

func (dt *diskTracker) React(ctx context.Context, id int64, content string) error {
	var reply bool
	if err := dt.with(func(tr *tracker) error {
		for _, c := range tr.comments[12] {
			reply = reply || (c.ID == id && strings.Contains(c.Body, "<!-- afk:revision-reply "))
		}
		return tr.React(ctx, id, content)
	}); err != nil {
		return err
	}
	if reply {
		dt.landed("after-reply-claim")
	}
	return nil
}

// A review is never a send-back here.
func (dt *diskTracker) PullRequestReviews(context.Context, int) ([]github.PullRequestReview, error) {
	return nil, nil
}

func (dt *diskTracker) LineComments(context.Context, int, int64) ([]github.LineComment, error) {
	return nil, nil
}

func (dt *diskTracker) PullRequestReviewReactions(context.Context, string) ([]github.Reaction, error) {
	return nil, nil
}

func (dt *diskTracker) ReactToPullRequestReview(context.Context, string, string) error {
	return errors.New("nothing here claims a review")
}

func (dt *diskTracker) ReactToIssue(context.Context, int, string) error {
	return errors.New("nothing here claims a pull request's description")
}

func (dt *diskTracker) Label(ctx context.Context, n int, label string) error {
	if err := dt.with(func(tr *tracker) error { return tr.Label(ctx, n, label) }); err != nil {
		return err
	}
	if label == handOff {
		dt.landed("after-label")
	}
	return nil
}

func (dt *diskTracker) Unlabel(ctx context.Context, n int, label string) error {
	return dt.with(func(tr *tracker) error { return tr.Unlabel(ctx, n, label) })
}

func (dt *diskTracker) EditPullRequest(context.Context, int, string) error {
	return fmt.Errorf("the description has no sensitive line to edit here")
}

func (dt *diskTracker) CheckRuns(_ context.Context, sha string) ([]github.CheckRun, error) {
	return green(sha, 0), nil
}

func (dt *diskTracker) RequiredChecks(context.Context, string) ([]string, error) {
	return nil, nil
}

func (dt *diskTracker) Diff(context.Context, int) (string, error) {
	return "", nil
}

func (dt *diskTracker) Compare(context.Context, string, string) (string, error) {
	return "", nil
}
