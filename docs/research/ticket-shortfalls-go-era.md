# Where the Go-era tickets fell short

Research for [#194](https://github.com/corygyarmathy/afk-agent/issues/194),
part of the map [#182](https://github.com/corygyarmathy/afk-agent/issues/182).
Gathered 2026-10-08, read-only, from GitHub alone. Extends
[Operator load on the Go-era pull requests](operator-load-go-era.md).

**Question.** On the Go-era pull requests, how often did the issue the work
started from fall short, in which way, and what did each case ask of the
operator? A shortfall is **stale** (a predecessor merged, or a decision landed
elsewhere, and changed what the issue assumed), **under-decided** (the issue
left a choice open that the implementer had to make), **wrong** (the issue
asked for something that did not hold) or **too big** (more than one concern,
or more than one sitting). This is evidence, not a recommendation.

## Short answer

1. **15 shortfalls across 4 tickets, from 5 pull requests.** Stale 5,
   under-decided 5, too big 3, wrong 2. Every ticket that had an issue fell
   short at least twice. #318 started from no issue, so it has none to count.
2. **About two in five of the advisory reviews' findings had the ticket as
   their cause.** On #324, #346 and #353, 14 of 32 numbered findings and 2 of
   4 questions trace to the issue rather than the code: 972 of the reviews'
   2,346 words (41%). Counted as distinct points, about 9 points and 2
   questions. On #353 the ticket is behind 8 of 9 findings. If its three
   "AC 3 unmet" findings are counted as the implementation's instead, the
   totals are 11 findings and 764 words (33%).
3. **The largest cost was one decision, made four times.** #321 left open
   whether the agent gets the `ci.yml` checks-matrix exception. #324 settled
   it in passing as "no". Then intake handed back #301 because of it, an
   afk-agent plan reversed #344's criterion on it, and the operator reversed
   that reversal twice in comments before filing #356 to remove the cause.
   That chain asked for 3 findings, 2 spec-change comments (186 words), two
   commits that undo each other, a hand-back still open, and a new issue and
   pull request.
4. **The costliest single ticket was #332, and its cost was mostly writing.**
   It deferred its options to "the flags the revise ticket defines". The
   blocker it named merged and decided there were none, four days before
   intake took it. It cost two blocker findings and a question on #346, an
   amendment of the issue (159 to 243 words), and two follow-up issues the
   operator wrote about ticket premises (1,047 words). By the operator's own
   account in skills#23, it also cost the agent 62 of 80 turns before its
   first edit.
5. **Most of the 15 were catchable before implementing.** 7 needed only
   reading, when the ticket was written, what it already named or the code
   it changes. 5 needed a check, when the work was taken, of the ticket
   against what had merged or been decided since. One (S13) belonged to
   planning, and one (S4) is marginal and the reviewer could have settled it
   from the binary. One (#324's mid-review afk-agent merges, S2) could not
   have been caught beforehand.

## Sources and method

Everything comes from `gh` against `corygyarmathy/dotfiles`,
`corygyarmathy/afk-agent` and `corygyarmathy/skills`. For each pull request I
read its body and edit history (GraphQL `userContentEdits`), comments,
submitted reviews (none), commits and closing references. For each issue I
read its body and edit history, comments, label events, its `blocked_by`
dependencies, and its cross-references. Merge times of predecessors come from
afk-agent's pull requests and this repository's `git log -S`.

**Which pull requests.** The prior study's four, #318, #324, #346 and #353,
plus **#313**. I found #313 by listing every comment by
`corygyarmathy-afk-agent[bot]` on dotfiles issues and pull requests #278 to
#364. The Go agent was enabled by #284 on 2026-09-13. Seven numbers had such
comments: #278 (a bash-prototype review from 2026-09-12, left out), #301 (an
issue with a hand-back), #313, #318, #324, #346 and #353. #313 has a Go-era
advisory review (2026-09-24) and the era's only send-back (a `/revise` about a
merge conflict, not about the ticket). Its issue, #301, is also the one the
hand-back is on. So n is **5 pull requests and 1 hand-back, from 4 tickets**:
#321 (→ #324), #332 (→ #346), #344 (→ #353) and #301 (→ #313 and the
hand-back). Only #346 was opened by the agent. The other four were the
operator's own, reviewed through `/review`.

