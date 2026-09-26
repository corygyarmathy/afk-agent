// Package git runs the git binary unattended, for the job kinds that work in a
// checkout.
package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Run runs one git command in dir, or in the process's own directory if dir is
// empty, and returns its output trimmed.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	return RunEnv(ctx, dir, nil, args...)
}

// RunEnv is Run with env added to the process's environment.
func RunEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Nothing on stdin and no prompt for credentials: an unattended fetch
	// that wants a password is a failure, not a wait.
	cmd.Env = append(append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Short is a commit's name as a message gives it: the first twelve characters.
func Short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
