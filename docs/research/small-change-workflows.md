# Small-change workflows on GitHub

Research for [#105](https://github.com/corygyarmathy/afk-agent/issues/105), part
of the map [#102](https://github.com/corygyarmathy/afk-agent/issues/102).
Gathered 2026-09-27. This reports what the sources say; it is not a process
design.

**Question.** How do teams keep changes small on GitHub - stacked versus
sequential pull requests, feature flags / dark launches / expand-contract,
splitting a ticket that turns out bigger than expected - using what GitHub
provides natively? What does each cost a single reviewer with a queue?

Confidence: **high** = stated directly in the cited primary source; **medium** =
stated by the source but with a gap (e.g. taken from a search snippet or a
summary of the page rather than read verbatim, or a preview feature that may
change); **low** = my inference from the sources, not stated by any of them.

## Summary

1. **"Small" has a published rule of thumb and a qualitative definition.**
   Google: "100 lines is usually a reasonable size for a CL, and 1000 lines is
   usually too large", files touched count too, and the real unit is "one
   self-contained change" that "addresses just one thing" and includes its
   tests. **High.**
2. **GitHub now has native stacked pull requests** (public preview since
   2026-07-30). Each layer shows only its own diff, branch protection and
   required checks of the stack's base apply to every layer, merges go bottom
   up, and upper layers are rebased and retargeted automatically - including
   after a squash merge. **Medium** (primary docs, but preview and "subject to
   change").
3. **Before stacks, GitHub's native support for PR-on-PR was only
   retargeting.** Deleting a merged head branch retargets PRs based on it to the
   merged PR's base. It does nothing about squash merges: GitHub's own docs warn
   that squash-merged commits reappear and conflicts recur. **High.**
4. **Sequential independent PRs against `master` cost rebuilds under this
   repo's ruleset.** With "branch must be up to date", every merge makes the
   other open PRs stale, and each needs an update and a fresh CI run. A merge
   queue removes that, but GitHub offers merge queues only to
   organization-owned repositories; this one is on a personal account. **High**
   for the docs; **medium** for the availability line (search snippet of the
   docs page).
5. **Feature flags, keystone interface, branch by abstraction and parallel
   change (expand-contract) let an unfinished feature land as a series of small
   PRs on the mainline** - Fowler and trunkbaseddevelopment.com present them as
   the way to integrate incomplete work. Their cost is carried code: toggles are
   "inventory which comes with a carrying cost", and an unfinished contract
   phase leaves you "worse" than you started. **High.**
6. **A change found to be too big: the sources say split it, and say it is
   cheaper to have written it small.** Google lets a reviewer reject a CL "for
   the sole reason of it being too large", calls splitting afterwards "a lot of
   work", and says a CL that truly cannot be split needs the reviewer's consent
   in advance. GitHub offers no splitting tool; drafts only block merging.
   **High.**
7. **The reviewer's costs are latency and context, not only line count.**
   Google: respond within one business day; slow reviews push low-quality code
   in and make "clean up later" never happen. Fowler: review delays mean work
   "is no longer on the top of [the author's] mind" when comments return.
   **High.** How that plays out with one human reviewer and an agent author is
   not something the sources address. **Low** / not covered.

## 1. What counts as small

Google's eng-practices, [*Small CLs*](https://google.github.io/eng-practices/review/developer/small-cls.html)
(read verbatim from the source Markdown):

- Why: small CLs are "reviewed more quickly" ("easier for a reviewer to find
  five minutes several times to review small CLs than to set aside a 30 minute
  block"), "reviewed more thoroughly" (with large ones "important points get
  missed or dropped"), "less likely to introduce bugs", "less wasted work if
  they are rejected", easier to merge, easier to design well, "less blocking on
  reviews", and "simpler to roll back".
- Definition: "one self-contained change": addresses "just one thing",
  includes related test code, is understandable from the CL, its description,
  the codebase or a CL already reviewed, leaves the system working, and is "not
  so small that its implications are difficult to understand" (a new API ships
  with a use of it).
- Numbers: "There are no hard and fast rules ... 100 lines is usually a
  reasonable size for a CL, and 1000 lines is usually too large, but it's up to
  the judgment of your reviewer." "A 200-line change in one file might be okay,
  but spread across 50 files it would usually be too large."
- "Reviewers rarely complain about getting CLs that are too small."
- Exceptions: whole-file deletions (count as about one line) and CLs generated
  by an automatic refactoring tool "that you trust completely".
- Refactorings go in a separate CL from feature changes or bug fixes;
  independent test changes can land first as their own CLs.
- "Don't break the build": with dependent CLs, "make sure the whole system
  keeps working after each CL is submitted."

GitHub's own guidance, [*Helping others review your changes*](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/getting-started/helping-others-review-your-changes):
"Small, focused pull requests are easier to review and safer to merge"; split
unwieldy changes into PRs that each address one objective, and use the
description to point reviewers at the files that matter most. (Medium: from a
fetched summary of the page.)

## 2. Stacked versus sequential pull requests

### Terms as the sources use them

- **Stacked**: each PR's base is the branch of the PR below it. Google:
  "write one small CL, send it off for review, and then immediately start
  writing another CL *based* on the first CL." GitHub: "two or more pull
  requests in the same repository" where "each subsequent pull request targets
  the branch of the pull request below it"
  ([About stacked PRs](https://docs.github.com/en/pull-requests/get-started/about-stacked-prs)).
- **Sequential / independent**: each PR targets `master` on its own. Google's
  "splitting by files" and "splitting vertically" produce these: separate CLs
  that "can both be reviewed simultaneously" even if one must be submitted
  first, or "independent parallel implementation tracks".

### GitHub without stacks: base branches, retargeting, squash merges

- A PR's base can be changed by hand. Doing so means "some commits may be
  removed from the timeline" and review comments "may also become outdated"
  ([Changing the base branch](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/proposing-changes-to-your-work-with-pull-requests/changing-the-base-branch-of-a-pull-request)).
  **High.**
- **Retargeting on merge**: when a merged PR's head branch is deleted, "GitHub
  checks for any open pull requests in the same repository that specify the
  deleted branch as their base branch" and "automatically updates any such pull
  requests, changing their base branch to the merged pull request's base
  branch" ([Merging a pull request](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/merging-a-pull-request)).
  Before May 2020 those PRs were closed instead
  ([changelog](https://github.blog/changelog/2020-05-19-pull-request-retargeting/)).
  Automatic deletion of head branches is a repository setting. **High.**
- **Squash merge interaction**: squash merging writes new commits, so the
  branch above still carries the originals. GitHub's docs state the same-branch
  case: "If you continue working on the head branch of a pull request after
  squashing and merging, and then create a new pull request between the same
  branches, commits that you previously squashed and merged will be listed in
  the new pull request. You may also have conflicts that you have to repeatedly
  resolve in each successive pull request"
  ([About merge methods](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/about-merge-methods-on-github)).
  Rebase merging likewise "will always update the committer information and
  create new commit SHAs". **High** for the quoted text. That a retargeted
  upper PR shows the lower layer's commits again after a squash merge, until it
  is rebased onto the new base, follows from this. **Medium**: inferred from the
  quoted mechanism; GitHub's docs do not state the stacked case this way.

### GitHub native stacked pull requests (public preview)

Private preview from 2026-04-13, public preview since
[2026-07-30](https://github.blog/changelog/2026-07-30-stacked-pull-requests-are-now-in-public-preview/).
Docs: [About](https://docs.github.com/en/pull-requests/get-started/about-stacked-prs),
[Reference](https://docs.github.com/en/pull-requests/reference/stacked-pull-requests),
[Creating](https://docs.github.com/en/pull-requests/how-tos/create-pull-requests/creating-stacked-pull-requests),
[Managing](https://docs.github.com/en/pull-requests/how-tos/create-pull-requests/managing-stacked-pull-requests),
[Merging](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/merging-stacked-pull-requests),
[CLI](https://docs.github.com/en/pull-requests/reference/stacked-prs-cli-commands),
[APIs and webhooks](https://docs.github.com/en/pull-requests/reference/stacked-pull-requests-apis-and-webhooks),
[Rollout tutorial](https://docs.github.com/en/pull-requests/tutorials/roll-out-stacked-prs).
All **medium**: primary, but "in public preview and subject to change", and
read through page summaries rather than verbatim.

- **Review**: each PR shows "only the diff for its layer". The UI has a stack
  icon with the layer number and a stack map in the merge box.
- **Rules and CI**: "Branch protection rules ... are enforced on every pull
  request in the stack, even mid-stack pull requests that don't directly target
  your default branch"; CI triggered by PRs on the default branch "run[s] for
  all pull requests in the stack". Required reviews, required status checks and
  CODEOWNERS are "evaluated against the stack base".
- **Merging**: "Pull requests must merge from the bottom up." Merging a higher
  PR lands it "and all unmerged pull requests below it ... together as a single
  operation". Merge commit, squash ("one clean, squashed commit per pull
  request") and rebase are all supported; the resulting history "is the same as
  merging each pull request individually, starting from the bottom". After a
  merge, "the next unmerged pull request is automatically rebased to target the
  stack base directly". Requires "a fully linear history between every branch
  in the stack". **Auto-merge is not supported** for stacks. Merge queues are
  supported.
- **Squash handling**: `gh stack rebase`, when a layer's PR has merged,
  "automatically switches to `--onto` mode" to replay upper commits on the merge
  target. So the squash problem above is handled by the tool. Server-side
  rebases from the website "don't produce signed commits".
- **Creating**: `gh stack` CLI extension (`init`/`add`/`submit`/`sync`/
  `rebase`/`modify`/`merge`/`unstack`), or on the web by basing PRs on each
  other and choosing "Create stack". Existing PRs whose branches line up can be
  linked into a stack. The changelog says coding agents can drive it "with the
  gh-stack skill".
- **API**: REST has a Stacks API to "list, read, create, extend, and dissolve
  stacks"; GraphQL is read-only (`stack`, `stackEntry`). Stacks must be merged
  through the asynchronous merge endpoint. Webhook `pull_request` payloads gain
  a `stack` property. I did not check which GitHub App permissions the Stacks
  API needs.
- **Limits**: same repository only (no cross-fork stacks), not in GitHub
  Desktop. GitHub pitches it at teams that "produce a high volume of code,
  either themselves or with Copilot or other coding agents".

### Merge friction for sequential PRs under this repo's ruleset

This repository's `protect-master` ruleset requires `go ci` to pass and "the
branch must be up to date with `master` first", with zero required approvals
and auto-merge off ([branch-protection.md](../agents/branch-protection.md)).

- GitHub on strict status checks: "More builds may be required, as you'll need
  to bring the head branch up to date after other collaborators update the
  target branch"
  ([About protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches)).
  **High.**
- Approvals, where required and set to dismiss when stale, go stale when "the
  diff changes ... because a contributor pushes new changes ... or clicks
  **Update branch**, or because a related pull request is merged into the
  target branch" (same page). Not binding here while approvals required = 0.
  **High.**
- A merge queue "provides the same benefits as the **Require branches to be up
  to date before merging** branch protection, but does not require a pull
  request author to update their pull request branch and wait for status checks
  to finish"
  ([Managing a merge queue](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)).
  **High.** They are "available in any public repository owned by an
  organization, or in private repositories owned by organizations using GitHub
  Enterprise Cloud". **Medium**: taken from the search engine's snippet of that
  page. `corygyarmathy/afk-agent` is on a personal account, so on that reading
  a merge queue is unavailable to it.

## 3. Landing unfinished work in small pieces

These let a large ticket land as several small PRs on the mainline without
stacking, because each piece is shippable in isolation.

- **Continuous integration framing** (Fowler,
  [*Continuous Integration*](https://martinfowler.com/articles/continuousIntegration.html)):
  commit to the mainline daily, and use feature flags, keystones and branch by
  abstraction so incomplete work can be integrated safely. On pre-integration
  review: "we have to find someone to do the code review, schedule their time,
  and wait for feedback ... this can easily end up being hours or days."
  (Medium: page summary.)
- **Branching patterns** (Fowler,
  [*Patterns for Managing Source Code Branches*](https://martinfowler.com/articles/branching-patterns.html)):
  "Frequent integration increases the frequency of merges but reduces their
  complexity and risk." On review latency: "If a developer finishes some work,
  and goes onto something else for a couple of days, then that work is no
  longer on the top of their mind when the review comments come back."
  (Medium: page summary.)
- **Keystone interface**
  ([bliki](https://martinfowler.com/bliki/KeystoneInterface.html)): "build all
  the back-end code, integrate, but don't build the user-interface", and add
  the keystone last. "When the UI can't be packaged into a simple keystone ...
  it's time to use Feature Flags." Latent code still needs tests. (High.)
- **Feature toggles** (Pete Hodgson on martinfowler.com,
  [*Feature Toggles*](https://martinfowler.com/articles/feature-toggles.html),
  read verbatim): release toggles let "in-progress features ... be checked into
  a shared" mainline, and "should generally not stick around much longer than a
  week or two". Toggles "require you to introduce new abstractions or
  conditional logic into your code. They also introduce a significant testing
  burden." "Savvy teams view the Feature Toggles in their codebase as inventory
  which comes with a carrying cost and seek to keep that inventory as low as
  possible." Remedies it lists: a removal task added to the backlog when a
  release toggle is introduced, expiration dates, "time bombs" that fail a
  test, and a cap on how many flags exist at once. (High.)
  trunkbaseddevelopment.com's
  [*Feature Flags*](https://trunkbaseddevelopment.com/feature-flags/) adds
  running CI for "each meaningful flag permutation" and scheduling flag removal
  "a month after the release". (Medium: page summary.)
- **Dark launching**
  ([bliki](https://martinfowler.com/bliki/DarkLaunching.html)): calling new
  back-end behaviour from existing users "without the users being able to tell
  it's being called", to measure load before announcing it; best for
  enhancements to existing interactions. It is a runtime-release technique; it
  keeps a change small only in combination with flags. (High for the
  definition; the last clause is **low**, my reading.)
- **Branch by abstraction**
  ([bliki](https://martinfowler.com/bliki/BranchByAbstraction.html)): add an
  abstraction over the current supplier, move clients onto it, build the new
  supplier behind it, switch clients over, remove the old. For "a large-scale
  change ... in gradual way that allows you to release the system regularly
  while the change is still in-progress." (High.)
- **Parallel change / expand-contract**
  ([bliki](https://martinfowler.com/bliki/ParallelChange.html)): expand (support
  old and new), migrate (move callers, "incrementally"), contract (remove the
  old). Costs: "during the migrate phase the supplier has to support two
  different versions", and "if the contract phase is not executed you might end
  up in a worse state than you started, therefore you need discipline to finish
  the transition". (High.)
- **Short-lived branches** (trunkbaseddevelopment.com,
  [*Short-Lived Feature Branches*](https://trunkbaseddevelopment.com/short-lived-feature-branches/)):
  "The branch should only last a couple of days", one developer, deleted after
  merge. (Medium: page summary.)

## 4. When a ticket turns out bigger than expected

- Google, *Small CLs*: reviewers "have discretion to reject your change outright
  for the sole reason of it being too large", usually asking for "a series of
  smaller changes". "It can be a lot of work to split up a change after you've
  already written it ... It's easier to just write small CLs in the first
  place." Plan the split "at a high level before diving into coding".
- Google, *Can't Make it Small Enough*: "This is very rarely true." Try a
  refactoring-only CL first to "pave the way". If that fails, "get consent from
  your reviewers in advance to review a large CL", and "expect to be going
  through the review process for a long time".
- Google reviewer guide, [*Speed*](https://google.github.io/eng-practices/review/reviewer/speed.html):
  for a CL "so large you're not sure when you will be able to have time to
  review it", ask for it to be split "into several smaller CLs that build on
  each other ... even if it takes additional work from the developer". If it
  cannot be split, "at least write some comments on the overall design ... and
  send it back". [*Navigating a CL*](https://google.github.io/eng-practices/review/reviewer/navigate.html):
  send major design comments immediately, because "a lot of the other code
  under review is going to disappear".
- Google, [*Handling pushback*](https://google.github.io/eng-practices/review/reviewer/pushback.html)
  on deferring part of the work to a follow-up: "usually unless the developer
  does the clean up *immediately* after the present CL, it never happens"; if
  it cannot be done now, "file a bug for the cleanup and assign it to
  themselves". This is the same risk Fowler names for an unfinished contract
  phase and Hodgson for stale toggles.
- GitHub native: nothing splits a PR for you. What exists is base-branch
  editing, stacks (existing PRs "can be converted into a stack if their
  branches line up"), and drafts: "Draft pull requests cannot be merged, and
  code owners are not automatically requested to review them"
  ([About pull requests](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/proposing-changes-to-your-work-with-pull-requests/about-pull-requests)).

All **high** except the GitHub-native line on stacks (**medium**, preview).

## 5. Costs to a single reviewer with a queue

What the sources say, per approach. Where a line goes beyond them it is marked
**low**.

| Approach | What the sources say it costs | Source |
| --- | --- | --- |
| One large PR | Needs a 30-minute block rather than five-minute slots; points "get missed or dropped"; more wasted work if the direction is wrong; harder rollback. | Google *Small CLs* |
| Sequential independent PRs to `master` | Each merge makes the other open PRs out of date under strict checks: "More builds may be required". No merge queue on a personal-account repository, so someone clicks Update branch and waits for CI each time. Google says independent CLs "can both be reviewed simultaneously", so the reviewer can take them in any order. | GitHub protected branches, merge queue docs; Google |
| Manual stacking (base on another PR's branch, no stack feature) | Retargeting on merge is automatic; squash-merged commits and conflicts reappear in the upper PR until it is rebased. Changing a base can remove commits from the timeline and outdate review comments. | GitHub merge docs, merge methods, base-branch docs |
| Native stacked PRs | Small per-layer diffs; protections, CI and CODEOWNERS of the base apply per layer; bottom-up merge; auto-merge unsupported; a change to a lower layer is propagated by cascading rebase. Preview status. Whether a rebase of an upper layer resets the reviewer's progress was not found in the docs. | GitHub stack docs |
| Feature flags / keystone / branch by abstraction / expand-contract | Each PR is small and mainline-safe, but the reviewer now also reviews the scaffolding (toggles, abstractions, dual paths), testing burden grows with flag states, and the follow-up removal PR is a separate item that tends not to happen unless tracked. | Hodgson; Fowler *ParallelChange*; Google pushback |
| Splitting after writing | "A lot of work" for the author; reviewer may insist; when it cannot be split, the reviewer is asked to consent in advance and to expect a long review. | Google *Small CLs*, *Speed* |
| Review latency, any approach | Google: respond within one business day; slow review delays everyone, draws complaints, and raises "pressure to allow developers to submit CLs that are not as good as they could be". Fowler/Wilsenach: approval waits and "too many changes in the queue" reduce feedback quality. | Google *Speed*; [Ship/Show/Ask](https://martinfowler.com/articles/ship-show-ask.html) |

Gaps: none of the sources models one reviewer serving an automated author.
Google's "write small CLs efficiently" advice assumes the author is blocked by
waiting and suggests stacking or parallel work to avoid it; with an agent
author the waiting cost falls on the queue rather than the author. That
reframing is mine. **Low.**

## Not checked

- Which GitHub App permissions the Stacks REST API requires, and whether the
  agent's App could create stacks.
- Merge-queue availability, read verbatim (only the search snippet of the docs
  page was seen).
- Whether native stack rebases leave per-file "viewed" state or review threads
  intact on upper layers.
- Tools outside GitHub (Graphite, ghstack, Sapling) were deliberately not
  researched.
