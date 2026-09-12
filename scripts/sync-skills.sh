#!/usr/bin/env bash
# Vendor agent skills from corygyarmathy/skills into this repository.
#
# The canonical copy of this script lives in that repository at
# `bin/vendor-skills`. A consumer keeps a copy (conventionally
# `scripts/sync-skills.sh`) because it has to run before anything is vendored.
# `verify` reports when the canonical copy has moved ahead of this one; it
# never overwrites itself, because a script that rewrites itself mid-run is a
# bug waiting for a slow clone.
#
# Usage:
#   sync-skills.sh verify              compare vendored copies against the lock
#   sync-skills.sh pull [--rev REF]    re-vendor, at the locked rev or REF
#   sync-skills.sh pull --add NAME     add a skill, then re-vendor
#
# Exit status: 0 agreement, 1 drift or failure.

set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
lock="$repo_root/skills-lock.json"
self="${BASH_SOURCE[0]}"

die() {
	printf 'sync-skills: %s\n' "$*" >&2
	exit 1
}

[ -f "$lock" ] || die "no skills-lock.json at $repo_root"

jq_lock() { jq -r "$1" "$lock"; }

source_url="$(jq_lock '.source.url')"
vendor_path="$(jq_lock '.vendorPath')"
link_path="$(jq_lock '.linkPath')"
source_prefix="$(jq_lock '.source.prefix')"
locked_rev="$(jq_lock '.source.rev')"

# A skill's hash is over its whole directory, not just SKILL.md: the reference
# files beside it are as load-bearing as the skill body, and a change to one of
# them is exactly the drift worth catching.
hash_dir() {
	local dir=$1
	[ -d "$dir" ] || { printf 'MISSING'; return; }
	(
		cd "$dir"
		find . -type f -print0 |
			LC_ALL=C sort -z |
			while IFS= read -r -d '' f; do
				printf '%s %s\n' "${f#./}" "$(sha256sum "$f" | cut -d' ' -f1)"
			done
	) | sha256sum | cut -d' ' -f1
}

hash_file() { sha256sum "$1" | cut -d' ' -f1; }

skill_names() { jq_lock '.skills | keys[]'; }

verify() {
	local drift=0 name have want
	while read -r name; do
		want="$(jq_lock ".skills[\"$name\"].hash")"
		have="$(hash_dir "$repo_root/$vendor_path/$name")"
		if [ "$have" = MISSING ]; then
			printf 'missing   %s (locked, not vendored)\n' "$name"
			drift=1
		elif [ "$have" != "$want" ]; then
			printf 'drifted   %s (vendored copy differs from the lock)\n' "$name"
			drift=1
		else
			printf 'ok        %s\n' "$name"
		fi
		if [ -n "$link_path" ] && [ ! -e "$repo_root/$link_path/$name" ]; then
			printf 'unlinked  %s (no %s/%s)\n' "$name" "$link_path" "$name"
			drift=1
		fi
	done < <(skill_names)

	# Anything vendored that the lock does not know about is drift in the other
	# direction, and the direction that silently accumulates.
	local dir
	for dir in "$repo_root/$vendor_path"/*/; do
		[ -d "$dir" ] || continue
		name="$(basename "$dir")"
		if [ "$(jq_lock ".skills | has(\"$name\")")" != true ]; then
			printf 'unlocked  %s (vendored, not in the lock)\n' "$name"
			drift=1
		fi
	done

	local want_self
	want_self="$(jq_lock '.script.hash')"
	if [ "$(hash_file "$self")" != "$want_self" ]; then
		printf 'note      this script differs from the copy recorded at the locked rev;\n'
		printf '          re-copy bin/vendor-skills from %s\n' "$source_url"
	fi

	return $drift
}

pull() {
	local rev="$locked_rev" add=""
	while [ $# -gt 0 ]; do
		case $1 in
		--rev)
			rev="${2:?--rev needs a ref}"
			shift 2
			;;
		--add)
			add="${2:?--add needs a skill name}"
			shift 2
			;;
		*) die "unknown argument: $1" ;;
		esac
	done

	# Not local: the EXIT trap fires after this function's scope is gone.
	checkout="$(mktemp -d)"
	trap 'rm -rf "$checkout"' EXIT

	git clone --quiet "$source_url" "$checkout/src"
	git -C "$checkout/src" checkout --quiet "$rev"
	rev="$(git -C "$checkout/src" rev-parse HEAD)"

	local names
	names="$(skill_names)"
	if [ -n "$add" ]; then
		[ -d "$checkout/src/$source_prefix/$add" ] || die "no skill '$add' at $rev"
		names="$(printf '%s\n%s\n' "$names" "$add" | LC_ALL=C sort -u)"
	fi

	mkdir -p "$repo_root/$vendor_path"
	local name
	while read -r name; do
		[ -n "$name" ] || continue
		[ -d "$checkout/src/$source_prefix/$name" ] || die "no skill '$name' at $rev"
		rm -rf "${repo_root:?}/$vendor_path/$name"
		cp -r "$checkout/src/$source_prefix/$name" "$repo_root/$vendor_path/$name"
		printf 'vendored  %s\n' "$name"
	done <<<"$names"

	# The harness reads skills from `linkPath`, but a symlinked *directory* of
	# skills is not followed, so each skill is linked individually and the
	# vendored tree stays the one copy under version control.
	if [ -n "$link_path" ]; then
		mkdir -p "$repo_root/$link_path"
		local rel
		rel="$(realpath --relative-to="$repo_root/$link_path" "$repo_root/$vendor_path")"
		while read -r name; do
			[ -n "$name" ] || continue
			ln -sfn "$rel/$name" "$repo_root/$link_path/$name"
		done <<<"$names"
	fi

	local new script_hash
	script_hash="$(hash_file "$checkout/src/bin/vendor-skills")"
	new="$(jq -n \
		--slurpfile lock "$lock" \
		--arg rev "$rev" \
		--arg scripthash "$script_hash" \
		'$lock[0] | .source.rev = $rev | .script.hash = $scripthash')"
	while read -r name; do
		[ -n "$name" ] || continue
		new="$(printf '%s' "$new" | jq \
			--arg n "$name" \
			--arg p "$source_prefix/$name" \
			--arg h "$(hash_dir "$repo_root/$vendor_path/$name")" \
			'.skills[$n] = {path: $p, hash: $h}')"
	done <<<"$names"
	printf '%s\n' "$new" >"$lock"

	printf 'locked    %s at %s\n' "$(jq_lock '.source.repo')" "$rev"
}

case "${1:-verify}" in
verify) verify ;;
pull)
	shift
	pull "$@"
	;;
*) die "usage: sync-skills.sh [verify | pull [--rev REF] [--add NAME]]" ;;
esac
