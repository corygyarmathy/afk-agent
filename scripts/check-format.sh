#!/usr/bin/env bash
# Check that everything this repository owns is gofmt-clean.
#
# `gofmt -l .` walked the whole tree, which stopped being the right question
# when `vendor/` arrived with the store's SQLite driver (ADR 0004): vendored
# code is upstream's, several files of it are not gofmt-clean, and reformatting
# them would only make `go mod vendor` revert the change. The package list from
# `go list` is the module's own view and excludes `vendor/`, so it is exactly
# the set we are responsible for.
#
# One definition, consumed by the agent, the editor and CI alike - the principle
# ci.yml inherits from `dotfiles` ADR 0008. It checks rather than writes:
# AGENTS.md tells the agent to run gofmt before committing, so formatting lands
# in its commits rather than as drift.
#
#   scripts/check-format.sh          # prints nothing, exits 0, when clean
#   scripts/check-format.sh -d       # also print the diffs
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# `go list` needs to resolve the module; with vendor/ present that is offline.
mapfile -t dirs < <(go list -f '{{.Dir}}' ./...)
if [ ${#dirs[@]} -eq 0 ]; then
	echo "check-format: go list found no packages" >&2
	exit 1
fi

unformatted="$(gofmt -l "${dirs[@]}")"
if [ -n "$unformatted" ]; then
	printf '%s\n' "$unformatted"
	if [ "${1:-}" = "-d" ]; then
		gofmt -d "${dirs[@]}"
	fi
	exit 1
fi
