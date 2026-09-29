package revise_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/revise"
)

// A push someone else makes during the revision is replayed onto rather than
// overwritten: their commit is kept, the revision's own commit goes on top of
// it, and the head the send-back was written against is still what the
// revision is read from.
func TestAPushDuringARevisionIsReplayedOnto(t *testing.T) {
	f := setupRevision(t)
	var theirs string
	f.model.then(func(dir string) error {
		if err := reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done.")(dir); err != nil {
			return err
		}
		if err := f.someoneElsePushes("other.txt"); err != nil {
			return err
		}
		theirs = f.remoteHead()
		return nil
	})

	job := f.step(revise.Watching)
	if job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s\n%s", job.State, revise.Watching, f.handBack())
	}
	at := f.remoteHead()
	if parent, err := run(f.remote, "git", "rev-parse", at+"^"); err != nil || parent != theirs {
		t.Errorf("the pushed head's parent is %s, want their push %s: only the revision's own commit goes on top", git.Short(parent), git.Short(theirs))
	}
	for _, name := range []string{"bar.txt", "other.txt", "feature.txt"} {
		if _, err := run(f.remote, "git", "cat-file", "-e", at+":"+name); err != nil {
			t.Errorf("the pushed head has no %s", name)
		}
	}
	if n := len(f.model.asked); n != 1 {
		t.Errorf("%d model runs, want 1: a clean replay is not the session's to redo", n)
	}
	if p := f.replayed(); p.Replays != 1 || p.Read != f.head {
		t.Errorf("progress is %+v, want one replay and the send-back's head %s still read", p, git.Short(f.head))
	}
}

// A replay that conflicts with their push hands back, naming the commit and
// the path, and pushes nothing.
func TestAConflictingReplayHandsBack(t *testing.T) {
	f := setupRevision(t)
	var theirs string
	f.model.then(func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("ours\n"), 0o644); err != nil {
			return err
		}
		if _, err := run(dir, "git", "add", "feature.txt"); err != nil {
			return err
		}
		if err := commitOn("bar.txt")(dir); err != nil {
			return err
		}
		if err := pushAs(f.t.TempDir(), f.remote, "feature.txt", "theirs\n"); err != nil {
			return err
		}
		theirs = f.remoteHead()
		return replyOn("## Points\n\n- \"Rename Foo\" done.")(dir)
	})

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest in start after a hand-back", job.State)
	}
	if at := f.remoteHead(); at != theirs {
		t.Errorf("the remote is at %s, want their push %s", git.Short(at), git.Short(theirs))
	}
	body := f.handBack()
	if !strings.Contains(body, "does not replay onto their push") || !strings.Contains(body, "in `feature.txt`") || !strings.Contains(body, "(revise: bar.txt)") {
		t.Errorf("the hand-back does not name the conflict:\n%s", body)
	}
	if !f.handedBack() {
		t.Error("no hand-back label on the pull request")
	}
}

// Someone who keeps pushing runs the replays out: the revision is replayed as
// many times as it may be, and the next push made during it hands back.
func TestReplaysRunOutAndHandBack(t *testing.T) {
	f := setupRevision(t)
	f.deps.Replays = 1
	// The gate pushes as someone else every time it passes, so the branch
	// has always moved by the time the revision is pushed.
	dir := t.TempDir()
	script := filepath.Join(dir, "push.sh")
	body := fmt.Sprintf(`n=$(cat %[1]s/n 2>/dev/null || echo 0); n=$((n+1)); echo $n > %[1]s/n
d=%[1]s/clone-$n
git clone --quiet %[2]s "$d" && cd "$d" && git switch --quiet feature &&
echo "$n" > "other-$n.txt" && git add . && git commit --quiet -m "someone else: $n" && git push --quiet origin feature
`, dir, f.remote)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	f.deps.Gate = "test -f ok && sh " + script
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."))

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest in start after a hand-back", job.State)
	}
	at := f.remoteHead()
	if _, err := run(f.remote, "git", "cat-file", "-e", at+":bar.txt"); err == nil {
		t.Error("the revision was pushed after its replays ran out")
	}
	if _, err := run(f.remote, "git", "cat-file", "-e", at+":other-2.txt"); err != nil {
		t.Error("the second push made during the revision is not on the remote")
	}
	if body := f.handBack(); !strings.Contains(body, "as many times as it may be (1)") {
		t.Errorf("the hand-back does not say the replays ran out:\n%s", body)
	}
}

