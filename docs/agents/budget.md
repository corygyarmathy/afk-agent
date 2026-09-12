# Budget observation

What the agent reads to decide whether new work may start, and what it does
about the answer. The decision it implements is [ADR 0001 §11,
§12](../adr/0001-a-go-state-machine-in-its-own-repository.md); the code is
[`internal/budget`](../../internal/budget).

## The endpoint

```bash
curl -H "Authorization: Bearer $KEY" https://opencode.ai/zen/go/v1/usage
```

```json
{"usage": {
  "rolling":  {"status": "ok",           "percent": 12,  "resetsAt": "2026-09-11T18:00:00Z"},
  "weekly":   {"status": "ok",           "percent": 41,  "resetsAt": "2026-09-15T00:00:00Z"},
  "monthly":  {"status": "rate-limited", "percent": 100, "resetsAt": "2026-09-27T00:00:00Z"}
}}
```

Undocumented, and added by
[anomalyco/opencode#16513](https://github.com/anomalyco/opencode/pull/16513).

- One account-wide dollar budget. There is no per-model dimension: when it is
  spent, every model is spent at once.
- The windows are independent, and the account is limited if **any** of them is.
  The response above is the live one from 2026-09-11.
- A window the agent has never heard of is read and counted like the three
  above.
- `resetsAt` is subscription-anniversary based, not calendar.
- The response also sees interactive use of the same account.

## What the agent does about it

| observed | what happens | jobs in flight |
| --- | --- | --- |
| any window `rate-limited` | due jobs are **deferred** to the last limited window's `resetsAt` | untouched |
| any window at or over the threshold | new jobs do not start; the queue is looked at again next poll | untouched |
| everything below the threshold | work starts | untouched |
| the endpoint cannot be read | the last good observation stands; with none, work starts | untouched |

A deferral names the window its timestamp came from, which is the window that
reopens last and not necessarily the one nearest its limit.

`afk run` is unaffected by all of it. A hand-invocation is an operator asking
for this job now.

Nothing here is written to the job store, and no ledger of the agent's own spend
is kept anywhere.

## Parameters

```
--budget-key <path>   AFK_BUDGET_KEY   file holding the usage API key
--budget-age <dur>    AFK_BUDGET_AGE   how long an observation is reused
--budget-at <pct>     AFK_BUDGET_AT    percentage of a window that stops new jobs
```

- Without `--budget-key` there is no admission control at all.
- `afk work` refuses to start with a key and no age.
- `--budget-age` is a floor on how often the endpoint is asked, and it applies to
  an attempt that failed as well as to one that answered. An endpoint that cannot
  be read is retried once an age rather than once per job, and it is logged at
  the same rate.
- Without `--budget-at`, only a window that is actually `rate-limited` stops
  work.
- The key is a path, not the key: an argument is visible in `ps` and an
  environment variable in `/proc`. It is read once, at startup, so a rotated key
  is a restart.

The values belong to the NixOS module in
[`corygyarmathy/dotfiles`](https://github.com/corygyarmathy/dotfiles), which
already holds the key as `opencode/api-key`: [`domain.md`](domain.md).

## Reading it by hand

```bash
afk budget --budget-key /run/credentials/afk-agent.service/opencode-api-key
```

```
rolling 12% (ok), resets 2026-09-11T18:00:00Z; weekly 41% (ok), resets 2026-09-15T00:00:00Z; monthly 100% (rate-limited), resets 2026-09-27T00:00:00Z
defer until 2026-09-27T00:00:00Z: monthly 100% (rate-limited), resets 2026-09-27T00:00:00Z
```

The tests in this repository run offline, so this is the only thing in the
binary that reaches the live endpoint.

## Pay-as-you-go

Not metered here. Models reached outside the Go subscription are bounded by the
account balance with auto-recharge disabled, which is a control on the
provider's side; exhausting it arrives as a transient failure and is handled as
one. See [ADR 0001
§12](../adr/0001-a-go-state-machine-in-its-own-repository.md) and
[`model-enrolment.md`](model-enrolment.md) for which providers are on the
subscription.
