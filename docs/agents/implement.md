# Implement

What `/implement` does, what it needs on the host, and how to run one by hand.
The decisions are [ADR 0001](../adr/0001-a-go-state-machine-in-its-own-repository.md)
§2-§5, §8, §10 and §14 (with its amendments for #40 and #51), and `dotfiles`
ADR 0004 §6 and ADR 0007, as ADR 0001 carries them. The spec is
[#40](https://github.com/corygyarmathy/afk-agent/issues/40) and its sub-issues
#49-#53. The code is [`internal/implement`](../../internal/implement).

## What it does

`/implement` on an open issue, from an account with write access that is not
the agent's, produces one branch, one pull request that closes the issue, and
one advisory review of the pull request's head. Then the pull request gets the
hand-off label. Any text after the word, on the same line or below it, reaches
the model as instructions. The agent never merges.

| transition | from | does |
| --- | --- | --- |
| `implement` | `start` | Reacts 👀 to every unanswered `/implement` (the claim). A closed issue stops there. An issue that already has the agent's open pull request gets one reply per command linking it. Otherwise the work starts. |
| `implement-claimed` | `claiming` | Reads the claims and replies back, and makes any that are missing again. Once all of them are there, the job moves on to the work, or rests. |
| `implement-run` | `implementing` | Clones the repository into a workspace on a new branch `<prefix><n>-<k>`, and runs one enrolled model on the `implement` skill. After a failure it continues the session that wrote the commits, with the failure. |
| `implement-gate` | `gating` | The agent runs the local gate itself. No commits: hand-back. Uncommitted changes, or a failing gate: back to the session, until `--gate-attempts` runs out, then hand-back. |
| `implement-push` | `pushing` | Checks every path any commit touches against the denylist, and counts the work's size, then pushes the commit it checked. A denied path hands back. |
| `implement-open` | `opening` | Reads the push back from the remote, then opens the pull request if it is not open already. Work over the size signal hands back on the issue instead, with its branch pushed. |
| `implement-watch` | `watching` | Reads CI's check runs on the pushed head, and the checks the base branch's rulesets require. Unfinished, or passing with a required check that has no run yet: looks again after `--ci-wait`. Green, every run passed and every required check among them: on to the review. Red: logs the failing checks to stderr as `<job>: CI caught what the local gate passed, ...` (`dotfiles` ADR 0007 §8), then back to the session, with what CI said, until `--ci-fixes` runs out, then hand-back. A head still unfinished at `--ci-ceiling` hands back, naming any required check that had not started and logging any check that had already failed. A run waiting for approval hands back. It is not logged, because it never ran, but a check that failed beside it is. |
| `implement-review` | `reviewing` | Makes the pull request's `review` job due, and waits for the review of the head. Hands back if someone else pushed to the branch, or the review job parked. Rests if the review job handed back this head: that hand-back is the pull request's. |
| `implement-hand-off` | `handing-off` | Applies the hand-off label, and reads it back until it is there. |
| `implement-handed-back` | `handing-back` | Reads the hand-back's comment and label back, each on its own, and makes whichever is missing again. Once both are there, the job rests. |
| `implement-resume` | `deferred` | Tries the tier again from its first model, after a limited budget or an exhausted tier. |

A **hand-back** is a comment saying what stopped the work, quoting the end of
the output that said so, plus the hand-back label. Before the push it goes on
the issue, and nothing was pushed. After the push it goes on the pull request
only, and the pull request stays open. Between the two - work over the size
signal, or a pull request that never opened - it goes on the issue, and says
where the pushed branch is. Either way the job comes to rest once
both are read back from the tracker, and its workspace goes at once.

A claim, a reply and a hand-back are each read back before the job moves on
([`internal/owed`](../../internal/owed)). The runner commits a decision before
it performs the effects, so a process killed between the two, or an effect
that errors, would otherwise lose the effect for good, with its key reserved.
A lost claim leaves the command looking unanswered for ever. A lost hand-back
is a silent stop: the job has come to rest and did not fail, so nobody is told.
What is owed waits in `<state dir>/owed/` until it has been read back. If the
state directory is wiped in between, a claim is decided again from the
tracker, and a hand-back rests without being made.

The review is a `review` job the implement job makes due, never a `/review`
comment (ADR 0001 §14). The review claims the request with a 👀 on the pull request's
description, and says the implement job asked for it
([`review.md`](review.md)).

## The push

- **The denylist** (`--denylist`) is globs: `**` spans directories and `*`
  stays within one segment. It is checked on every commit in the push, not just
  the net diff, and a rename is checked under both of its names.
- **The push comes from a relay.** The workspace's `.git` is the model's to
  write, and git obeys it: a hook, an `fsmonitor` command or a `url.insteadOf`
  there would run with the token, or send it elsewhere. So the branch is copied
  into a bare repository only the agent writes, the denylist is checked there,
  and that exact commit is pushed from there.
- **The token** reaches `git` only in that one process's environment, as an
  HTTP header scoped to the remote's URL, minted as the process starts. The
  clone, the read of which branches are taken and the reads of where a push
  landed carry it the same way, isolated from the agent user's global and
  system `git` configuration, so a private repository is implemented as a
  public one is, and nothing of the token is left in the workspace.
- **Every push is `--force-with-lease`**, pinned to the commit the agent last
  saw its own push land at. A session that amends its own pushed commits does
  not stall the job, and anyone else's push to the branch is never rewritten
  (ADR 0001, amendment of 2026-09-25).

## The size signal

A pull request is one concern, reviewable in one sitting, tests included
([#107](https://github.com/corygyarmathy/afk-agent/issues/107)). Changed lines
are the signal for that, not the rule: `--size-signal` is how many changed
non-test lines the work may have before its size needs a decision. The code is
[`internal/size`](../../internal/size), which `/revise` reuses.

- **The session is told first.** The prompt gives the rule and the signal, so
  the session can stop at a coherent first piece by itself and say what is left.
- **The count is the agent's.** It is made by git on the commit the push sends,
  in the relay, never from what the session says. Each line added and each line
  removed counts once. Left out: files deleted whole, binary files, vendored
  files (under `vendor/`, `third_party/` or `node_modules/`, or marked
  `linguist-vendored`) and generated ones (lock files by name, Go's
  `// Code generated ... DO NOT EDIT.` line, or marked `linguist-generated`).
  An attribute set false in the head's `.gitattributes` takes a convention's
  exclusion back. Tests - `_test` files, `test/`, `tests/`, `testdata/`,
  `spec/` and the like - are counted beside it, and do not count towards it.
- **Over it**, the work is pushed and no pull request is opened. The issue gets
  a hand-back with both counts, the signal and the branch, so the work is kept
  and a human decides what becomes of it. At the signal or under it, nothing
  changes.
- **The override** is the command's own instructions, and only those: "don't
  split" or "do not split", anywhere in them, opens the pull request whatever
  its size, and the session is told not to stop early. Unattended work has no
  instructions, so it has no override.

## What it needs on the host

- **git** and **sh**, on `PATH`.
- **A git commit identity.** The agent sets none: sessions commit as the agent
  user's `user.name` and `user.email`, which the NixOS module sets.
- **opencode**, at `--opencode`, with credentials for every provider enrolled in
  the implement tier.
- **opencode's data directory holds the sessions and the credentials.**
  Sessions have to survive between transitions for a retry to continue the
  session that failed. `XDG_DATA_HOME` moves the directory (observed with
  opencode 1.18.31), but `auth.json`, the provider credentials, moves with it.
  So pointing it into the agent's state directory means the credentials are
  wiped whenever the state is. If a session is gone anyway, the retry starts a
  new session given the failure: weaker, and the job carries on.
- **The `implement` skill**, where opencode discovers it from the workspace: in
  the implemented repository's `.agents/skills/`, or in the per-user skills
  directory. The model is told to reply that the skill is missing rather than
  implement without it, and then the gate finds no commits and hands back.
- **The GitHub App**, as for review ([`review.md`](review.md#the-apps-permissions)),
  plus these:

  | permission | level | for | |
  | --- | --- | --- | --- |
  | Contents | write | the push, and the clone and every read of the remote's branches (write includes read) | the grant confirmed on the App by the operator, 2026-09-25; a read of a private repository not observed |
  | Pull requests | write | opening the pull request, its labels | confirmed on the App by the operator, 2026-09-25 |
  | Issues | write | the claim, replies, hand-backs and labels on the issue | by GitHub's documentation; not verified |
  | Checks | read | CI's check runs | by GitHub's documentation; not verified |

  The base branch's required checks are read with Metadata: read, which the
  App already has (by GitHub's documentation; not verified). Only rulesets
  are read: a check required by legacy branch protection is not waited for,
  and reading it would need Administration: read.

  A push that touches `.github/workflows/` would also need Workflows: write.
  The denylist is expected to stop such a push first.
- **The heavy-build token.** `implement-run` and `implement-gate` hold it, so
  `afk work` needs `--token heavy-build=<n>`.

The state directory is the directory holding `--store`. Beside the store,
implementing keeps `workspaces/<job>` (the clone the model works in),
`relays/<job>.git` (the copy pushes are made from), `progress/<job>.json`
(branch, base, session, gate attempts and fixes, the last failure, the pushed
head) and `notes/<job>.json` (the last error of a push, a pull request, a
review request or a label, for the hand-back to quote). All of it is disposable. Lost before the push, the work starts over.
Lost after it, the pull request is handed back rather than fixed on a new
branch.

## Parameters

`afk help` lists them, and the NixOS module sets them
([`domain.md`](domain.md)). Without `--branch-prefix`, `afk work` neither runs
implement jobs nor answers `/implement`. With it, all of these are required:
`--gate`, `--gate-attempts`, `--implement-tier`, `--hand-off-label`,
`--denylist`, `--ci-wait`, `--ci-ceiling`, `--ci-fixes` and `--size-signal`
(whose default, 400, is the NixOS module's), plus model choice,
`--effect-rounds` and `--hand-back-label` as for review. `--implement-needs` is
optional.

- `--effect-rounds` bounds the rounds of a push, a pull request, a review
  request or a hand-off label that never appears. Out of rounds, the work is
  handed back - on the issue while there is no pull request, and on the pull
  request once there is - with the last error, and the job rests. A claim, a
  reply or a hand-back out of rounds is a failed attempt instead, as it is for
  review.
- A push the lease refuses because someone else pushed to the branch, or made
  it before the agent's first push, is handed back at once rather than made
  again: every later push would be refused the same way.
- `--ci-wait` is also how often a review not yet posted is looked for.
- `--model-timeout` bounds each model run. One still going when it runs out is
  killed with everything it started, and is a transient failure: the job
  stays, and its next run tries the next candidate. Each transient failure is
  a log line, and the last candidate's defers the tier with it, which is what
  the operator is told: [`notification.md`](notification.md). `implement-run` can make
  two runs, when the session it continues has gone, and each has the bound.
- `--lease` should be longer than a model run, than the gate, and than a push.
  A lease that lapses mid-run lets another worker take the job; the store
  refuses the first run's commit, so the work is wasted rather than
  duplicated. Nothing ties it to `--model-timeout`: a short lease takes a dead
  holder's job back quickly, and a long bound lets a slow run finish. The lease
  is renewed when a transition commits and held until its effects finish, and
  the push is one. An effect still running when the renewed lease lapses is
  cancelled.

## Running one by hand

Every step runs with no daemon present (ADR 0001 §4). With the parameters in
the environment:

```bash
afk run implement          --issue 7   # claim; -> claiming
afk run implement-claimed  --issue 7   # -> implementing, once the claim is read back
afk run implement-run      --issue 7   # the model; -> gating
afk run implement-gate     --issue 7   # -> pushing, or back to implementing
afk run implement-push     --issue 7   # -> opening
afk run implement-open     --issue 7   # -> watching (run again after the pull request opens)
afk run implement-watch    --issue 7   # -> reviewing, once CI is green
afk run implement-review   --issue 7   # makes review-pr-<m> due; run the review, then again
afk run implement-hand-off --issue 7   # -> start, not scheduled: done
```

A hand-back moves the job to `handing-back`, and `afk run implement-handed-back
--issue 7` rests it once the comment and the label are both there.
