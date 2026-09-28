You are implementing issue #{{.Number}} in the repository checked out in this
directory, on the branch `{{.Branch}}`.

Implement it with the `implement` skill, and follow the skill except where this
prompt says otherwise. If that skill is not available to you, do not implement
the issue another way: reply with one sentence saying the skill was missing,
and change nothing.

The issue is the spec. It has been fetched for you, verbatim, into
`.git/afk-issue.md`: its title, its body, and any instructions from the person
who asked for it to be implemented. Where the instructions and the issue
disagree, the instructions are the more recent word.
{{if .Whole}}
The person who asked wants the work as one pull request, whatever its size. Do
not stop early to keep it small.
{{- else}}
A pull request is one concern, reviewable in one sitting, tests included. The
agent counts your commits: over {{.Signal}} changed lines, not counting tests,
generated, vendored or lock files, or files deleted whole, it opens no pull
request, and hands the work back to a human. If the issue will not fit, stop at
a coherent first piece that does - a refactor the rest needs is a natural one -
commit that, and say in your reply what is left.
{{- end}}
{{- if .Failed}}

This is not the first attempt. An earlier session worked on this branch, and
its commits are still on it. Then a check failed: {{.Why}} The output is in
`.git/afk-gate.log`. Read it first, and fix the cause.
{{- end}}

This workspace is not the one the skill expects, in three ways:

- **No one is in the session.** Where the skill says to ask the user, do not
  wait for an answer: take the path the skill gives for when there is none, and
  say which you took.
- **There are no credentials for the tracker, and none for the remote.** Do not
  fetch the issue with `gh`. Do not push, and do not create, switch or delete
  branches: commit to `{{.Branch}}`, and the agent pushes it.
- **You are not the gate.** When you finish, the agent runs the local gate,
  `{{.Gate}}`, in this directory. Run it yourself before you finish, and commit
  everything the work needs: the gate is run on your commits. Uncommitted
  changes to tracked files are a failure, and untracked files are deleted
  before the gate runs.

When you finish, write your part of the pull request's description to
`.git/afk-description.md`, in GitHub-flavoured markdown. It is orientation for
the person who reviews the work: what they need and cannot cheaply get from the
issue or the diff. Use these headings, and leave out a section with nothing to
say rather than write "none":

- `## Start here` - where to start reading the diff, as `path:line`, and one
  line on where the behaviour lives. The agent makes it a link.
- `## Where the ticket didn't decide` - choices you made where the issue was
  silent or out of date, including each path you took because nobody was here
  to ask, and any choice that bears on security.
- `## Not verified` - what you could not check, and behaviour the diff cannot
  show, such as what only a run on the host would.
- `## Recipe` - only for work that is one large, mechanical change: the command
  or the transformation rule that makes it, and where the diff departs from it.

Aim for one or two lines an item, and the whole description on one screen.
Do not write: what changed file by file, anything that would restate the issue,
that tests pass or the gate passed, any self-rating such as "safe" or "low
risk", or a list of hand-checks you ran. The agent writes the rest of the
description itself, and a description with no `## Start here` is left out.
