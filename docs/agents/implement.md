# Implement

What `/implement` does, what it needs on the host, and how to run one by hand.
The decisions are [ADR 0001](../adr/0001-a-go-state-machine-in-its-own-repository.md)
§2-§5, §8, §10 and §14 (with its amendments for #40 and #51), and `dotfiles`
ADR 0004 §6 and ADR 0007, as ADR 0001 carries them. The spec is
[#40](https://github.com/corygyarmathy/afk-agent/issues/40) and its sub-issues
#49-#53. The transitions are
[`internal/implement`](../../internal/implement); the workspace, the relay, the
local gate and its retries, the denylist, the leased push and the hand-back on
a pull request are [`internal/work`](../../internal/work), which `/revise` uses
too ([#131](https://github.com/corygyarmathy/afk-agent/issues/131)).

## What it does

`/implement` on an open issue, from an account with write access that is not
the agent's, produces one branch, one pull request that closes the issue, and
one advisory review of the pull request's head. Then the pull request gets the
hand-off label. Any text after the word, on the same line or below it, reaches
the model as instructions. The agent never merges.

The same job is made with nobody asking for an issue carrying the eligibility
label, when `afk` is given `--eligibility-label`
([`triage-labels.md`](triage-labels.md#the-eligibility-label)). The claim is a
👀 on the issue itself, and there is nothing to answer.

| transition | from | does |
| --- | --- | --- |
| `implement` | `start` | Reacts 👀 to every unanswered `/implement` (the claim). A closed issue stops there. Otherwise it reacts 👀 to the issue itself too. An issue that already has the agent's open pull request gets one reply per command linking it. Otherwise the work starts. |
| `implement-claimed` | `claiming` | Reads the claims and replies back, and makes any that are missing again. Once all of them are there, the job moves on to the work, or rests. |
| `implement-run` | `implementing` | Clones the repository into a workspace on a new branch `<prefix><n>-<k>`, and runs one enrolled model on the `implement` skill. After a failure it continues the session that wrote the commits, with the failure. |
| `implement-gate` | `gating` | The agent runs the local gate itself. No commits: hand-back. Uncommitted changes, or a failing gate: back to the session, until `--gate-attempts` runs out, then hand-back. |
| `implement-push` | `pushing` | Checks every path any commit touches against the denylist, counts the work's size and matches the diff against the [sensitive paths](#sensitive-paths), then pushes the commit it checked. A denied path hands back. Work over the size signal before its first push is first pushed as it is to `<branch>-whole` and read back there, then goes back to the session instead, once, to be [cut](#the-size-signal). |
| `implement-open` | `opening` | Reads the push back from the remote, then, for a first piece, files its rest and blocks it, then opens the pull request, with its [description](#the-description), if it is not open already. If it is, brings the sensitive line up to the push. Work over the size signal hands back on the issue instead, with its branch pushed. |
| `implement-watch` | `watching` | Reads CI's check runs on the pushed head, and the checks the base branch's rulesets require. Unfinished, or passing with a required check that has no run yet: looks again after `--ci-wait`. Green, every run passed and every required check among them: on to the review. Red: logs the failed and timed-out checks to stderr as `<job>: CI caught what the local gate passed, ...` (`dotfiles` ADR 0007 §8), then back to the session, with what CI said, until `--ci-fixes` runs out, then hand-back. A head still unfinished at `--ci-ceiling` hands back, naming any required check that had not started and logging any check that had already failed. A run waiting for approval hands back. It is not logged, because it never ran, but a check that failed beside it is. A cancelled check goes back for the fix but is not logged. [`afk caught`](#what-ci-caught) counts the same catches from GitHub, with the differences listed there. |
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

## The description

The pull request's description is orientation for the operator's review:
what the operator needs and cannot cheaply get from the issue or the diff. It
does not summarise the change, and the commit messages, not the description,
are the record. [#111](https://github.com/corygyarmathy/afk-agent/issues/111)
has the reasoning. The code is
[`internal/implement/description.go`](../../internal/implement/description.go).
Its sections, in this order, each left out when it has nothing to say:

1. **The link line**, `Closes #N`, or `Part of #N. The rest is #R.` on a
   first piece, naming the issue filed for the rest
   ([the size signal](#the-size-signal)). The advisory review reads that line
   back ([`review.md`](review.md)), so it is the one straight after the
   marker. The agent's.
2. **The sensitive line**, only on a pull request that touches a sensitive
   path: `**Sensitive:** job store schema (…), CI (…)`, each label the
   operator named that matched, in the operator's order, with the files it
   matched, or with a count of them when listing them would take the body over
   GitHub's 65,536 characters. The agent's.
3. **The reminder**, a blockquote the agent writes: read the issue, then the
   description, then the diff from **Start here**; do your own reading before
   the advisory review; end by merging, sending back or closing. It links
   `--review-procedure`. Without that parameter it says it has no link to the
   procedure.
4. **Start here**, the entry point and where the behaviour lives.
5. **Where the ticket didn't decide**, the choices the session made where the
   issue was silent, including the paths it took because nobody was there to
   ask.
6. **Not verified**, what the session could not check, and behaviour the diff
   cannot show.
7. **Recipe**, only on a deliberately large, single-concern change.

- **Sections 4-7 are the session's**, written to `.git/afk-description.md`
  under those headings. The prompt gives the soft target (an item one or two
  lines, the whole on one screen) and what not to write: a file-by-file
  account, a restatement of the issue, "tests pass", a self-rating, or a list
  of hand-checks. Nothing is capped or cut.
- **The title is the issue's** on a `Closes` pull request, whatever the file
  says. A `Part of` pull request, the first piece of an issue too big for one,
  takes its title from the file's first line when that line is not a heading
  ([#111](https://github.com/corygyarmathy/afk-agent/issues/111),
  [#127](https://github.com/corygyarmathy/afk-agent/issues/127)). A line longer
  than GitHub takes for a title (256 characters) is no title rather than a cut
  one, and a piece with no title keeps the issue's. The prompt asks for that
  line only when the size rule is in force and the session stops at a first
  piece, or is cut to one, and the line is never part of the body.
- **The agent orders them**, drops a heading it did not name and a section that
  says only "none", and links each `path:line` that names a file to that line
  at the pushed head, as the advisory review's citations are.
- **The pull request opens with the agent's parts only** when the file is
  missing, is not a regular file, cannot be read, has no `## Start here`, or
  makes a body over GitHub's 65,536 characters even with the sensitive files
  counted. None of these is a gate failure. Each but the missing file is a log
  line, and so is counting the sensitive files.
- **Written once**, when the pull request opens. A session after that - a CI
  fix, or a new session that takes one over - is not asked for the file.
- **The sensitive line is recomputed on every push**, the first and each fix
  after it, so a later push that newly touches a sensitive path adds it. Only
  that line is edited: the rest of the description stays as it opened, and
  the files are counted rather than listed when listing them would take it
  over GitHub's limit. An edit that never lands after `--effect-rounds`, or
  one over the limit even counted, is a log line, and the work goes on.

### Sensitive paths

`--sensitive` names the paths that deserve closer reading
([#112](https://github.com/corygyarmathy/afk-agent/issues/112)): the
operator's review reads the files the line lists line by line. It is one-sided:
nothing is ever marked safe to skim. The code is
[`internal/sensitive`](../../internal/sensitive).

- **The operator names them**, as `<label>=<globs>` entries separated by `;`,
  with the globs separated by `,`:
  `job store schema=internal/store/**;CI=.github/workflows/**`. A label may
  have spaces in it, and not a comma, a parenthesis or a control character.
- **The agent matches them**, with the denylist's glob matching, against the
  paths the pull request's diff changes: net, base to head, as the pull request
  shows them. No model rates anything.
- **Empty by default**, which is the feature off. It is a parameter, never a
  file in the repository.
- **Nothing else reads it.** It adds no label, the review queue is unaware of
  it, and the advisory review reads the description without it.

## The size signal

A pull request is one concern, reviewable in one sitting, tests included
([#107](https://github.com/corygyarmathy/afk-agent/issues/107)). Changed lines
are the signal for that, not the rule: `--size-signal` is how many changed
non-test lines the work may have before its size needs a decision. The code is
[`internal/size`](../../internal/size), which `/revise` reuses.

- **The session is told first.** The prompt gives the rule and the signal, so
  the session can stop at a coherent first piece by itself and say what is left.
- **The count is the agent's.** It is made by git on the commit the push sends,
  in the relay, never from what the session says. Generated, vendored, lock and
  binary files and files deleted whole are left out, and tests are counted
  beside it. [`internal/size`](../../internal/size/size.go) says what falls in
  each.
- **Over it, the work is cut once**
  ([#127](https://github.com/corygyarmathy/afk-agent/issues/127)). Before its
  first push, it goes back to the session that wrote it, as a fix does, told
  the counts and the signal: leave the branch at a first coherent piece, write
  what is left to `.git/afk-remainder.md`, and give the piece a title as the
  description file's first line. A refactor the rest needs is the natural
  first piece, and an incidental one goes in the rest. The piece is gated and
  measured again, with the gate's attempts afresh, as a fix has. There is no
  second cut, and a fix after the push is never cut.
- **The work is kept before it is cut.** It is pushed as it is to
  `<branch>-whole` first, and read back there, so nothing the cut does loses
  it: a cut that fails - its gate red to the last attempt, nothing committed,
  or the branch switched - hands back on the issue naming that branch rather
  than saying nothing was pushed, and a rest's issue names it too, since some
  of what is left may be written there. A new branch skips a number whose
  `-whole` is still on the remote. A `-whole` branch the agent did not push is
  left alone, and the work is not cut: it is pushed as it is, and handed back
  as over the signal. So is work whose `-whole` push never lands. The agent
  never deletes a `-whole` branch; that is the operator's, once the work is
  merged or dropped.
- **A piece under the signal opens as part of the issue.** Before its pull
  request opens, the agent files the rest as a new issue in the same
  repository: what the session said is left, and the branch that is the first
  piece. It is blocked by the issue with a native dependency, and carries no
  label, so whether and when it is worked is the operator's decision. Then the
  pull request opens once, under the session's title, with
  `Part of #N. The rest is #R.` in place of `Closes #N`, and GitHub's
  cross-reference puts it on the rest's timeline. The rest and its dependency
  are each read back from the tracker and made under the next key until they
  are there. The rest is read back closed as well as open, so one the operator
  closes at once is not filed again. A rest never filed hands the work back on
  the issue, as a pull request never opened does. A dependency that never
  lands fails nothing: the description says so under the link line, for the
  operator to add by hand. Once seen, the dependency is not made again, so one
  the operator removes stays removed.
- **A session may stop at a first piece by itself**, as the prompt tells it
  it can, and say what is left in the same file. Its pull request is a piece
  too, with no cut: `Part of`, not `Closes`, so merging it does not close an
  issue with work left.
- **A piece still over the signal**, or work the session left as it was
  because it found no coherent piece, is pushed and no pull request is opened.
  The issue gets a hand-back with both counts, the signal, the cut and the
  branch - and `<branch>-whole`, the work before the cut - so the work is kept
  and a human decides what becomes of it. At the
  signal or under it, nothing changes.
- **The override** is the instructions of the command the job claimed, and
  only those: "don't split" or "do not split", anywhere in them, opens the pull
  request whatever its size, and the session is told not to stop early. A
  command an earlier job claimed is not read, and unattended work claims no
  command, so it has no override.

## What it needs on the host

- **git 2.40 or later**, and **sh**, on `PATH`. The size signal reads the
  head's `.gitattributes` with `git check-attr --source`, which 2.40 added.
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
  | Issues | write | the claim, replies, hand-backs and labels on the issue; filing a first piece's rest, and its native dependency | by GitHub's documentation for the claim and the rest; the dependency endpoints' documentation names no permission; not verified |
  | Checks | read | CI's check runs | by GitHub's documentation; not verified |
  | Actions | read | [`afk caught`](#what-ci-caught)'s workflow runs and their jobs | not verified: the endpoints' documentation names no permission |

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
(branch, base, session, the session's description and what it says is left,
whether the work was cut and the commit kept on `<branch>-whole`, the rest's issue and whether it was blocked, the sensitive line, gate attempts
and fixes, the last failure, the pushed head), `requests/<job>.json` (which command the job's claim took, for its
instructions) and `notes/<job>.json` (the last error of a push, a pull request, a
review request or a label, for the hand-back to quote). All of it is disposable. Lost before the push, the work starts over.
Lost after it, the pull request is handed back rather than fixed on a new
branch.

## Parameters

`afk help` lists them, and the NixOS module sets them
([`domain.md`](domain.md)). Without `--branch-prefix`, `afk work` neither runs
implement jobs nor answers `/implement`. With it, all of these are required:
`--gate`, `--gate-attempts`, `--implement-tier`, `--hand-off-label`,
`--denylist`, `--ci-wait`, `--ci-ceiling`, `--ci-fixes` and `--size-signal`,
plus model choice,
`--effect-rounds` and `--hand-back-label` as for review. `--implement-needs`,
`--review-procedure` and `--sensitive` are optional. `/revise` runs on the same
gate, attempts, denylist and tier: there is no revise tier of its own.

- `--review-procedure` is the URL of the operator's review procedure. Give
  one on the default branch, not a permalink: each pull request links the
  version current when it opens.
- `--sensitive` is the sensitive paths: [Sensitive paths](#sensitive-paths).
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

## What CI caught

`afk caught` counts the catches: every check that failed or timed out on a head
the agent pushed, once for each head. It reads them from GitHub's workflow runs
and keeps nothing ([ADR 0006](../adr/0006-what-ci-caught-is-read-from-github-not-recorded.md)).
It takes `--repo`, `--app-id` and `--app-key`, and nothing else.

```bash
afk caught          # each check and how many heads it failed on, then what that covers
afk caught --list   # every catch first: when, the head, its branch, the check, the job's page
```

The count and the watch's log line name the same catches, except where:

- The check is not a GitHub Actions job. The watch logs it; `afk caught` reads
  Actions only.
- A human re-ran the check and it passed. The watch logged it when it failed;
  GitHub then serves the run as green, and it is not a catch.
- The run is older than GitHub keeps: 90 days at most on a public repository,
  from 2026-10-01.
- The log line was written twice, when a watch's commit failed. The count is
  once.

A check that had already failed when a human's push cancelled its run is a
catch in both. A check that was itself cancelled is a catch in neither.

The output ends by saying how many runs the agent started, and warns when that
is none, which is more likely a wrong login than a quiet agent. When GitHub
serves only part of the runs that did not pass, it says how many it served and
how many there are.
