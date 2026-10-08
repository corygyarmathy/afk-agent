---
name: implement
description: "Implement a piece of work based on a spec or set of tickets."
disable-model-invocation: true
---

Implement the work the spec or tickets describe, and only that.

If you were passed a ticket reference, fetch it from the issue tracker and state its title before starting. If the reference is ambiguous and a user is in the session, ask; otherwise say which ticket you took it to mean.

Call the Skill tool with "tdd" where possible.

Run the narrowest check that tells you something while you work, and the repo's full checks once before you finish. Commit everything the work needs to the current branch.

Finish with a report for the person who reviews the work, shaped as a pull request body: call the Skill tool with "pr".
