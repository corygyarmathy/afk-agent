package implement_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/implement"
)

// Work that passes the gate is pushed once, and opens one pull request that
// closes the issue.
func TestCleanWorkIsPushedAndOpensOnePullRequest(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	head, _ := run(f.workspace(), "git", "rev-parse", "HEAD")
	if at, _ := run(f.remote, "git", "rev-parse", "refs/heads/afk/7-1"); at != head {
		t.Errorf("the remote's afk/7-1 is at %q, want the workspace's head %q", at, head)
	}
	if len(f.tr.opened) != 1 {
		t.Fatalf("%d pull requests opened, want 1", len(f.tr.opened))
	}
	pr := f.tr.opened[0]
	if pr.Title != "Reserve a job" || pr.Head != "afk/7-1" || pr.Base != "main" {
		t.Errorf("pull request = %+v, want the issue's title, from afk/7-1 into main", pr)
	}
	for _, want := range []string{implement.PRMarker(7), "Closes #7.", "Done.", "advisory review"} {
		if !strings.Contains(pr.Body, want) {
			t.Errorf("the description does not contain %q:\n%s", want, pr.Body)
		}
	}
}

// A push lost after its decision was committed - a kill, or a push that
// failed - is noticed on the remote, and made again under the next key. One
// push lands, and one pull request.
func TestALostPushIsMadeAgain(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	calls := 0
	f.deps.Token = func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("minting a token: 502 Bad Gateway")
		}
		return "", nil
	}

	errs := f.drive()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "502") {
		t.Fatalf("errors: %v, want the one lost push", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	if calls != 2 || len(f.tr.opened) != 1 {
		t.Errorf("%d pushes tried and %d pull requests opened, want 2 and 1", calls, len(f.tr.opened))
	}
}

// A pull request whose opening was lost is opened again, and one whose
// opening succeeded slowly is not opened twice.
func TestAPullRequestIsOpenedOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opens bool
		want  int
	}{
		{"the request failed", false, 2},
		{"it opened, and the call said it failed", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.model.then(commit("ok"))
			f.tr.open = func(call int) (bool, error) {
				if call == 1 {
					return tc.opens, errors.New("502 Bad Gateway")
				}
				return true, nil
			}
			if errs := f.drive(); len(errs) != 1 {
				t.Fatalf("errors: %v, want the one failed call", errs)
			}
			if j := f.now(); j.State != implement.Watching {
				t.Fatalf("job in %q, want %q", j.State, implement.Watching)
			}
			if len(f.tr.opened) != 1 || f.tr.opens != tc.want {
				t.Errorf("%d pull requests open after %d calls, want 1 after %d", len(f.tr.opened), f.tr.opens, tc.want)
			}
		})
	}
}

// Work that touches a denylisted path, in any of its commits, never reaches
// the remote, and is handed back on the issue.
func TestADeniedPathIsNeverPushed(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
			return err
		}
		if err := commit(".github/workflows/steal.yml")(dir); err != nil {
			return err
		}
		// Taken out again, so the net diff is clean and only the history
		// carries it.
		if _, err := run(dir, "git", "rm", "--quiet", ".github/workflows/steal.yml"); err != nil {
			return err
		}
		if _, err := run(dir, "git", "commit", "--quiet", "-m", "tidy"); err != nil {
			return err
		}
		return commit("ok")(dir)
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if b := f.remoteBranches(); b != "main" {
		t.Errorf("the remote has %q, want nothing pushed", b)
	}
	if len(f.tr.opened) != 0 {
		t.Error("a pull request was opened")
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "`.github/workflows/steal.yml`") || !strings.Contains(posted[0].Body, "denylist") {
		t.Fatalf("comments %+v, want one hand-back naming the denied path", posted)
	}
	if strings.Join(f.tr.labels, ",") != "needs-decision" {
		t.Errorf("labels %v, want the hand-back label", f.tr.labels)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
}

