# Spend: what a job's footer says it cost

Every job that runs a model ends what it writes with a footer: what that job's
own runs cost, a line for each model it ran on
([#22](https://github.com/corygyarmathy/afk-agent/issues/22)). It is there so
the operator can tell what a tier's members cost on real work, and decide
whether a model earns its place in the tier. The code is
[`internal/spend`](../../internal/spend).

```
What this job's own model runs cost, estimated at list price as opencode reports it: not a bill, and not the account's spend.
opencode-go/deepseek-v4-flash · 1.5k in · 500 out · $0.0300
opencode-go/deepseek-v4-pro · 1.2M in (1.1M cached) · 34.5k out · $0.5000
```

## Where it goes

| job | footer on |
| --- | --- |
| `implement` | the pull request's [description](implement.md#the-description), last. Brought up to date on every later push, by the same edit as the sensitive line, so a CI fix's run is counted. |
| `revise` | its [reply](revise.md#the-reply), last. A revision's push edits the description's sensitive line and leaves its footer as it is: that footer is the implement job's. |
| `review` | the advisory review comment, last. |
| any of them | each hand-back comment the job writes. A job that could not finish still spent the money, and that may be the more interesting number. |

Each is written as part of a body the agent already writes under an
idempotency key, so a replayed transition does not write the footer twice.

## What it counts

- **What opencode reports.** For each run, the cost of each step at the model's
  list price and its tokens. Tokens in are all the input, cached reads
  included; tokens out are output and reasoning. A sub-agent's session is read
  after the run and counted on the line of the model the run was on, since a
  sub-agent runs on it unless its agent names a model of its own. A sub-agent
  whose session cannot be read makes the figure a floor, shown as `≥ $…` with
  how many went unread.
- **Failed runs too.** A run that fails transiently - a timeout, a provider
  error - is counted as far as it got, because it was paid for. That is how one
  job comes to have two lines: the retry runs on the next model in the tier
  (ADR 0001 §10).
- **No price, no figure.** A model opencode has no price for shows its tokens
  and `no listed price`, never `$0.0000`.
- **No usage, no footer.** A job whose runs reported nothing has no footer
  rather than a zero one. The description keeps the footer's hidden lines, with
  nothing between them, so a later push can fill them in.

The price is opencode's own, not the catalogue's dearest band
(`model.Price`, [`model-enrolment.md`](model-enrolment.md)): opencode knows
which band a request landed in and what was read from cache. It is still a list
price, and an estimate: a subscription or a negotiated rate bills something
else.

## What it is not

- **Not a decision.** Nothing reads it back. Admission, the resolver, a retry
  and a deferral are decided exactly as they would be without it, and a test
  in `internal/spend` checks that none of the packages that decide imports it.
- **Not the account's spend.** It counts this job's runs. A [budget
  observation](budget.md) counts the account, interactive use included, and the
  two are never reconciled: they answer different questions (ADR 0001 §11).
- **Not durable.** It is kept in the job's state directory until the job
  writes it. That directory is disposable (ADR 0001 §6), and a footer lost with
  it is missing or short, never wrong in the other direction. A footer that
  cannot be kept is a log line; the job goes on.
- **Not shown to the reviewer.** The advisory review reads the description
  without its footer, as it reads it without its sensitive line.
