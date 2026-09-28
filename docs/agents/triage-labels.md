# Triage labels

What labels mean on a tracker this agent works, and which of them the agent
writes.

## A label names a state, not an actor

The agent acts on **commands**, never on labels, with one exception: the
eligibility label (below). So every other label is status that a human reads,
and saying "for human" or "for agent" in its name adds nothing. A label says
what the subject needs next. It does not say who does it. Bots elsewhere work
the same way: Kubernetes' Prow applies `needs-rebase` and `do-not-merge/hold`,
and neither names the bot.

## The two the agent writes

| Label            | Sits on               | Means                                                              |
| ---------------- | --------------------- | ------------------------------------------------------------------ |
| `needs-review`   | a pull request        | The **hand-off**: CI is green and a review has been posted.        |
| `needs-decision` | an issue or a PR      | The **hand-back**: the agent stopped without finishing. Its comment says why. |

`needs-review` is a signal, not a control. Nothing merges on it, and nothing
stops a merge before it is applied (ADR 0001 §15).

`needs-decision` does not mean "back to the queue". Sending a subject the agent
could not finish straight round again burns its retry budget on the same
failure. A human decides what happens next: reshape it and issue the command
again, or take it by hand.

Before the push, the hand-back sits on the issue. After the push, it sits on the
pull request and nowhere else, because by then the work and the failure both
belong to the pull request (`dotfiles` ADR 0007 §2).

## The triage labels

| Label          | Sits on  | Means                                                                   |
| -------------- | -------- | ----------------------------------------------------------------------- |
| `needs-triage` | an issue | Nobody has looked at it yet.                                            |
| `needs-info`   | an issue | It cannot be worked until its author answers a question on it.          |
| `ready`        | an issue | Triaged and specified. Anyone may take it, by hand or with `/implement`. |
| `wontfix`      | an issue | Decided against. Closed, not queued.                                    |

A human writes these, and the agent never reads them. `ready` replaces the
prototype's `ready-for-human`. Being ready is a fact about the issue, not about
who picks it up, and `/implement` can be issued on any issue.

## The eligibility label

The one label the agent reads. It opts an issue in to being taken **unattended**,
with no command. It is a queue filter, not an instruction: it says the issue
*may* be taken, never that a particular thing should happen to it.

Its string is `--eligibility-label` (`AFK_ELIGIBILITY_LABEL`), set by the
NixOS module, and matched without regard to case, as GitHub matches label names.
There is no default: without it, nothing is taken unattended. The string on
this tracker is `ready-for-agent`.

Intake reads it on every pass ([`internal/intake`](../../internal/intake)). An
open issue is taken when all of these hold:

- it carries the label, and it is an issue, not a pull request;
- it has no open blocker, read from its native dependencies
  (`issue_dependencies_summary.blocked_by`,
  [`issue-tracker.md`](issue-tracker.md#dependencies-between-issues)). An issue
  the API serves with no dependency summary is not taken, because its blockers
  cannot be read;
- it has no `implement` job in the store, whatever made it. A command's work
  that came to rest, handed off or handed back, is not the queue's to start
  over;
- it does not carry the agent's claim, a 👀 on the issue itself. That is what
  stops a wiped store taking it again.

Taking it makes the job `/implement` would make, due now. The job claims the
issue with a 👀 on it, because nothing asked and there is no command to claim.
Admission still decides when the job starts, as it does for every job.

**The order** is lowest issue number first, which on GitHub is oldest first,
whatever order the listing serves. Anything that takes only some of the
eligible issues takes the first ones in this order.

## What does not carry over

The prototype in `corygyarmathy/dotfiles` also wrote `agent-working` and
`agent-revising`. Neither survives. A job being taken is shown by the **claim**:
a 👀 reaction on whatever asked for the work, which is the command comment, or
for a review another job asked for, what that job posted. Work nobody asked
for, taken through the eligibility label, is claimed on the issue itself. A label that meant "a
process is on this" conflated the claim with the lease, and ADR 0001 §7 keeps
them apart. `agent-ready-for-review` and `agent-stuck` become `needs-review` and
`needs-decision`.

If a transition you are implementing needs a tracker-visible state that is not
here, say so on its issue, decide it there in the vocabulary of `CONTEXT.md`,
and add it here.

## The strings are configuration

The names above are the strings in use, for a human reading a tracker with `gh`.
They are **parameters**, not decisions. The label strings this agent reads and
writes are owned by the NixOS module in `corygyarmathy/dotfiles` that configures
it (ADR 0001, *Decision*; see [`domain.md`](domain.md)). Code here takes them
from configuration. Do not hard-code them, and do not treat this file as their
home.

## What exists on the trackers

As of 2026-09-24, only `ready-for-agent` exists on `corygyarmathy/afk-agent`.
`corygyarmathy/dotfiles` still carries the prototype's labels. A label is
created, or an old one renamed, when the transition that writes it lands. It is
not created ahead of time.
