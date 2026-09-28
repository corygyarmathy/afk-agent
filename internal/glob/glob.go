// Package glob matches repository paths against the operator's globs: the
// denylist's, and the sensitive paths'. Both read a pattern the same way, so a
// glob the operator writes for one means the same in the other.
//
// A pattern is a slash-separated glob. `*`, `?` and `[...]` match within one
// path segment, as path.Match has them, and a segment that is exactly `**`
// matches any number of segments, none included: `.github/**` is everything
// under `.github`, and `**/secrets.yaml` is that file anywhere.
package glob

import (
	"fmt"
	"path"
	"strings"
)

// Matching is the paths among paths that a pattern in patterns matches, in the
// order paths names them, once each.
func Matching(patterns, paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		for _, pattern := range patterns {
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
			// A malformed pattern matches nothing here. Valid refuses
			// one before it gets this far.
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}

// Valid reports the first of patterns that is not a well-formed glob, so a
// typo is a refusal at startup rather than a pattern that silently matches
// nothing. what names the list, for the refusal.
func Valid(what string, patterns []string) error {
	for _, pattern := range patterns {
		if pattern == "" || strings.HasPrefix(pattern, "/") {
			return fmt.Errorf("%s pattern %q is not a path relative to the repository", what, pattern)
		}
		for _, seg := range strings.Split(pattern, "/") {
			// git names a path with none of these, so a pattern that
			// has one matches nothing: `secrets/`, `./flake.lock`.
			if seg == "" || seg == "." || seg == ".." {
				return fmt.Errorf("%s pattern %q has an empty, . or .. segment, and would match no path", what, pattern)
			}
			if seg == "**" {
				continue
			}
			if _, err := path.Match(seg, ""); err != nil {
				return fmt.Errorf("%s pattern %q: %v", what, pattern, err)
			}
		}
	}
	return nil
}
