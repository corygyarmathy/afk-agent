# Review

What `/review` does, what it needs on the host, and how to run one by hand. The
decisions are [ADR 0001](../adr/0001-a-go-state-machine-in-its-own-repository.md)
§2, §5, §7, §10 and §14, and
[ADR 0005](../adr/0005-the-agent-authenticates-as-its-github-app.md) for how the
agent authenticates; the spec is
[#3](https://github.com/corygyarmathy/afk-agent/issues/3),
[#29](https://github.com/corygyarmathy/afk-agent/issues/29),
[#44](https://github.com/corygyarmathy/afk-agent/issues/44),
[#124](https://github.com/corygyarmathy/afk-agent/issues/124) and
[#132](https://github.com/corygyarmathy/afk-agent/issues/132); the code is
[`internal/intake`](../../internal/intake) and
[`internal/review`](../../internal/review).

## What it does

A `/review` comment on an open pull request, from an account with write access
that is not the agent's, produces one advisory review comment on that pull
request's current head. So does the implement job's request, once CI is green
on the pull request it opened ([`implement.md`](implement.md)): it makes the
review job due, never comments. The review claims that request with a 👀 on the
pull request's description, which only counts on a pull request the agent
wrote, and the review comment says the implement job asked for it. The review
never gates, never merges, never pushes, and writes no label. The comment ends
with what its runs cost ([`spend.md`](spend.md)).

The description's 👀 is taken by the first review of the agent's pull request
that finds it missing. A `/review` during the CI watch takes it before the
implement job has asked, and that review says the implement job asked for it.
When the job asks, that review of its head is already there, and it hands off
on it.

The revise job asks the same way, once CI is green on a revision
([`revise.md`](revise.md)). Its request is claimed with a 👀 on the reply it
posted for that head, the agent's comment carrying the revision's reply marker,
never on the description. A reply for an older head is not claimed. A review
job already due when the revise job asks claims the reply from `review-run`,
by way of `review`. The review links the reply it claimed.

The revise job's review covers the revision's **delta**: from the head the
send-back was written against to the head the revision left, which is what the
operator's second sitting reads. The reply names the first in a hidden
`<!-- afk:revision-read head=<sha> -->` line, which counts only in the agent's
own comment. The rest of the pull request is context for the delta, not
something it reviews again. Every other review - a `/review`, the implement
job's, or a revise job's whose reply has no such line, or whose head has moved
since - covers the pull request against its base.

| transition | from | does |
| --- | --- | --- |
| `review` | `start` | reacts 👀 to every unanswered `/review`, to the implement job's pull request if it has not yet, and to the revise job's reply for the head if it has not yet (the claims), then either moves on to `reviewing` or, if the head already has a review, replies "Already reviewed" to each command and rests |
| `review-claimed` | `claiming` | reads the claims and replies back, makes any that are missing again, and once all of them are there moves on to `reviewing` or rests |
| `review-run` | `reviewing` | checks the head out into a fresh workspace, going back to `start` if the revise job's reply for it is unclaimed, beside the diff and the issues the pull request closes or is a first piece of, and has one enrolled model run the `reviewing-changes` skill on it; a transient failure tries the next model, an exhausted tier or a limited budget defers |
| `review-post` | `posting` | posts the reply, under a key numbered by posting round; out of rounds, owes a hand-back instead and moves to `handing-back` |
| `review-verify` | `verifying` | rests once the reply is on the pull request, and sends it round again if it is not |
| `review-handed-back` | `handing-back` | rests once the hand-back's comment and label are on the pull request, and makes whichever is missing again |
| `review-resume` | `deferred` | tries the tier again from its first model |

The review is the `reviewing-changes` skill's four-axis report. The workspace
is not what the skill expects - a shallow checkout, and no credentials for the
tracker - so `review-run` fetches what the skill would have: the diff into
`.git/afk-pr.diff`, and the pull request's description and every issue it
closes (by GitHub's closing keywords, `Closes #7`) into `.git/afk-pr-spec.md`.
A first piece of an issue too big for one pull request links it with the
implement kind's link line instead, `Part of #7. The rest is #9.`, straight
after its marker (#127): the spec then holds #7, headed as only partly done by
the piece, and #9, the issue filed for the rest, headed as out of scope, so
that what the piece leaves for later is not reported as missing. Work with an
acceptance criterion it cannot meet by itself links its issue with `Refs #7`
there instead (#199): the spec holds #7, headed as not closed by the pull
request. `Part of` or `Refs` anywhere else in a description links nothing.
A description the agent wrote goes without its sensitive line
([`implement.md`](implement.md#sensitive-paths)), so a pull request that
touches a sensitive path is reviewed as any other, and without its spend
footer ([`spend.md`](spend.md)). A description anyone else wrote goes as they
wrote it. An issue it cannot find is noted there as a gap, not a failure.
The diff is the tracker's three-dot compare from the pull request's base
commit, as the pull request reports it, to the head checked out. That is the
diff the pull request's own endpoint gives, but that endpoint refuses a pull
request that touches more than 300 files, and compare has no such cap (#176).
For a revision's delta, `.git/afk-pr.diff` holds the delta alone, read from the
tracker's compare of the two heads, and the pull request's diff against its
base goes beside it in `.git/afk-pr-whole.diff`, which the prompt passes as
the skill's context diff: every sub-agent may read it, and none reviews it.
The spec file starts
with the send-back the delta answers: each command the reply names, as the
operator wrote it, with a review's line comments under it, and a command no
longer there noted as a gap. The description and issues follow it as
background, since the delta takes on the send-back's points rather than the
whole issue. The skill's fold cut counts the delta like any diff, so a small
revision folds Approach into Correctness and runs two sub-agents. The prompt,
[`internal/review/prompt.md`](../../internal/review/prompt.md), tells the model
where those are. A pull request that closes no issue, and is no issue's first
piece, is reviewed against its description, and the report says so.

### What the operator sees

The advisory review is one comment on the pull request, and the whole of it
sits inside one `<details>`, collapsed. Its summary names the reviewed head and
nothing else: "Advisory review of `abc1234`. Open it after your own reading".
A revision's review names its range instead: "Advisory review of
`abc1234..def5678`".
There are no counts by severity and no verdict, because a line like "0
blockers" is what invites a rubber stamp. The wrapper is the agent's, added when it posts; the
model is told not to add one of its own.

Nothing is posted on a line of the diff: no review threads in "Files changed",
which cannot be collapsed and open threads that decide nothing. Each finding
cites its `file:line` as a permalink at the reviewed head instead. The model
writes a plain `path:line`, and `review-run` makes the link, while the checkout
of the head is still there: a citation is linked only when it names a file in
that checkout, and anything else is posted as written. The links are the
agent's rather than the model's so that they are there whether or not the model
follows the prompt.

What is in it is the skill's to decide, at two of the skill's inputs that the
prompt passes through: the severity floor and the fold cut, `--review-floor`
and `--review-fold-cut` ([Parameters](#parameters)). Findings are numbered
across the advisory review, from 1 in each, so a send-back can cite one
("advisory 3").

An advisory review is never deleted, and each advisory review is a new
comment, so a citation resolves to the latest advisory review before the
send-back that cites it. A finding from an earlier review, on code a revision
did not change, is not in the latest one: a send-back quotes it rather than
cite it by number. The tracker the review job writes through can only add a
comment, and a replayed transition finds the advisory review of its head
already there and posts nothing.

It is edited once, and only on the agent's own pull request: when the
implement or revise job that asked for it has made a
[correction](implement.md#the-correction) of its Correctness and Standards
findings. Every finding keeps its number, so a send-back still cites it, and
the review's `head=` marker keeps meaning the head it reviewed. Corrected, the
summary names both heads, "Advisory review of `abc1234`; corrected to
`def5678`, checked by reproductions and CI, not re-reviewed", each corrected
finding collapses to one line linking its commit, and the citations stay at
the reviewed head. A correction CI passed whose commits named no finding says
so in the summary instead of "corrected to". Failed, the pull request is back at the reviewed head, and
the findings are marked as advice a correction failed on. Nothing reviews the
corrected head again. A review that `/review` asked for on someone else's pull
request is never corrected, and never edited.

A review is recognised on the tracker by a hidden `<!-- afk:review head=<sha> -->`
line in the agent's comment, and a command as answered by the agent's 👀
reaction. The claims and the "Already reviewed" replies are read back before
the job moves on, as the review itself is, because a kill between the commit
and the reaction would otherwise leave a command looking unanswered for ever
([`internal/owed`](../../internal/owed)). Neither is kept in the store, so deleting the store costs dedup
history and never a second review of a head already reviewed.

## What it needs on the host

- **git**, on `PATH`. The head is fetched shallow from
  `https://github.com/<owner>/<name>.git`, with an installation token in that
  one `git` process's environment and none of the agent user's global or
  system `git` configuration, so a private repository is reviewed as a public
  one is. That process runs in a bare repository the agent owns, never in the
  workspace the session reads, and the head is brought into the workspace
  from there with no token. The App needs Contents: read for it
  ([The App's permissions](#the-apps-permissions)).
- **opencode**, at `--opencode`, with credentials for every provider enrolled in
  the review tier.
- **The GitHub App**, `--app-id`, with its private key in the file at
  `--app-key`. It must be installed on `--repo` with the permissions in
  [The App's permissions](#the-apps-permissions). The agent mints its own
  installation tokens, scoped to `--repo`, and its login is the App's
  `<slug>[bot]` account, read from GitHub at startup.
- **The `reviewing-changes` skill**, where opencode discovers it from the
  workspace: in the reviewed repository's own `.agents/skills/`, or in the
  per-user skills directory under the agent's `$HOME`. The model is told to
  reply that the skill is missing rather than review without it.
- **The enrolment file**, at `--enrolment`: [`model-enrolment.md`](model-enrolment.md).

The state directory is the directory holding `--store`. Beside the store it
holds the catalogue cache (`models.json`), a workspace per review while it runs
(`workspaces/`), replies written and not yet seen on the pull request
(`replies/`), and what the review's runs have spent until it is at rest
(`spent/`). All of it is disposable.

## The App's permissions

The smallest set, by the names and levels on the App's settings page:

| permission | level |
| --- | --- |
| Metadata | read |
| Contents | read |
| Pull requests | write |
| Issues | read |

What each request accepts, by the `X-Accepted-GitHub-Permissions` header
GitHub returned on 2026-09-25 (the review comment's is from GitHub's
[permissions table](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps)),
and what was observed on `corygyarmathy/dotfiles` with tokens minted below the
installation's grants:

| request | accepts | observed |
| --- | --- | --- |
| the 👀 claim, on a command on a pull request | Issues: write | refused (403) with Metadata only; accepted with Pull requests: write and no Issues grant |
| the review comment | Issues: write, or Pull requests: write | not verified |
| the 👀 claim on the implement job's pull request, and reading its reactions | not recorded | not verified |
| listing open issues and pull requests, which intake reads commands from | not recorded | not verified |
| listing open pull requests, which `implement` finds the agent's pull request in | Pull requests: read | not verified: served with Metadata only |
| reading a pull request | Pull requests: read, or Contents: read | not verified: served with Metadata only |
| reading the diff between two commits, for the pull request's diff from its base and a revision's delta | not recorded | not verified |
| listing a pull request's reviews, and one review's line comments, for a revision's send-back | as for the revise job ([`revise.md`](revise.md)) | as there |
| reading the issues a pull request closes, or is a first piece of, and a first piece's rest | Issues: read | not verified: served with Metadata only |
| reading comments | Issues: read, or Pull requests: read | not verified: served with Metadata only |
| reading reactions | Issues: read | not verified: served with Metadata only |
| the `git` fetch of the pull request's head | Contents: read | by GitHub's documentation; not verified. A public repository serves it with no credentials at all |

Metadata is granted to every App and cannot be withheld. The App's own
requests (`GET /app`, finding the installation, minting a token) use its JWT
and need no installation permission.

Pull requests: read with Issues: write also covers every row, by the header
and the table, but Issues: write was not observed on the claim without Pull
requests: write.

Two things GitHub's documentation does not say:

- The claim's header, and GitHub's permissions table, name Issues: write only.
  GitHub accepted Pull requests: write for it.
- A public repository serves every read without its grant, so a missing read
  grant shows up only on a private one. No private repository has been
  reviewed yet.

## Parameters

`afk help` lists them, and the NixOS module sets them
([`domain.md`](domain.md)). These interact with a review in ways worth knowing
before choosing values:

- `--review-floor` is the least severity an advisory review reports, and
  `--review-fold-cut` the changed lines below which it folds Approach into
  Correctness. What each means, how the cut is counted, and the default each
  has when unset are the skill's:
  [`reviewing-changes`](../../.agents/skills/reviewing-changes/SKILL.md). Go
  checks only that the floor is one of the skill's severities and the cut a
  positive count. Neither is a gate.
- `--model-timeout` bounds the model run. One still going when it runs out is
  killed with everything it started, and is a transient failure: the job
  stays, and its next run tries the next candidate. Each transient failure is
  a log line, and the last candidate's defers the tier with it, which is what
  the operator is told: [`notification.md`](notification.md).
- `--lease` should be longer than a model run, and than posting the reply. A
  lease that lapses mid-run lets another worker take the job; the store refuses
  the first run's commit, so the work is wasted rather than duplicated, but it
  is still wasted. Nothing ties it to `--model-timeout`: a short lease takes a
  dead holder's job back quickly, and a long bound lets a slow run finish. The
  lease is renewed when a transition commits and bounds its effects, so a post
  still going when it lapses is cancelled - but one that had already reached
  GitHub can still land after a verify has looked for the reply, and been
  posted again.
- `--model-attempts` bounds the candidates tried before a tier is exhausted.
  Only a model run that failed transiently moves on to the next candidate. Any
  other error in a run - the tracker, the checkout - counts against
  `--max-attempts`, as any transition's does, and the next run keeps the
  candidate, so an error that keeps coming back parks the job rather than
  exhausting the tier.
- `--tier-wait` is how long an exhausted tier defers before the resume tries it
  again from its first model, and `--tier-notify-after` is how many times in a
  row it may run out before the operator is told:
  [`notification.md`](notification.md).
- `--effect-rounds` bounds the times a review, a claim or a reply is posted
  before one that never appears counts as never landing. A review out of rounds
  is handed back: a short comment on the pull request saying so, with the last
  error, and `--hand-back-label`. A claim or a reply out of rounds is a failed
  attempt, since there is nothing to hand back through; the attempt after it -
  a retry, or an operator freeing the parked job - has rounds of its own. The
  two bounds multiply: before the job parks, a claim or a reply that never
  appears is made up to `--effect-rounds` × `--max-attempts` times.

## Running one by hand

Every step runs with no daemon present (ADR 0001 §4). With the parameters in the
environment:

```bash
# Read the tracker once. Prints the jobs it made due, e.g. `review-pr-12 due ...`.
afk intake

# Then each transition in turn, reading the state each one prints.
afk run review         --pr 12   # claim; -> claiming
afk run review-claimed --pr 12   # -> reviewing, or rest after "already reviewed"
afk run review-run     --pr 12   # the model run; -> posting
afk run review-post    --pr 12   # -> verifying
afk run review-verify  --pr 12   # -> start, not scheduled: done
```

A review out of posting rounds moves to `handing-back` instead, and
`afk run review-handed-back --pr 12` rests it once the comment and the label
are both there.

`afk run review --pr 12` works without `afk intake` too: naming the subject
creates the job.

## What is not done yet

- A review job's requirements are a tier and capabilities. A context minimum
  and a price ceiling are supported by the resolver and not yet parameters.
