# Notification

What reaches the operator when nobody is watching, and what does not. The
decisions it implements are [ADR 0001 §10 and
§13](../adr/0001-a-go-state-machine-in-its-own-repository.md); the code is
[`internal/notify`](../../internal/notify).

## The three conditions

| condition | when it fires | what it says |
| --- | --- | --- |
| a job **parked** after a failure | a transition failed and there was no retry left to schedule, or the job is in a state no transition runs from | the job, its state, its attempts, the tracker subject, and the error |
| a budget window is **spent** | the provider reports a window `rate-limited` while the pool is deciding whether to start a job | the window, its percent, and when it reopens |
| a job's model tier **stays exhausted** | the job's tier has run out `--tier-notify-after` times in one episode | the job, the tracker subject, how many times since when, what the tier said, and when it tries again |

Nothing else notifies. Not a pull request ready for review, not a job handed
back, not a red CI run, not a retry still in flight, not a window approaching
its threshold. Those are states, and the operator queries them when they choose
to look: `afk budget` for the budget, the job store for the queue, GitHub for
everything about the work itself.

The happy path publishes nothing at all.

## What is not a notification

- **A hand-back.** A job that came to rest without failing is a state. The
  condition is a park *with* an error, not a park. The exception is a hand-back
  whose own effect failed: the state moved and the job is resting, but the
  comment that was to tell the human never posted, so the tracker says nothing
  and this channel is the only thing left. A hand-back that posted its comment
  is silent.
- **A deferral, on its own.** A deferred job comes back on its own. A limited
  budget defers to the timestamp the provider gave, or for `--tier-wait` when
  it gave none; the operator hears about the budget as the second condition,
  not about each job it defers. An exhausted tier defers for `--tier-wait`,
  which is the agent's guess rather than the provider's, and is silent until it
  has happened `--tier-notify-after` times in one episode.
- **A retry.** A failure the backoff rescheduled has not come to rest.
- **Approaching a limit.** New jobs stop starting and the queue is looked at
  again next poll. See [`budget.md`](budget.md).
- **`afk run`.** A hand-invocation notifies nothing; the operator is reading
  the output.

## An exhausted tier

`review-run` and `implement-run` defer a job whose every candidate model failed
transiently, and the resume sends it back to try the tier again from its first
model. A tier that recovers is a bad few minutes at a provider, and says
nothing (ADR 0001 §10). A tier that does not - every enrolled model unknown to
the provider, or an outage longer than the wait - would otherwise defer and
resume indefinitely, and the only sign would be a job that is always in
`deferred`.

So the pool counts. An **episode** starts the first time a job's tier runs out,
and ends the next time the job moves anywhere other than back to the model or
into `deferred` - the model answered, or something else moved the job on - or
the next time the job parks, which hands it to the operator. A candidate failing
on the way, or a run that errored and was rescheduled, is inside the episode.
The operator is told once, on the `--tier-notify-after`th exhaustion of an
episode; a later episode of the same job is told again.

The count is in memory, like the suppression below. A restart starts every
episode again, so a tier that is still out is told again once it has run out
`--tier-notify-after` more times; whether it should survive a restart is #91.
Only a move the pool makes ends an episode: a job moved on by a hand-run
`afk run`, or by an edit to the store, keeps its episode until the process
restarts, so a later exhaustion of that job is counted into it.

## Repeats

The same occurrence of a condition is published once per process. A limited
window is re-observed by every worker on every pass, and a parked job stays
parked, so what is suppressed is the repeat rather than the condition:

- a budget window is one occurrence per `resetsAt`, so the next time that window
  is spent is published again;
- a parked job is one occurrence per state and attempt count, so a job an
  operator freed and which parked again is published again;
- an exhausted tier is one occurrence per job and episode.

A limit the provider reports with no `resetsAt` is the one occurrence this does
not divide: its key is the same every time, so a later exhaustion of that window
is silent for the life of the process. The pool waits and re-asks on its own
poll there rather than deferring, so the condition itself is not lost.

Suppression is in memory. A restart re-publishes a condition that is still true.

## Parameters

```
--notify-url <url>          AFK_NOTIFY_URL          ntfy topic to publish to, topic included
--notify-key <path>         AFK_NOTIFY_KEY          file holding the ntfy token
--tier-notify-after <n>     AFK_TIER_NOTIFY_AFTER   exhaustions of a job's tier, in one episode,
                                                    before the operator is told
```

- Without `--notify-url` nothing is notified.
- `--tier-notify-after` is required with `--notify-url`, and at least 1. One is
  every bad few minutes at a provider; a larger count is a job deferred for
  about that many `--tier-wait`s before anyone hears. Given without
  `--notify-url` it is refused.
- `--notify-key` is optional; without it the publish carries no `Authorization`
  header. A key with no URL is refused.
- The key is a path, not the key: an argument is visible in `ps` and an
  environment variable in `/proc`. It is read once, at startup, so a rotated
  token is a restart.
- A publish that fails is one log line and changes nothing downstream. It does
  not suppress the next attempt, so a channel that is down keeps saying so.

The values belong to the NixOS module in
[`corygyarmathy/dotfiles`](https://github.com/corygyarmathy/dotfiles), which
already runs the ntfy server and holds a token as
`monitoring/ntfy/alerts-token`: [`domain.md`](domain.md).

## Reading it by hand

One of the three has been read off the real server, and the others have not:

- **A park** was published to the live topic and read back off it, through the
  "no transition runs from this state" path below.
- **A budget window that is spent** has not been. `budget.Endpoint` is a
  constant and the status comes from the provider, so a `rate-limited` window
  cannot be arranged - it has to be caught the next time the account is
  actually limited.
- **An exhausted tier** has not been. It needs every candidate in a tier to
  fail transiently at the provider, `--tier-notify-after` times over, so it
  reaches both the network and a model.

The first two need neither to reach:

```bash
# A park. Any transition that fails, with no retry policy configured, parks on
# its first attempt; the pool is what publishes, so this is `afk work` and not
# `afk run`.
afk work --store /var/lib/afk/state.db --lease 5m --workers 1 --poll 5s \
  --token-wait 30s --notify-url https://ntfy.example/afk-agent \
  --notify-key /run/credentials/afk-agent.service/ntfy-token \
  --tier-notify-after 3
```

```bash
# A spent budget. `afk budget` says whether a window is actually limited; when
# one is, the same `afk work` invocation publishes it on its first pass.
afk budget --budget-key /run/credentials/afk-agent.service/opencode-api-key
```

The tests in this repository run offline, so the publish seam is stubbed in
them and the live server is reached only this way.
