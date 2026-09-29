package work_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/work"
)

// A revision that merged in an unrelated history brings a commit with no
// parent into the replay, which comes before the merge. The replay stops on
// it, and says it has no parent rather than calling it a merge commit.
func TestAReplayStopsOnACommitWithNoParentAndSaysSo(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	src, relay, ws := filepath.Join(root, "src"), filepath.Join(root, "relay.git"), filepath.Join(root, "ws")
	run(t, root, "init", "--quiet", "--initial-branch=main", src)
	commit(t, src, "read")
	run(t, root, "clone", "--quiet", "--bare", src, relay)
	run(t, root, "clone", "--quiet", src, ws)
	commit(t, src, "theirs")
	run(t, relay, "fetch", "--quiet", src, "+refs/heads/main:refs/heads/main")
	onto := run(t, relay, "rev-parse", "refs/heads/main")
	run(t, ws, "switch", "--quiet", "--orphan", "unrelated")
	commit(t, ws, "a root of its own")
	rootCommit := run(t, ws, "rev-parse", "HEAD")
	run(t, ws, "switch", "--quiet", "main")
	run(t, ws, "-c", "user.name=afk", "-c", "user.email=afk@example.invalid", "merge", "--quiet", "--allow-unrelated-histories", "-m", "merge", "unrelated")
	before := run(t, ws, "rev-parse", "HEAD")

	_, conflict, err := work.Replay(context.Background(), ws, relay, "main", onto)
	if err != nil {
		t.Fatal(err)
	}
	if conflict == nil || conflict.Commit != rootCommit {
		t.Fatalf("conflict = %+v, want the replay stopped on %s", conflict, rootCommit)
	}
	said := conflict.Said(onto)
	if !strings.Contains(said, "has no parent") || strings.Contains(said, "merge commit") {
		t.Errorf("the hand-back says %q, want it to say the commit has no parent", said)
	}
	if got := run(t, ws, "rev-parse", "HEAD"); got != before {
		t.Errorf("the workspace moved to %s, want it left at %s", got, before)
	}
}