// A merge commit lists no paths of its own to `git log`, so a denied path the
// merge itself adds is found by diffing it against each parent.
func TestADeniedPathAddedInAMergeIsNeverPushed(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		for _, args := range [][]string{
			{"switch", "--quiet", "--create", "side"},
			{"commit", "--quiet", "--allow-empty", "-m", "side"},
			{"switch", "--quiet", "-"},
		} {
			if _, err := run(dir, "git", args...); err != nil {
				return err
			}
		}
		if err := commit("ok")(dir); err != nil {
			return err
		}
		if _, err := run(dir, "git", "merge", "--quiet", "--no-ff", "--no-commit", "side"); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, ".github", "workflows", "steal.yml"), []byte("x\n"), 0o644); err != nil {
			return err
		}
		if _, err := run(dir, "git", "add", ".github"); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "--no-edit")
		return err
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if b := f.remoteBranches(); b != "main" {
		t.Errorf("the remote has %q, want nothing pushed", b)
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "`.github/workflows/steal.yml`") {
		t.Fatalf("comments %+v, want one hand-back naming the denied path", posted)
	}
}

// The workspace's .git is the model's to write. A hook it leaves there never
// runs at the push, which carries the token.
func TestTheWorkspacesHooksDoNotRunAtThePush(t *testing.T) {
	f := setup(t, newTracker())
	ran := filepath.Join(t.TempDir(), "hook-ran")
	f.model.then(func(dir string) error {
		hook := filepath.Join(dir, ".git", "hooks", "pre-push")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nenv > "+ran+"\n"), 0o755); err != nil {
			return err
		}
		return commit("ok")(dir)
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	if _, err := os.Stat(ran); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the workspace's pre-push hook ran at the push: %v", err)
	}
}

// Nor does its configuration: a url.insteadOf in the workspace that would send
// the push, and its token, to another remote is not what the push reads.
func TestTheWorkspacesConfigurationDoesNotRedirectThePush(t *testing.T) {
	f := setup(t, newTracker())
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	if _, err := run("", "git", "init", "--quiet", "--bare", elsewhere); err != nil {
		t.Fatal(err)
	}
	f.model.then(func(dir string) error {
		if _, err := run(dir, "git", "config", "url."+elsewhere+".insteadOf", f.remote); err != nil {
			return err
		}
		return commit("ok")(dir)
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if b, _ := run(elsewhere, "git", "for-each-ref", "--format=%(refname:short)", "refs/heads"); b != "" {
		t.Errorf("the push went to the workspace's redirect: it has %q", b)
	}
	if b := f.remoteBranches(); !strings.Contains(b, "afk/7-1") {
		t.Errorf("the remote has %q, want afk/7-1 pushed there", b)
	}
}

// Nor does the agent user's own configuration, which a session running as that
// user can write: the push, and the read of where it landed, take none.
func TestTheGlobalConfigurationDoesNotRedirectThePush(t *testing.T) {
	f := setup(t, newTracker())
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	if _, err := run("", "git", "init", "--quiet", "--bare", elsewhere); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	f.model.then(func(dir string) error {
		if err := os.WriteFile(global, []byte("[url \""+elsewhere+"\"]\n\tinsteadOf = "+f.remote+"\n"), 0o644); err != nil {
			return err
		}
		t.Setenv("GIT_CONFIG_GLOBAL", global)
		return commit("ok")(dir)
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if b, _ := run(elsewhere, "git", "for-each-ref", "--format=%(refname:short)", "refs/heads"); b != "" {
		t.Errorf("the push went to the global redirect: it has %q", b)
	}
	if b := f.remoteBranches(); !strings.Contains(b, "afk/7-1") {
		t.Errorf("the remote has %q, want afk/7-1 pushed there", b)
	}
}

// A relay found already in place, which the agent did not make in this push,
// is not trusted: its configuration could redirect the push as the
// workspace's could.
func TestARelayMadeBeforeThePushIsNotReused(t *testing.T) {
	f := setup(t, newTracker())
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	if _, err := run("", "git", "init", "--quiet", "--bare", elsewhere); err != nil {
		t.Fatal(err)
	}
	f.model.then(func(dir string) error {
		planted := filepath.Join(f.deps.StateDir, "relays", f.job.ID+".git")
		if _, err := run("", "git", "init", "--quiet", "--bare", planted); err != nil {
			return err
		}
		if _, err := run(planted, "git", "config", "url."+elsewhere+".insteadOf", f.remote); err != nil {
			return err
		}
		return commit("ok")(dir)
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if b, _ := run(elsewhere, "git", "for-each-ref", "--format=%(refname:short)", "refs/heads"); b != "" {
		t.Errorf("the push went to the planted relay's redirect: it has %q", b)
	}
	if b := f.remoteBranches(); !strings.Contains(b, "afk/7-1") {
		t.Errorf("the remote has %q, want afk/7-1 pushed there", b)
	}
}

// Progress lost after the pull request opened is recovered from the tracker.
// Lost before it, the work starts over on a branch nobody has pushed.
func TestLostProgressAfterThePushIsReadFromTheTracker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opened bool
		want   string
	}{
		{"the pull request is open", true, implement.Watching},
		{"no pull request yet", false, implement.Implementing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, newTracker())
			if tc.opened {
				f.tr.prs = append(f.tr.prs, agentPR(40, "afk/7-1"))
			}
			f.setState(implement.Opening)
			if _, err := f.run.Run(context.Background(), "implement-open", f.job.ID); err != nil {
				t.Fatal(err)
			}
			if got := f.now().State; got != tc.want {
				t.Errorf("job in %q, want %q", got, tc.want)
			}
		})
	}
}

// amend is a turn that rewrites the branch's last commit.
func amend(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "ok"), []byte("amended\n"), 0o644); err != nil {
		return err
	}
	if _, err := run(dir, "git", "commit", "--quiet", "--all", "--amend", "-m", "amended"); err != nil {
		return err
	}
	return nil
}

