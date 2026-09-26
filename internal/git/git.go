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
		return "", fmt.Errorf("git %s: %w: %s", command(args), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// command is the git command args run: the first argument that is not an
// option to git itself.
func command(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-c" || a == "-C":
			i++
		case !strings.HasPrefix(a, "-"):
			return a
		}
	}
	return ""
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

	// Refused is told the token a process carried when the remote answered
	// it with a 401, so that the next process mints another rather than
	// presenting a revoked one again (ADR 0005 §2). Nil tells nothing.
	Refused func(token string)
}

// refused is what a git process prints when the remote answered its token with
// a 401.
//
// git reports no HTTP status, and its message for a 401 is not one about
// authentication: with no terminal to prompt on, it says it could not read a
// username. What it does do on a 401, and on nothing else, is ask a credential
// helper for a username and password. The helper env configures for the
// remote's URL gives none; it prints this, so a refusal is read from a line the
// agent wrote rather than from git's wording, in whatever language git speaks.
const refused = "afk: the remote refused the token"

// Run runs one git command that reaches the remote, in dir, isolated from the
// global and system configuration, with the token for the remote's URL. The
// command names the remote by its URL: a name would be looked up in a
// configuration the session may have written.
//
// An empty dir is the root, not the agent's own directory: git takes the
// configuration of whatever repository it runs in, which isolation does not
// shut out, and a repository around the agent's directory could send the
// token elsewhere. A path in args is therefore absolute.
//
// A process the remote refused with its token tells Refused, and still fails:
// whether to try again is the caller's to decide, as it is for a request.
func (r Remote) Run(ctx context.Context, dir string, args ...string) (string, error) {
	env, token, err := r.env(ctx)
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = "/"
	}
	out, err := RunEnv(ctx, dir, env, args...)
	if err != nil && token != "" && r.Refused != nil && strings.Contains(err.Error(), refused) {
		r.Refused(token)
	}
	return out, err
}

// env is the environment a git process that reaches the remote runs with, and
// the token it carries: empty if none. It is git's own
// configuration-by-environment, so the token is not an argument (visible in ps
// to everyone) or a file. The header is scoped to the remote's URL, so a request
// to any other URL does not carry it. So is the credential helper that reports a
// refusal of it (refused): it supplies no credential, and is asked only when the
// remote has answered the header with a 401. Unexported, so that nothing runs git
// with the token except Run, which acts on that report.
func (r Remote) env(ctx context.Context) ([]string, string, error) {
	env := append([]string{"GIT_TERMINAL_PROMPT=0"}, Isolated...)
	if r.Token == nil {
		return env, "", nil
	}
	token, err := r.Token(ctx)
	if err != nil || token == "" {
		return env, "", err
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return append(env,
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http."+r.URL+".extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
		"GIT_CONFIG_KEY_1=credential."+r.URL+".helper",
		"GIT_CONFIG_VALUE_1=!echo '"+refused+"' >&2; :",
	), token, nil
}
