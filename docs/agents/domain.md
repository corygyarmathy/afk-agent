# Domain docs

How to consume this repository's domain documentation, and the ADR/plan boundary
it holds to. For the full map of which document owns which fact, see
[`documentation.md`](documentation.md); this file covers the domain record only.

## Before exploring, read these

- **[`CONTEXT.md`](../../CONTEXT.md)** at the repository root: the glossary. This
  repository is single-context; there is no `CONTEXT-MAP.md` and there should
  not be one.
- **[`docs/adr/`](../adr/)**: the ADRs touching the area you are about to work
  in. [ADR 0001](../adr/0001-a-go-state-machine-in-its-own-repository.md) is the
  whole design and is the one to read first, whatever you were asked to do.

## Use the glossary's vocabulary

When your output names a domain concept - an issue title, a test name, a type
name, a hypothesis, a commit message - use the term as `CONTEXT.md` defines it,
and not a synonym it explicitly lists under `_Avoid_`.

This matters more here than in most repositories, because several pairs in that
glossary are distinctions the prototype got wrong and this design exists to fix.
A **claim** is not a **lease**. A **hand-off** is not a **hand-back**. A
**command** is not a **label**. Collapsing either pair in a name or a comment
re-introduces the confusion the vocabulary was written to end.

If the concept you need is not in the glossary, that is a signal: either you are
inventing language the project does not use, or there is a real gap. Say which
you think it is rather than picking a word and moving on.

## Flag ADR conflicts

If what you are about to do contradicts an ADR, surface it rather than silently
overriding it:

> _Contradicts ADR 0001 §3 (waiting is never in-process), but worth reopening
> because…_

## Keep parameters out of ADRs

An ADR records a decision and the reasoning behind it, at a date. A parameter is
a value that is expected to change.

The test: **would reversing this send you back to the Alternatives section, or
just to a text editor?**

- Back to Alternatives -> a decision. It belongs in an ADR.
- Just a text editor -> a parameter.

In this repository parameters have a particular home, and it is not this
repository. Worker counts, poll intervals, retry budgets, thresholds, tier
membership, budget ceilings, branch prefixes and label strings are owned by the
NixOS module in [`corygyarmathy/dotfiles`](https://github.com/corygyarmathy/dotfiles)
that packages and configures this agent (ADR 0001, *Decision*). Code here reads
them from configuration; it does not hard-code them, and an ADR here does not
state them.

Watch for the sentence that fuses both, which is the failure mode that actually
bites: "implementation gets up to 2 retries, and review is always a separate
pass in a fresh context" welds a number you will change to a decision you will
not. State the decision, then name where the parameter lives.

## Never amend an accepted ADR in place

A **Proposed** ADR has not been accepted and may be amended in place, with an
"Amended YYYY-MM-DD" block recording the change in the ADR's own words, so the
decision and its revisions stay together until it is accepted. ADR 0001 is
Proposed and is on that footing today.

When an **Accepted** decision genuinely changes, write a new ADR and set the old
one's status to `Superseded by ADR NNNN`. Leave its body alone: a superseded ADR
is still the correct record of what was decided and why, and rewriting it
destroys the only thing it was for.

This is what keeps the record one hop deep - from any ADR to its successor,
never a trail of revisions. The current state was never the ADR's job.

Relocating content without changing a decision - moving a parameter out, fixing
a link - is not superseding and needs no new ADR. Say so in the commit message.

## Format

[`docs/adr/TEMPLATE.md`](../adr/TEMPLATE.md) is the format of record, and it
wins over anything a skill carries. The `domain-modeling` skill's
`ADR-FORMAT.md` says the same thing and defers to it.
