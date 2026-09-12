# ADR 0004: The job store is SQLite, this repository's one vendored dependency

- **Status:** Proposed
- **Date:** 2026-09-12
- **Related Artefacts:**
    - Implements: [ADR 0001](0001-a-go-state-machine-in-its-own-repository.md) §5, §6, §7 - the state split, the disposable store, and the claim/lease distinction.
    - Specified by: issue #1, "The job store: leases, scheduling, and idempotency history".
    - Amends: `AGENTS.md`, whose "no third-party dependencies" rule this is the exception to.

## Context

Issue #1 specifies the job store as SQLite in the state directory, in WAL mode,
with embedded migrations. `AGENTS.md` says this repository has no third-party
dependencies and that adding one is an ADR-sized decision. Go's standard library
ships `database/sql` but no SQLite driver, so the two cannot both hold: either
the store is not SQLite, or the rule gets an exception. This document is that
decision being made rather than assumed.

The store is small and is expected to stay small at today's shape - one
operator's tracker, tens of jobs, a write per transition rather than per request.
That was the basis of an initial recommendation to write the store by hand
against a single file, using an advisory lock for the single writer and an
atomic rename as the commit. At that size the argument holds on its own terms:
the whole dataset fits in memory with room to spare, so rewriting all of it per
commit costs nothing measurable, and the machinery a database exists to provide -
journals, page allocation, B-trees - is machinery for a problem this store does
not have.

It answered the wrong question. The size of the dataset today is not a
constraint anyone has committed to; this agent is experimental and its ultimate
shape is not yet known. Two forces settle it against the hand-written store, and
neither is about performance.

The first is that crash behaviour is this project's critical path rather than a
detail of it. ADR 0001's entire premise is that a crash, a restart or a host
upgrade costs at most one transition, unattended, at 04:00. A hand-written store
puts the correctness of that promise in fsync ordering, directory syncs and
partial-write recovery that this repository would own and would have to get
right. SQLite has had those edges found by other people for twenty years. Buying
tested durability for the one property the design is built to guarantee is a
better trade than saving a dependency.

The second is that query flexibility is what cannot be retrofitted cheaply. A
question nobody has thought of yet is an index and a `SELECT` against a
database; against an in-memory structure it is a new method, a new loop and a
new release. The same applies to the operator at 04:00, for whom
`sqlite3 state.db '...'` is a real tool and a serialised blob is not - which
speaks to the cost ADR 0001 already books, that "what is happening right now"
moves from `systemctl status` to a query against the store.

## Decision

**1. The job store is SQLite**, in a file in the state directory, in WAL mode,
with `synchronous=FULL` and its schema built by embedded migrations that the
binary applies on open. There is no migration step an operator has to remember.

`synchronous=FULL` rather than WAL's usual `NORMAL`, because what `NORMAL` risks
losing to a power cut is the last few transactions, and those transactions hold
idempotency keys. Writes happen once per transition, so the extra fsync costs
nothing this agent will notice.

**2. The driver is `modernc.org/sqlite`, and it is vendored.** Pure Go rather
than a cgo binding, so the binary stays a single static artefact that
cross-compiles - a property the NixOS module that packages this agent depends
on. Vendored rather than fetched, so that the tree is hermetic: `vendor/` is why
`scripts/offline-test.sh` can keep `GOPROXY=off`, which is what turns an
unvendored import into a loud failure instead of a download.

**3. This is the repository's one dependency, and the bar for a second is this
document.** The rule in `AGENTS.md` changes from "none" to "one, and adding
another is an ADR". A bright line at zero was easy to police and is now gone; the
replacement is that the standard library is the default answer and an exception
is argued in writing.

**4. The store sits behind a Go interface, and its tests are written against
that interface rather than against SQLite.** Not as a hedge to swap the driver
out - it is ordinary design, and it is what lets a transition (#2) be tested
against a store it constructs rather than a file it has to clean up. ADR 0001 §6
makes the store disposable, so a future change of backing would be a new
implementation of the same interface with no data migration behind it.

**5. Nothing re-derivable from GitHub may be stored, and a test enforces it.**
ADR 0001 §5 splits state ownership, and issue #1 names that invariant as the one
that decays quietly. The schema's columns are checked against an allow-list that
carries, per column, the reason it is run state rather than work state. A column
added to save a round trip fails the build, which is the point: the failure is
the conversation about whether it belongs.

## Consequences

**Positive**

- Crash-anywhere-and-resume rests on SQLite's transactional guarantees rather
  than on durability code this repository would own and would have to defend.
- Reserving an idempotency key in the same transaction as the state change it
  belongs to is a `BEGIN`/`COMMIT`, which is what issue #1 asks for and is not
  expressible any other way through the store's API.
- A new question about the store's contents is an index and a query, and the
  operator gets `sqlite3` as a diagnostic tool for free.
- Taking a lease is a single `UPDATE ... WHERE` that selects and leases in one
  statement, so it is atomic under concurrent workers without this repository
  writing a locking protocol.

**Negative**

- `vendor/` is 137 MB across 2,003 files, of which `modernc.org/libc` is 71 MB
  and `modernc.org/sqlite` 57 MB - mostly C transpiled to Go, for every platform
  rather than the one this agent runs on. Every clone pays that, and diffs and
  code search have to learn to ignore it. Measured on 2026-09-12 at
  `modernc.org/sqlite@v1.58.0`.
- The zero-dependency rule was self-enforcing and no longer is. "One" is a
  number that only holds if someone keeps saying no.
- The dependency is a transpilation of a C library rather than a Go
  reimplementation, so a bug in it is a bug in generated code, in a place this
  repository cannot easily reason about.
- Two checks changed shape to accommodate `vendor/`: the formatter now runs over
  the module's own packages rather than the whole tree, and
  `scripts/offline-test.sh` must say `-mod=vendor` rather than `-mod=readonly`,
  because the latter overrides Go's vendor default and sends a cold CI runner to
  the network. Both are the kind of incidental complexity a dependency brings
  with it.

## Alternatives considered

- **A hand-written store: one file, an advisory lock for the single writer, an
  atomic rename as the commit.** Keeps the dependency count at zero, and at
  today's dataset size is both correct and cheap - the whole store fits in
  memory, so rewriting it per commit is free. Rejected on the two grounds in
  *Context*: it puts crash correctness in code this repository owns, at the one
  point where the design promises the most, and it makes every future question
  about the data a code change. The size argument it rests on is an observation
  about today rather than a constraint anyone has agreed to.
- **`modernc.org/sqlite` pinned by `go.sum` but not vendored.** Keeps the tree
  small - no 137 MB, no 2,003 files. Rejected because `scripts/offline-test.sh`
  would have to fetch dependencies in a networked step before sealing the
  namespace, which means relaxing the `GOPROXY=off` guard that makes an
  unvendored import fail loudly. Trading away a working guard to save disk is
  the wrong direction when disk is the cheap resource.
- **`github.com/mattn/go-sqlite3`, the cgo binding.** Far less vendored source,
  and it is the more widely used driver. Rejected because cgo costs the static
  binary and easy cross-compilation, which the NixOS module packaging this agent
  relies on, and because a C toolchain then becomes a build requirement for
  every contributor and every CI runner.
- **Postgres, or any server database.** Rejected without much weighing: it puts
  a service between the agent and its own run state, on a single host serving a
  single operator, and ADR 0001 §6 says this store is disposable and not backed
  up - which is not a thing to run a server for.
