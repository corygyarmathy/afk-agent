package work

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
)

// Conflict is a replay that could not be made: the commit that would not go
// onto the new head, and why.
type Conflict struct {
	// Commit is the work's commit that stopped the replay, and Subject its
	// first line.
	Commit  string
	Subject string

	// Paths is where it conflicts with the new head. Empty for a commit
	// that is not replayed at all: a merge commit, or one with no parent,
	// which a merge of an unrelated history brings in.
	Paths []string

	// Parents is how many parents Commit has.
	Parents int
}

// Said is what a hand-back says about a replay onto onto that conflicted.
func (c Conflict) Said(onto string) string {
	switch {
	case len(c.Paths) > 0:
		return fmt.Sprintf("`%s` (%s) conflicts with `%s` in %s.", git.Short(c.Commit), c.Subject, git.Short(onto), Quoted(c.Paths))
	case c.Parents == 0:
		return fmt.Sprintf("`%s` (%s) has no parent, and the agent does not replay a history with a root of its own onto `%s`.", git.Short(c.Commit), c.Subject, git.Short(onto))
	}
	return fmt.Sprintf("`%s` (%s) is a merge commit, and the agent does not replay one onto `%s`.", git.Short(c.Commit), c.Subject, git.Short(onto))
}

// Replay puts the work's own commits - those on the workspace's branch that
// onto does not have - on top of onto, and leaves the workspace's branch
// checked out at the result, which it returns. onto must already be in the
// relay (FetchInto), which is where the replay is made.
//
// The relay is a repository the agent owns and has isolated, and the replay
// runs no hook and reads no configuration the model wrote: the commits are
// copied out of the workspace as Relay copies them, each is merged onto the
// last with `git merge-tree` and written with `git commit-tree`, and the result
// is brought back with Import. Nothing here carries the token.
//
// Only the work's commits are replayed. Everything onto has - the head the work
// started from, and anyone else's push on top of it - is kept as it is. A
// commit that conflicts, a merge commit, or a commit with no parent stops the
// replay with nothing in the workspace changed. A workspace already on top of
// onto is left as it is.
func Replay(ctx context.Context, workspace, relayDir, branch, onto string) (head string, conflict *Conflict, err error) {
	const work, replayed = "refs/afk/work", "refs/afk/replayed"
	if _, err := git.RunEnv(ctx, relayDir, git.Isolated, "fetch", "--quiet", "--no-tags", "--force", workspace, "+refs/heads/"+branch+":"+work); err != nil {
		return "", nil, err
	}
	tip, err := git.RunEnv(ctx, relayDir, git.Isolated, "rev-parse", work)
	if err != nil {
		return "", nil, err
	}
	base, err := MergeBase(ctx, relayDir, onto, tip)
	if err != nil {
		return "", nil, err
	}
	if base == onto {
		return tip, nil, nil
	}
	list, err := git.RunEnv(ctx, relayDir, git.Isolated, "rev-list", "--reverse", "--topo-order", "--parents", base+".."+tip)
	if err != nil {
		return "", nil, err
	}

	head = onto
	for _, line := range strings.Split(list, "\n") {
		ids := strings.Fields(line)
		if len(ids) == 0 {
			continue
		}
		commit := ids[0]
		if len(ids) != 2 {
			return "", &Conflict{Commit: commit, Subject: subject(ctx, relayDir, commit), Parents: len(ids) - 1}, nil
		}
		tree, paths, err := mergeTree(ctx, relayDir, ids[1], head, commit)
		if err != nil {
			return "", nil, err
		}
		if len(paths) > 0 {
			return "", &Conflict{Commit: commit, Subject: subject(ctx, relayDir, commit), Paths: paths, Parents: 1}, nil
		}
		if head, err = recommit(ctx, relayDir, commit, tree, head); err != nil {
			return "", nil, err
		}
	}
	if _, err := git.RunEnv(ctx, relayDir, git.Isolated, "update-ref", replayed, head); err != nil {
		return "", nil, err
	}
	return head, nil, Import(ctx, relayDir, workspace, branch, replayed, head)
}

// mergeTree is commit's change from parent, merged onto head: the tree it
// makes, or the paths it conflicts in. Nothing is written but objects.
func mergeTree(ctx context.Context, dir, parent, head, commit string) (tree string, conflicts []string, err error) {
	out, code, err := gitStatus(ctx, dir, git.Isolated, nil, "merge-tree", "--write-tree", "--no-messages", "--name-only", "-z", "--merge-base="+parent, head, commit)
	if err != nil {
		return "", nil, err
	}
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	if code == 0 {
		return fields[0], nil, nil
	}
	seen := map[string]bool{}
	for _, p := range fields[1:] {
		if p != "" && !seen[p] {
			seen[p] = true
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) == 0 {
		// A conflict with no path, which git does not report; a replay
		// that stops still says it stopped.
		conflicts = []string{"(git named no path)"}
	}
	return "", conflicts, nil
}

// recommit writes tree as commit's replay onto parent: its author, its
// committer and its message, committed now.
func recommit(ctx context.Context, dir, commit, tree, parent string) (string, error) {
	who, err := git.Output(ctx, dir, git.Isolated, "log", "-1", "--format=%an%x00%ae%x00%aI%x00%cn%x00%ce", commit)
	if err != nil {
		return "", err
	}
	f := strings.Split(strings.TrimSuffix(who, "\n"), "\x00")
	if len(f) != 5 {
		return "", fmt.Errorf("reading %s's author and committer: %q", git.Short(commit), who)
	}
	message, err := git.Output(ctx, dir, git.Isolated, "log", "-1", "--format=%B", commit)
	if err != nil {
		return "", err
	}
	env := append(append([]string{}, git.Isolated...),
		"GIT_AUTHOR_NAME="+f[0], "GIT_AUTHOR_EMAIL="+f[1], "GIT_AUTHOR_DATE="+f[2],
		"GIT_COMMITTER_NAME="+f[3], "GIT_COMMITTER_EMAIL="+f[4],
	)
	out, code, err := gitStatus(ctx, dir, env, strings.NewReader(message), "commit-tree", tree, "-p", parent, "-F", "-")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git commit-tree exited %d", code)
	}
	return strings.TrimSpace(out), nil
}

// gitStatus runs one git command whose exit status is part of its answer: its
// stdout exactly as written, and the status it exited with. err is for a git
// that could not be run or was cancelled, and carries its stderr.
func gitStatus(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) (out string, code int, err error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return "", 0, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return stdout.String(), 1, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), 0, nil
}

// subject is a commit's first line, or its name if git cannot say.
func subject(ctx context.Context, dir, commit string) string {
	s, err := git.RunEnv(ctx, dir, git.Isolated, "log", "-1", "--format=%s", commit)
	if err != nil || s == "" {
		return git.Short(commit)
	}
	return s
}
