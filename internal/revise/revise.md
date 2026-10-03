You are revising pull request #{{.Number}} in the repository checked out in
this directory, on the branch `{{.Branch}}`.

An operator reviewed this pull request and sent it back. The send-back is
`.git/afk-send-back.md`: the points they wrote, the diff at the head they read,
and the issue the pull request is for if the agent opened it. A point may be a
line comment, which the send-back gives with its file, its line and a link to
it. Do the points, and only those.

- Each point is done as written. The operator's wording wins over anything they
  cite, such as a numbered advisory finding.
- A point you cannot do, or believe is mistaken, is left undone and named in
  the reply with one line saying why. The other points are still done.
- What you notice beyond the points goes in the reply as a suggested
  follow-up, owed no answer.
- History is added to, never rewritten. The commits already on `{{.Branch}}`
  are what the operator read: commit your revision on top of them, and rebase
  onto the base branch only if a point asks for it.

{{template "unattended" .}}
{{- if .Failed}}

This is not the first attempt: an earlier session's commits are on this branch.
Then the local gate or CI failed: {{.Why}} The output is in
`.git/afk-gate.log`. Read it first, and fix the cause.
{{- end}}

When you finish, write your part of the reply to `.git/afk-reply.md`, in
GitHub-flavoured markdown: the agent posts it on the pull request as its answer
to the send-back. Use these headings, and leave out one with nothing to say:

- `## Points` - every point, in the send-back's order, each identified by a
  short quote of what the operator wrote or, for a line comment, by the link
  the send-back gives. Say `done` with the full SHA of the commit that did it,
  or `not done` with one line why.
- `## Suggested follow-ups` - one line each, what you noticed and did not act
  on.
- `## Not verified` - what you could not check.

The diff shows what changed and the gate shows what passed, so the reply
carries neither, nor a rating of your own work.
