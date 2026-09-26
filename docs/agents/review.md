# Review

What `/review` does, what it needs on the host, and how to run one by hand. The
decisions are [ADR 0001](../adr/0001-a-go-state-machine-in-its-own-repository.md)
§2, §5, §7, §10 and §14, and
[ADR 0005](../adr/0005-the-agent-authenticates-as-its-github-app.md) for how the
agent authenticates; the spec is
[#3](https://github.com/corygyarmathy/afk-agent/issues/3),
[#29](https://github.com/corygyarmathy/afk-agent/issues/29) and
[#44](https://github.com/corygyarmathy/afk-agent/issues/44); the code is
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
never gates, never merges, never pushes, and writes no label.

The description's 👀 is taken by the first review of the agent's pull request
that finds it missing. A `/review` during the CI watch takes it before the
implement job has asked, and that review says the implement job asked for it.
When the job asks, that review of its head is already there, and it hands off
on it.

| transition | from | does |
| --- | --- | --- |
| `review` | `start` | reacts 👀 to every unanswered `/review`, and to the implement job's pull request if it has not yet (the claims), then either moves on to `reviewing` or, if the head already has a review, replies "Already reviewed" to each command and rests |
| `review-claimed` | `claiming` | reads the claims and replies back, makes any that are missing again, and once all of them are there moves on to `reviewing` or rests |
| `review-run` | `reviewing` | checks the head out into a fresh workspace, beside the diff and the issues the pull request closes, and has one enrolled model run the `reviewing-changes` skill on it; a transient failure tries the next model, an exhausted tier or a limited budget defers |
| `review-post` | `posting` | posts the reply, under a key numbered by posting round; out of rounds, owes a hand-back instead and moves to `handing-back` |
| `review-verify` | `verifying` | rests once the reply is on the pull request, and sends it round again if it is not |
| `review-handed-back` | `handing-back` | rests once the hand-back's comment and label are on the pull request, and makes whichever is missing again |
| `review-resume` | `deferred` | tries the tier again from its first model |

The review is the `reviewing-changes` skill's four-axis report. The workspace
is not what the skill expects - a shallow checkout, and no credentials for the
tracker - so `review-run` fetches what the skill would have: the diff into
`.git/afk-pr.diff`, and the pull request's description and every issue it
closes (by GitHub's closing keywords, `Closes #7`) into `.git/afk-pr-spec.md`.
An issue it cannot find is noted there as a gap, not a failure. The prompt,
[`internal/review/prompt.md`](../../internal/review/prompt.md), tells the model
where those are. A pull request that closes no issue is reviewed against its
description, and the report says so.

A review is recognised on the tracker by a hidden `<!-- afk:review head=<sha> -->`
line in the agent's comment, and a command as answered by the agent's 👀
reaction. The claims and the "Already reviewed" replies are read back before
the job moves on, as the review itself is, because a kill between the commit
and the reaction would otherwise leave a command looking unanswered for ever
([`internal/owed`](../../internal/owed)). Neither is kept in the store, so deleting the store costs dedup
history and never a second review of a head already reviewed.

## What it needs on the host

- **git**, on `PATH`. The head is fetched shallow from
  `https://github.com/<owner>/<name>.git` with no credentials: a private
  repository cannot be reviewed yet.
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
(`workspaces/`), and replies written and not yet seen on the pull request
(`replies/`). All of it is disposable.

## The App's permissions

The smallest set, by the names and levels on the App's settings page:

| permission | level |
| --- | --- |
| Metadata | read |
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
| reading a pull request, and its diff | Pull requests: read, or Contents: read | not verified: served with Metadata only |
| reading the issues a pull request closes | Issues: read | not verified: served with Metadata only |
| reading comments | Issues: read, or Pull requests: read | not verified: served with Metadata only |
| reading reactions | Issues: read | not verified: served with Metadata only |

Metadata is granted to every App and cannot be withheld. The App's own
requests (`GET /app`, finding the installation, minting a token) use its JWT
and need no installation permission. The review's `git` fetch sends no
credentials.

Pull requests: read with Issues: write also covers every row, by the header
and the table, but Issues: write was not observed on the claim without Pull
requests: write.

Two things GitHub's documentation does not say:

- The claim's header, and GitHub's permissions table, name Issues: write only.
  GitHub accepted Pull requests: write for it.
- A public repository serves every read without its grant, so a missing read
  grant shows up only on a private one - which the review's credential-less
  `git` fetch cannot review yet.

## Parameters

`afk help` lists them, and the NixOS module sets them
([`domain.md`](domain.md)). Two interact with a review in ways worth knowing
before choosing values:

- `--lease` must be longer than a model run, and than posting the reply. A
  lease that lapses mid-run lets another worker take the job; the store refuses
  the first run's commit, so the work is wasted rather than duplicated, but it
  is still wasted. The lease is renewed when a transition commits and held
  until its effects finish, so one that lapses mid-post lets a verify look for
  the reply before it lands, and post it again.
- `--model-attempts` bounds the candidates tried before a tier is exhausted.
  Only a model run that failed transiently moves on to the next candidate. Any
  other error in a run - the tracker, the checkout - counts against
  `--max-attempts`, as any transition's does, and the next run keeps the
  candidate, so an error that keeps coming back parks the job rather than
  exhausting the tier.
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

- Exhausting a tier does not notify. It is owed before the agent runs
  unattended: [#24](https://github.com/corygyarmathy/afk-agent/issues/24).
- A review job's requirements are a tier and capabilities. A context minimum
  and a price ceiling are supported by the resolver and not yet parameters.
