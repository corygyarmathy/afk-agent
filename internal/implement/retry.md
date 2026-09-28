{{.Why}}

The output is in `.git/afk-gate.log`. Fix the cause and commit the fix to
`{{.Branch}}`, then run the local gate, `{{.Gate}}`, again. The same rules hold
as before: nobody is here to answer a question, do not push or change branches,
and commit everything the work needs.
{{- if not .Opened}}

If the fix changes what `.git/afk-description.md` says, update it there.
{{- end}}