// The workspace's configuration is the session's to write, and nothing it says
// reaches the replay: the fetch of their push carries the token in the relay,
// never in the workspace, and bringing the replay in runs no hook the session
// left.
func TestAHostileWorkspaceConfigDoesNotReachTheReplay(t *testing.T) {
	f := setupRevision(t)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	hooks := t.TempDir()
	ran := filepath.Join(t.TempDir(), "hook-ran")
	for _, h := range []string{"post-checkout", "reference-transaction"} {
		if err := os.WriteFile(filepath.Join(hooks, h), []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.model.then(func(dir string) error {
		if err := reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done.")(dir); err != nil {
			return err
		}
		for _, kv := range [][]string{
			{"url." + elsewhere + ".insteadOf", f.remote},
			{"http.extraheader", "Authorization: Basic bm90LXlvdXJz"},
			{"http.proxy", "http://127.0.0.1:9"},
			{"core.hooksPath", hooks},
		} {
			if _, err := run(dir, "git", "config", kv[0], kv[1]); err != nil {
				return err
			}
		}
		return f.someoneElsePushes("other.txt")
	})

	job := f.step(revise.Watching)
	if job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s\n%s", job.State, revise.Watching, f.handBack())
	}
	at := f.remoteHead()
	for _, name := range []string{"bar.txt", "other.txt"} {
		if _, err := run(f.remote, "git", "cat-file", "-e", at+":"+name); err != nil {
			t.Errorf("the remote's head has no %s: the replay did not reach it", name)
		}
	}
	if _, err := os.Stat(elsewhere); err == nil {
		t.Error("something reached the url the workspace's insteadOf named")
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("a hook the session configured ran")
	}
}

// Their push is on the remote and not the agent's to push: a denied path in it
// does not stop the revision's replay.
func TestTheDenylistAfterAReplayReadsOnlyTheRevisions(t *testing.T) {
	f := setupRevision(t)
	f.model.then(func(dir string) error {
		if err := reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done.")(dir); err != nil {
			return err
		}
		return f.someoneElsePushes("flake.lock")
	})

	job := f.step(revise.Watching)
	if job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s: their flake.lock is not the revision's\n%s", job.State, revise.Watching, f.handBack())
	}
	if _, err := run(f.remote, "git", "cat-file", "-e", f.remoteHead()+":bar.txt"); err != nil {
		t.Error("the revision was not pushed")
	}
}

// A push that drops the head the agent last saw on the branch is not replayed
// onto: that would push over what was read.
func TestAPushThatDropsTheReadHeadIsNotReplayedOnto(t *testing.T) {
	f := setupRevision(t)
	f.model.then(func(dir string) error {
		if err := reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done.")(dir); err != nil {
			return err
		}
		_, err := run(f.remote, "git", "update-ref", "refs/heads/feature", "refs/heads/main")
		return err
	})
	main, err := run(f.remote, "git", "rev-parse", "main")
	if err != nil {
		t.Fatal(err)
	}

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest in start after a hand-back", job.State)
	}
	if at := f.remoteHead(); at != main {
		t.Errorf("the remote is at %s, want where they left it, %s", git.Short(at), git.Short(main))
	}
	if body := f.handBack(); !strings.Contains(body, "where the send-back was written") {
		t.Errorf("the hand-back does not say the branch was pushed over:\n%s", body)
	}
}

// A push someone makes on top of the revision's own, after it landed and
// before the agent read it back, is not a push made during the revision: the
// revision is on the branch, so nothing is replayed and nothing is handed back.
// A bot that pushes after every push does this routinely.
func TestAPushOnTopOfTheRevisionsOwnPushIsNotReplayedOnto(t *testing.T) {
	for _, replays := range []int{2, 0} {
		t.Run(fmt.Sprintf("replays %d", replays), func(t *testing.T) {
			f := setupRevision(t)
			f.deps.Replays = replays
			f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."))

			f.claim()
			if job := f.step(revise.Pushed); job.State != revise.Pushed {
				t.Fatalf("the job is in %q, want %s", job.State, revise.Pushed)
			}
			ours := f.remoteHead()
			if err := f.someoneElsePushes("formatted.txt"); err != nil {
				t.Fatal(err)
			}
			theirs := f.remoteHead()

			job := f.step(revise.Watching)
			if job.State != revise.Watching {
				t.Fatalf("the job is in %q, want %s\n%s", job.State, revise.Watching, f.handBack())
			}
			if at := f.remoteHead(); at != theirs {
				t.Errorf("the remote is at %s, want their push %s left as it is", git.Short(at), git.Short(theirs))
			}
			if p := f.replayed(); p.Replays != 0 || p.Pushed != ours {
				t.Errorf("progress is %+v, want no replay and the revision's own push %s as the lease", p, git.Short(ours))
			}
			if n := len(f.model.asked); n != 1 {
				t.Errorf("%d model runs, want 1", n)
			}
		})
	}
}

type replayedProgress struct {
	Read    string `json:"read"`
	Pushed  string `json:"pushed"`
	Replays int    `json:"replays"`
}

// replayed is the revision's progress, as far as a replay moves it.
func (f *revFixture) replayed() replayedProgress {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.deps.StateDir, "progress", jobID()+".json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var p replayedProgress
	if err := json.Unmarshal(b, &p); err != nil {
		f.t.Fatal(err)
	}
	return p
}
