package work_test

import (
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/work"
)

// A denylist with a malformed glob is a refusal, and a good one is not, so a
// typo cannot silently deny nothing.
func TestValidDenylist(t *testing.T) {
	for _, list := range [][]string{nil, {""}, {"/etc/passwd"}, {"src/[a"}, {"secrets/"}, {"./flake.lock"}, {"a//b"}, {"../x"}, {"a/./b"}} {
		if err := work.ValidDenylist(list); err == nil {
			t.Errorf("ValidDenylist(%q) = nil, want a refusal", list)
		}
	}
	if err := work.ValidDenylist([]string{".github/**", "**/x", "a/*.go"}); err != nil {
		t.Errorf("ValidDenylist refused a good list: %v", err)
	}
}
