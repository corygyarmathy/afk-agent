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
	for _, list := range [][]string{nil, {""}, {"/etc/passwd"}, {"src/[a"}, {"secrets/"}, {"./flake.lock"}, {"a//b"}, {"../x"}, {"a/./b"}} {
		if err := ValidDenylist(list); err == nil {
			t.Errorf("ValidDenylist(%q) = nil, want a refusal", list)
		}
	}
	if err := ValidDenylist([]string{".github/**", "**/x", "a/*.go"}); err != nil {
		t.Errorf("ValidDenylist refused a good list: %v", err)
	}
}