**Counting.** One shortfall per row; a pull request can have several. A
finding or question counts toward a shortfall when its stated cause is the
issue's text: a criterion, a premise, or a gap in it. When the cause is the
code, it does not count. Words are counted as in the prior study:
whitespace tokens of the Markdown source, with HTML comments and
`<details>`/`<summary>` removed. The classification and the "caught by" column
are my judgement, from the record. Where a row is a close call, it says so.

## The shortfalls

| # | Ticket → PR | Kind | What fell short | Reading it asked | Decisions it asked | Rewrites and follow-ups | Catchable before implementing, by |
|---|---|---|---|---|---|---|---|
| S1 | #321 → #324 | stale | The parameter table has `--ci-rounds` and no `--effect-rounds`, and it says the table is "required once `--branch-prefix` is set". afk-agent#77 (#63) renamed `--ci-rounds` to `--ci-fixes` and added `--effect-rounds`. Per #324's description, `--effect-rounds` and `--hand-back-label` are also required for review. I did not trace which afk-agent change made `--hand-back-label` so. It merged 2026-09-26 07:54Z, after the issue (09-25 16:14Z) and before the PR (11:27Z). | Description, 2 of 5 "out of date" bullets. Approach 5 (51 words). | Approach 5 (consider), declined in the operator's reply (25 words). | None to #321 (0 edits). | A check at start against afk-agent's `implement.md`, which the issue names as its source. |
| S2 | #321 → #324 | stale | `--model-timeout` and `--tier-notify-after` became required (afk-agent#96 and #97, for #91 and #93). They merged 11:57Z and 12:04Z, after the PR opened and after its review (11:51Z). | "Out of date" bullet 4; "Also in this push" (23 words). | None in the review. The operator bumped and supplied both (`b20c5ac`). | None. | Not before implementing: they merged mid-review. |
| S3 | #321 → #324 | under-decided | The denylist row ends "(and whatever the `checks` exception needs, if the agent is to use it)". #324 decided it in a Security bullet (67 words): no exception, so a ticket that adds a check is handed back at the push. | That bullet. Downstream: S10 and S15. | None on #324. Downstream: see S10 and S15. | Downstream: #344's two comments, #356 and #357. | The operator when writing. The issue flags the choice itself. |
| S4 | #321 → #324 | under-decided (close call) | The parameter table sets no minimum for the counts, and the PR chose `ints.positive`. The issue does say `afk help` is the list of record, so this is marginal. | Spec 5 (31 words). | Spec 5 (consider), declined (21 words): the binary refuses 0. | None. | The reviewer, from the binary. Not clearly the ticket's. |
| S5 | #321 → #324 | too big | Acceptance item 2 is a hand run on homelab01 after deploy, so no pull request could meet it. | Spec 1 (71 words). | Spec 1 (should-fix). The description went from `Closes` to `Refs`, with a 27-word reply. | #321 stayed open 6 more days and was closed with no closer (2026-10-02). | The operator when writing. |
| S6 | #332 → #346 | stale | It asked for `revise.tier`/`revise.needs` "mapping to the flags the revise ticket defines" and "whatever other flags the revise ticket adds". afk-agent#163 (for #149, one of #332's blockers) merged 2026-09-29. With it, `revise.md` said "There is no revise tier of its own". The issue was written 09-28 and taken 10-03. | Agent's description, 1 of 4 "didn't decide" bullets (the section is 242 words). Findings 2 and 4 (74 + 54 words) and Q2 (41). | Findings 2 and 4 (both blocker, one point) and Q2. | #332 amended (159 → 243 words, criteria struck through). Follow-ups filed: skills#23 (614 words) and dotfiles#360 (433). | A check, when taken, against the merged blocker. The operator re-applied `ready-for-agent` at 03:49Z on 10-03, 23 minutes before the PR. |
| S7 | #332 → #346 | under-decided | "Check the token wiring covers it" did not say how. The agent answered in prose. | Finding 3 (62 words). | Finding 3 (should-fix). | #332 amended to "with a check that fails if a lock bump drops it". The operator wrote that check (`0a0c6de`). | The operator when writing. |
| S8 | #332 → #346 | under-decided (close call) | Nothing said the pin must move past afk-agent#149. The agent wrote "No flake.lock bump" and was wrong. | One "didn't decide" bullet, rewritten by the operator. | None in the review: it missed this. | Operator commit `27238c2`, which bumps ddbc97d → 66d5a3a. | A check, when taken, of the pin against the merged blocker. |
| S9 | #332 → #346 | too big | Criterion 2, "/revise on a PR in the live repository is answered by the deployed agent", needs a deploy. | One "Not verified" bullet. | None recorded. | #332 closed by the merge with the box unticked. | The operator when writing. |
| S10 | #344 → #353 | stale | AC 2 keeps the checks-matrix exception "with exactly one home". The plan on afk-agent#100 decided the opposite at 2026-10-03 12:24Z, 4 minutes before #353 opened, and the PR followed the plan. #344 was not updated. | Findings 3, 7, 8 (81 + 56 + 86 words). | Findings 3 (blocker), 7 and 8 (should-fix): one point. | Two spec-change comments on #344 (113 + 73 words), the second reversing the first. Commit `260c8dc` adds the refusal and `5fbc82b` undoes it. #356 filed (371 words), and #357 merged (132+/71−). | A check, when taken, against the plan decided since. Or the operator amending #344 when the plan decided. |
| S11 | #344 → #353 | wrong | It cites "homelab01's `implement.sensitive` globs". The option that blocks a push is `implement.denylist`, and `sensitive` is empty on homelab01. | Q1 (39 words). | Q1. | Corrected in #344's comment. Commit `0b33c6`. | The operator when writing, by reading `hosts/homelab01/default.nix`. |
| S12 | #344 → #353 | under-decided (close call) | Step 3 defaults to "mark it superseded", while AC 3 says no document may claim the pre-claim check. `domain.md` treats a Proposed ADR, which 0004 was, as amended in place. The PR left §5's body as it was. | Finding 2 (62 words), and the AC 3 findings 4, 6, 9 (58 + 82 + 68). | Finding 2 (should-fix), and 4, 6, 9 (one point: one blocker, two should-fix). | Commit `260c8dc`: "ADR 0004 was Proposed, so its §5 … carry an in-place note". | The operator when writing, from `domain.md`. Findings 4, 6 and 9 could instead be counted as the implementation's. |
| S13 | #344 (+ afk-agent#100) → #353 | too big | afk-agent#100's plan put its dotfiles items "with #344". The PR carried both: retire the docs, and say each instruction once. #344 alone was one concern. | Finding 5 (56 words). | Finding 5 (should-fix, scope). | None. | At planning. |
| S14 | #301 → #313 | wrong | The issue's premise that `find -name '.*' -prune` acts "at the top level only" was false. `-name` tests every entry, and the real divergence was in the watchers. | 112 words and a table in "Which semantics win" (175 words). | None: the review raised nothing on it. | #301 never edited. | The operator when writing, by running the `find`. The implementer caught it by measuring. |
| S15 | #301 → hand-back | stale | #301 had `ready-for-agent` from 2026-09-24, the day #313 started implementing it by hand. #324 (09-26, S3) then made any check-adding work a push hand-back. Intake, turned on 10-02 by #343, took #301 that evening. | The hand-back (34 words). | One hand-back: "Reshape the issue and `/implement` again, or take it by hand." | `needs-decision` added. #301 is still open with `ready-for-agent` and `needs-decision`, and #313 is still open. | A check, when taken, against the merged #324 denylist. The label could also have come off when #313 opened. |

