# Branch protection

What protects `master`, how to read it back, and what the rules mean for you.
The rules live on GitHub, not in this repository, so this is the reference for
them. Why they are shaped this way is
[ADR 0003](../adr/0003-master-is-protected-by-a-ruleset-with-no-bypass.md).

## What is configured

One repository ruleset, `protect-master`, over `~DEFAULT_BRANCH` and
`refs/heads/master`:

| Rule | Effect |
| --- | --- |
| `pull_request` | Nothing reaches `master` except through a pull request. Zero required approvals. |
| `required_status_checks` | `go ci` must pass, and the branch must be up to date with `master` first. |
| `non_fast_forward` | No force-push. |
| `deletion` | The branch cannot be deleted. |

`bypass_actors` is empty. `current_user_can_bypass` reads `never`, for the
operator and for every token, repository-admin rights included. Repository-level
auto-merge is off.

## Rules for working here

- Never push to `master`. Branch, open a pull request, leave the merge to a
  human (AGENTS.md, ADR 0001 §15).
- Never weaken, disable or edit the ruleset. Not to land a change, not to unwedge
  a branch, not temporarily. If you believe a change cannot land without it, say
  so and stop; the procedure below is the operator's and is not yours to run.
- Do not rename the `go ci` job in [`ci.yml`](../../.github/workflows/ci.yml).
  Its `name:` is the required status-check context. Renaming it blocks every
  merge, silently and with no bypass, until the ruleset is changed to match. If a
  rename is genuinely needed, it is a ruleset change first and is therefore the
  operator's.
- Expect a green `go ci` before asking for a merge. Run the four checks in
  AGENTS.md locally rather than pushing to find out.

## Reading it back

```bash
gh api repos/corygyarmathy/afk-agent/rulesets \
  --jq '.[] | "\(.name)\t\(.enforcement)"'
gh api repos/corygyarmathy/afk-agent/rules/branches/master --jq '.[].type'
```

Do not use `gh api repos/corygyarmathy/afk-agent/branches/master/protection`. It
reports *legacy* branch protection only and answers `404 Branch not protected`
for a branch covered by a ruleset, which is what it answers for `dotfiles` too.
That 404 is not evidence that `master` is unprotected.

## Operator procedure: going around the ruleset

For the operator, not the agent. There is no client-side way through, so the
rule is turned off and back on.

1. GitHub → Settings → Rules → Rulesets → `protect-master` → Enforcement status
   → **Disabled**. Use the UI, not the API: the API equivalent is a `PUT` that
   replaces the whole ruleset, so a mistyped call is a rewrite rather than a
   toggle.
2. Do the one thing that needed it.
3. Set enforcement back to **Active**, and read it back with the commands above.
   `master` is unprotected until this step is done.
