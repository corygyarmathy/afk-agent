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
| a `rate-limited` window the operator has **waived** | work starts, spending the pay-as-you-go balance, until the waiver's timestamp | untouched |
| any window at or over the threshold | new jobs do not start; the queue is looked at again next poll | untouched |
| everything below the threshold | work starts | untouched |
| the endpoint cannot be read | the last good observation stands; with none, work starts | untouched |

A deferral names the window its timestamp came from, which is the window that
reopens last and not necessarily the one nearest its limit. A window that is
waived is left out of all of it, the threshold included.

A window that is `rate-limited` also reaches the operator once, as a
notification: [`notification.md`](notification.md). Approaching one does not.
The first job admitted under a waiver is told once per waiver, because the
endpoint cannot say whether the provider's fallback is on and a waiver set with
it off fails every run.

## Waiving a window

A **waiver** is the operator's permission for work to carry on through one spent
window until that window next resets (CONTEXT.md: waiver). It spends the
pay-as-you-go balance, and the operator is the only one who grants one, because
carrying on through a spent window is a decision to spend money.

```bash
# Work continues through the spent monthly window until 2026-09-27T00:00:00Z,
# the window's own reset as `afk budget` prints it.
afk work ... --budget-key /run/credentials/afk-agent.service/opencode-api-key \
  --budget-waive monthly=2026-09-27T00:00:00Z
```

- **Turn the provider's fallback on first.** "Use balance" in the console,
  billing settings. The agent cannot see that setting, and a waiver set with it
  off fails every run as a transient failure: the waiver notification is how the
  operator notices sooner.
- **The timestamp is the window's `resetsAt`**, as `afk budget` prints it. The
  waiver lapses when it passes, so the next time that window is spent work defers
  again. The endpoint moves `resetsAt` by up to a second between observations,
  which is why nothing compares the two exactly: the operator's timestamp is the
  window's own, and the next period starts about a month later.
- **Name windows, not `rolling`/`weekly`/`monthly` specifically.** Which names
  may be waived is the module's to restrict; the agent applies whatever it is
  given.
- **A waiver binds resolution as well as admission**, so a job already in flight
  and a hand-invocation resolve against the same waived window rather than being
  refused by it.

The short windows are waited out rather than waived: a rolling window's percent
falls as old usage ages out, and a waiver on it would spend the balance to save
minutes.

`afk run` is unaffected by all of it. A hand-invocation is an operator asking
for this job now.

Nothing here is written to the job store, and no ledger of the agent's own spend
is kept anywhere.

## Parameters

```
--budget-key <path>      AFK_BUDGET_KEY      file holding the usage API key
--budget-age <dur>       AFK_BUDGET_AGE      how long an observation is reused
--budget-at <pct>        AFK_BUDGET_AT       percentage of a window that stops new jobs
--budget-waive <w>=<t>   AFK_BUDGET_WAIVE    waive a window until a resetsAt, repeatable
```

- Without `--budget-key` there is no admission control at all.
- `afk work` refuses to start with a key and no age.
- `--budget-age` is a floor on how often the endpoint is asked, and it applies to
  an attempt that failed as well as to one that answered. An endpoint that cannot
  be read is retried once an age rather than once per job, and it is logged at
  the same rate.
- Without `--budget-at`, only a window that is actually `rate-limited` stops
  work.
- `--budget-waive` takes `<window>=<RFC3339>` and may be given more than once;
  `AFK_BUDGET_WAIVE` is the same list comma-separated. A waiver is refused
  without `--budget-key`, a malformed one is refused at startup, and one window
  waived twice is refused rather than letting a timestamp win.
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

With a waiver, the same reading admits and says which window is waived and
until when - whether or not that window is spent yet, so a waiver can be checked
before it is relied on:

```bash
afk budget --budget-key /run/credentials/afk-agent.service/opencode-api-key \
  --budget-waive monthly=2026-09-27T00:00:00Z
```

```
rolling 12% (ok), resets 2026-09-11T18:00:00Z; weekly 41% (ok), resets 2026-09-15T00:00:00Z; monthly 100% (rate-limited), resets 2026-09-27T00:00:00Z
start (waived: monthly until 2026-09-27T00:00:00Z)
monthly is waived until 2026-09-27T00:00:00Z
```

The tests in this repository run offline, so this is the only thing in the
binary that reaches the live endpoint.

## Pay-as-you-go

Not metered here. Models reached outside the Go subscription are bounded by the
account balance with auto-recharge disabled, which is a control on the
provider's side; exhausting it arrives as a transient failure and is handled as
one. A waiver deliberately crosses that boundary - the operator turns on the
provider's fallback and waives a window, and work continues on the balance until
the window resets. See [ADR 0001
§12](../adr/0001-a-go-state-machine-in-its-own-repository.md) and
[`model-enrolment.md`](model-enrolment.md) for which providers are on the
subscription.
