package work_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/work"
)

// The agent's git in a workspace runs nothing the workspace's configuration
// names: no filter driver .gitattributes points at, no hook, no fsmonitor, and
// no command to list an alternate's refs. A session may have written all of
// them before the agent imports, resets, cleans or reads the workspace (#134).
func TestTheAgentsGitRunsNothingAWorkspaceNames(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	src, relay, ws, ran := filepath.Join(root, "src"), filepath.Join(root, "relay.git"), filepath.Join(root, "ws"), filepath.Join(root, "ran")
	if err := os.Mkdir(ran, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, root, "init", "--quiet", "--initial-branch=main", src)
	write(t, filepath.Join(src, ".gitattributes"), "* filter=ev.il\n")
	write(t, filepath.Join(src, "a.txt"), "one\n")
	run(t, src, "add", ".")
	commit(t, src, "one")
	run(t, root, "clone", "--quiet", "--bare", src, relay)
	run(t, root, "clone", "--quiet", src, ws)
	write(t, filepath.Join(src, "b.txt"), "two\n")
	run(t, src, "add", ".")
	commit(t, src, "two")
	run(t, relay, "fetch", "--quiet", src, "+refs/heads/main:refs/heads/main")
	head := run(t, relay, "rev-parse", "refs/heads/main")

	// What a session could leave: every program here marks that it ran.
	touch := func(name string) string { return "touch " + filepath.Join(ran, name) }
	hooks := filepath.Join(root, "hooks")
	if err := os.Mkdir(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"post-checkout", "reference-transaction", "pre-auto-gc"} {
		write(t, filepath.Join(hooks, hook), "#!/bin/sh\n"+touch(hook)+"\n")
		os.Chmod(filepath.Join(hooks, hook), 0o755)
	}
	fsmonitor := filepath.Join(root, "fsmonitor")
	write(t, fsmonitor, "#!/bin/sh\n"+touch("fsmonitor")+"\n")
	os.Chmod(fsmonitor, 0o755)
	alternate := filepath.Join(root, "alternate.git")
	run(t, root, "clone", "--quiet", "--bare", src, alternate)
	write(t, filepath.Join(ws, ".git", "objects", "info", "alternates"), filepath.Join(alternate, "objects")+"\n")
	for _, kv := range [][2]string{
		{"filter.ev.il.clean", touch("clean") + "; cat"},
		{"filter.ev.il.smudge", touch("smudge") + "; cat"},
		{"filter.ev.il.required", "true"},
		{"core.hooksPath", hooks},
		{"core.fsmonitor", fsmonitor},
		{"core.alternateRefsCommand", touch("alternate-refs") + "; true"},
	} {
		run(t, ws, "config", kv[0], kv[1])
	}
	write(t, filepath.Join(ws, "a.txt"), "changed\n")

	// The fixture bites: git as the session would run it runs them.
	run(t, ws, "status", "--porcelain")
	if marks(t, ran) == "" {
		t.Fatal("a plain git status ran nothing the workspace names, so this test shows nothing")
	}
	os.RemoveAll(ran)
	os.Mkdir(ran, 0o755)

	ctx := context.Background()
	if dirty, err := work.Uncommitted(ctx, ws); err != nil || !strings.Contains(dirty, "a.txt") {
		t.Errorf("Uncommitted = %q, %v; want a.txt changed", dirty, err)
	}
	if err := work.Import(ctx, relay, ws, "main", "refs/heads/main", head); err != nil {
		t.Fatal(err)
	}
	if n, err := work.Commits(ctx, ws, head+"~1"); err != nil || n != 1 {
		t.Errorf("Commits = %d, %v; want 1", n, err)
	}
	if b, err := work.BranchOf(ctx, ws); err != nil || b != "main" {
		t.Errorf("BranchOf = %q, %v; want main", b, err)
	}
	write(t, filepath.Join(ws, "untracked.txt"), "x\n")
	if err := work.Reset(ctx, ws, head+"~1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "b.txt")); err == nil {
		t.Error("Reset left the commit it reset past")
	}
	if got := marks(t, ran); got != "" {
		t.Errorf("the agent's git ran %s, which the workspace named", got)
	}
}

// A url.*.insteadOf in the workspace cannot turn Import's fetch from the relay
// into a command, even with the transport that runs one allowed there.
func TestImportRunsNoTransportTheWorkspaceNames(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	src, relay, ws, ran := filepath.Join(root, "src"), filepath.Join(root, "relay.git"), filepath.Join(root, "ws"), filepath.Join(root, "ran")
	run(t, root, "init", "--quiet", "--initial-branch=main", src)
	commit(t, src, "one")
	run(t, root, "clone", "--quiet", "--bare", src, relay)
	run(t, root, "clone", "--quiet", src, ws)
	head := run(t, relay, "rev-parse", "refs/heads/main")
	run(t, ws, "config", "protocol.ext.allow", "always")
	run(t, ws, "config", "url.ext::sh -c touch% "+ran+".insteadOf", relay)

	// The fixture bites: a plain fetch from the relay runs the command.
	exec.Command("git", "-C", ws, "fetch", "--quiet", relay).Run()
	if _, err := os.Stat(ran); err != nil {
		t.Fatal("a plain fetch ran no ext:: command, so this test shows nothing")
	}
	os.Remove(ran)

	// Refused or not, what matters is that nothing ran.
	work.Import(context.Background(), relay, ws, "main", "refs/heads/main", head)
	if _, err := os.Stat(ran); err == nil {
		t.Error("the fetch from the relay ran the workspace's ext:: command")
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, message string) {
	t.Helper()
	run(t, dir, "-c", "user.name=afk", "-c", "user.email=afk@example.invalid", "commit", "--quiet", "--allow-empty", "-m", message)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// marks is what ran, by the markers it left in dir.
func marks(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ", ")
}
