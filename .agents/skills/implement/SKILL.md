---
name: implement
description: "Implement a piece of work based on a spec or set of tickets."
disable-model-invocation: true
---

Implement the work the spec or tickets describe, and only that.

Use /tdd where possible.

Run the narrowest check that tells you something while you work, and the repo's full checks once before you finish. Commit everything the work needs to the current branch.

Finish with a report for the person who reviews the work: what they need and cannot cheaply get from the spec or the diff. Use these headings, and leave out one with nothing to say:

- `## Start here` - where to start reading the diff, as `path:line`, and one line on where the behaviour lives.
- `## Where the ticket didn't decide` - choices you made where the spec was silent or out of date, including each one made because nobody was there to ask, and any that bears on security.
- `## Not verified` - what you could not check, and behaviour the diff cannot show, such as what only a run on the host would.
- `## Recipe` - only for work that is one large, mechanical change: the command or transformation rule that makes it, and where the diff departs from it.

One or two lines an item, and the whole report on one screen. The diff already shows what changed and the checks show what passed, so the report carries neither, nor a rating of the work.
