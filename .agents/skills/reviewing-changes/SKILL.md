---
name: reviewing-changes
description: 'Review the changes since a fixed point (commit, branch, tag, or merge-base) on four axes: Standards, Spec, Correctness, and Approach, in parallel sub-agents, reported side by side. Use when the user wants a branch, a PR, or work in progress reviewed, or asks to "review since X".'
---

Four-axis review of the diff between `HEAD` and a fixed point:

- **Standards**: does the code conform to this repo's documented coding standards?
- **Spec**: does the code faithfully implement the originating issue / spec?
- **Correctness**: where does the code produce wrong output, crash, or break a caller?
- **Approach**: is this the right way to do it, and what does it do to the rest of the codebase?

The axes run as **parallel sub-agents** with fresh contexts, one per axis except where step 4 folds two into one, then this skill aggregates their findings under one heading per axis. Run this skill in a session that did not write the change: the reviewer that shares the author's reasoning accepts the author's justifications.

The review takes a **severity floor** as input, one of the three severities below: `should-fix` by default, `blocker` or `consider` when a caller asks for it. Nothing below the floor is produced, so every finding in the report is worth reading.

It also takes a **fold cut**, a count of changed lines, default `50`. A diff below the cut is small enough to fold Approach into Correctness (step 4).

This repo's issue tracker is described in `docs/agents/issue-tracker.md`. If that file is missing and a spec has to be fetched from a tracker, say so rather than guessing at a `gh` invocation - a review that cites the wrong tracker is worse than one that admits it has no spec.

## Severities

Each finding carries one severity, and the floor cuts between them in the same place every time:

- **blocker**: produces wrong output, loses data, breaks a caller, or fails the spec. Merging it as it stands would be a mistake.
- **should-fix**: works today, but a concrete cost follows if it merges: an untested failure path, a documented standard broken, a design that makes the next change harder. The finding names that cost; a finding that can't name one is `consider`.
- **consider**: a matter of taste or a possible improvement. Merging without it costs nothing concrete.

## Process

### 1. Pin the fixed point

Use the fixed point the caller gave (a commit SHA, branch name, tag, `main`, `HEAD~5`, etc.). If none was given, use the merge-base with the default branch, and state that assumption in the report.

Capture the diff command once: `git diff <fixed-point>...HEAD` (three-dot, so the comparison is against the merge-base). This covers committed work only; if the working tree is dirty, say so in the report. Also note the list of commits via `git log <fixed-point>..HEAD --oneline`.

Before going further, confirm the fixed point resolves (`git rev-parse <fixed-point>`) and the diff is non-empty. A bad ref or empty diff should fail here, not inside four parallel sub-agents.

A caller whose checkout has no base may give a diff file instead of a fixed point. The file is then the diff wherever a step uses the diff command, and there is no commit list.

### 2. Identify the spec source

Look for the originating spec, in this order:

1. A path or issue the caller passed.
2. Issue references in the commit messages (`#123`, `Closes #45`, GitLab `!67`, etc.), fetched via the workflow in `docs/agents/issue-tracker.md`.
3. A spec file under `docs/`, `specs/`, or `.scratch/` matching the branch name or feature.

If nothing is found and a user is in the session, ask where the spec is. Otherwise, or if they say there isn't one, the **Spec** sub-agent is skipped and the report says "no spec available".

### 3. Identify the standards sources

Anything in the repo that documents how code should be written, such as `CODING_STANDARDS.md`, `CONTRIBUTING.md`, or `AGENTS.md`.

At a `consider` floor, the Standards axis also carries the **smell baseline** in [`SMELLS.md`](SMELLS.md): a fixed set of Fowler code smells that applies even when a repo documents nothing.

### 4. Fold the axes

Two folds, each decided by an input. Nothing else adds a sub-agent: not a large diff, and not a sensitive path.

- **Standards folds into Spec above a `consider` floor.** At `should-fix` or `blocker`, the Spec sub-agent also gets the Standards brief and the standards-source files, and its brief adds: "Report these under `## Standards`, apart from your `## Spec` findings." At a `consider` floor, or with no spec, Standards runs in its own sub-agent.
- **Approach folds into Correctness below the fold cut.** Count the changed lines: non-test lines only, leaving out generated, vendored and lock files. Below the cut, no Approach sub-agent runs; the Correctness sub-agent also gets Approach's step 2, **Blast radius**, verbatim, and its brief adds: "Report these under `## Approach`, apart from your `## Correctness` findings."

### 5. Spawn the sub-agents in parallel

Where the harness runs no sub-agents, work each axis in turn from the same inputs, and say beside the scope notes that the axes were not reviewed independently.

