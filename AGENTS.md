# Agent instructions

`afk-agent` is an unattended agent that takes work from a GitHub issue tracker,
does it, and leaves a pull request for a human to review and merge. Go, with one
vendored dependency: the job store's SQLite driver (ADR 0004).

## Where things are

- [`CONTEXT.md`](CONTEXT.md) - the vocabulary.
- [`docs/adr/`](docs/adr/) - the decisions, and the reasoning behind them.
- [`docs/agents/issue-tracker.md`](docs/agents/issue-tracker.md) - the tracker,
  and the dependency edges the backlog is ordered by.
- [`docs/agents/domain.md`](docs/agents/domain.md) - the ADR/plan boundary, and
  where this project's parameters live.
- [`docs/agents/documentation.md`](docs/agents/documentation.md) - which
  document owns which fact.
- [`docs/agents/triage-labels.md`](docs/agents/triage-labels.md) - what the
  tracker's labels mean.
- [`docs/agents/model-enrolment.md`](docs/agents/model-enrolment.md) - the files
  model choice reads, and what has to be in them.
- [`docs/agents/budget.md`](docs/agents/budget.md) - what the agent reads to
  decide whether new work may start, and what it does about the answer.
- [`docs/agents/review.md`](docs/agents/review.md) - what `/review` does, what
  it needs on the host, and how to run one by hand.
- [`docs/agents/notification.md`](docs/agents/notification.md) - the two
  conditions that reach the operator, and everything that deliberately does not.
- [`docs/agents/branch-protection.md`](docs/agents/branch-protection.md) - what
  protects `master`, and what that means for you.

Read what the work needs. None of it is required reading.

## Conventions

- Use `CONTEXT.md`'s words when you name things, and not the synonyms it lists
  under `_Avoid_`.
- `gofmt` is the formatter of record. Run it before you commit, so formatting
  lands in your commits rather than as drift. `scripts/check-format.sh` is the
  check, and it covers this module's packages rather than the whole tree -
  `vendor/` is excluded.
- Tests run offline. No network, no live process, no model, no GitHub.
- Do not add a dependency. `go.mod` has exactly one, the job store's SQLite
  driver, and ADR 0004 records why it was worth an exception and what the bar
  is. Adding a second is an ADR-sized decision rather than an implementation
  detail, and the answer is usually the standard library.
- Everything in `vendor/` is upstream's. Do not edit it and do not format it;
  re-vendor with `go mod vendor` after a version change and commit the result.

## Checks

```bash
scripts/check-format.sh     # prints nothing
go build ./...
go vet ./...
scripts/offline-test.sh     # go test ./... with no network
```

These four are the gate, in this order, in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml) on every pull request.
Run them locally rather than waiting for CI to tell you.

`scripts/offline-test.sh` runs the tests in a network namespace with no route
out, so "tests run offline" is enforced rather than trusted; it refuses to run
at all on a host where it cannot isolate the network. `go test ./...` is still
the faster thing to run while you iterate - the script is what decides whether
the tests were honest about it.

## Do not

- Merge, or weaken branch protection to get something through (ADR 0001 §15,
  ADR 0003). There is no bypass, and disabling the ruleset is the operator's
  procedure: [`branch-protection.md`](docs/agents/branch-protection.md).
- Hard-code a parameter. Counts, intervals, thresholds, ceilings and label
  strings belong to the NixOS module that configures this agent.

## Skills

`.agents/skills/` is vendored from
[`corygyarmathy/skills`](https://github.com/corygyarmathy/skills) and pinned in
`skills-lock.json` (ADR 0002). To check the copies against the pin, or re-vendor
them:

```bash
go run github.com/corygyarmathy/skills/cmd/vendor-skills@latest verify
go run github.com/corygyarmathy/skills/cmd/vendor-skills@latest pull
```

Edit a skill in that repository rather than here; `verify` reports a local edit,
and the next `pull` overwrites it.

## Reporting

Say what you ran and what it said. Do not write that something is verified,
correct or unchanged unless you ran something that shows it - "I did not check
this" is a useful sentence and an honest one.
