You are reviewing pull request #{{.Number}} in the repository checked out in this
directory, at commit {{.Head}}.

Review it with the `reviewing-changes` skill. If that skill is not available to
you, do not review the change another way: reply with one sentence saying the
skill was missing.

Run it at {{if .Floor}}a severity floor of `{{.Floor}}`{{else}}the skill's default severity floor{{end}},
and {{if .FoldCut}}a fold cut of {{.FoldCut}} changed lines{{else}}its default fold cut{{end}}, with these inputs:

{{if .Since -}}
- **The diff is the file `.git/afk-pr.diff`: the range {{.Since}}..{{.Head}}**,
  what a revision added on top of {{.Since}}, the head the operator read and
  sent back.
- **The context diff is `.git/afk-pr-whole.diff`**: the whole pull request's
  diff against its base.
- The checkout is shallow: it holds the head commit and no base.
- **The spec is `.git/afk-pr-spec.md`**: the send-back the range answers, the
  operator's commands verbatim, and then as background the pull request's
  title and description and each issue it closes. The send-back is the spec.
  The description is the author's claims, to be checked against the code like
  the commit messages. The issues say what the whole pull request is for: do
  not report what they ask for and the range does not touch as missing.
{{- else -}}
- **The diff is the file `.git/afk-pr.diff`.** The checkout is shallow: it
  holds the head commit and no base.
- **The spec is `.git/afk-pr-spec.md`**: the pull request's title and
  description, then each issue the pull request closes, verbatim. The issues
  are the spec. The description is the author's claims, to be checked against
  the code like the commit messages. If the file holds no issue, the
  description is the only spec there is: use it, and say so under `## Spec`.
  A pull request that is the first piece of its issue has that issue headed as
  only partly done, followed by the issue filed for the rest, headed as
  out of scope: review the piece against the part of the issue it takes on,
  and do not report what the rest's issue holds as missing.
{{- end}}
{{template "nobody"}}

Your reply is posted for you as one comment on the pull request, inside a
`<details>` the agent adds. Reply with the skill's report and nothing around
it, in GitHub-flavoured markdown. Write no links: each `path:line` citation of
a file in this checkout becomes a permalink at the reviewed head, which is the
only way a reader reaches the line.
