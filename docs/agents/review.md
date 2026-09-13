# Review

What `/review` does, what it needs on the host, and how to run one by hand. The
decisions are [ADR 0001](../adr/0001-a-go-state-machine-in-its-own-repository.md)
§2, §5, §7, §10 and §14, and
[ADR 0005](../adr/0005-the-agent-authenticates-as-its-github-app.md) for how the
agent authenticates; the spec is
[#3](https://github.com/corygyarmathy/afk-agent/issues/3) and
[#29](https://github.com/corygyarmathy/afk-agent/issues/29); the code is
[`internal/intake`](../../internal/intake) and
[`internal/review`](../../internal/review).

## What it does

A `/review` comment on an open pull request, from an account with write access
that is not the agent's, produces one advisory review comment on that pull
request's current head. It never gates, never merges, never pushes, and writes
no label.

| transition | from | does |
| --- | --- | --- |
| `review` | `start` | reacts 👀 to every unanswered `/review` (the claim), then either moves to `reviewing` or, if the head already has a review, replies "Already reviewed" and rests |
| `review-run` | `reviewing` | checks the head out into a fresh workspace and runs one enrolled model on it; a transient failure tries the next model, an exhausted tier or a limited budget defers |
| `review-post` | `posting` | posts the reply, under a key numbered by posting round |
| `review-verify` | `verifying` | rests once the reply is on the pull request, and sends it round again if it is not |
| `review-resume` | `deferred` | tries the tier again from its first model |

A review is recognised on the tracker by a hidden `<!-- afk:review head=<sha> -->`
line in the agent's comment, and a command as answered by the agent's 👀
reaction. Neither is kept in the store, so deleting the store costs dedup
history and never a second review of a head already reviewed.

## What it needs on the host

- **git**, on `PATH`. The head is fetched shallow from
  `https://github.com/<owner>/<name>.git` with no credentials: a private
  repository cannot be reviewed yet.
- **opencode**, at `--opencode`, with credentials for every provider enrolled in
  the review tier.
- **The GitHub App**, `--app-id`, with its private key in the file at
  `--app-key`. It must be installed on `--repo` with permission to read pull
  requests and write issue comments and reactions. The agent mints its own
  installation tokens, scoped to `--repo`, and its login is the App's
  `<slug>[bot]` account, read from GitHub at startup.
- **The enrolment file**, at `--enrolment`: [`model-enrolment.md`](model-enrolment.md).

The state directory is the directory holding `--store`. Beside the store it
holds the catalogue cache (`models.json`), a workspace per review while it runs
(`workspaces/`), and replies written and not yet seen on the pull request
(`replies/`). All of it is disposable.

## Parameters

`afk help` lists them, and the NixOS module sets them
([`domain.md`](domain.md)). Two interact with a review in ways worth knowing
before choosing values:

- `--lease` must be longer than a model run. A lease that lapses mid-run lets
  another worker take the job; the store refuses the first run's commit, so the
  work is wasted rather than duplicated, but it is still wasted.
- `--model-attempts` bounds two things: the candidates tried before a tier is
  exhausted, and the times a reply is posted before a reply that never appears
  is handed back.

## Running one by hand

Every step runs with no daemon present (ADR 0001 §4). With the parameters in the
environment:

```bash
# Read the tracker once. Prints the jobs it made due, e.g. `review-pr-12 due ...`.
afk intake

# Then each transition in turn, reading the state each one prints.
afk run review        --pr 12   # claim; -> reviewing, or "already reviewed" and rest
afk run review-run    --pr 12   # the model run; -> posting
afk run review-post   --pr 12   # -> verifying
afk run review-verify --pr 12   # -> start, not scheduled: done
```

`afk run review --pr 12` works without `afk intake` too: naming the subject
creates the job.

## What is not done yet

- Exhausting a tier does not notify. It is owed before the agent runs
  unattended: [#24](https://github.com/corygyarmathy/afk-agent/issues/24).
- A review job's requirements are a tier and capabilities. A context minimum
  and a price ceiling are supported by the resolver and not yet parameters.
