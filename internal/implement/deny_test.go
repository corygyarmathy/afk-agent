package implement

import (
	"testing"
)

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
