You are reviewing pull request #{{.Number}} in the repository checked out in this
directory, at commit {{.Head}}.

Review it with the `reviewing-changes` skill, and follow the skill except where
this prompt says otherwise. If that skill is not available to you, do not review
the change another way: reply with one sentence saying the skill was missing. A
review that is not the skill's, reported as if it were, is worse than none.

This workspace is not the one the skill expects, in three ways:

- **The checkout is shallow.** It holds the head commit and no base, so there is
  nothing to `git diff` against and no commit list. The pull request's diff
  against its base is in `.git/afk-pr.diff`. Wherever the skill uses the diff
  command, use that file: give its path to every sub-agent in place of the
  command.
- **There are no credentials for the tracker.** Do not fetch the spec with `gh`
  or through `docs/agents/issue-tracker.md`; it has been fetched for you, into
  `.git/afk-pr-spec.md`. That file holds the pull request's title and
  description, then each issue the pull request closes, verbatim. The issues are
  the spec. The description is the author's claims, to be checked against the
  code like the commit messages. If the file holds no issue, the description is
  the only spec there is: use it, and say so under `## Spec`.
- **No one is in the session.** Where the skill says to ask the user, do not
  wait for an answer: take the path the skill gives for when there is none, and
  say which you took. If you cannot run sub-agents, work each axis in turn from
  the same inputs, and say in the summary line that the axes were not reviewed
  independently.

Your review is advice for the human who will decide whether to merge. It does not
gate anything and it cannot block a merge, so do not write as if it could. Do not
modify files, commit, push, or comment anywhere: your reply is posted for you.

Reply with the skill's report, in GitHub-flavoured markdown, and nothing else - no
preamble about what you are going to do.
