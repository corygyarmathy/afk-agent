//go:build unix

package revise_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
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
	envRevRed    = "AFK_REV_KILL_RED"
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

			doomed := revKillHelper(dir, state, remote, head, at, ready, false)
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
			finish := revKillHelper(dir, state, remote, head, "", "", false)
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

// A process killed at any point in a revision whose first push CI fails, then
// run again, still pushes once per round: the revision's push, and the fix's.
// The fix is the revision's session again, and its push the same leased one.
//
// A kill point `state.n` is the nth time the job is committed to state, so
// `.2` is the fix's round. The job is seeded in revising, so "revising" is the
// red run sent back to the session. "session.2" kills inside the fix's session,
// after it has committed. Not `#`: the subtest's name is in the store's path,
// and the store opens it as a URI, where `#` begins a fragment.
func TestKillingARevisionsFixStillPushesOncePerRound(t *testing.T) {
	for _, at := range []string{"watching", "revising", "session.2", "gating.2", "pushing.2", "pushed.2", "watching.2"} {
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

			doomed := revKillHelper(dir, state, remote, head, at, ready, true)
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

			finish := revKillHelper(dir, state, remote, head, "", "", true)
			if out, err := finish.CombinedOutput(); err != nil {
				t.Fatalf("finishing the revision after the kill: %v\n%s", err, out)
			}

			if n := pushCount(t, count); n != 2 {
				t.Errorf("%d pushes landed, want exactly two: the revision's and the fix's", n)
			}
			after := remoteFeatureHead(t, remote)
			if _, err := run(remote, "git", "cat-file", "-e", after+":fix.txt"); err != nil {
				t.Error("the fix is not on the remote")
			}
			if !isAncestor(t, remote, head, after) {
				t.Errorf("the send-back's head %s is not an ancestor of %s", head, after)
			}
			if got := finalState(t, state); got != revise.Replying {
				t.Errorf("the job is in %q, want %s: green after the fix", got, revise.Replying)
			}
		})
	}
}

// revKillHelper is the command for a helper process. An empty at is the process
// that finishes; a non-empty one dies there. red has CI fail every head
// without the fix on it.
func revKillHelper(dir, state, remote, head, at, ready string, red bool) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRevisions$")
	cmd.Env = append(os.Environ(),
		envRevDir+"="+dir, envRevRemote+"="+remote, envRevHead+"="+head,
		envRevAt+"="+at,
	)
	if red {
		cmd.Env = append(cmd.Env, envRevRed+"=1")
	}
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
	red := os.Getenv(envRevRed) != ""
	if red {
		// Each session does what the workspace still needs: the revision,
		// then the fix CI asks for. A killed session's commits stay for the
		// next, as a fix's do.
		session = func(dir string) error {
			if _, err := os.Stat(filepath.Join(dir, "bar.txt")); os.IsNotExist(err) {
				return reviseOn("bar.txt", finishedReply)(dir)
			}
			if _, err := os.Stat(filepath.Join(dir, "fix.txt")); os.IsNotExist(err) {
				if err := commitOn("fix.txt")(dir); err != nil {
					return err
				}
				if killAt == "session.2" {
					os.WriteFile(os.Getenv("AFK_REV_KILL_READY"), nil, 0o644)
					select {}
				}
			}
			return nil
		}
	}
	switch killAt {
	case "", "session.2":
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
	if red {
		tr.checks = func(sha string, _ int) []github.CheckRun {
			if _, err := run(remote, "git", "cat-file", "-e", sha+":fix.txt"); err == nil {
				return green(sha, 0)
			}
			return []github.CheckRun{{Name: "test", Status: "completed", Conclusion: "failure"}}
		}
	}
	d := &revise.Deps{
		Tracker:       tr,
		Model:         &reviser{turns: []func(string) error{session, session}},
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
		CIWait:        10 * time.Minute,
		CICeiling:     2 * time.Hour,
		CIFixes:       2,
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

// finalState is the job's state once the helpers are done.
func finalState(t *testing.T, state string) string {
	t.Helper()
	s, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	return jobState(t, s, jobID())
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
// performs. `state.n` is the nth commit to state.
type killStore struct {
	store.Store
	at    string
	ready string
	seen  map[string]int
}

func (k *killStore) Commit(ctx context.Context, c store.Commit) error {
	if err := k.Store.Commit(ctx, c); err != nil {
		return err
	}
	if k.seen == nil {
		k.seen = map[string]int{}
	}
	k.seen[c.State]++
	want, nth, _ := strings.Cut(k.at, ".")
	if nth == "" {
		nth = "1"
	}
	if want == c.State && nth == strconv.Itoa(k.seen[c.State]) {
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
