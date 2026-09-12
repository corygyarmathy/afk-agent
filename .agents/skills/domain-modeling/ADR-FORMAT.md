# ADR Format

ADRs live in `docs/adr/`, one decision per file, numbered sequentially:
`0001-slug.md`, `0002-slug.md`. Scan the directory for the highest number and
increment. Create the directory lazily - only when the first ADR is needed.

If the repo has a `docs/adr/TEMPLATE.md`, that file wins over this one. Read it
and follow it.

## Template

```md
# ADR NNNN: <title>

- **Status:** Proposed | Accepted | Superseded by ADR XXXX
- **Date:** YYYY-MM-DD
- **Related Artefacts:** (Optional)
    - Verb: Artefact_Name

## Context

What's the situation? What forces are at play? A paragraph or two - the reader
should understand why a decision is needed.

## Decision

What did we decide? Be specific. "Use Postgres" is a decision; "use a database"
is not.

## Consequences

**Positive**

- ...

**Negative**

- ...

## Alternatives considered (Optional)

- **<Option B>.** Why rejected.
- **<Option C>.** Why rejected.
```

The title states the decision, not the topic: "A Go state machine in its own
repository", not "Repository structure".

## A decision, not a parameter

The test: **would reversing this send you back to the Alternatives section, or
just to a text editor?**

- Back to Alternatives -> it's a decision, and it belongs here. If you can't
  write an honest Alternatives entry with a genuine contender, it isn't one.
- Just a text editor -> it's a parameter. Counts, retry budgets, timeouts,
  branch prefixes, label names, path lists, option paths. These belong wherever
  the repo keeps its parameters - a plan, a config module, a constants file -
  even when they were settled in the same conversation as the decision.

Watch for the sentence that fuses both, which is the failure mode that actually
bites: "implementation gets up to 2 retries, and review is always a separate
pass in a fresh context" welds a number you will change to a decision you won't.
State the decision, then name where the parameter lives.

## Never amend an accepted ADR in place

A **Proposed** ADR has not been accepted and may be amended in place. Record the
change in an "Amended YYYY-MM-DD" block in the ADR's own words, so the decision
and its revisions stay together until it is accepted.

When an **Accepted** decision genuinely changes, write a new ADR and set the old
one's status to `Superseded by ADR NNNN`. Leave its body alone. A superseded ADR
is still the correct record of what was decided and why, and rewriting it
destroys the only thing it was for.

This is what keeps the record one hop deep: from any ADR to its successor, never
a trail of revisions. The current state was never the ADR's job.

Relocating content without changing a decision - moving a parameter out, fixing
a broken link - is not superseding and needs no new ADR. Say so in the commit
message.

## When to offer an ADR

All three must be true:

1. **Hard to reverse**: the cost of changing your mind later is meaningful.
2. **Surprising without context**: a future reader will look at the code and
   wonder "why on earth did they do it this way?"
3. **The result of a real trade-off**: there were genuine alternatives and you
   picked one for specific reasons.

If any is missing, skip it. An easy-to-reverse decision just gets reversed; an
unsurprising one prompts no question; one with no alternative records nothing
beyond "we did the obvious thing".

### What qualifies

- **Architectural shape.** "The write model is event-sourced, the read model is
  projected into Postgres." "A job is a persisted state machine and a transition
  is the unit of execution."
- **Integration patterns between contexts.** "Ordering and Billing communicate
  via domain events, not synchronous HTTP."
- **Technology choices that carry lock-in.** Database, message bus, auth
  provider, deployment target, language. Not every library - the ones that would
  take a quarter to swap out.
- **Boundary and scope decisions.** "Customer data is owned by the Customer
  context; other contexts reference it by ID only." The explicit no-s are as
  valuable as the yes-s.
- **Deliberate deviations from the obvious path.** "Manual SQL instead of an ORM,
  because X." Anything where a reasonable reader would assume the opposite -
  these stop the next engineer from "fixing" something that was deliberate.
- **Constraints not visible in the code.** "We can't use AWS, for compliance."
  "Responses must be under 200ms, by partner contract."
- **A decision not to hold an opinion.** "Spend is bounded by a vendor-enforced
  ceiling; this program does not meter against it." Stated as a decision because
  the day someone adds metering is the day the reasoning needs to be visible,
  and an omission is not visible.
- **Rejected alternatives where the rejection is non-obvious.** If you considered
  GraphQL and picked REST for subtle reasons, record it; otherwise someone
  suggests GraphQL again in six months.
