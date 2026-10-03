{{define "nobody" -}}
- **No one is in the session.** Where a skill says to ask the user, take the
  path it gives for when nobody answers, and say which you took.
{{- end -}}
This workspace is not an interactive one, in three ways:

{{template "nobody"}}
- **There are no credentials for the tracker or the remote.** What the work
  needs from either has been fetched into `.git/` for you. Commit to
  `{{.Branch}}`, the branch you are on, and the agent pushes it. Do not
  create, switch or delete branches.
- **The agent runs the local gate, `{{.Gate}}`, on your commits when you
  finish.** Run it yourself first. Untracked files are deleted before it runs
  and uncommitted changes to tracked files fail it, so commit everything the
  work needs.
