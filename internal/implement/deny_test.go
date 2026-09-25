package implement

import (
	"strings"
	"testing"
)

func TestTheDenylistMatchesGlobs(t *testing.T) {
	list := []string{".github/**", "flake.lock", "**/secrets.yaml", "nix/*.nix"}
	for name, want := range map[string]bool{
		".github/workflows/ci.yml":   true,
		".github/CODEOWNERS":         true,
		".github":                    true,
		"flake.lock":                 true,
		"sub/flake.lock":             false,
		"secrets.yaml":               true,
		"hosts/a/secrets.yaml":       true,
		"hosts/a/secrets.yaml.bak":   false,
		"nix/module.nix":             true,
		"nix/deeper/module.nix":      false,
		"github/workflows/ci.yml":    false,
		"internal/implement/deny.go": false,
	} {
		if got := len(denied(list, []string{name})) == 1; got != want {
			t.Errorf("%s denied = %v, want %v", name, got, want)
		}
	}
}

func TestDeniedNamesEachPathOnce(t *testing.T) {
	got := denied([]string{"flake.lock"}, []string{"a", "flake.lock", "b", "flake.lock"})
	if strings.Join(got, ",") != "flake.lock" {
		t.Errorf("denied = %v, want [flake.lock]", got)
	}
}

func TestAMalformedDenylistIsRefused(t *testing.T) {
	for _, list := range [][]string{nil, {""}, {"/etc/passwd"}, {"src/[a"}} {
		if err := ValidDenylist(list); err == nil {
			t.Errorf("ValidDenylist(%q) = nil, want a refusal", list)
		}
	}
	if err := ValidDenylist([]string{".github/**", "**/x", "a/*.go"}); err != nil {
		t.Errorf("ValidDenylist refused a good list: %v", err)
	}
}

// The token reaches git through the environment, for the remote's URL only,
// and not at all for a remote that is not HTTPS.
func TestThePushEnvironmentCarriesTheTokenForTheRemoteOnly(t *testing.T) {
	env := strings.Join(pushEnv("https://github.com/o/n.git", "ghs_secret"), "\n")
	for _, want := range []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/o/n.git.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic eC1hY2Nlc3MtdG9rZW46Z2hzX3NlY3JldA==",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("environment does not contain %q:\n%s", want, env)
		}
	}
	if env := strings.Join(pushEnv("/srv/remote.git", "ghs_secret"), "\n"); strings.Contains(env, "GIT_CONFIG") {
		t.Errorf("a local remote was given the token:\n%s", env)
	}
}
