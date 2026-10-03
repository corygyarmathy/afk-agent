{{.Why}}

The output is in `.git/afk-gate.log`. Fix the cause, commit the fix to
`{{.Branch}}`, and run the local gate again.
{{- if not .Opened}} If the fix changes what `.git/afk-description.md` says,
update it.
{{- end}}
