# ADR 0006: What CI caught is read from GitHub, not recorded by the agent

- **Status:** Proposed
- **Date:** 2026-09-28
- **Related Artefacts:**
    - Implements: [`corygyarmathy/dotfiles` ADR 0007](https://github.com/corygyarmathy/dotfiles/blob/HEAD/docs/adr/0007-the-pull-request-opens-before-the-review.md) §8 - what CI catches that the local gate did not is recorded, so that whether the CI stage earns its latency can be answered rather than argued about.
    - Specified by: issue #86, "Collect what CI caught beyond a log line".
    - Follows: #80, which logs each catch as it happens.

## Context

`implement-watch` writes one line to stderr for each check that fails on a head
the local gate passed (#80). The question §8 exists to answer is one of counts
across jobs: which checks keep failing after the gate passed, and how often.
The log answers it only by grepping the journal for as long as the journal keeps
it. The line is also written at least once rather than exactly once: it is
logged before the runner commits, and a failed commit runs the step again.

A durable record kept by the agent has nowhere to go. The job store holds run
state only, and is disposable and not backed up (ADR 0001 §5, §6). A history
of catches is not run state, and a wipe of the store would lose it. The state
directory beside the store is lost the same way. Keeping the record in either
place is the backup ADR 0001 §6 says should be a loud signal.

GitHub already holds the record. The gate runs before every push the agent
makes, so every head the agent pushed passed it, and a catch is a workflow job
that failed on one of those heads. The Actions API names the account that
started each run, and keeps how each of its jobs finished.

## Decision

**1. A catch is read from GitHub when someone asks, and the agent writes
nothing to record it.** `afk caught` reads the workflow runs the agent's own
`[bot]` account started on the heads it pushed, and counts the jobs that failed
on them. The operator reads it. Nothing else does, and nothing aggregates it on
a schedule.

**2. A catch is one check on one head.** It holds the head, its branch, the
check, the failed job's page, and when the run was created. A check is counted
once for each head, however many runs saw it fail there, so the count is exact
where the log is at least once. A check that fails again on the next head - the
fix did not fix it - is the next catch: the gate passed that head too, so it let
the failure through again. The fix round, the pull request and the job are not
held; the head and its branch lead to each of them.

**3. A check a human re-runs green is not a catch.** A failure that passes when
nothing changed is one no gate could have caught, which is not what §8 asks
about.

**4. The log line stays, and names the same catches.** It is what the operator
sees in the journal when a catch happens, and `afk caught` is what counts
catches. Neither replaces the other, and a duplicated log line no longer changes
a count.

## Consequences

**Positive**

- No schema, no write path, and no new outward effect. ADR 0001 §5 holds as it
  stands: the record is work state, and GitHub owns it.
- A wipe of the store or the state directory loses nothing that `afk caught`
  reads.
- Every catch GitHub still keeps is counted, including those made before this
  was built.

**Negative**

- Only GitHub Actions is read. A check run from another CI app, which the watch
  reads through the Checks API and can fail on, is not counted. This
  repository's CI is Actions alone.
- The App needs Actions: read, which it did not need before.
- It lasts only as long as GitHub keeps the runs. From 2026-10-01 a workflow
  run is kept for the repository's Actions retention period, which defaults to
  90 days and can be at most 90 on a public repository and 400 on a private
  one; before then runs were kept 400 days or more
  ([changelog](https://github.blog/changelog/2026-08-27-actions-retention-will-cover-checks-workflow-runs-and-statuses/),
  [settings](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository)).
  This repository and `corygyarmathy/dotfiles` are both public, so `afk caught`
  counts the last 90 days at most. That is a window, not the durable record
  issue #86 asked for, and a longer one needs the agent to keep a copy after
  all.
- GitHub serves only the first thousand runs of a filtered listing, newest
  first, though its count is of all of them, so a longer history is counted in
  part. `afk caught` says so when it happens.
- A pull request a human opens from a branch the agent pushed may have its
  first run read as the human's, and its catches missed. GitHub documents the
  `actor` filter as the account that made the push, and a run's actor as the
  account that triggered it; which one it is when they differ has not been
  checked.

## Alternatives considered

- **A table in the job store**, written in the same transaction as the watch's
  commit. It is exactly once and easy to count with SQL. Rejected: it is not
  run state, and it would be the first thing in the store that a wipe loses
  and that GitHub cannot give back. ADR 0001 §6 would need reversing for it.
- **An append-only file in the state directory**, written after a reserved
  idempotency key. Rejected for the same reason as the table: the state
  directory is wiped as well, and a second way of persisting things is a cost
  paid for durability it does not have.
- **Walking the agent's pull requests, their commits and force-push events, and
  each head's check runs.** It reads every check app, not only Actions, with the
  Checks: read permission the App already has. Rejected: a commit does not say
  who pushed it, and the agent does not set the commit's identity, so a human's
  fast-forward push after a hand-back cannot be told from the agent's. It also
  takes several requests for each pull request.
- **Marking each pushed head with a commit status the gate passed.** Explicit,
  and visible in a pull request's checks. Rejected: it is a new outward effect
  on the push path, it needs Statuses: write, and it records something GitHub
  already says.
- **The log line alone, with the journal's retention set in `dotfiles`.**
  Rejected: counting would still mean grepping, and a duplicated line would
  still be counted twice.
