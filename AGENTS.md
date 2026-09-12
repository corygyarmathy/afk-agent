# Agent instructions

## Repository

`afk-agent` is an unattended agent that takes work from a GitHub issue tracker,
does it, and leaves a pull request for a human to review and merge. It is a Go
program with no dependencies outside the standard library.

Read these before doing anything, in this order:

- [`CONTEXT.md`](CONTEXT.md) - the vocabulary. Use its words; it lists the
  synonyms to avoid for a reason.
- [`docs/adr/0001-a-go-state-machine-in-its-own-repository.md`](docs/adr/0001-a-go-state-machine-in-its-own-repository.md) -
  the whole design, as fifteen numbered decisions. Cite them by section (`ADR
  0001 §4`) when structure follows from one.
- [`docs/agents/`](docs/agents/) - how to use the tracker, which document owns
  which fact, and the ADR/plan boundary. Details below.

This repository is early. Most of what is described above is not implemented
yet; `afk help` shows the surface the transitions will be invoked through. If a
ticket seems to assume machinery that does not exist, check whether it does
before building around its absence.

## Working method

1. Isolate work in a git worktree.
2. Read the ticket, then trace how the existing code actually works before
   editing it. This codebase is small enough to read in full; do that rather
   than pattern-matching.
3. State important assumptions when the request is ambiguous. Do not broaden a
   focused change into an unrelated refactor.
4. Make the smallest correct change, and preserve the established naming and
   layout.
5. Add or update a test when changing observable behaviour.
6. Run the narrowest useful check, then the full one. Report the commands, the
   results, and anything you could not run.

Do not merge, and do not weaken branch protection to make something pass
(ADR 0001 §15). Merge is a human act here, including for the agent's own work.

## What this codebase holds to

These are the conventions a change is expected to keep. They are consequences of
ADR 0001 rather than taste, and where one has a section number it is given.

- **Every transition is invokable standalone, with no daemon present**
  (§4). A transition that can only be reached through the scheduler is badly
  factored. This is simultaneously the command surface, the integration-test
  entry point and the operator's recovery tool, so it is never "just for
  debugging".
- **Nothing waits in-process** (§3). A job that must wait for CI, a rate-limit
  window or a human persists its state and schedules re-entry. A `time.Sleep`
  in a transition path, or a loop polling until something becomes true, is a
  design error and not a shortcut.
- **No network in the test path.** Every test runs offline, with no live
  process, no model and no GitHub. Behaviour that needs one of those is reached
  through an interface with a fake in tests; the resolver is pure precisely so
  that it can be tested at all (§9). A test that would pass or fail depending on
  the network is not a test.
- **Table-driven tests**, in the shape `internal/cli/cli_test.go` already uses:
  a slice of named cases, `t.Run(tt.name, ...)`, one behaviour per case. Test
  through the package's public surface, not its internals.
- **Every outbound side-effect carries an idempotency key** (§5). This is what
  makes crash-anywhere-and-resume safe rather than merely survivable. A new
  outbound call without one is a defect even when nothing has crashed yet.
- **A claim is not a lease** (§7), and neither is the other. The prototype
  conflated them and replied to things twice; keep the words apart in names,
  comments and commit messages.
- **The local store is disposable** (§6). Everything in it is re-derivable from
  GitHub or cheap to lose. If you find yourself wanting to protect something in
  it, that is a signal that state ownership has been violated, not a reason to
  add a backup.
- **No third-party dependencies** without a decision. `go.mod` has none. Adding
  one is an ADR-sized choice, not an implementation detail.
- **Parameters live elsewhere.** Counts, intervals, thresholds, ceilings, tier
  membership and label strings belong to the NixOS module that configures this
  agent, in `corygyarmathy/dotfiles`. Read them from configuration; do not
  hard-code them and do not state them in an ADR. See
  [`docs/agents/domain.md`](docs/agents/domain.md).

## Formatting and validation

`gofmt` is the formatter of record. Run it before you commit, so formatting
lands in your commits rather than as drift a later push cannot carry.

```bash
gofmt -l .          # must print nothing
go build ./...
go vet ./...
go test ./...
```

`go test ./...` is the full suite and is expected to stay fast: these are
millisecond unit tests, which is part of why the repository is separate from the
fleet configuration at all. If a test you add takes seconds, say why.

## Documentation

Which document owns which fact - issue, ADR, plan, findings note, pull request,
commit, README, code comment - is set out in
[`docs/agents/documentation.md`](docs/agents/documentation.md). Read it before
writing a plan, an ADR, or anything that might restate a fact another document
already owns.

Two rules bite most often here:

- The vocabulary has one home, `CONTEXT.md`. Link to it; do not re-explain a
  term in a comment or a README.
- An accepted ADR is never amended in place. ADR 0001 is still *Proposed*, so it
  may be amended with a dated block; once accepted, a changed decision is a new
  ADR and the old one is marked superseded.

## Agent skills

The skills in `.agents/skills/` (linked into `.claude/skills/` for harnesses
that look there) are the intended way to do the work, not a menu:

- `implement` for working a ticket, `tdd` for the red-green loop inside it,
  `code-review` for the review pass afterwards, in its own context.
- `domain-modeling` when you are changing `CONTEXT.md` or writing an ADR -
  which is the active discipline, not merely reading the glossary.
- `codebase-design` for the module/interface/depth/seam vocabulary when the
  shape of an interface is the question.
- `diagnosing-bugs` when something is broken and the cause is not obvious.

They are vendored from [`corygyarmathy/skills`](https://github.com/corygyarmathy/skills)
and pinned in `skills-lock.json`. `./scripts/sync-skills.sh verify` reports
whether the copies here still match the pin; `pull` re-vendors them. Edit a
skill in that repository and pull, rather than editing the copy here - a local
edit is not wrong, but `verify` will report it, and it will be overwritten by
the next pull. See [ADR 0002](docs/adr/0002-skills-are-forked-and-vendored-from-a-repository-we-own.md).

Supporting documents the skills expect:

- **Issue tracker**: GitHub, via `gh`, plus the native dependency edges this
  backlog is ordered by. See
  [`docs/agents/issue-tracker.md`](docs/agents/issue-tracker.md).
- **Triage labels**: two are settled, the rest deliberately are not. See
  [`docs/agents/triage-labels.md`](docs/agents/triage-labels.md).
- **Domain docs**: single-context - one `CONTEXT.md` and `docs/adr/` at the
  root. See [`docs/agents/domain.md`](docs/agents/domain.md).

## Response expectations

For each completed task, summarise:

- What changed and why.
- Files changed.
- Checks run, and their results.
- Assumptions, residual risks, and anything you did not verify.

Do not write that something is verified, correct or unchanged unless you ran
something that shows it. "I did not check this" is a useful sentence and an
honest one; a tick against a criterion you inferred is neither.
