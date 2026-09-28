package implement

import (
	"fmt"

	"github.com/corygyarmathy/afk-agent/internal/glob"
)

// ValidDenylist reports the first pattern that is not a well-formed glob
// (package glob), so a typo is a refusal at startup rather than a pattern
// that silently denies nothing.
func ValidDenylist(denylist []string) error {
	if len(denylist) == 0 {
		return fmt.Errorf("the denylist is empty")
	}
	return glob.Valid("denylist", denylist)
}
