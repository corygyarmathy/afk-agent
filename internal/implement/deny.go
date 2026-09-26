package implement

import (
	"fmt"
	"path"
	"strings"
)

// denied is the paths among paths that a pattern in denylist matches, in the
// order paths names them, once each.
//
// A pattern is a slash-separated glob. `*`, `?` and `[...]` match within one
// path segment, as path.Match has them, and a segment that is exactly `**`
// matches any number of segments, none included: `.github/**` is everything
// under `.github`, and `**/secrets.yaml` is that file anywhere.
func denied(denylist, paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		for _, pattern := range denylist {
			if matches(pattern, p) {
				seen[p] = true
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func matches(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			rest := pattern[1:]
			for i := 0; i <= len(name); i++ {
				if matchSegments(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pattern[0], name[0]); err != nil || !ok {
			// A malformed pattern matches nothing here. ValidDenylist
			// refuses one before it gets this far.
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}

// ValidDenylist reports the first pattern that is not a well-formed glob, so
// a typo is a refusal at startup rather than a pattern that silently denies
// nothing.
func ValidDenylist(denylist []string) error {
	if len(denylist) == 0 {
		return fmt.Errorf("the denylist is empty")
	}
	for _, pattern := range denylist {
		if pattern == "" || strings.HasPrefix(pattern, "/") {
			return fmt.Errorf("denylist pattern %q is not a path relative to the repository", pattern)
		}
		for _, seg := range strings.Split(pattern, "/") {
			// git names a path with none of these, so a pattern that
			// has one matches nothing: `secrets/`, `./flake.lock`.
			if seg == "" || seg == "." || seg == ".." {
				return fmt.Errorf("denylist pattern %q has an empty, . or .. segment, and would match no path", pattern)
			}
			if seg == "**" {
				continue
			}
			if _, err := path.Match(seg, ""); err != nil {
				return fmt.Errorf("denylist pattern %q: %v", pattern, err)
			}
		}
	}
	return nil
}
