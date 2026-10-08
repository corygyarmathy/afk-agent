# Implement

What `/implement` does, what it needs on the host, and how to run one by hand.
The decisions are [ADR 0001](../adr/0001-a-go-state-machine-in-its-own-repository.md)
§2-§5, §8, §10 and §14 (with its amendments for #40 and #51), and `dotfiles`
ADR 0004 §6 and ADR 0007, as ADR 0001 carries them. The spec is
[#40](https://github.com/corygyarmathy/afk-agent/issues/40) and its sub-issues
#49-#53. The transitions are
[`internal/implement`](../../internal/implement); the workspace, the relay, the
local gate and its retries, the denylist, the leased push, the CI watch and its
fixes, and the hand-back on a pull request are
[`internal/work`](../../internal/work); the wait for the review and the
hand-off label are [`internal/handoff`](../../internal/handoff). `/revise` uses
both too ([#131](https://github.com/corygyarmathy/afk-agent/issues/131)).

## What it does

`/implement` on an open issue, from an account with write access that is not
the agent's, produces one branch, one pull request that closes the issue (or
[refers to it](#a-criterion-the-work-cannot-meet)), and one advisory review of
the pull request's head. A review with a Correctness or
Standards finding is [corrected](#the-correction) first. Then the pull request
gets the hand-off label. Any text after the word, on the same line or below it, reaches
the model as instructions. The agent never merges.

The same job is made with nobody asking for an issue carrying the eligibility
label, when `afk` is given `--eligibility-label`
([`triage-labels.md`](triage-labels.md#the-eligibility-label)). The claim is a
👀 on the issue itself, and there is nothing to answer.

| transition | from | does |
| --- | --- | --- |
| `implement` | `start` | Reacts 👀 to every unanswered `/implement` (the claim). A closed issue stops there. Otherwise it reacts 👀 to the issue itself too. An issue that already has the agent's open pull request gets one reply per command linking it. Otherwise the work starts. |
| `implement-claimed` | `claiming` | Reads the claims and replies back, and makes any that are missing again. Once all of them are there, the job moves on to the work, or rests. |
| `implement-run` | `implementing` | Clones the repository into a workspace on a new branch `<prefix><n>-<k>`, fetches the issue's [premises](#premises-and-gaps) into it, and runs one enrolled model on the `implement` skill. After a failure it continues the session that wrote the commits, with the failure, unless that session has [grown too long](#a-session-too-long-to-continue). |
| `implement-gate` | `gating` | The agent runs the local gate itself. No commits: hand-back, with the session's questions if it [stopped on gaps](#a-stop-on-a-gap). Uncommitted changes, or a failing gate: back to the session, until `--gate-attempts` runs out, then hand-back. In a [correction](#the-correction), each of those fails the correction instead, and so does one that rewrote the head the review read. |
| `implement-push` | `pushing` | Checks every path any commit touches against the denylist, counts the work's size and matches the diff against the [sensitive paths](#sensitive-paths), then pushes the commit it checked. A denied path hands back, or fails a correction. Work over the size signal before its first push is first pushed as it is to `<branch>-whole` and read back there, then goes back to the session instead, once, to be [cut](#the-size-signal). |
| `implement-open` | `opening` | Reads the push back from the remote, then, for a first piece, files its rest and blocks it, then opens the pull request, with its [description](#the-description), if it is not open already. If it is, brings the sensitive line and the spend footer up to the push. Work over the size signal hands back on the issue instead, with its branch pushed. |
| `implement-watch` | `watching` | Reads CI's check runs on the pushed head, and the checks the base branch's rulesets require. Unfinished, or passing with a required check that has no run yet: looks again after `--ci-wait`. Green, every run passed and every required check among them: on to the review. Red: logs the failed and timed-out checks to stderr as `<job>: CI caught what the local gate passed, ...` (`dotfiles` ADR 0007 §8), then back to the session, with what CI said, until `--ci-fixes` runs out, then hand-back. A head still unfinished at `--ci-ceiling` hands back, naming any required check that had not started and logging any check that had already failed. A run waiting for approval hands back. It is not logged, because it never ran, but a check that failed beside it is. A cancelled check goes back for the fix but is not logged. [`afk caught`](#what-ci-caught) counts the same catches from GitHub, with the differences listed there. In a [correction](#the-correction), every hand-back but someone else's push fails the correction instead; a failed correction back at the head the review read is not watched again. |
| `implement-review` | `reviewing` | Makes the pull request's `review` job due, and waits for the review of the head. Hands back if someone else pushed to the branch, or the review job parked. Rests if the review job handed back this head: that hand-back is the pull request's. A review with a Correctness or Standards finding goes back to the session, once, for a [correction](#the-correction). Once the correction is green, or has failed, edits the review to say so, and reads the edit back. |
| `implement-hand-off` | `handing-off` | Applies the hand-off label, and reads it back until it is there. |
| `implement-handed-back` | `handing-back` | Reads the hand-back's comment and label back, each on its own, and makes whichever is missing again. Once both are there, the job rests. |
| `implement-resume` | `deferred` | Tries the tier again from its first model, after a limited budget or an exhausted tier. |

A **hand-back** is a comment saying what stopped the work, quoting the end of
the output that said so (or, for a [stop on a gap](#a-stop-on-a-gap), the
session's questions), plus the hand-back label. Before the push it goes on
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

## Premises and gaps

The implementing session checks the issue before its first edit, and stops on
a gap rather than guess: the `implement` skill's own step. What the agent does
is give that session the issue's premises, and do something with its answer
([#199](https://github.com/corygyarmathy/afk-agent/issues/199)). The code is
[`internal/premise`](../../internal/premise) and
[`internal/implement/gaps.go`](../../internal/implement/gaps.go).

### The premises

A [premise](../../GLOSSARY.md) is an outside fact the issue rests on. The
session has no credentials for the tracker or the remote, so it can check only
a premise the agent fetched for it.

- **Which links.** Each in the issue's `Premises` section: a heading named
  Premises, at any level, to the next heading at its level or above. A URL, or
  a `#N` or `owner/name#N` reference. An issue with no such section fetches
  nothing, and the prompt says nothing of premises.
- **What is fetched**, into `.git/afk-premises/` beside the issue, before the
  session starts:
  - a file permalink (`/blob/<rev>/<path>`): the file at the linked revision,
    and at the current head of its repository's default branch, with whether
    the two differ;
  - an issue, a pull request or a comment link, and a `#N`: the thread as it is
    now, its description and every comment, with a linked comment marked.
    Review comments on a pull request's diff are not in it.

  `index.md` there lists each link and what it was fetched into. The prompt
  points the session at it.
- **Once for each workspace.** A gate retry, a CI fix or a session that takes
  over reads what the first run read. A new `/implement` is a new workspace,
  and fetches again.
- **A link that can't be fetched fails nothing**: one on another host, one that
  is neither a file permalink nor a thread, a private repository the App is
  not installed on, a file gone at the head. The index says why, and the
  session lists it under **Not verified**. A fetch with any of these is a log
  line, with the count.
- **Credentials.** A premise in the agent's own repository is read with its own
  token. One in another repository is read as the App on its installation
  there, with a token minted for that repository alone
  ([ADR 0005](../adr/0005-the-agent-authenticates-as-its-github-app.md) §4).
  Where the App has no installation, it is read with no token, as anyone would:
  a public repository is read, within GitHub's limit for unauthenticated
  requests from the host, and a private one is not fetched.

### A stop on a gap

The first session is told that, with nobody to ask, a gap in the issue means
committing nothing, and writing its questions as its report. A session that
does commits nothing, and opens its report with `Stopped on gaps: nothing
changed.`

- **It is a hand-back on the issue**, as a session that commits nothing
  already is, and nothing was pushed. Its detail is the session's questions **in
  full**, each with its recommended answer, rather than the end of the output.
  Questions that would take the comment over GitHub's 65,536 characters are cut
  at a line, with a note that says so, and a log line.
- **The next step it names** is answering with `/implement <answers>`. The
  answers are that command's instructions, and reach the next session as the
  more recent word than the issue. The agent never edits the issue.
- **It cannot loop.** Unattended intake does not take a handed-back issue
  again, because its job is still in the store: only a command runs it again.
- Only work not yet pushed is read for it. A report without the line, or with
  nothing after it, is a session that committed nothing, and is handed back as
  one.

### A criterion the work cannot meet

An acceptance criterion the pull request can't meet by itself - one that needs
a deploy or a hand run - is listed under **Not verified**, as the `pr` skill
says, and in `.git/afk-unmet.md`, as the prompt asks. Work with one says
`Refs #N` rather than `Closes #N`, so merging it does not close an issue with a
check still to do. The file is read before the push only, as what is left is,
and a file that says only "none" is no criterion. A first piece is `Part of`
either way. The advisory review reads `Refs` back as the issue the work is
reviewed against ([`review.md`](review.md)).

## The correction

On the agent's own pull request, the Correctness and Standards findings of the
advisory review are the agent's to make right before the hand-off, not advice
([#193](https://github.com/corygyarmathy/afk-agent/issues/193)). Making them
right is a **correction**: one pass, checked by the local gate and CI, and
never by a second advisory review. The code is
[`internal/correction`](../../internal/correction), and where it leaves the job
is [`internal/implement/correct.go`](../../internal/implement/correct.go).

- **Which findings.** Every finding under the review's `## Correctness` or
  `## Standards` heading, of any severity. A merged duplicate sits under the
  axis whose evidence is strongest, so it counts when that is one of the two.
  A review with none is handed off as it is.
- **Who.** The session that wrote the branch, continued, unless it has [grown
  too long](#a-session-too-long-to-continue): then a fresh session, given the
  issue, the branch's commits and the findings. The findings are in
  `.git/afk-findings.md`, each as the review wrote it, numbered as the review
  numbers it, a Correctness finding with its reproduction's source.
- **What it is asked.** To commit each reproduction as a regression test and
  make it pass, to make the code meet each cited standard, and to leave alone a
  finding it finds mistaken. Its commits go on top of the head the review read,
  which it never rewrites, and each names the findings it corrects with a
  `Corrects: advisory <n>` trailer.
- **Then, as for any push**: the local gate and its attempts, the denylist, the
  leased push, the size and the sensitive line recomputed, and CI with
  `--ci-fixes`. The gate's attempts and the fixes start afresh for it. The size
  signal never cuts it, as it never cuts a fix.
- **Corrected**: CI green. The advisory review is edited. Each finding a commit
  names collapses to one line linking the newest commit that names it, with
  the finding folded beneath it. A correctable finding no commit names stays,
  marked as advice a correction was attempted on. The summary names both heads:
  reviewed at A, corrected to B, checked by reproductions and CI, not
  re-reviewed. When no commit names any finding, the summary names both heads
  but says the correction named none, so the findings are advice. The citations
  stay permalinks at A, and the review's `head=` marker keeps meaning A. The
  trailers are read from the commits in the relay the correction was pushed
  from, never in the workspace.
- **Failed**: the gate red at its last attempt, nothing committed, a rewrite of
  the reviewed head, a denied path, a push that never landed, or CI red past
  `--ci-fixes`, past `--ci-ceiling` or waiting for an approval. It is not a
  hand-back. The branch goes back to the head the review read, under the
  lease, if the correction had pushed past it. That head is not watched again,
  as CI was green on it before the review. The review is edited to say the
  correction failed and why, and each correctable finding is marked "correction
  attempted, failed": advice. Then the hand-off. Each failure is a log line too.
- **Someone else's push**, or a record lost with the state directory, hands
  back as at any other time, and the review is left as it was posted. The
  branch is read again before the review is edited, so a push during a
  correction that failed without pushing hands back too.
- **A failed correction replayed**, after a process killed between the
  failure and its move, goes where the failure sent it: back to the reviewed
  head, then the edit and the hand-off. It is never handed back for the work
  it already failed on.
- **An edit that never lands** after `--effect-rounds`, or a review that has
  gone, is a log line. The hand-off goes on: the review is advice.

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
  public one is, and nothing of the token is left in the workspace. No process
  that carries it runs in or under a workspace at all: `git.Remote` refuses
  one, so a fetch into a workspace goes through a repository the agent owns
  and is brought in with no token.
- **Every push is `--force-with-lease`**, pinned to the commit the agent last
  saw its own push land at. A session that amends its own pushed commits does
  not stall the job, and anyone else's push to the branch is never rewritten
  (ADR 0001, amendment of 2026-09-25).

## The description

The pull request's description is orientation for the operator's review:
what the operator needs and cannot cheaply get from the issue or the diff. It
does not list what changed - its Summary shows a change's shape only where the
diff does not - and the commit messages, not the description, are the record.
[#111](https://github.com/corygyarmathy/afk-agent/issues/111) has the
reasoning. The code is
[`internal/implement/description.go`](../../internal/implement/description.go).
Its sections, in this order, each left out when it has nothing to say:

1. **The link line**, `Closes #N`, or `Part of #N. The rest is #R.` on a
   first piece, naming the issue filed for the rest
   ([the size signal](#the-size-signal)), or `Refs #N` on work with
   [a criterion it cannot meet](#a-criterion-the-work-cannot-meet). The
   advisory review reads that line back ([`review.md`](review.md)), so it is
   the one straight after the marker. The agent's.
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
5. **Summary**, the change's shape, only when the diff alone does not show it:
   a visual such as pseudocode, a call or file tree, a `diff` of one, or
   Mermaid.
6. **Evidence**, before and after for behaviour the checks do not show.
7. **Where the ticket didn't decide**, the choices the session made where the
   issue was silent, including the paths it took because nobody was there to
   ask.
8. **Not verified**, what the session could not check, and behaviour the diff
   cannot show.
9. **Merge danger**, `**Door:**` one-way or two-way, and `**Blast radius:**`
   in a word, each with an optional why.
10. **Recipe**, only on a deliberately large, single-concern change.
11. **The spend footer**, what the job's runs have cost so far
    ([`spend.md`](spend.md)). The agent's.

- **Sections 4-10 are the session's**: the `implement` skill's closing report,
  which the prompt asks for in `.git/afk-description.md`. The prompt names the
  headings, because the skill that runs is whichever one opencode finds on the
  host (below), at whatever version that is. What goes under them is the
  [`pr` skill's](../../.agents/skills/pr/SKILL.md), which `implement` calls
  for it. Nothing is capped or cut.
- **The title is the issue's** on a `Closes` or `Refs` pull request, whatever
  the file says. A `Part of` pull request, the first piece of an issue too big for one,
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
  counted. A Summary visual has no step of its own there: the skill keeps the
  rest of the part to one screen, and a visual is far short of the limit, so a
  body over it is output that ran away rather than a visual that needs cutting
  ([#196](https://github.com/corygyarmathy/afk-agent/issues/196)). None of
  these is a gate failure. Each but the missing file is a log
  line, and so is counting the sensitive files.
- **Written once**, when the pull request opens. A session after that - a CI
  fix, or a new session that takes one over - is not asked for the file.
- **The sensitive line is recomputed on every push**, the first and each fix
  after it, so a later push that newly touches a sensitive path adds it. So is
  the spend footer, so a fix's run is counted. Only those two are edited: the description is read again as the edit is made, so
  the rest of it - an edit the operator made included - is left as it is, and
  the files are counted rather than listed when listing them would take it
  over GitHub's limit. An edit that never lands after `--effect-rounds`, or
  one over the limit even counted, is a log line, and the work goes on.
- **A revision's push recomputes it too**
  ([#148](https://github.com/corygyarmathy/afk-agent/issues/148)), with the
  same edit. The diff it matches is the whole pull request's, from where its
  head meets the base branch's current tip, fetched at the push: a pull
  request the operator rebased onto a newer tip before sending it back does
  not count the commits between the two as its own. The size is measured on
  the same diff and kept for the revision's reply
  ([#149](https://github.com/corygyarmathy/afk-agent/issues/149)). A measure
  that fails - the pull request or its base branch could not be read - is a
  log line: the push goes on, and the sensitive line is left as it is. A
  revision's push leaves the spend footer alone: the revision reports its own.

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
- **The `implement` skill**, at a version that checks the ticket before its
  first edit and stops on a gap
  ([`skills#32`](https://github.com/corygyarmathy/skills/issues/32)), and the
  **`pr` skill** it calls for the description's sections, where opencode discovers them from the workspace: in
  the implemented repository's `.agents/skills/`, or in the per-user skills
  directory. The model is told to reply that the `implement` skill is missing
  rather than implement without it, and then the gate finds no commits and
  hands back. Without `pr`, the prompt still lists the headings the agent
  reads, but nothing tells the session what goes under them.
- **The GitHub App**, as for review ([`review.md`](review.md#the-apps-permissions)),
  plus these:

  | permission | level | for | |
  | --- | --- | --- | --- |
  | Contents | write | the push, and the clone and every read of the remote's branches (write includes read) | the grant confirmed on the App by the operator, 2026-09-25; a read of a private repository not observed |
  | Pull requests | write | opening the pull request, its labels; editing the advisory review once a correction is done with | confirmed on the App by the operator, 2026-09-25; the edit not verified |
  | Issues | write | the claim, replies, hand-backs and labels on the issue; filing a first piece's rest, and its native dependency | by GitHub's documentation for the claim and the rest; the dependency endpoints' documentation names no permission; not verified |
  | Contents, Issues | read | on another repository the App is installed on: an issue's [premises](#the-premises) there. Where it is not installed, a public repository is read with no token | not verified |
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
(branch, base, session and its last turn's input tokens, the session's description and what it says is left,
whether the work was cut and the commit kept on `<branch>-whole`, the criteria the work cannot meet, whether premises were fetched, the rest's issue and whether it was blocked, the sensitive line, gate attempts
and fixes, the last failure, the pushed head, the correction and its findings, what its sessions have spent), `requests/<job>.json` (which command the job's claim took, for its
instructions) and `notes/<job>.json` (the last error of a push, a pull request, a
review request or a label, for the hand-back to quote). All of it is disposable. Lost before the push, the work starts over.
Lost after it, the pull request is handed back rather than fixed on a new
branch.

## A session too long to continue

A gate retry, a fix, a cut and a correction each continue the session that
wrote the commits, unless it has grown too long
([#192](https://github.com/corygyarmathy/afk-agent/issues/192)).

Before a continuation the agent reads the input tokens of the session's last
turn: its last step's input, cached reads and writes included, kept in the
job's progress when the run finished, or failed. Over `--fresh-session-at`, a
fresh session takes over instead, given the issue, the branch's commits and the
failure, the cut or the findings, as one does after a session gone with opencode's data. It
is one rule for every continuation, and for `/revise` too. Each time it applies
is a log line naming the session, its size and the threshold. `0` continues
every session, and so does one whose size is unknown: a progress written before
the size was kept.

## Parameters

`afk help` lists them, and the NixOS module sets them
([`domain.md`](domain.md)). Without `--branch-prefix`, `afk work` neither runs
implement jobs nor answers `/implement`. With it, all of these are required:
`--gate`, `--gate-attempts`, `--implement-tier`, `--hand-off-label`,
`--denylist`, `--ci-wait`, `--ci-ceiling`, `--ci-fixes`, `--size-signal` and
`--fresh-session-at`, plus model choice,
`--effect-rounds` and `--hand-back-label` as for review. `--implement-needs`,
`--review-procedure` and `--sensitive` are optional. `/revise` runs on the same
parameters, and one of its own: [`revise.md`](revise.md#parameters).

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
- `--fresh-session-at` is the input tokens of a session's last turn over which
  it is not continued: [A session too long to continue](#a-session-too-long-to-continue).
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

A review with something to correct sends `implement-review` back to
`implementing` instead, and the correction runs `implement-run` to
`implement-watch` again. Once it is green, or has failed, `implement-review`
edits the review and moves on to `handing-off`.

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
