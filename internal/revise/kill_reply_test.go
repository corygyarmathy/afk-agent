//go:build unix

package revise_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/github/githubtest"
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
			dt := diskTracker(dir, remote, "", "")
			start := newTracker()
			start.PullRequests[12].Labels = []string{"bug"}
			if err := dt.Save(start); err != nil {
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
			snap, err := dt.Load()
			if err != nil {
				t.Fatal(err)
			}
			var answers, replies, reviews []github.Comment
			for _, c := range snap.CommentsOn[12] {
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
				t.Fatalf("%d answers to the command and %d replies, want the one reply\n%v", len(answers), len(replies), snap.CommentsOn[12])
			}
			if n := snap.Reacts[replies[0].ID]; n != 1 {
				t.Errorf("%d claims landed on the reply, want one", n)
			}
			if len(reviews) != 1 {
				t.Errorf("%d reviews of %s, want one", len(reviews), git.Short(after))
			}
			if !github.HasLabel(snap.PullRequests[12].Labels, handOff) || github.HasLabel(snap.PullRequests[12].Labels, "needs-decision") {
				t.Errorf("the labels are %v, want handed off and not handed back", snap.PullRequests[12].Labels)
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
	dt := diskTracker(dir, remote, killAt, ready)

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

// diskTracker is the fixture tracker in a file in dir, so that what a killed
// process did to pull request 12 outlives the process. Its head is the
// remote's, as GitHub's moves with a push. A non-empty killAt is the `after-`
// point this process dies at, having told the parent it is there: once the
// write it names has landed.
func diskTracker(dir, remote, killAt, ready string) *githubtest.File {
	dt := &githubtest.File{Path: filepath.Join(dir, "tracker.json"), KillAt: killAt, Ready: ready}
	dt.New = func() *githubtest.Tracker {
		tr := newTracker()
		tr.Live = remote
		// A review is never a send-back here, and nothing edits.
		tr.FailOn("ReactToPullRequestReview", errors.New("nothing here claims a review"))
		tr.FailOn("EditComment", errors.New("PATCH comment: this test edits no comment"))
		tr.FailOn("EditPullRequest", errors.New("the description has no sensitive line to edit here"))
		return tr
	}
	dt.After = func(c githubtest.Call) {
		switch {
		case c.Method == "Comment" && strings.Contains(c.Text, "<!-- afk:revision-reply "):
			dt.Die("after-reply")
		case c.Method == "Comment" && strings.Contains(c.Text, "<!-- afk:review head="):
			dt.Die("after-review")
		case c.Method == "React":
			tr, err := dt.Load()
			if err != nil {
				return
			}
			for _, cm := range tr.CommentsOn[12] {
				if cm.ID == c.ID && strings.Contains(cm.Body, "<!-- afk:revision-reply ") {
					dt.Die("after-reply-claim")
				}
			}
		case c.Method == "Label" && c.Text == handOff:
			dt.Die("after-label")
		}
	}
	return dt
}
