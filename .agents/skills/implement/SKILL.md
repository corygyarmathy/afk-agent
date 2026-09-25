---
name: implement
description: "Implement a piece of work based on a spec or set of tickets."
disable-model-invocation: true
---

Implement the work described by the user in the spec or tickets.

Before changing anything, record the starting commit (`git rev-parse HEAD`): it is the fixed point the review diffs against.

Use /tdd where possible, at pre-agreed seams.

Run typechecking regularly, single test files regularly, and the full test suite once at the end.

Commit your work to the current branch.

Finish by reporting the starting commit and the spec or tickets you worked from, so /reviewing-changes can run against them in a fresh session.
