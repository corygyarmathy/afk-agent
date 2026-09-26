// Package git runs the git binary unattended, for the job kinds that work in a
// checkout.
package git

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
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

// Isolated is the environment that keeps git to the configuration of the
// repository it runs in, and nothing from the agent user's home or the system:
// a session running as the agent's user could write either.
var Isolated = []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}

// Remote is the tracker's repository, and the credential that reaches it.
//
// Every read of it and every push to it carries the token the same way: in the
// environment of the one git process that needs it, minted as that process
// starts, and never in a file. A clone therefore leaves nothing of the token in
// the workspace, and none is held across a model run to expire in it.
type Remote struct {
	// URL is the tracker's clone URL, or a local path in a test.
	URL string

	// Token is the App's installation token. Nil sends none, which is a
	// local remote in a test.
	Token func(ctx context.Context) (string, error)
}

// Run runs one git command that reaches the remote, in dir, isolated from the
// global and system configuration, with the token for the remote's URL. The
// command names the remote by its URL: a name would be looked up in a
// configuration the session may have written.
func (r Remote) Run(ctx context.Context, dir string, args ...string) (string, error) {
	env, err := r.Env(ctx)
	if err != nil {
		return "", err
	}
	return RunEnv(ctx, dir, env, args...)
}

// Env is the environment a git process that reaches the remote runs with: git's
// own configuration-by-environment, so the token is not an argument (visible in
// ps to everyone) or a file. The header is scoped to the remote's URL, so a
// redirect elsewhere does not carry it.
func (r Remote) Env(ctx context.Context) ([]string, error) {
	env := append([]string{"GIT_TERMINAL_PROMPT=0"}, Isolated...)
	if r.Token == nil {
		return env, nil
	}
	token, err := r.Token(ctx)
	if err != nil || token == "" {
		return env, err
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http."+r.URL+".extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
	), nil
}
