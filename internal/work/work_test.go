package work_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/work"
)

// A denylist with a malformed glob is a refusal, and a good one is not, so a
// typo cannot silently deny nothing.
func TestValidDenylist(t *testing.T) {
	for _, list := range [][]string{nil, {""}, {"/etc/passwd"}, {"src/[a"}, {"secrets/"}, {"./flake.lock"}, {"a//b"}, {"../x"}, {"a/./b"}} {
		if err := work.ValidDenylist(list); err == nil {
			t.Errorf("ValidDenylist(%q) = nil, want a refusal", list)
		}
	}
	if err := work.ValidDenylist([]string{".github/**", "**/x", "a/*.go"}); err != nil {
		t.Errorf("ValidDenylist refused a good list: %v", err)
	}
}

// Ancestor says no only when git does. A check it could not make - a
// cancelled context - is an error, because a no hands the work back and
// clears it.
func TestAncestorSaysNoOnlyWhenGitDoes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, args := range [][]string{
		{"init", "--quiet", dir},
		{"-C", dir, "-c", "user.name=afk", "-c", "user.email=afk@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "one"},
		{"-C", dir, "-c", "user.name=afk", "-c", "user.email=afk@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "two"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ctx := context.Background()
	if ok, err := work.Ancestor(ctx, dir, "HEAD~1", "HEAD"); !ok || err != nil {
		t.Errorf("Ancestor(HEAD~1, HEAD) = %v, %v; want true", ok, err)
	}
	if ok, err := work.Ancestor(ctx, dir, "HEAD", "HEAD~1"); ok || err != nil {
		t.Errorf("Ancestor(HEAD, HEAD~1) = %v, %v; want false, no error", ok, err)
	}
	if _, err := work.Ancestor(ctx, dir, "HEAD", "no-such-ref"); err == nil {
		t.Error("Ancestor of a ref that is not there is not an error")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := work.Ancestor(cancelled, dir, "HEAD~1", "HEAD"); err == nil {
		t.Error("Ancestor with a cancelled context is not an error")
	}

	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := work.HasCommit(ctx, dir, strings.TrimSpace(string(head))); !ok || err != nil {
		t.Errorf("HasCommit(HEAD) = %v, %v; want true", ok, err)
	}
	if ok, err := work.HasCommit(ctx, dir, strings.Repeat("ab", 20)); ok || err != nil {
		t.Errorf("HasCommit of a commit that is not there = %v, %v; want false, no error", ok, err)
	}
}