### By kind

| Kind | Count | Rows | Ticket-caused findings / questions |
|---|---|---|---|
| stale | 5 | S1, S2, S6, S10, S15 | 6 findings (Approach 5; 2, 4; 3, 7, 8), 1 question (#346 Q2), 1 hand-back |
| under-decided | 5 | S3, S4, S7, S8, S12 | 6 findings (Spec 5; 3; 2, 4, 6, 9) |
| too big | 3 | S5, S9, S13 | 2 findings (Spec 1; 5) |
| wrong | 2 | S11, S14 | 0 findings, 1 question (#353 Q1) |
| **All** | **15** | | **14 findings, 2 questions, 1 hand-back** |

### By pull request

| PR | Ticket | Shortfalls | Ticket-caused findings / all | Ticket-caused questions / all | Ticket-caused review words / all |
|---|---|---|---|---|---|
| #318 | none | - | - | - | - |
| #313 | #301 | 1 (S14) | 0 / 0 numbered | 0 / 0 | 0 / 458 |
| #324 | #321 | 5 | 3 / 18 | 0 / 1 | 153 / 1,068 |
| #346 | #332 | 4 | 3 / 5 | 1 / 2 | 231 / 514 |
| #353 | #344 | 4 | 8 / 9 | 1 / 1 | 588 / 764 |
| **#324, #346, #353** | | **13** | **14 / 32** | **2 / 4** | **972 / 2,346 (41%)** |

S15 is on #301 with no pull request. #313's review numbers nothing. Its
"Minor" and "Risks" items are about the code.

The descriptions also carry the ticket's shortfalls as reading. #324's "Where
the issue was out of date" (114 words) and its denylist bullet (67), #346's
"Where the ticket didn't decide" (242 in the agent's version), and #313's
premise correction (112) add up to 535 words. The prior study left
description questions out of its decision count. Of #346's four "didn't
decide" bullets, three are S6, S7 and S8. The fourth, "No user to ask", is
about the session, not the ticket. Of #324's five "out of date" bullets, three
are S1 and S2. The first, the pin bump, the issue already implied ("bumping
the flake input alone"). The fifth is about the binary's order of reads, not
something the issue asserted.

## The checks-matrix chain

S3, S10 and S15 are one undecided question. It surfaced on three tickets
over eight days:

1. **#321** (2026-09-25): "whatever the `checks` exception needs, if the
   agent is to use it."
2. **#324** (09-26): no exception, because it can't be expressed as a glob
   and the App has no Workflows permission. A check-adding ticket is handed
   back at the push.
3. **#301** (10-02): intake takes a `ready-for-agent` issue whose work adds a
   check, and hands it back. The issue is still open, and so is #313, the
   pull request that does the same work by hand.
4. **afk-agent#100's plan** (10-03 12:24Z): "a ticket that adds a flake check
   is `ready-for-human`." #344 (10-02) still says to keep the exception.
5. **#353** (10-03): follows the plan. The review raises one blocker and two
   should-fix on it, all one point.
6. **#344 comments** (10-04 04:07Z and 04:24Z): the operator first adopts the
   plan's "dropped". Seventeen minutes later the operator reverses it to
   "kept", and files #356 to move the matrix's names out of
   `.github/workflows/`. #357 merges at 05:10Z.

The operator decided the question four times: #344's "keep", the plan's
"drop", the first comment's "drop" and the second comment's "keep". It also
produced a hand-back. Two of those decisions were written into commits that
undo each other.

## What already exists

The operator has already filed work on two of these causes. It is listed here
as part of the record, not assessed:

- [skills#23](https://github.com/corygyarmathy/skills/issues/23) (closed):
  `to-tickets` states each ticket's **premises**, the outside facts it rests
  on, with a permalink to each, checked when the ticket is sliced. It was
  written from S6.
- [dotfiles#360](https://github.com/corygyarmathy/dotfiles/issues/360)
  (open): `triage-labels.md` gets a check that "its premises hold today"
  before `ready-for-agent`. A ticket whose premise depends on an open
  blocker stays off the label until that blocker closes. Also written from S6.

Both are about premises, which covers S6, S8 and S11, and arguably S1 and
S15. Neither covers too big (S5, S9, S13), or an open choice that the ticket
itself flags (S3, S7).

## What I could not determine

- **What the operator's own sessions spent.** Four of the five pull requests
  were the operator's own work, and the shortfalls on them (S1 to S5, S10 to
  S14) were met first inside those sessions. GitHub records only what reached
  the description and the commits. The 62-of-80 turns for S6 is the
  operator's figure in skills#23. I did not see the session behind it.
- **Whether the operator re-read #332 when re-applying the label** at 03:49Z
  on 10-03. The label events are recorded; the reading is not.
- **The job behind #301's hand-back.** Its spend, how far it got and why it
  took an issue that already had an open pull request are not on GitHub.
- **A discrepancy in #360.** It says #332 was taken "as soon as afk-agent#131
  closed". GitHub shows afk-agent#131 closed at 12:28Z on 10-03, after #346
  opened at 04:12Z. #332's recorded blockers are afk-agent#149 (closed
  09-29) and dotfiles#336 (closed 03:48Z on 10-03), so #336 is the one that
  fits the timing.
- **Whether #318 had a ticket elsewhere.** Its review says "no tracker issue
  is attached", and nothing on GitHub references one.
- **Why #321 closed with no closer** on 10-02, or whether its hand run was
  done.
- **Whether this generalises.** Four tickets, one implemented by the agent.
  The kinds are spread evenly enough that no single kind dominates at this n.
