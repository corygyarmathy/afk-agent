# Issue tracker: GitHub

Issues and specs for this repository live as GitHub issues on
`corygyarmathy/afk-agent`. Use the `gh` CLI for all operations; it infers the
repository from `git remote -v` when run inside a clone.

This is the tracker the agent being built here will eventually work. Everything
below is equally the human convention and the machine one - there is no second,
agent-only interface.

## Conventions

- **Create an issue**: `gh issue create --title "..." --body "..."`. Use a
  heredoc for multi-line bodies.
- **Read an issue**: `gh issue view <number> --comments`.
- **List issues**:
  `gh issue list --state open --json number,title,body,labels --jq '[.[] | {number, title, body, labels: [.labels[].name]}]'`,
  with `--label` and `--state` filters as needed.
- **Comment**: `gh issue comment <number> --body "..."`
- **Label**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`
- **Close**: `gh issue close <number> --comment "..."`

GitHub shares one number space across issues and pull requests, so a bare `#42`
may be either. Resolve with `gh pr view 42` and fall back to `gh issue view 42`.

## Dependencies between issues

This backlog is ordered by GitHub's **native issue dependencies** rather than by
prose in the body, because the dependency is then visible in the UI and
queryable. Add an edge with:

```bash
gh api --method POST \
  repos/corygyarmathy/afk-agent/issues/<blocked>/dependencies/blocked_by \
  -F issue_id=<blocker-database-id>
```

`<blocker-database-id>` is the blocker's numeric **database id**
(`gh api repos/corygyarmathy/afk-agent/issues/<n> --jq .id`), not its `#number`
and not its `node_id`. Read the live gate from
`issue_dependencies_summary.blocked_by`, which counts open blockers only.

An issue with an open blocker is not ready to be worked, whatever its labels
say.

## Pull requests as a request surface

**No.** Pull requests here are the agent's output and the human's review
surface, not a channel for incoming feature requests. A comment command on a
pull request (`/review`, `/revise`) is an instruction about that pull request,
which `CONTEXT.md` calls a **command**; it is not a ticket. The work itself is
asked for on the issue that describes it (`/implement`).

## When a skill says "publish to the issue tracker"

Create a GitHub issue here.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --comments`.
