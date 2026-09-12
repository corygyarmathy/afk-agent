# Notification

What reaches the operator when nobody is watching, and what does not. The
decision it implements is [ADR 0001
§13](../adr/0001-a-go-state-machine-in-its-own-repository.md); the code is
[`internal/notify`](../../internal/notify).

## The two conditions

| condition | when it fires | what it says |
| --- | --- | --- |
| a job **parked** after a failure | a transition failed and there was no retry left to schedule, or the job is in a state no transition runs from | the job, its state, its attempts, the tracker subject, and the error |
| a budget window is **spent** | the provider reports a window `rate-limited` while the pool is deciding whether to start a job | the window, its percent, and when it reopens |

Nothing else notifies. Not a pull request ready for review, not a job handed
back, not a red CI run, not a retry still in flight, not a window approaching
its threshold. Those are states, and the operator queries them when they choose
to look: `afk budget` for the budget, the job store for the queue, GitHub for
everything about the work itself.

The happy path publishes nothing at all.

## What is not a notification

- **A hand-back.** A job that came to rest without failing is a state. The
  condition is a park *with* an error, not a park.
- **A deferral.** A deferred job comes back on its own at the timestamp the
  provider gave; nobody has to do anything.
- **A retry.** A failure the backoff rescheduled has not come to rest.
- **Approaching a limit.** New jobs stop starting and the queue is looked at
  again next poll. See [`budget.md`](budget.md).
- **`afk run`.** A hand-invocation notifies nothing; the operator is reading
  the output.

## Repeats

The same occurrence of a condition is published once per process. A limited
window is re-observed by every worker on every pass, and a parked job stays
parked, so what is suppressed is the repeat rather than the condition:

- a budget window is one occurrence per `resetsAt`, so the next time that window
  is spent is published again;
- a parked job is one occurrence per state and attempt count, so a job an
  operator freed and which parked again is published again.

Suppression is in memory. A restart re-publishes a condition that is still true.

## Parameters

```
--notify-url <url>    AFK_NOTIFY_URL   ntfy topic to publish to, topic included
--notify-key <path>   AFK_NOTIFY_KEY   file holding the ntfy token
```

- Without `--notify-url` nothing is notified.
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

Both conditions are tested by hand once each, against the real server. Neither
needs a network or a model to reach:

```bash
# A park. Any transition that fails, with no retry policy configured, parks on
# its first attempt; the pool is what publishes, so this is `afk work` and not
# `afk run`.
afk work --store /var/lib/afk/state.db --lease 5m --workers 1 --poll 5s \
  --token-wait 30s --notify-url https://ntfy.example/afk-agent \
  --notify-key /run/credentials/afk-agent.service/ntfy-token
```

```bash
# A spent budget. `afk budget` says whether a window is actually limited; when
# one is, the same `afk work` invocation publishes it on its first pass.
afk budget --budget-key /run/credentials/afk-agent.service/opencode-api-key
```

The tests in this repository run offline, so the publish seam is stubbed in
them and the live server is reached only this way.
