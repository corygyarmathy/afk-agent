package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
)

// inWorkspace runs one git command in a workspace, as the agent: with none of
// the programs the workspace's configuration names run, since that
// configuration is the model's to write and a session may already have run
// there.
//
// Isolation (git.Isolated) shuts out the global and system configuration; this
// shuts out what the repository's own configuration would otherwise have the
// agent's git run:
//
//   - hooks (core.hooksPath), and an fsmonitor (core.fsmonitor);
//   - every filter driver it defines - clean, smudge and process - which
//     .gitattributes, also the model's, names, and which checkout, reset and
//     status run;
//   - the command a fetch runs to list an alternate's refs
//     (core.alternateRefsCommand), which objects/info/alternates can make it
//     read;
//   - every transport but a local one (GIT_ALLOW_PROTOCOL), so that a
//     url.*.insteadOf cannot turn a fetch from the relay into an ext:: command;
//   - submodule recursion, which would fetch and check out as well.
//
// The overrides are passed in the environment rather than as -c, because a
// filter driver's name is the model's and may hold an "=".
//
// Nothing else the agent does in a workspace reaches the remote: that is
// git.Remote's to refuse (Untrusted).
func inWorkspace(ctx context.Context, dir string, args ...string) (string, error) {
	env, err := hardened(ctx, dir)
	if err != nil {
		return "", err
	}
	return git.RunEnv(ctx, dir, env, args...)
}

// hardened is the environment inWorkspace runs git in, for the workspace at
// dir.
func hardened(ctx context.Context, dir string) ([]string, error) {
	overrides := [][2]string{
		{"core.hooksPath", os.DevNull},
		{"core.fsmonitor", "false"},
		{"core.alternateRefsCommand", "true"},
		{"submodule.recurse", "false"},
		{"fetch.recurseSubmodules", "false"},
	}
	drivers, err := filterDrivers(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, name := range drivers {
		for _, key := range []string{"clean", "smudge", "process"} {
			overrides = append(overrides, [2]string{"filter." + name + "." + key, ""})
		}
		// A required filter with no command fails the checkout rather
		// than being skipped.
		overrides = append(overrides, [2]string{"filter." + name + ".required", "false"})
	}

	env := append([]string{}, git.Isolated...)
	env = append(env, "GIT_ALLOW_PROTOCOL=file", fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(overrides)))
	for i, o := range overrides {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, o[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, o[1]))
	}
	return env, nil
}

// filterDrivers is the name of every filter driver the configuration at dir
// defines. Reading the configuration runs nothing.
func filterDrivers(ctx context.Context, dir string) ([]string, error) {
	out, err := git.Output(ctx, dir, git.Isolated, "config", "--null", "--name-only", "--get-regexp", `^filter\.`)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		// No key matched.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, key := range strings.Split(out, "\x00") {
		// filter.<name>.<variable>, where the name may hold dots of its
		// own and the variable holds none.
		rest, ok := strings.CutPrefix(key, "filter.")
		if !ok {
			continue
		}
		i := strings.LastIndex(rest, ".")
		if i <= 0 {
			continue
		}
		if name := rest[:i]; !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}
