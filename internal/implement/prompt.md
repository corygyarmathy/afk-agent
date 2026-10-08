You are implementing issue #{{.Number}} in the repository checked out in this
directory, on the branch `{{.Branch}}`.

Implement it with the `implement` skill. If that skill is not available to you,
do not implement the issue another way: reply with one sentence saying the
skill was missing, and change nothing.

The issue is the spec. It is in `.git/afk-issue.md`, verbatim: its title, its
body, and any instructions from the person who asked for it to be implemented.
Where the instructions and the issue disagree, the instructions are the more
recent word.
{{- if .Premises}}

The issue's premises, the outside facts it rests on, are fetched for you into
`.git/{{.Dir}}/`: `{{.Index}}` there lists each link in the issue's Premises
section and the files it was fetched into. A file permalink is there at its
linked revision and at the head of its repository's default branch, and an
issue, a pull request or a comment as its thread is now. A link the index says
was not fetched is a premise whose source can't be read.
{{- end}}
{{- if not (or .Failed .Cutting .Correcting .Opened)}}

Nobody can answer a question while you work, so a gap the skill finds in the
issue means committing nothing. Write the skill's report, with its questions,
to `.git/afk-description.md` and stop: the agent posts them on the issue, and
the answers come back to the next session as instructions.
{{- end}}
{{if .Whole}}
The person who asked wants the work as one pull request, whatever its size. Do
not stop early to keep it small.
{{- else}}
A pull request is one concern, reviewable in one sitting: keep the work under
{{.Signal}} changed lines, not counting tests, generated, vendored or lock
files, or files deleted whole. If the issue will not fit, stop
at a coherent first piece that does - a refactor the rest needs is a natural
one - and write what is left to `.git/afk-remainder.md`, in GitHub-flavoured
markdown. The agent files it as an issue, for a human to decide on. If the
work is the whole issue, do not write that file.
{{- end}}
{{- if .Failed}}

This is not the first attempt: an earlier session's commits are on this branch.
Then a check failed: {{.Why}} The output is in `.git/afk-gate.log`. Read it
first, and fix the cause.
{{- end}}
{{- if .Cutting}}

This is not the first attempt: an earlier session's commits are on this branch.

{{template "cut" .}}
{{- end}}
{{- if .Correcting}}
{{- if not .Failed}}

This is not the first attempt: an earlier session's commits are on this branch.
{{- end}}

{{template "correct" .}}
{{- end}}

{{template "unattended" .}}
{{- if not .Opened}}

Write the skill's closing report to `.git/afk-description.md`, in
GitHub-flavoured markdown, rather than as your reply: it becomes the pull
request's description. The agent reads it under these headings and no others:
`## Start here`, `## Summary`, `## Evidence`,
`## Where the ticket didn't decide`, `## Not verified`, `## Merge danger` and
`## Recipe`. A description with no `## Start here` is left out.
{{- if not .Whole}} If you stopped at a first piece, make the file's first line
a plain line, not a heading, that gives a title for the piece: the issue's
title describes the whole job.
{{- end}}

If an acceptance criterion needs more than this work to meet it - a deploy, or
a run by hand - list it under `## Not verified`, and write it to
`.git/afk-unmet.md` too, one line each: the pull request then refers to the
issue without closing it.
{{- end}}
