#!/usr/bin/env bash
# Run `go test` with no network available.
#
# AGENTS.md says tests run offline. That is a constraint on the tests, so it is
# enforced here rather than trusted: a test that reaches GitHub, a model or a
# live process passes on a developer's laptop and then fails, or worse succeeds,
# on an unattended runner at 04:00.
#
# CI calls this script rather than `go test` directly, so the gate and the
# developer run the same thing (the single-pipeline principle that `dotfiles`
# ADR 0008 settled for formatting).
#
# Loopback stays up. A test serving itself over 127.0.0.1 is not network access,
# and forbidding it would rule out httptest for no gain.
#
# Arguments are `go test` flags, and ./... is always appended after them, so the
# script never has to tell a flag's value from a package path. Naming a package
# as well is harmless - `go test` deduplicates the list - but -run is the way to
# narrow a run here:
#
#   scripts/offline-test.sh
#   scripts/offline-test.sh -run TestMain_ExitCodes -v
set -euo pipefail

# The toolchain must not reach out either. Every dependency is in `vendor/`
# (ADR 0004), so there is nothing legitimate to fetch; GOPROXY=off turns an
# import that nobody vendored into a failure here instead of a download, and
# GOTOOLCHAIN=local reports "go.mod requires a newer Go" rather than trying to
# fetch that newer Go.
#
# -mod=vendor, not -mod=readonly. Go already defaults to vendor when the
# directory is present, but setting -mod explicitly overrides that default, and
# -mod=readonly sends the build to the module cache instead - which on a cold
# CI runner is a download, and with GOPROXY=off is a failure. Naming vendor here
# keeps the two consistent.
export GOFLAGS="${GOFLAGS:-} -mod=vendor"
export GOPROXY=off
export GOTOOLCHAIN=local

# Compile with the network still present, so a failure to isolate below is not
# confused with a build failure, and so the build cache is written as the
# invoking user rather than through the namespace's uid mapping. Without "$@":
# those are `go test` flags, and `go build` rejects most of them.
go build ./...

# The environment is passed explicitly rather than inherited, because the
# privileged path below crosses `sudo` and `runuser`, each of which resets it -
# on a GitHub runner that silently cost PATH, so `go` resolved to an older
# toolchain than go.mod asks for and the run died trying to download one from
# inside the namespace it had just been sealed into. Resolving `go` and its
# caches out here, where the environment is still ours, is what keeps the two
# paths running the same toolchain against the same caches.
go_env=(
	"PATH=$PATH"
	"HOME=$HOME"
	"GOFLAGS=$GOFLAGS"
	"GOPROXY=$GOPROXY"
	"GOTOOLCHAIN=$GOTOOLCHAIN"
	"GOCACHE=$(go env GOCACHE)"
	"GOMODCACHE=$(go env GOMODCACHE)"
)
# `env` and `go` by absolute path for the same reason: the inner shell may have
# a PATH neither of us chose, and that PATH is exactly what the runner failure
# above was.
test_cmd=("$(command -v env)" "${go_env[@]}" "$(command -v go)" test "$@" ./...)

# `ip link set lo up` is best-effort: a host without iproute2 still runs the
# tests, it just has no loopback, and saying so beats a mystery dial failure.
readonly inner='ip link set lo up 2>/dev/null || printf "offline-test: could not bring loopback up; tests using 127.0.0.1 will fail\n" >&2
exec "$@"'

if unshare --user --map-root-user --net true 2>/dev/null; then
	# Unprivileged: a user namespace makes us root inside it, which is enough
	# to own a fresh network namespace that has no route anywhere.
	printf 'offline-test: network isolated by an unprivileged user namespace\n' >&2
	exec unshare --user --map-root-user --net -- \
		bash -c "$inner" offline-test "${test_cmd[@]}"
fi

if sudo -n true 2>/dev/null; then
	# Fallback for hosts that refuse unprivileged user namespaces - Ubuntu's
	# AppArmor restriction is the one that bites, and a GitHub runner is such a
	# host. Root owns the namespace and hands the tests straight back to the
	# invoking user, so nothing runs as root and the caches keep their owner.
	printf 'offline-test: network isolated by sudo unshare --net\n' >&2
	exec sudo -n unshare --net -- \
		bash -c "$inner" offline-test runuser -u "$(id -un)" -- "${test_cmd[@]}"
fi

# Never fall through to a networked `go test`: a gate that quietly stops gating
# is worse than no gate, because it reports success.
printf 'offline-test: cannot isolate the network - unprivileged user namespaces are unavailable and there is no passwordless sudo.\n' >&2
printf 'offline-test: refusing to run the tests with the network up. Run `go test ./...` directly if you accept that.\n' >&2
exit 1
