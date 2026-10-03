# Spend: what a job's footer says it cost

Every job that runs a model ends what it writes with a footer: what that job's
own runs cost, a line for each model it ran on
([#22](https://github.com/corygyarmathy/afk-agent/issues/22)). The code is
[`internal/spend`](../../internal/spend).

```
What this job's own model runs cost, estimated at list price: not a bill, and not the account's spend.
opencode-go/deepseek-v4-flash · 1.5k in · 500 out · $0.0300
opencode-go/deepseek-v4-pro · 1.2M in (1.1M cached) · 34.5k out · $0.5000
```

## Where it goes

| job | footer on |
| --- | --- |
| `implement` | the pull request's [description](implement.md#the-description), last. Brought up to date on every later push, by the same edit as the sensitive line, so a CI fix's run is counted. A footer that would take the description over GitHub's limit is left as it was, and the sensitive line is still written. |
| `revise` | its [reply](revise.md#the-reply), last. A revision's push edits the description's sensitive line and leaves its footer as it is: that footer is the implement job's. |
| `review` | the advisory review comment, last. A review counts from its claim: runs for a review that was never written are in no footer. |
| any of them | each hand-back comment the job writes. |

A replayed transition does not write the footer twice: each footer is part of a
body the agent writes under an idempotency key.

## What it counts

- **Tokens.** Tokens in are all the input, cached reads included; tokens out
  are output and reasoning. A sub-agent's session is read after the run and
  counted on the line of the model the run was on. A sub-agent whose session
  cannot be read makes the figure a floor, shown as `≥ $…` with how many went
  unread. A run killed at its bound has no time left to read them, so its
  sub-agents are always unread.
- **Cost.** opencode's cost for the run, at list price. A run opencode reports
  no cost for is priced from the catalogue's dearest band
  ([`model-enrolment.md`](model-enrolment.md)), and its line says how much of
  the figure is that: `at the catalogue's dearest price`.
- **Failed runs too.** A run that fails transiently - a timeout, a provider
  error - is counted as far as it got. That is how one job comes to have two
  lines: the stay moves the next run to the next model in the tier (ADR 0001
  §10).
- **No price, no figure.** A model with no price from opencode or the catalogue
  shows its tokens and `no listed price`, never `$0.0000`. A model the
  catalogue lists as free shows `$0.0000`.
- **No spend reported, no footer.** A job whose runs reported nothing has no
  footer. The description keeps the footer's hidden lines, with nothing between
  them, so a later push can fill them in.

## What it is not

- **Not a decision.** Nothing reads it back. Admission, the resolver, a stay
  and a deferral are decided exactly as they would be without it. Each kind has
  a test that its stays and deferrals are the same with spend reported as
  without.
- **Not the account's spend.** It counts this job's runs. A [budget
  observation](budget.md) counts the account, interactive use included, and the
  two are never reconciled (ADR 0001 §11).
- **Not a bill.** A subscription or a negotiated rate bills something else.
- **Not durable.** It is kept in the job's state directory until the job
  writes it. That directory is disposable (ADR 0001 §6): a footer lost with it
  is missing or short. A footer that cannot be kept is a log line, and the job
  goes on.
- **Not shown to the reviewer.** The advisory review reads a description the
  agent wrote without its footer, as it reads it without its sensitive line. A
  description anyone else wrote is read as they wrote it.
