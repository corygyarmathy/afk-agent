---
name: to-tickets
description: Break a plan, spec, or the current conversation into a set of tracer-bullet tickets, each declaring its blocking edges, published to the configured tracker (edges as text in one file per ticket locally, or native blocking links on a real tracker).
disable-model-invocation: true
---

# To Tickets

Break a plan, spec, or conversation into a set of **tickets**: tracer-bullet vertical slices, each declaring the tickets that **block** it.

The issue tracker and triage label vocabulary should have been provided to you. If not, tell the user to run `/setup-cory-gyarmathy-skills`.

## Process

### 1. Gather context

Work from whatever is already in the conversation context. If the user passes a reference (a spec path, an issue number or URL) as an argument, fetch it and read its full body and comments.

### 2. Explore the codebase (optional)

If you have not already explored the codebase, do so to understand the current state of the code. Ticket titles and descriptions should use the project's domain glossary vocabulary, and respect ADRs in the area you're touching.

Look for opportunities to prefactor the code to make the implementation easier. "Make the change easy, then make the easy change."

### 3. Draft vertical slices

Break the work into **tracer bullet** tickets.

<vertical-slice-rules>

- Each slice cuts a narrow but COMPLETE path through every layer the change touches (e.g. storage, domain logic, the API or CLI or UI that exposes it, tests): vertical, NOT a horizontal slice of one layer
- A completed slice is demoable or verifiable on its own
- A slice is **one concern the operator can review in one sitting**, tests included. No code exists yet, so size it by judgement: "a few hundred changed lines" is a rough guide, not a measure. A single fresh context window is the agent's ceiling, not the slice's size
- Split a slice that needs an "and" to describe it
- Split a slice that mixes a refactor with behaviour: the preparatory refactor becomes its own first slice, blocking the behaviour
- A mechanical change (reformat, rename, codemod) is a ticket of its own, holding only that change, and its "What to build" names the **recipe**: the command that reproduces it, or the transformation rule
- A deliberately large, single-concern ticket (a wide refactor's migrate batch, a formatter run) is a **recipe ticket**: it runs past one sitting on purpose, so it is reviewed by its recipe. Its "What to build" names the recipe where it can, and states that it needs an explicit `/implement` asked for as one PR. It takes the `recipe-ticket` triage role, never `ready-for-agent`. If the repo's triage-label mapping has no `recipe-ticket` role, publish it with no role and tell the user to rerun `/setup-cory-gyarmathy-skills`. A mechanical ticket that fits one sitting still names its recipe, but it is an ordinary ticket and takes `ready-for-agent`

</vertical-slice-rules>

Give each ticket its **blocking edges**: the other tickets that must complete before it can start. A ticket with no blockers can start immediately.

**Wide refactors are the exception to vertical slicing.** A **wide refactor** is one mechanical change (rename a column, retype a shared symbol) whose **blast radius** fans across the whole codebase, so a single edit breaks thousands of call sites at once and no vertical slice can land green. Don't force it into a tracer bullet; sequence it as **expand–contract**. First expand: add the new form beside the old so nothing breaks. Then migrate the call sites over in batches sized by blast radius (per package, per directory), each batch its own recipe ticket blocked by the expand, keeping CI green batch to batch because the old form still exists. Finally contract: delete the old form once no caller remains, in a ticket blocked by every migrate batch. When even the batches can't stay green alone, keep the sequence but let them share an integration branch that all block a final integrate-and-verify ticket; green is promised only there.

### 4. Settle each ticket's premises

A **premise** is a fact a ticket takes from outside itself and outside the code it changes: another repository's interface, flag or documented behaviour, a decision recorded on another ticket, what a blocker will produce. A premise the ticket leaves open hands its research to the implementing agent, and research is the most expensive thing an agent does.

State each premise as the fact itself, with a permalink to its source of record (a file at a commit, a comment, a tagged doc). "Maps to the flags the revise ticket defines" names where the fact will be decided, so it is not a premise. "afk-agent has no revise tier; a revision runs on implement's tier and needs (`docs/agents/revise.md` at <rev>)" is. A ticket with no premises says "None".

A **waiting premise** depends on what a blocker produces, not only on its order, so it can't hold until the blocker lands. State it as the outcome the blocker is expected to produce, taken from the plan or the blocker's ticket, link the blocker in place of a permalink, and mark it "confirm once <blocker> closes". A ticket with a waiting premise is published as `needs-triage`, whatever role it would otherwise carry; once the blocker closes, `/triage` confirms the premise and returns the ticket to that role. A blocker that only orders the work leaves the role as it is.

Confirm every other premise against its source now. One you cannot confirm becomes a question in the quiz, and stays out of the ticket until the user settles it.

### 5. Quiz the user

Present the proposed breakdown as a numbered list. For each ticket, show:

- **Title**: short descriptive name
- **Blocked by**: which other tickets (if any) must complete first
- **What it delivers**: the end-to-end behaviour this ticket makes work
- **Recipe ticket**: if it is one (runs past one sitting on purpose), and its recipe
- **Premises**: each as its fact and source, marking any waiting premise

Ask the user:

- Does the granularity feel right? (too coarse / too fine) Can each ticket be reviewed in one sitting?
- Are the blocking edges correct: does each ticket only depend on tickets that genuinely gate it?
- Should any tickets be merged or split further?
- Each premise you could not confirm, as its own question

Iterate until the user approves the breakdown.

### 6. Publish the tickets to the configured tracker

Publish the approved tickets. **How** depends on the tracker `/setup-cory-gyarmathy-skills` configured; the tickets are the same either way, only the shape of the blocking edges changes:

- **Local files** → write one file per ticket under `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01` in dependency order (blockers first). Each file's "Blocked by" lists the numbers/titles it depends on. Use the per-ticket file template below: one ticket per file, never a single combined file.
- **A real issue tracker (GitHub, Linear, …)** → publish one issue per ticket in dependency order (blockers first) so each ticket's blocking edges can reference real identifiers. Use the platform's native blocking / sub-issue relationship where it has one; otherwise set each ticket's "Blocked by" to the blocking issues. Apply each ticket's triage role unless instructed otherwise: `ready-for-agent`, `recipe-ticket` for a recipe ticket (see the slice rules), or `needs-triage` (see step 4). A `ready-for-agent` ticket is agent-grabbable by construction.

Work the **frontier**: any ticket whose blockers are all done. For a purely linear chain that means top to bottom.

Do NOT close or modify any parent issue.

<local-ticket-template>

# <NN>: <Ticket title>

**What to build:** the end-to-end behaviour this ticket makes work, from the user's perspective, not a layer-by-layer implementation list.

**Premises:** each outside fact the ticket rests on, with a permalink to its source of record, or "None".

**Blocked by:** the numbers/titles of the tickets that gate this one, or "None (can start immediately)".

**Status:** the ticket's triage role: `ready-for-agent`, `recipe-ticket` for a recipe ticket (see the slice rules), or `needs-triage` (see step 4).

- [ ] Acceptance criterion 1
- [ ] Acceptance criterion 2

</local-ticket-template>

<issue-template>

## Parent

A reference to the parent issue on the tracker (if the source was an existing issue, otherwise omit this section).

## What to build

The end-to-end behaviour this ticket makes work, from the user's perspective, not layer-by-layer implementation.

## Premises

- Each outside fact the ticket rests on, with a permalink to its source of record, or "None".

## Acceptance criteria

- [ ] Criterion 1
- [ ] Criterion 2

## Blocked by

- A reference to each blocking ticket, or "None (can start immediately)".

</issue-template>

In either form, avoid specific file paths or code snippets: they go stale fast. A premise's permalink is pinned to a revision, so it stays. Exception: if a prototype produced a snippet that encodes a decision more precisely than prose can (state machine, reducer, schema, type shape), inline it and note briefly that it came from a prototype. Trim to the decision-rich parts, not a working demo, just the important bits.
