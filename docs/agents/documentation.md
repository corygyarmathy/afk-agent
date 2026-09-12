# Documentation: what lives where

Every fact in this repository has exactly one canonical home. Every other
document that touches it links to that home rather than restating it. Before
writing a paragraph, check whether it is already someone else's job below.

## Issue - the spec, permanent

Problem statement and acceptance criteria. It does not move once implementation
starts: a changed shape is a comment on the issue, not a rewrite somewhere else.
Nothing else in this list restates a spec; they link the issue instead.

Where the issues live and how to read them: [`issue-tracker.md`](issue-tracker.md).

## CONTEXT.md - the vocabulary, one home

The glossary, and nothing else. No implementation detail, no decisions, no
scratch notes. When a term is sharpened, it is sharpened there and the documents
that use it are left alone - they were already using the word.

## ADR - a decision, one per file

See [`domain.md`](domain.md) for the discipline: what counts as a decision
rather than a parameter, where this repository's parameters actually live, and
why an accepted ADR is never amended in place.

## Plan - the work, disposable

Tracks only what is currently in flight: what is active, what it is blocked on.
Not a spec (the issue's job) and not a changelog (the pull request's and the
commit's).

A shipped item collapses to one line - issue, pull request or code path - and
its Problem/Approach/Testing content is deleted rather than archived. Never cite
a plan by path or item number from anything that has to keep working after the
plan changes shape; point at the issue or the code. Delete the file once nothing
in it is active: it has no standing as a historical record.

There is no plan file in this repository today. The backlog is the tracker, and
the dependency edges between issues are the ordering. Do not create one to hold
prose that the issues already own.

## Findings note - measured evidence, historical

Exists only when something was actually run and produced numbers worth keeping:
a benchmark, a load test, a memory measurement. Dated evidence, cited by the ADR
whose decision it justified. If nothing was measured, this document does not
exist.

ADR 0001 contains two claims of this kind already - the prototype's line count,
and the 8 GB `nix flake check` peak that OOM-killed a session. If you are about
to write "this is slow" or "this exhausts the host" in a decision, measure it
first or say plainly that you did not.

## Pull request description - the bridge

Links the issue, and summarises what changed and why for a reviewer. Becomes the
permanent changelog entry once merged. A claim in a pull request description
that is not true of the diff is one of the two findings the review path treats
as most serious; do not make claims the diff does not support.

## Commit message - atomic, for git-archaeology

What and why, for this one commit.

## README - orientation only

What this is, how to build it, and a pointer into the ADRs for why it is shaped
this way. No implementation detail: an algorithm explained in a README belongs
in a code comment or an ADR.

## AGENTS.md - the house style

How to work in this repository: conventions, checks, what not to touch. Not a
place for domain vocabulary (`CONTEXT.md`), decisions (`docs/adr/`), or specs
(the tracker). It points at those and states the rest once.

## Code comment - non-obvious why only

Earns its place only if the code cannot say it for itself: a workaround, a
constraint from outside the file, a rejected alternative and why. A comment
restating what the next few lines do is a no-op; delete it rather than shorten
it.

The existing comments in `internal/cli` are the intended register - they cite
the ADR section or the issue number a piece of structure comes from, so the
reason survives the next person who wonders why it is like that.
