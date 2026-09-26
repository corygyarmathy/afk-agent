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
{{- if .Failed}}

This is not the first attempt. An earlier session worked on this branch, and
its commits are still on it. When it finished, the local gate failed. The
gate's output is in `.git/afk-gate.log`. Read it first, and make the gate pass.
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

Reply with a short summary of what you changed and why, in GitHub-flavoured
markdown. No preamble.
