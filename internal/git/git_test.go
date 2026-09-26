package git_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/git"
)

// The token reaches git through the environment, for the remote's URL only,
// minted afresh for every process.
func TestTheTokenIsInTheEnvironmentForTheRemoteOnly(t *testing.T) {
	mints := 0
	r := git.Remote{URL: "https://github.com/o/n.git", Token: func(context.Context) (string, error) {
		mints++
		return "ghs_secret", nil
	}}
	for range 2 {
		env, err := r.Env(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(env, "\n")
		for _, want := range []string{
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.https://github.com/o/n.git.extraheader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic eC1hY2Nlc3MtdG9rZW46Z2hzX3NlY3JldA==",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("environment does not contain %q:\n%s", want, joined)
			}
		}
	}
	if mints != 2 {
		t.Errorf("the token was minted %d times for two processes, want 2", mints)
	}
}

// No token, none sent: still isolated. A token that cannot be minted is an
// error, not a read without one.
func TestARemoteWithoutATokenSendsNone(t *testing.T) {
	env, err := git.Remote{URL: "/srv/remote.git"}.Env(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(env, "\n"); strings.Contains(joined, "extraheader") || !strings.Contains(joined, "GIT_CONFIG_NOSYSTEM=1") {
		t.Errorf("environment without a token:\n%s", joined)
	}

	failing := git.Remote{URL: "https://github.com/o/n.git", Token: func(context.Context) (string, error) {
		return "", errors.New("502 Bad Gateway")
	}}
	if _, err := failing.Run(context.Background(), "", "ls-remote", failing.URL); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want the mint's", err)
	}
}

// A read given no directory does not take one from the agent's own: a
// repository around the agent's working directory has a configuration that
// neither the global nor the system one shuts out, and it could send the read,
// and its token, elsewhere.
func TestAReadWithNoDirectoryTakesNoRepositoryItIsIn(t *testing.T) {
	root := t.TempDir()
	src, elsewhere, around := filepath.Join(root, "src.git"), filepath.Join(root, "elsewhere.git"), filepath.Join(root, "around")
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", src},
		{"init", "--quiet", "--bare", elsewhere},
		{"init", "--quiet", around},
		{"-C", around, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "--allow-empty", "-m", "elsewhere"},
		{"-C", around, "push", "--quiet", elsewhere, "HEAD:refs/heads/elsewhere"},
		{"-C", around, "config", "url." + elsewhere + ".insteadOf", src},
	} {
		if _, err := git.RunEnv(context.Background(), "", git.Isolated, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(around)

	r := git.Remote{URL: src}
	if heads, err := r.Run(context.Background(), "", "ls-remote", r.URL); err != nil || heads != "" {
		t.Errorf("ls-remote = %q, %v: want the empty remote's nothing", heads, err)
	}
}
