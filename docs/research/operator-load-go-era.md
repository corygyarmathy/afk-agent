# Operator load on the Go-era pull requests

Research for [#185](https://github.com/corygyarmathy/afk-agent/issues/185),
part of the map [#182](https://github.com/corygyarmathy/afk-agent/issues/182).
Gathered 2026-10-07, read-only, from GitHub alone.

**Question.** Count **operator load** (`CONTEXT.md`) for
`corygyarmathy/dotfiles` #318, #324, #346 and #353: the reading (description,
advisory review, replies, changed lines) and the decisions (questions,
findings cited or let go, size-signal decisions). Which part dominates, how
much is the advisory review, and does the measure count cleanly?

## Short answer

1. **The changed lines dominate the reading: 57% of the words across the four.**
   The advisory review is **29%** (3,189 of 11,031 words), the description
   15%. No pull request had a reply in the `CONTEXT.md` sense (an agent's
   revision reply): none was sent back.
2. **The advisory review is the larger read on small pull requests.** On #346
   (36 changed lines) the review is 514 words against 398 in the diff, 40% of
   the reading, and 77 wrapped lines against 36 diff lines. On the large ones
   (#324, #353) it falls to 22-27%.
3. **Findings are nearly all of the decisions.** 39 findings and 6 questions
   across four pull requests, and no size-signal decision. Counted as distinct
   points rather than numbered items, the 39 findings are about 30: reviews
   number the same issue under several axes (#353's nine findings are four
   points; #346's two blockers are one).
4. **Not one decision was taken through a send-back.** On #324 the operator
   wrote a 528-word reply answering all 18 findings and the question (7
   addressed, 11 declined), although findings are owed no answer. On #346 and
   #353 the operator changed the pull request by hand with no written record
   of which findings were cited or let go. #318 is still open with all nine
   decisions pending.
5. **The measure counts reading cleanly and decisions poorly.** Reading
   counts mechanically once three choices are fixed (which version of the
   description and diff, words or lines, whether deleted files count).
   "Findings cited or let go" leaves no trace on GitHub except where the
   operator chose to write one, so for two of the four it can only be
   inferred from commits.

## Sources and method

Everything below comes from `gh` against `corygyarmathy/dotfiles`: each pull
request's body and its edit history (GraphQL `userContentEdits`), its issue
comments, review threads and submitted reviews (there were none of either on
any of the four), its commits, and two diffs - the pull request's final diff
(`pulls/N`, diff media type) and the diff at the head the advisory review
names (`compare/<base.sha>...<reviewed head>`).

- **Words** are whitespace-separated tokens of the Markdown source, with HTML
  comments (the `afk:` markers) and `<details>`/`<summary>` tags removed. A
  Markdown link counts its URL as one token, which inflates the advisory
  reviews a little (they link every location).
- **Prose lines** are Markdown source lines wrapped at 80 columns. Unwrapped
  source lines are useless here: a finding is one long line.
- **Changed lines** are `+`/`-` lines in the diff. "Non-test" leaves out
  `flake.lock`, files deleted whole, and `checks/` (the dotfiles repository's
  tests), approximating the size signal's rule. That is my approximation, not
  the agent's count: these pull requests predate the `afk-test` attribute.
- **Diff words** are the words on changed lines, leaving out `flake.lock` and
  files deleted whole.
- The description counted is **the version the advisory review was written
  against**, from the edit history. #318 was edited before its review, so
  its final body is that version; #324, #346 and #353 were edited after.

## Reading, at the reviewed head

| PR | Description words (lines) | Advisory review words (lines) | Replies | Changed lines: all / non-test | Diff words | Total words | Review share |
|---|---|---|---|---|---|---|---|
| #318 | 467 (53) | 842 (117) | 0 | 149 / 90 | 1,059 | 2,368 | 36% |
| #324 | 487 (58) | 1,068 (123) | 0 | 473 / 407 | 2,388 | 3,943 | 27% |
| #346 | 380 (41) | 514 (77) | 0 | 36 / 36 | 398 | 1,292 | 40% |
| #353 | 266 (30) | 765 (104) | 0 | 440 / 189 | 2,397 | 3,428 | 22% |
| **All** | **1,600** | **3,189** | **0** | **1,098 / 722** | **6,242** | **11,031** | **29%** |

In lines rather than words the shares barely move: 182 description, 421
review, 1,098 changed lines, so the review is 25% of 1,701.

**The same, at the merged head.** Three of the four changed after their
review. #324's final diff is 532 lines and 2,817 words, and its final
description 752 words; #346's final diff is 123 lines and 780 words; #353's is
442 lines and 2,582 words. #318 is unmerged and unchanged since its review.
Counted at the final head, the review's share falls to 23% on #324 and 30% on
#346.

**Who wrote what.** Only #346 was opened by the agent (`/implement`, for
dotfiles #332); its advisory review was asked for by the implement job. #318,
#324 and #353 were opened under the operator's own account, and the review
came from an operator's `/review` comment. On those three the description is
reading the operator's own side produced, not the agent's.

## Decisions

| PR | Questions | Findings (blocker / should-fix / consider) | Distinct points | Size-signal decisions | How the findings were settled |
|---|---|---|---|---|---|
| #318 | 2 | 7 (0 / 7 / 0) | 7 | 0 | Not yet: the pull request is open, so all 9 are pending. |
| #324 | 1 | 18 (0 / 3 / 15) | ~15 | 0 | A 528-word operator reply (51 lines): 7 addressed, 11 declined, the question answered. No send-back. |
| #346 | 2 | 5 (2 / 3 / 0) | 4 | 0 | No reply, no send-back. Three operator commits and a rewritten description addressed findings 1-4 and question 2; finding 5 and question 1 were let go silently. |
| #353 | 1 | 9 (2 / 7 / 0) | 4 | 0 | No reply, no send-back. Three post-review operator commits and two description edits addressed the triage-rule findings (1, 3, 7, 8) and the ADR §5 findings (4, 6, 9). Whether 2, 5 and the question were acted on is not recorded. |
| **All** | **6** | **39 (4 / 20 / 15)** | **~30** | **0** | |

Notes on the table:

- **Distinct points** is my judgement. #324's review itself says Approach 4
  and Correctness 1 share a root; the operator's reply settles Standards 4
  and Correctness 2 by the same change as Approach 1. #346's blockers 2 and 4 are the same missing `revise.tier`.
  #353's findings 1, 3, 7 and 8 are the same three lines of
  `triage-labels.md`, and 4, 6 and 9 the same ADR §5.
- **#324 is a pre-floor review.** Its 15 `consider` findings (all five of
  Standards) would not be raised by the later reviews, which fold Standards
  into Spec at a `should-fix` floor. On the three later reviews every finding
  is `should-fix` or `blocker`.
- **No size-signal decision arose.** The signal (`sizeSignal = 400` on
  homelab01, as configured at #346's reviewed head) applies to work `/implement` and `/revise`
  push, not to a `/review`. #346 had 36 changed lines. #324 had 407 non-test
  lines by my approximation and would have been over, but the operator wrote
  it.
- **Questions in the description.** #346's "Where the ticket didn't decide"
  (four bullets) and #324's "Where the issue was out of date" (five) are
  decisions the description puts to the operator, and the blockers on #346
  are about one of them. `CONTEXT.md` names "questions put to them" without
  saying whether these count. They are not in the 6 above.

## Where the measure is ambiguous

1. **Which version.** The description and diff change after the review on
   three of the four. Counting "at the reviewed head" measures what the agent
   handed over; "at merge" mixes in the operator's own work, which the
   definition excludes only for "the agent's own commits".
2. **The operator's own writing.** #324's 528-word triage reply is a reply,
   but the operator wrote it. Reading it is not load; writing it is, and the
   measure has no slot for writing. On the three operator-authored pull
   requests the description is similarly the operator's own.
3. **Lines or words.** Prose lines depend on wrap width and a diff line is
   denser than a prose line. Words are the comparable unit across the four
   kinds of reading; lines suit only the diff.
4. **Deleted files.** #353 deletes `afk-eligibility.md` whole (251 lines).
   The size signal leaves it out; whether the operator reads it is not
   something GitHub records. Counted in, #353's diff is 440 lines; out, 189.
5. **Findings or points.** Numbered findings over-count the decisions by about
   a quarter, because one issue is filed under several axes. Distinct points
   need a judgement call per review.
6. **Cited or let go.** A finding is "cited" only by a send-back, and none of
   these had one. Otherwise its fate is in commits and description edits (or,
   on #324, a reply the operator was not owed), so "let go" is
   indistinguishable from "not reached" without asking the operator.
7. **Description questions.** See the last note under Decisions.

## What I could not determine

- What the operator actually read, or in what order: GitHub records none of
  it, and the measure deliberately does not use time.
- On #346 and #353, which findings the operator cited or let go deliberately.
  I matched post-review commits and description edits to findings by their
  content; that is inference, not a record.
- The agent's own non-test line count for these pull requests. The
  `afk-test` attribute did not exist yet, so the non-test column is my
  approximation (`checks/` as tests).
- Whether any of this generalises: four pull requests, three of them
  operator-authored, and one (#324) reviewed before the severity floor.
