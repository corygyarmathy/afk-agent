//go:build unix

package revise_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The environment the parent hands a helper process.
const (
	envRevDir    = "AFK_REV_KILL_DIR"
	envRevRemote = "AFK_REV_KILL_REMOTE"
	envRevHead   = "AFK_REV_KILL_HEAD"
	envRevAt     = "AFK_REV_KILL_AT"
)

// A process killed at any point in a revision, then run again, still pushes
// once: the push is committed and its key reserved before it is performed, so
// a kill between the two loses the effect and the next process makes it again
// under the next round, never twice under one.
//
// Done by actually killing a process: SIGKILL runs no defer and releases no
// lease. The remote is a real bare repository, and counts the pushes that land
// through a pre-receive hook.
//
// "session" kills the process inside the model's run, after the session has
// committed and written a reply: what it left is not the revision, and the
// session that finishes starts again from the send-back's head.
func TestKillingARevisionStillPushesOnce(t *testing.T) {
	for _, at := range []string{"session", "gating", "pushed", "watching"} {
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
			ready := filepath.Join(dir, "ready")

			doomed := revKillHelper(dir, state, remote, head, at, ready)
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

			// A second process, the way a restarted worker would pick the
			// job up. It runs an hour ahead, past the killed process's
			// lease.
			finish := revKillHelper(dir, state, remote, head, "", "")
			if out, err := finish.CombinedOutput(); err != nil {
				t.Fatalf("finishing the revision after the kill: %v\n%s", err, out)
			}

			if n := pushCount(t, count); n != 1 {
				t.Errorf("%d pushes landed, want exactly one", n)
			}
			// The revision is on the remote, on top of the send-back's head.
			after := remoteFeatureHead(t, remote)
			if after == head {
				t.Error("the revision was never pushed")
			}
			if !isAncestor(t, remote, head, after) {
				t.Errorf("the send-back's head %s is not an ancestor of %s", head, after)
			}
			if _, err := run(remote, "git", "cat-file", "-e", after+":half.txt"); err == nil {
				t.Error("the killed session's commit was pushed")
			}
			if reply := keptReply(t, state); reply != finishedReply {
				t.Errorf("the reply kept is %q, want the finished session's %q", reply, finishedReply)
			}
		})
	}
}

// revKillHelper is the command for a helper process. An empty at is the process
// that finishes; a non-empty one dies there.
func revKillHelper(dir, state, remote, head, at, ready string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRevisions$")
	cmd.Env = append(os.Environ(),
		envRevDir+"="+dir, envRevRemote+"="+remote, envRevHead+"="+head,
		envRevAt+"="+at,
	)
	if at != "" {
		cmd.Env = append(cmd.Env, "AFK_REV_KILL_READY="+ready)
	}
	return cmd
}

