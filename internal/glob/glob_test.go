package glob

import (
	"strings"
	"testing"
)

func TestPatternsMatchGlobs(t *testing.T) {
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
		if got := len(Matching(list, []string{name})) == 1; got != want {
			t.Errorf("%s matched = %v, want %v", name, got, want)
		}
	}
}

func TestMatchingNamesEachPathOnce(t *testing.T) {
	got := Matching([]string{"flake.lock"}, []string{"a", "flake.lock", "b", "flake.lock"})
	if strings.Join(got, ",") != "flake.lock" {
		t.Errorf("Matching = %v, want [flake.lock]", got)
	}
}

func TestAMalformedPatternIsRefused(t *testing.T) {
	for _, list := range [][]string{{""}, {"/etc/passwd"}, {"src/[a"}, {"secrets/"}, {"./flake.lock"}, {"a//b"}, {"../x"}, {"a/./b"}} {
		if err := Valid("test", list); err == nil {
			t.Errorf("Valid(%q) = nil, want a refusal", list)
		}
	}
	if err := Valid("test", []string{".github/**", "**/x", "a/*.go"}); err != nil {
		t.Errorf("Valid refused a good list: %v", err)
	}
}
