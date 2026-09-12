# Triage labels

What this repository's tracker uses labels for, and - deliberately - how little
of that is settled.

## The two that are settled

| Label             | Sits on             | Means                                                                   |
| ----------------- | ------------------- | ----------------------------------------------------------------------- |
| `ready-for-agent` | an issue            | This issue may be worked unattended. It is the queue.                   |
| `agent-stuck`     | an issue or a PR    | The agent stopped without finishing and a human needs to look.          |

These two are the **eligibility label** and the hand-back marker as `CONTEXT.md`
defines them, and they are settled because the design needs exactly them:
something that admits work to the queue, and something that asks for attention.

`ready-for-agent` is the single label the agent reads rather than writes, and it
is a filter rather than an instruction. It says this issue *may* be worked; it
never says a particular thing should happen to it. Every request for the agent
to *do* something is a **command** on a pull request instead.

`agent-stuck` is deliberately not "back to `ready-for-agent`". Re-applying the
eligibility label to a ticket the agent could not finish sends it straight round
the queue again to burn its retry budget on the same failure. A human decides
what happens next.

## The rest are not settled

The prototype in `corygyarmathy/dotfiles` also writes `agent-working`,
`agent-ready-for-review` and `agent-revising`, and its tracker carries the full
`needs-triage` / `needs-info` / `ready-for-human` / `wontfix` triage vocabulary.
Some of that will reappear here. Not all of it will, and what does will not
necessarily mean what it means there - several of those labels exist to express
things this design expresses differently. Claim and lease are now distinct
concepts (ADR 0001 §7), so a label that conflated them does not survive
unchanged.

So: do not create the rest of that set here on the assumption that it carries
over. If a transition you are implementing needs a tracker-visible state, say so
on the issue and let it be decided then, in the vocabulary of `CONTEXT.md`.

## The strings are configuration

The names above are the strings currently in use for a human reading this
tracker with `gh`. They are **parameters**, not decisions: the label strings this
agent reads and writes are owned by the NixOS module in
`corygyarmathy/dotfiles` that configures it (ADR 0001, *Decision*; see
[`domain.md`](domain.md)). Code here takes them from configuration. Do not
hard-code them, and do not treat this table as their home.

## Nothing here is created yet

As of this writing the labels above do not exist on `corygyarmathy/afk-agent` and
no issue carries one. This file records the vocabulary, not the state of the
tracker. Creating them is a deliberate act, taken when the prototype is actually
pointed at this repository
([`corygyarmathy/dotfiles#275`](https://github.com/corygyarmathy/dotfiles/issues/275)).