Every sub-agent prompt carries the same inputs and nothing else:

- The diff command and commit list, or the diff file's path.
- The spec, pasted verbatim or as a path to it, never as your summary of it.
- The severity floor, with the severity definitions above.
- The inputs specific to its axis, below.

Leave out any explanation of why the change was made the way it was. Each reviewer judges the code cold; the reasons it needs are in the spec and the code. Tell each one that commit messages are the author's claims, to be checked against the code rather than taken as reasons.

Every brief ends with the same output rules: "The floor is `<floor>`: report nothing below it. Rank findings most severe first. Each finding is at most about 3 lines: its severity, `path:line` (or `path:first-last`, the path from the repository root), what's wrong, and a one-line fix, with a short code suggestion when one fits. Read beyond the diff wherever judging a hunk needs it. Under 400 words; if the cap bites, drop the least severe findings."

A folded sub-agent writes two headings, and the ranking and the cap apply to each one alone, so no axis is ranked against the other: its brief says "Rank findings most severe first within each heading" and "Under 400 words per heading; if the cap bites, drop that heading's least severe findings."

Standards is checklist matching: when it runs in its own sub-agent, run it on a smaller, cheaper model where the harness lets you choose. The other axes need the strongest model available.

**Standards** also gets the standards-source files from step 3 (folded, see step 4). Brief: "Report every place the diff violates a documented standard: cite the standard (file + the rule). Skip anything tooling enforces."

At a `consider` floor, paste [`SMELLS.md`](SMELLS.md) in full as well (the sub-agent has no other access to it), and add to the brief: "Also report any baseline smell you spot, under the baseline's rules: name it and quote the hunk."

**Spec** brief: "Report (a) requirements the spec asked for that are missing or partial; (b) behaviour in the diff that wasn't asked for (scope creep); (c) requirements that look implemented but where the implementation looks wrong. Quote the spec line for each finding." Skipped when there is no spec.

**Correctness** brief: "Find the ways this change produces wrong output, crashes, loses data, or breaks an existing caller: edge cases, error paths, concurrency, resource cleanup, and the invariants the surrounding code relies on. Every finding states a **failure scenario**: the inputs and state, and the wrong result they produce. A suspicion you cannot turn into a failure scenario is listed separately as a question." Folded, see step 4.

**Approach** brief: "Judge the approach the change takes, not its details. Work in this order:

1. **Design it twice.** Before reading the diff, read the spec and the code it touches, and sketch in a few lines how you would have done it. Then read the diff and compare. Where your sketch is simpler, that is a finding.
2. **Blast radius.** Work out every interface, type, schema, config key, or behaviour the diff changes, and find their callers and dependents across the repo. Report each one the change affects but didn't update, and existing code the change duplicates instead of reusing.
3. **Pre-mortem.** Assume that six months from now this change caused a problem. Find the most likely one: migration, reversibility, performance at scale, operability, security, or a future change it makes harder. Report it as a finding when it clears the floor.
4. **Spec pushback.** Report where the code shows the spec itself was wrong, unnecessary, or missing a case. With no spec, list as questions whether the change should exist at all, and whether a smaller change solves the same problem."

### 6. Aggregate

Present the reports under `## Standards`, `## Spec`, `## Correctness` and `## Approach` headings, lightly cleaned. Keep the axes separate: do not merge or rerank findings across them (see _Why separate axes_). An axis with no findings collapses to one line under its heading.

Next to the scope notes (an assumed fixed point, a dirty working tree, no spec), one line names each fold from step 4 that happened, e.g. "Standards reviewed with Spec; Approach with Correctness." It tells an empty heading apart from one nobody looked at. When nothing folded, there is no such line.

Number the findings `1…n` continuously across the whole report, so any one can be cited alone ("advisory 3"). Each keeps the about-3-line shape from the output rules; a Correctness finding's "what's wrong" is its failure scenario. After the findings, list the questions (suspicions with no failure scenario) as `Q1…Qn`.

A review with nothing at or above the floor still reports, and says so, naming the floor.

The review is advisory: it carries no verdict and changes nothing.

## Why separate axes

A change can pass one axis and fail another:

- Code that follows every standard but implements the wrong thing → **Standards pass, Spec fail.**
- Code that does exactly what the issue asked but breaks the project's conventions → **Spec pass, Standards fail.**
- Code that meets the spec and the standards but fails on an empty input → **Correctness fail.**
- Code that is correct and on-spec but rebuilds a helper the repo already has, or bolts a special case onto a module that should have been reshaped → **Approach fail.**

Reporting them separately stops one axis from masking another.
