---
name: implement
description: "Implement a piece of work based on a spec or set of tickets."
disable-model-invocation: true
---

Implement the work the spec or tickets describe, and only that.

If you were passed a ticket reference, fetch it from the issue tracker and state its title before starting. If the reference is ambiguous and a user is in the session, ask; otherwise say which ticket you took it to mean.

## Check the ticket before the first edit

Read the ticket against the code, and against what its premises link to. These are **gaps**:

1. **A premise that doesn't hold**: it is false, or its source has changed since the ticket was written. Where the ticket links a revision and the current one can be read, compare the two. A waiting premise, one marked "confirm once <blocker> closes", doesn't hold yet while its blocker is open.
2. **A choice left to settle**: one the ticket marks "settle first", or one it hands to another ticket or repository instead of stating it ("whatever X defines", "the flags Y adds").
3. **A broken rule**: something the ticket asks for that a stated rule of the repository forbids, in the agent instructions, a decision record, or the glossary. Name the rule.

Instructions that came with the request settle any gap they answer. If a user is in the session, ask about each remaining gap, with a recommended answer, and go ahead once they are answered. List each gap settled either way, with its answer, under Where the ticket didn't decide.

With nobody to ask, stop on a gap: no edits, no commits. The report then opens with the line `Stopped on gaps: nothing changed.`, followed by the gaps alone, one line each: the gap as a question, and a recommended answer that settles it in one line (for a broken rule, the compliant alternative).

Decide everything else yourself, go ahead, and record it in the report:

- A choice the ticket is silent on, or marks as the implementer's: make it, and list it under Where the ticket didn't decide.
- An acceptance criterion the pull request can't meet by itself, because it needs a deploy or a hand run: do the rest, and list the criterion under Not verified.
- A premise whose source can't be read: go ahead, and list it under Not verified.

## Build

Call the Skill tool with "tdd" where possible.

Run the narrowest check that tells you something while you work, and the repo's full checks once before you finish. Commit everything the work needs to the current branch.

Finish with a report for the person who reviews the work, shaped as a pull request body: call the Skill tool with "pr".