// TestHelperRevisions is not a test. It is the process a kill test kills, and
// then the one that finishes the revision.
func TestHelperRevisions(t *testing.T) {
	dir := os.Getenv(envRevDir)
	if dir == "" {
		t.Skip("run by the kill test, not directly")
	}
	state := filepath.Join(dir, "state")
	remote := os.Getenv(envRevRemote)
	head := os.Getenv(envRevHead)
	killAt := os.Getenv(envRevAt)

	s, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var st store.Store = s
	session := reviseOn("bar.txt", finishedReply)
	switch killAt {
	case "":
	case "session":
		ready := os.Getenv("AFK_REV_KILL_READY")
		session = func(dir string) error {
			if err := reviseOn("half.txt", "## Points\n\n- half done")(dir); err != nil {
				return err
			}
			os.WriteFile(ready, nil, 0o644)
			select {}
		}
	default:
		st = &killStore{Store: s, at: killAt, ready: os.Getenv("AFK_REV_KILL_READY")}
	}

	tr := newTracker()
	tr.pr.HeadSHA = head
	tr.pr.HeadRef = "feature"
	d := &revise.Deps{
		Tracker:       tr,
		Model:         &reviser{turns: []func(string) error{session}},
		Store:         st,
		Login:         agent,
		Repo:          repo,
		Remote:        git.Remote{URL: remote},
		Resolve:       func(context.Context) (model.Candidates, error) { return model.Candidates{refFirst}, nil },
		Bound:         1,
		TierWait:      time.Hour,
		Rounds:        3,
		Gate:          "echo checking; test -f ok || { echo 'FAIL: no ok' >&2; exit 1; }",
		Attempts:      3,
		Denylist:      []string{"flake.lock"},
		HandOffLabel:  handOff,
		HandBackLabel: "needs-decision",
		StateDir:      state,
	}
	reg := transition.MustRegistry(revise.Transitions(d)...)

	clock := time.Now()
	if killAt == "" {
		// Past the killed process's lease, rather than waiting it out.
		clock = clock.Add(time.Hour)
	}
	r := &transition.Runner{Store: st, Registry: reg, Holder: "helper-" + killAt, LeaseTTL: time.Minute, Clock: func() time.Time { return clock }}

	ctx := context.Background()
	id := jobID()
	for range 30 {
		job, err := s.Job(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		next, ok := reg.Next(job.Kind, job.State)
		if !ok || job.NextRunAt.IsZero() {
			return
		}
		r.Run(ctx, next.Name, id)
	}
	t.Fatalf("the revision never finished; it is in %q", jobState(t, s, id))
}

// finishedReply is the reply the session that finishes the revision writes.
const finishedReply = "## Points\n\n- \"Rename Foo\" done."

// keptReply is the reply the revision's progress kept.
func keptReply(t *testing.T, state string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "progress", jobID()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p.Reply
}

func jobState(t *testing.T, s store.Store, id string) string {
	job, err := s.Job(context.Background(), id)
	if err != nil {
		return "?"
	}
	return job.State
}

// killStore is a store that kills the process at a named state change, after
// the change is committed: the point between a commit and the effect it
// performs.
type killStore struct {
	store.Store
	at    string
	ready string
}

func (k *killStore) Commit(ctx context.Context, c store.Commit) error {
	if err := k.Store.Commit(ctx, c); err != nil {
		return err
	}
	if k.at == c.State {
		os.WriteFile(k.ready, nil, 0o644)
		select {}
	}
	return nil
}

// seedReviseJob creates the revision job in the state #146 starts from, and
// the send-back it works from, so a helper needs no claim.
func seedReviseJob(t *testing.T, state, head string) error {
	t.Helper()
	s, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	now := time.Now()
	if _, err := s.Ensure(context.Background(), store.KindRevise, store.Subject{Type: store.SubjectPR, Number: 12}, revise.Revising, now); err != nil {
		return err
	}
	sb := map[string]any{
		"head":   head,
		"ref":    "feature",
		"points": []map[string]any{{"comment": 1, "text": "Rename Foo to Bar."}},
	}
	b, err := json.Marshal(sb)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(state, "send-backs"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(state, "send-backs", jobID()+".json"), b, 0o644)
}

// revisionRemoteCounting builds the bare remote and installs a pre-receive
// hook that appends to a counter for every push that lands.
func revisionRemoteCounting(t *testing.T, dir string) (remote, head, count string) {
	t.Helper()
	remote, head = revisionRemote(t)
	count = filepath.Join(dir, "pushes")
	hook := filepath.Join(remote, "hooks", "pre-receive")
	script := "#!/bin/sh\necho push >> " + count + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return remote, head, count
}

func pushCount(t *testing.T, count string) int {
	t.Helper()
	b, err := os.ReadFile(count)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(b)))
}

func remoteFeatureHead(t *testing.T, remote string) string {
	t.Helper()
	out, err := run(t.TempDir(), "git", "ls-remote", remote, "refs/heads/feature")
	if err != nil {
		t.Fatal(err)
	}
	sha, _, _ := strings.Cut(out, "\t")
	return sha
}

func isAncestor(t *testing.T, remote, a, b string) bool {
	t.Helper()
	dir := t.TempDir()
	if _, err := run(dir, "git", "clone", "--quiet", remote, dir); err != nil {
		t.Fatal(err)
	}
	_, err := run(dir, "git", "merge-base", "--is-ancestor", a, b)
	return err == nil
}
