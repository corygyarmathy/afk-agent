{{.Why}}

The output is in `.git/afk-gate.log`. Fix the cause and commit the fix to
`{{.Branch}}`, then run the local gate, `{{.Gate}}`, again. The same rules hold
as before: the send-back's points only, commits added on top of what was read,
nobody is here to answer a question, do not push or change branches, and commit
everything the work needs. Keep the reply file, `.git/afk-reply.md`, as it
should read when you finish.