// A session that rewrites a commit the agent already pushed is not a stalled
// job: the push is leased on the agent's own last push, and replaces it.
func TestTheAgentMayRewriteItsOwnPush(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	f.model.then(amend)
	f.setState(implement.Implementing)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors after the amend: %v", errs)
	}
	head, _ := run(f.workspace(), "git", "rev-parse", "HEAD")
	if at, _ := run(f.remote, "git", "rev-parse", "refs/heads/afk/7-1"); at != head {
		t.Errorf("the remote's afk/7-1 is at %q, want the amended head %q", at, head)
	}
	if j := f.now(); j.State != implement.Watching || len(f.tr.opened) != 1 {
		t.Errorf("job in %q with %d pull requests, want %q with 1", j.State, len(f.tr.opened), implement.Watching)
	}
}

// Anyone else's push to the branch is never rewritten: the remote is not
// where the agent left it, so the lease refuses the push.
func TestAPushByAnyoneElseIsNeverRewritten(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	human := filepath.Join(t.TempDir(), "human")
	if _, err := run("", "git", "clone", "--quiet", "--branch", "afk/7-1", f.remote, human); err != nil {
		t.Fatal(err)
	}
	if err := commit("review-fix")(human); err != nil {
		t.Fatal(err)
	}
	if _, err := run(human, "git", "push", "--quiet", "origin", "afk/7-1"); err != nil {
		t.Fatal(err)
	}
	theirs, _ := run(human, "git", "rev-parse", "HEAD")

	f.model.then(amend)
	f.setState(implement.Implementing)
	errs := f.drive()
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "stale info") {
		t.Errorf("errors %v, want the push refused by its lease", errs)
	}
	if at, _ := run(f.remote, "git", "rev-parse", "refs/heads/afk/7-1"); at != theirs {
		t.Errorf("the remote's afk/7-1 is at %q, want the other push, %q, left alone", at, theirs)
	}
}
