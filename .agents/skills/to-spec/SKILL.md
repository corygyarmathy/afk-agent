---
name: to-spec
description: "Turn the current conversation into a spec and publish it to the project issue tracker: no interview, just synthesis of what you've already discussed."
disable-model-invocation: true
---

This skill takes the current conversation context and codebase understanding and produces a spec. Do NOT interview the user; just synthesize what you already know.

The issue tracker and triage label vocabulary should have been provided to you. If not, tell the user to run `/setup-cory-gyarmathy-skills`.

## Process

1. Explore the repo to understand the current state of the codebase, if you haven't already. Use the project's domain glossary vocabulary throughout the spec, and respect any ADRs in the area you're touching.

2. Sketch out the seams at which you're going to test the feature. Existing seams should be preferred to new ones. Use the highest seam possible. If new seams are needed, propose them at the highest point you can. The fewer seams across the codebase, the better - the ideal number is one.

Check with the user that these seams match their expectations.

3. Write the spec using the template below, then publish it to the project issue tracker. Apply the `ready-for-agent` triage label - no need for additional triage.

<spec-template>

## Problem Statement

The problem being solved, from the perspective of whoever meets it: an end user, an operator, a calling service, a developer using an API.

## Solution

The solution to the problem, from that same perspective.

## Behaviour

A LONG, numbered list of the behaviours the finished work must exhibit. Each one is observable from outside, at the seams agreed in step 2, so a test there could check it. Phrase each as a contract:

1. When <trigger or input>, <actor or component> <observable outcome>

<behaviour-example>
1. When a job's lease expires before it reports completion, the scheduler returns it to the queue with its attempt count incremented
2. When a job has exhausted its retry budget, the scheduler marks it failed and does not requeue it
3. When a customer opens their accounts page, it shows the current balance of each account
</behaviour-example>

Give failure and edge cases the same coverage as the happy path: invalid input, partial failure, concurrent callers, a restart mid-operation. A property that must hold at all times rather than in response to a trigger is an **invariant**; list those at the end ("A job is never leased to two workers at once").

This list should be extremely extensive and cover all aspects of the feature.

## Implementation Decisions

A list of implementation decisions that were made. This can include:

- The modules that will be built/modified
- The interfaces of those modules that will be modified
- Technical clarifications from the developer
- Architectural decisions
- Schema changes
- API contracts
- Specific interactions

Do NOT include specific file paths or code snippets. They may end up being outdated very quickly.

Exception: if a prototype produced a snippet that encodes a decision more precisely than prose can (state machine, reducer, schema, type shape), inline it within the relevant decision and note briefly that it came from a prototype. Trim to the decision-rich parts, not a working demo, just the important bits.

## Testing Decisions

A list of testing decisions that were made. Include:

- A description of what makes a good test (only test external behavior, not implementation details)
- Which modules will be tested
- Prior art for the tests (i.e. similar types of tests in the codebase)

## Out of Scope

A description of the things that are out of scope for this spec.

## Further Notes

Any further notes about the feature.

</spec-template>
