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

There are two populations, and they are reported separately:

- **A. dotfiles**: `corygyarmathy/dotfiles` pull requests that the afk agent
  reviewed (and, in one case, wrote), from four tickets. These are written
  `dotfiles#N`.
- **B. afk-agent**: this repository's own pull requests. An interactive
  Claude Opus session implemented each ticket, and a fresh session reviewed
  it with the `reviewing-changes` skill. 22 pull requests, from 22 tickets.
  Bare `#N` means this repository.

## Short answer

1. **The tickets fell short in both populations, but differently.**
   dotfiles: 15 shortfalls on 4 tickets, spread across the kinds (stale 5,
   under-decided 5, too big 3, wrong 2). afk-agent: 23 on 22 tickets, nearly
   all under-decided (stale 1, under-decided 15, wrong 5, too big 2). Eight
   of the 22 afk-agent tickets had no shortfall that cost anything beyond a
   line in the description.
2. **The ticket's share of the review's findings was about twice as high on
   dotfiles.**

   | | Ticket-caused findings | Ticket-caused questions |
   |---|---|---|
   | dotfiles | 14 of 32 (44%), or 11 (34%) strictly | 2 of 4 |
   | afk-agent | 23 of 114 (20%), or 16 (14%) without the three close calls | 0 of 14 |
   | Combined | 37 of 146 (25%), or 27 (18%) strictly | 2 of 18 |

   On afk-agent the findings are counted as the operator's follow-up comments
   record them, because the reviews themselves were never posted. So the
   afk-agent numbers are counts of recorded points, not of numbered findings,
   and no word share can be given for them.
3. **Staleness was a dotfiles problem.** Of the 6 stale shortfalls, 5 were on
   dotfiles, where tickets depended on afk-agent and waited days for it to
   move. On afk-agent, most tickets were filed in batches from a resolved
   map and implemented within hours. The one stale case (#22, priced "from
   the catalogue") sat for three weeks while #152 changed what the review
   reported.
4. **On afk-agent, the gap usually reached the operator as a question during
   the session, not as a finding.** Six pull requests record 19 decisions
   "agreed before building" or "confirmed with the operator". The
   unattended agent on dotfiles had nobody to ask ("No user to ask"), so the
   same gaps arrived as description bullets and findings.
5. **The biggest afk-agent cost crossed repositories.** #132 asked for "the
   whole PR as context" in a skill that could not take a context diff. That
   cost a review point, skills#19 (278 words), and a re-vendor pull request
   (#179). On dotfiles, the biggest cost was the checks-matrix exception,
   decided four times (below).
6. **Almost every shortfall was catchable when the ticket was written.** On
   afk-agent, 20 of 23 were, from text the ticket's writer already had or the
   code it changes. One was caught before implementing (#149, rewritten 14
   minutes after filing), one during it (#128, edited 4 minutes before its
   pull request), and one needed a check when the work was taken (#22). On
   dotfiles, 7 of 15 were catchable when written and 5 needed that check.

## Side by side

| | A. dotfiles | B. afk-agent | Combined |
|---|---|---|---|
| Pull requests (tickets) | 5 (4) + 1 hand-back | 22 (22) | 27 (26) |
| Implementer | the afk agent (1), the operator's own sessions (4) | interactive Opus session | |
| Reviewer | afk agent on an enrolled model (`deepseek-v4-pro`) | fresh Opus session, `reviewing-changes` | |
| Shortfalls | 15 | 23 | 38 |
| stale | 5 | 1 | 6 |
| under-decided | 5 | 15 | 20 |
| wrong | 2 | 5 | 7 |
| too big | 3 | 2 | 5 |
| Shortfalls per ticket | 3.8 | 1.0 | 1.5 |
| Ticket-caused findings | 14 / 32 (44%) | 23 / 114 (20%) | 37 / 146 (25%) |
| Ticket-caused questions | 2 / 4 | 0 / 14 | 2 / 18 |
| Decisions asked during implementation | 0 recorded | 19, on 6 pull requests | |
| Issues rewritten or amended after work began | 2 (dotfiles#332 edited, dotfiles#344 two comments) | 2 (#128 edited, #145 amended by comment) | 4 |
| Follow-up issues the shortfall caused | 3 (skills#23, dotfiles#360, dotfiles#356) | 2 (skills#19, #139) | 5 |
| Catchable when written / when taken / not before / other | 7 / 5 / 1 / 2 | 20 / 1 / 0 / 2 | 27 / 6 / 1 / 4 |

"Other" is S4 and S13 for dotfiles, and #149 and #128 for afk-agent, which
were caught before or during the work.

## A. dotfiles: the afk agent's reviews

### Sources and method

Everything comes from `gh` against `corygyarmathy/dotfiles`,
`corygyarmathy/afk-agent` and `corygyarmathy/skills`. For each pull request I
read its body and edit history (GraphQL `userContentEdits`), comments,
submitted reviews (none), commits and closing references. For each issue I
read its body and edit history, comments, label events, its `blocked_by`
dependencies, and its cross-references. Merge times of predecessors come from
afk-agent's pull requests and this repository's `git log -S`.

**Which pull requests.** The prior study's four, dotfiles#318, dotfiles#324, dotfiles#346 and dotfiles#353,
plus **dotfiles#313**. I found dotfiles#313 by listing every comment by
`corygyarmathy-afk-agent[bot]` on dotfiles issues and pull requests dotfiles#278 to
dotfiles#364. The Go agent was enabled by dotfiles#284 on 2026-09-13. Seven numbers had such
comments: dotfiles#278 (a bash-prototype review from 2026-09-12, left out), dotfiles#301 (an
issue with a hand-back), dotfiles#313, dotfiles#318, dotfiles#324, dotfiles#346 and dotfiles#353. dotfiles#313 has a Go-era
advisory review (2026-09-24) and the era's only send-back (a `/revise` about a
merge conflict, not about the ticket). Its issue, dotfiles#301, is also the one the
hand-back is on. So n is **5 pull requests and 1 hand-back, from 4 tickets**:
dotfiles#321 (→ dotfiles#324), dotfiles#332 (→ dotfiles#346), dotfiles#344 (→ dotfiles#353) and dotfiles#301 (→ dotfiles#313 and the
hand-back). Only dotfiles#346 was opened by the agent. The other four were the
operator's own, reviewed through `/review`.

**Counting.** One shortfall per row; a pull request can have several. A
finding or question counts toward a shortfall when its stated cause is the
issue's text: a criterion, a premise, or a gap in it. When the cause is the
code, it does not count. Words are counted as in the prior study:
whitespace tokens of the Markdown source, with HTML comments and
`<details>`/`<summary>` removed. The classification and the "caught by" column
are my judgement, from the record. Where a row is a close call, it says so.

### The shortfalls

| # | Ticket → PR | Kind | What fell short | Reading it asked | Decisions it asked | Rewrites and follow-ups | Catchable before implementing, by |
|---|---|---|---|---|---|---|---|
| S1 | dotfiles#321 → dotfiles#324 | stale | The parameter table has `--ci-rounds` and no `--effect-rounds`, and it says the table is "required once `--branch-prefix` is set". afk-agent#77 (#63) renamed `--ci-rounds` to `--ci-fixes` and added `--effect-rounds`. Per dotfiles#324's description, `--effect-rounds` and `--hand-back-label` are also required for review. I did not trace which afk-agent change made `--hand-back-label` so. It merged 2026-09-26 07:54Z, after the issue (09-25 16:14Z) and before the PR (11:27Z). | Description, 2 of 5 "out of date" bullets. Approach 5 (51 words). | Approach 5 (consider), declined in the operator's reply (25 words). | None to dotfiles#321 (0 edits). | A check at start against afk-agent's `implement.md`, which the issue names as its source. |
| S2 | dotfiles#321 → dotfiles#324 | stale | `--model-timeout` and `--tier-notify-after` became required (afk-agent#96 and #97, for #91 and #93). They merged 11:57Z and 12:04Z, after the PR opened and after its review (11:51Z). | "Out of date" bullet 4; "Also in this push" (23 words). | None in the review. The operator bumped and supplied both (`b20c5ac`). | None. | Not before implementing: they merged mid-review. |
| S3 | dotfiles#321 → dotfiles#324 | under-decided | The denylist row ends "(and whatever the `checks` exception needs, if the agent is to use it)". dotfiles#324 decided it in a Security bullet (67 words): no exception, so a ticket that adds a check is handed back at the push. | That bullet. Downstream: S10 and S15. | None on dotfiles#324. Downstream: see S10 and S15. | Downstream: dotfiles#344's two comments, dotfiles#356 and dotfiles#357. | The operator when writing. The issue flags the choice itself. |
| S4 | dotfiles#321 → dotfiles#324 | under-decided (close call) | The parameter table sets no minimum for the counts, and the PR chose `ints.positive`. The issue does say `afk help` is the list of record, so this is marginal. | Spec 5 (31 words). | Spec 5 (consider), declined (21 words): the binary refuses 0. | None. | The reviewer, from the binary. Not clearly the ticket's. |
| S5 | dotfiles#321 → dotfiles#324 | too big | Acceptance item 2 is a hand run on homelab01 after deploy, so no pull request could meet it. | Spec 1 (71 words). | Spec 1 (should-fix). The description went from `Closes` to `Refs`, with a 27-word reply. | dotfiles#321 stayed open 6 more days and was closed with no closer (2026-10-02). | The operator when writing. |
| S6 | dotfiles#332 → dotfiles#346 | stale | It asked for `revise.tier`/`revise.needs` "mapping to the flags the revise ticket defines" and "whatever other flags the revise ticket adds". afk-agent#163 (for #149, one of dotfiles#332's blockers) merged 2026-09-29. With it, `revise.md` said "There is no revise tier of its own". The issue was written 09-28 and taken 10-03. | Agent's description, 1 of 4 "didn't decide" bullets (the section is 242 words). Findings 2 and 4 (74 + 54 words) and Q2 (41). | Findings 2 and 4 (both blocker, one point) and Q2. | dotfiles#332 amended (159 → 243 words, criteria struck through). Follow-ups filed: skills#23 (614 words) and dotfiles#360 (433). | A check, when taken, against the merged blocker. The operator re-applied `ready-for-agent` at 03:49Z on 10-03, 23 minutes before the PR. |
| S7 | dotfiles#332 → dotfiles#346 | under-decided | "Check the token wiring covers it" did not say how. The agent answered in prose. | Finding 3 (62 words). | Finding 3 (should-fix). | dotfiles#332 amended to "with a check that fails if a lock bump drops it". The operator wrote that check (`0a0c6de`). | The operator when writing. |
| S8 | dotfiles#332 → dotfiles#346 | under-decided (close call) | Nothing said the pin must move past afk-agent#149. The agent wrote "No flake.lock bump" and was wrong. | One "didn't decide" bullet, rewritten by the operator. | None in the review: it missed this. | Operator commit `27238c2`, which bumps ddbc97d → 66d5a3a. | A check, when taken, of the pin against the merged blocker. |
| S9 | dotfiles#332 → dotfiles#346 | too big | Criterion 2, "/revise on a PR in the live repository is answered by the deployed agent", needs a deploy. | One "Not verified" bullet. | None recorded. | dotfiles#332 closed by the merge with the box unticked. | The operator when writing. |
| S10 | dotfiles#344 → dotfiles#353 | stale | AC 2 keeps the checks-matrix exception "with exactly one home". The plan on afk-agent#100 decided the opposite at 2026-10-03 12:24Z, 4 minutes before dotfiles#353 opened, and the PR followed the plan. dotfiles#344 was not updated. | Findings 3, 7, 8 (81 + 56 + 86 words). | Findings 3 (blocker), 7 and 8 (should-fix): one point. | Two spec-change comments on dotfiles#344 (113 + 73 words), the second reversing the first. Commit `260c8dc` adds the refusal and `5fbc82b` undoes it. dotfiles#356 filed (371 words), and dotfiles#357 merged (132+/71−). | A check, when taken, against the plan decided since. Or the operator amending dotfiles#344 when the plan decided. |
| S11 | dotfiles#344 → dotfiles#353 | wrong | It cites "homelab01's `implement.sensitive` globs". The option that blocks a push is `implement.denylist`, and `sensitive` is empty on homelab01. | Q1 (39 words). | Q1. | Corrected in dotfiles#344's comment. Commit `0b33c6`. | The operator when writing, by reading `hosts/homelab01/default.nix`. |
| S12 | dotfiles#344 → dotfiles#353 | under-decided (close call) | Step 3 defaults to "mark it superseded", while AC 3 says no document may claim the pre-claim check. `domain.md` treats a Proposed ADR, which 0004 was, as amended in place. The PR left §5's body as it was. | Finding 2 (62 words), and the AC 3 findings 4, 6, 9 (58 + 82 + 68). | Finding 2 (should-fix), and 4, 6, 9 (one point: one blocker, two should-fix). | Commit `260c8dc`: "ADR 0004 was Proposed, so its §5 … carry an in-place note". | The operator when writing, from `domain.md`. Findings 4, 6 and 9 could instead be counted as the implementation's. |
| S13 | dotfiles#344 (+ afk-agent#100) → dotfiles#353 | too big | afk-agent#100's plan put its dotfiles items "with dotfiles#344". The PR carried both: retire the docs, and say each instruction once. dotfiles#344 alone was one concern. | Finding 5 (56 words). | Finding 5 (should-fix, scope). | None. | At planning. |
| S14 | dotfiles#301 → dotfiles#313 | wrong | The issue's premise that `find -name '.*' -prune` acts "at the top level only" was false. `-name` tests every entry, and the real divergence was in the watchers. | 112 words and a table in "Which semantics win" (175 words). | None: the review raised nothing on it. | dotfiles#301 never edited. | The operator when writing, by running the `find`. The implementer caught it by measuring. |
| S15 | dotfiles#301 → hand-back | stale | dotfiles#301 had `ready-for-agent` from 2026-09-24, the day dotfiles#313 started implementing it by hand. dotfiles#324 (09-26, S3) then made any check-adding work a push hand-back. Intake, turned on 10-02 by dotfiles#343, took dotfiles#301 that evening. | The hand-back (34 words). | One hand-back: "Reshape the issue and `/implement` again, or take it by hand." | `needs-decision` added. dotfiles#301 is still open with `ready-for-agent` and `needs-decision`, and dotfiles#313 is still open. | A check, when taken, against the merged dotfiles#324 denylist. The label could also have come off when dotfiles#313 opened. |

#### By kind

| Kind | Count | Rows | Ticket-caused findings / questions |
|---|---|---|---|
| stale | 5 | S1, S2, S6, S10, S15 | 6 findings (Approach 5; 2, 4; 3, 7, 8), 1 question (dotfiles#346 Q2), 1 hand-back |
| under-decided | 5 | S3, S4, S7, S8, S12 | 6 findings (Spec 5; 3; 2, 4, 6, 9) |
| too big | 3 | S5, S9, S13 | 2 findings (Spec 1; 5) |
| wrong | 2 | S11, S14 | 0 findings, 1 question (dotfiles#353 Q1) |
| **All** | **15** | | **14 findings, 2 questions, 1 hand-back** |

#### By pull request

| PR | Ticket | Shortfalls | Ticket-caused findings / all | Ticket-caused questions / all | Ticket-caused review words / all |
|---|---|---|---|---|---|
| dotfiles#318 | none | - | - | - | - |
| dotfiles#313 | dotfiles#301 | 1 (S14) | 0 / 0 numbered | 0 / 0 | 0 / 458 |
| dotfiles#324 | dotfiles#321 | 5 | 3 / 18 | 0 / 1 | 153 / 1,068 |
| dotfiles#346 | dotfiles#332 | 4 | 3 / 5 | 1 / 2 | 231 / 514 |
| dotfiles#353 | dotfiles#344 | 4 | 8 / 9 | 1 / 1 | 588 / 764 |
| **dotfiles#324, dotfiles#346, dotfiles#353** | | **13** | **14 / 32** | **2 / 4** | **972 / 2,346 (41%)** |

S15 is on dotfiles#301 with no pull request. dotfiles#313's review numbers nothing. Its
"Minor" and "Risks" items are about the code.

The descriptions also carry the ticket's shortfalls as reading. dotfiles#324's "Where
the issue was out of date" (114 words) and its denylist bullet (67), dotfiles#346's
"Where the ticket didn't decide" (242 in the agent's version), and dotfiles#313's
premise correction (112) add up to 535 words. The prior study left
description questions out of its decision count. Of dotfiles#346's four "didn't
decide" bullets, three are S6, S7 and S8. The fourth, "No user to ask", is
about the session, not the ticket. Of dotfiles#324's five "out of date" bullets, three
are S1 and S2. The first, the pin bump, the issue already implied ("bumping
the flake input alone"). The fifth is about the binary's order of reads, not
something the issue asserted.

### The checks-matrix chain

S3, S10 and S15 are one undecided question. It surfaced on three tickets
over eight days:

1. **dotfiles#321** (2026-09-25): "whatever the `checks` exception needs, if the
   agent is to use it."
2. **dotfiles#324** (09-26): no exception, because it can't be expressed as a glob
   and the App has no Workflows permission. A check-adding ticket is handed
   back at the push.
3. **dotfiles#301** (10-02): intake takes a `ready-for-agent` issue whose work adds a
   check, and hands it back. The issue is still open, and so is dotfiles#313, the
   pull request that does the same work by hand.
4. **afk-agent#100's plan** (10-03 12:24Z): "a ticket that adds a flake check
   is `ready-for-human`." dotfiles#344 (10-02) still says to keep the exception.
5. **dotfiles#353** (10-03): follows the plan. The review raises one blocker and two
   should-fix on it, all one point.
6. **dotfiles#344 comments** (10-04 04:07Z and 04:24Z): the operator first adopts the
   plan's "dropped". Seventeen minutes later the operator reverses it to
   "kept", and files dotfiles#356 to move the matrix's names out of
   `.github/workflows/`. dotfiles#357 merges at 05:10Z.

The operator decided the question four times: dotfiles#344's "keep", the plan's
"drop", the first comment's "drop" and the second comment's "keep". It also
produced a hand-back. Two of those decisions were written into commits that
undo each other.

### What already exists

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

### What I could not determine (dotfiles)

- **What the operator's own sessions spent.** Four of the five pull requests
  were the operator's own work, and the shortfalls on them (S1 to S5, S10 to
  S14) were met first inside those sessions. GitHub records only what reached
  the description and the commits. The 62-of-80 turns for S6 is the
  operator's figure in skills#23. I did not see the session behind it.
- **Whether the operator re-read dotfiles#332 when re-applying the label** at 03:49Z
  on 10-03. The label events are recorded; the reading is not.
- **The job behind dotfiles#301's hand-back.** Its spend, how far it got and why it
  took an issue that already had an open pull request are not on GitHub.
- **A discrepancy in dotfiles#360.** It says dotfiles#332 was taken "as soon as afk-agent#131
  closed". GitHub shows afk-agent#131 closed at 12:28Z on 10-03, after dotfiles#346
  opened at 04:12Z. dotfiles#332's recorded blockers are afk-agent#149 (closed
  09-29) and dotfiles#336 (closed 03:48Z on 10-03), so dotfiles#336 is the one that
  fits the timing.
- **Whether dotfiles#318 had a ticket elsewhere.** Its review says "no tracker issue
  is attached", and nothing on GitHub references one.
- **Why dotfiles#321 closed with no closer** on 10-02, or whether its hand run was
  done.
- **Whether this generalises.** Four tickets, one implemented by the agent.
  The kinds are spread evenly enough that no single kind dominates at this n.

## B. afk-agent: interactive Opus sessions

### Sources and method

The data comes from `gh` against `corygyarmathy/afk-agent` (and
`corygyarmathy/skills` for one follow-up). I listed every merged or closed
pull request, 87 in all from #11 to #191 (all merged; none closed unmerged), with its closing references,
comments, submitted reviews, files and body.

**Selection.** A pull request qualifies when both of these hold:

- it closes an issue (`closingIssuesReferences`);
- it records a review on GitHub. That is either a comment answering an
  advisory review ("Review follow-up", "Changes after review", "Applied the
  review"), or a passage in the description saying what a review changed
  ("The last two commits address an advisory four-axis review", "After
  review").

**None of the 87 has the review itself posted.** No comment or review body
is in the four-axis shape. The reviews were run in separate sessions and only
their consequences reached GitHub. So the second condition is the closest
thing GitHub holds to "has a review on the pull request".

**22 qualify.** The pull requests are #13, #19, #20, #70, #84, #92, #140,
#141, #142, #144, #150, #153, #155, #156, #157, #159, #163, #164, #166, #167,
#171 and #175. In the same order, they close #9, #2, #4, #60, #65, #85,
#125, #128, #129, #139, #127, #145, #39, #146, #148, #147, #149, #160, #165,
#133, #22 and #132. The reviews of #13 and #19 are two-axis (Standards and
Spec), from before the skill had four. Of the rest, those that name their
axes name four.

**Excluded:**

- **No closing issue (22):** #11, #25, #38, #42, #43, #45, #55, #56, #57,
  #114–#118, #121, #122, #174, #181, #187–#189 and #191. These include the
  glossary and research pull requests. #56, #57 and #174 do record a review.
- **Closing an issue, but no review recorded (43):** #12, #14–#16, #21, #23,
  #30–#33, #35, #46, #48, #54, #59, #69, #71, #75, #77–#83, #89, #90,
  #94–#97, #101, #137, #138, #143, #151, #152, #158, #162 and #177–#180.
  #81's description has a section titled "The first acceptance
  criterion is not met". It is a ticket shortfall with no review to measure it
  by, so it is not counted.
- Docs-only research pull requests and wayfinder maps: none of them close an
  issue, so they fall out under the first rule.

**Counting.** The classification and the "caught by" column are the same as
for dotfiles, with three differences:

- **Findings are the review's points as the follow-up records them.** Where
  the follow-up gives the review's own count or numbering (#155, #156, #157,
  #159, #166, #167), that count is used. Otherwise it is one per item the
  follow-up lists, or one per item in the description's "before them" list.
  This undercounts any finding that was let go without being mentioned.
- **No review words.** The reviews are not on GitHub.
- **Description-only gaps are not rows.** A "Where the ticket didn't decide"
  or "Judgement calls" bullet that cost nothing past its line is tallied
  separately (about 27 across the 22), because those sections run to three or
  four small choices on most of these pull requests. A gap gets a row when it
  also caused a finding, a decision asked during the session, a rewrite or a
  follow-up issue. On dotfiles, every such bullet already had a cost, so this
  rule changes nothing there.

### The shortfalls

| # | Ticket → PR | Kind | What fell short | Decisions it asked | Rewrites and follow-ups | Catchable before implementing, by |
|---|---|---|---|---|---|---|
| B1 | #132 → #175 | under-decided | It never said what a delta review's spec is. The review would have judged the delta against the whole issue, and reported most of it missing. | 1 review point, fixed (`66299af`): the spec is the send-back. | None to #132 (0 edits). | The operator when writing. |
| B2 | #132 → #175 | wrong | "With the whole PR as context" assumed `reviewing-changes` could take a context diff. Its step 5 gives each reviewer "the same inputs and nothing else". | 1 review point. | skills#19 filed (278 words). #179 re-vendored the skills once it landed. | The operator when writing, by reading the skill's inputs. |
| B3 | #132 → #175 | under-decided | 3 choices "agreed before building": how the review learns the send-back head, how the whole PR is passed, and where the delta comes from. | 3, during the session. | None. | The operator when writing. |
| B4 | #22 → #171 | stale | "Price comes from the catalogue (`model.Price`)", written 2026-09-12. By 10-03 the review already used opencode's reported cost (#152, merged 09-28). The session switched to opencode's figure. | A session decision, then 1 review point. The operator restored the catalogue as the fallback (`67b5dcf`). | The description was left out of date on 3 points, by the operator's own note. | A check when taken. The ticket was 3 weeks old. |
| B5 | #22 → #171 | under-decided | Three "Open design questions" (where the record lives, what the harness reports, hand-backs). | 5, "agreed before building". | None. | When writing. The ticket said they were open. |
| B6 | #22 → #171 | under-decided | "A test should be able to say so" did not say how. | 1 review point, rewritten as kind-by-kind tests (`a24dc4a`). | None. | When writing. |
| B7 | #133 → #167 | under-decided | Nothing covered a review written on a head the pull request has since left. | Advisories 4 and 5 (one point), fixed (`a444cb0`). | None. | When writing. |
| B8 | #133 → #167 | under-decided | How the two send-back forms order together, and how "in flight" reads across them. | 2, "agreed before building". | None. | When writing. |
| B9 | #133 → #167 | too big | 740 added non-test lines, over the size signal. "One PR" was agreed with the operator, and the commits were split by layer. | 1, during the session. | None. | At planning. |
| B10 | #165 → #166 | wrong | Step 3's premise that the watch replays onto someone else's push and posts a second reply was false. `work.Watch` hands back. | Findings 2 and 3 (one point), and finding 1 in part. | #165 not edited. The follow-up says it "has the same wrong premise". | When writing, by reading `work.Watch`. |
| B11 | #149 → #163 | under-decided | "Where the review's request is claimed: to decide before this is built." | None after the rewrite. | Rewritten 14 minutes after filing, before any work (542 → 685 words, and the title changed), with a 270-word comment on #113. | Caught before implementing, by the operator. |
| B12 | #149 → #163 | under-decided | 4 choices "confirmed with the operator": strict reply sections, a hand-back linking the reply, when `afk work` builds revise, and the shared wait. | 4, during the session. | None. | When writing. |
| B13 | #146 → #156 | under-decided | It never said which tier a revision draws from. | A Spec finding: it runs on the implement tier, with no revise tier. | None. This is the fact dotfiles#332 later went stale against (S6). | When writing. |
| B14 | #146 → #156 | under-decided | "The denylist runs before every push" left the range open. | A Spec finding: only `Read..head`, so a human's commits can't block. | None. | When writing. |
| B15 | #39 → #155 | under-decided | It flagged that `resetsAt` moves by up to a second, and left how a waiver matches its period undecided. | Findings 3, 6 and 7 (one point), fixed with a 2s drift rule. | None. | When writing. The ticket names the problem. |
| B16 | #145 → #153 | wrong | "Claiming takes the hand-off label off" did not hold for a refusal, which must keep the pull request in the review queue. | Findings 2 and 6 (one point). | Amended by a 131-word comment on #145 ("because the issue does not move"). | When writing. |
| B17 | #145 → #153 | under-decided | What "in flight" means, whether several commands make one send-back, and what "a branch the App can push to" means. A fourth was the session's own call. | 3, "confirmed with the operator". | None. | When writing. |
| B18 | #127 → #150 | under-decided (close call) | A cut that fails its gate is not covered. The first version threw away work that had passed the gate. | 1 review point: keep `<branch>-whole` first. | None. | When writing. |
| B19 | #128 → #141 | too big | The ticket also covered a revision's push. | None. | The issue was edited 4 minutes before the PR to move that to #131 (286 → 303 words). It became #148. | During implementation. |
| B20 | #125 → #140 | under-decided (close call) | The ticket did not carry #111's rule that a `Part of` title is the description file's first line. #125's parsing dropped that line. | None numbered. | #139 filed (246 words, "Found reviewing #125"), and added as a blocker of #127. | When writing, from #111. |
| B21 | #60 → #70 | wrong | The issue's fix ("a subject whose `updated_at` has not moved is not read again") misses a comment posted in the same second. | 1 review point: settle on a second read. | None. | When writing, by probing `updated_at`. |
| B22 | #2 → #19 | under-decided (close call) | "Nothing sleeps holding a job" did not say how a wait for a resource token fits. | 1 Spec point. The bounded wait under a live lease was kept. | None. | When writing. |
| B23 | #9 → #13 | wrong (close call) | It asked `AGENTS.md` to state "the conventions this codebase will hold to". The result restated ADR 0001. | 5 review points with one root. `AGENTS.md` went from 150 lines to 66. | None. | When writing. |

#### By kind

| Kind | Count | Rows | Ticket-caused findings |
|---|---|---|---|
| stale | 1 | B4 | 1 |
| under-decided | 15 | B1, B3, B5–B8, B11–B15, B17, B18, B20, B22 | 10 (B1 1, B6 1, B7 2, B13 1, B14 1, B15 3, B18 1, B22 1) |
| wrong | 5 | B2, B10, B16, B21, B23 | 11 (B2 1, B10 2, B16 2, B21 1, B23 5) |
| too big | 2 | B9, B19 | 0 |
| **All** | **23** | | **23 of 114. No question was caused by a ticket (0 of 14).** |

Three close calls carry 7 of the 23 findings: B18 (1), B22 (1) and B23 (5).
Without them it is 16 of 114 (14%).

#### By pull request

| PR | Ticket | Shortfalls | Ticket-caused findings / recorded | Questions recorded | Decisions in session |
|---|---|---|---|---|---|
| #175 | #132 | 3 | 2 / 5 | 0 | 3 |
| #171 | #22 | 3 | 2 / 7 | 0 | 5 |
| #167 | #133 | 3 | 2 / 6 | 2 | 3 |
| #166 | #165 | 1 | 2 / 4 | 0 | 0 |
| #164 | #160 | 0 | 0 / 1 | 0 | 0 |
| #163 | #149 | 2 | 0 / 7 | 0 | 4 |
| #159 | #147 | 0 | 0 / 3 | 4 | 0 |
| #157 | #148 | 0 | 0 / 5 | 0 | 0 |
| #156 | #146 | 2 | 2 / 12 | 3 | 1 |
| #155 | #39 | 1 | 3 / 9 | 1 | 0 |
| #153 | #145 | 2 | 2 / 6 | 4 | 3 |
| #150 | #127 | 1 | 1 / 8 | 0 | 0 |
| #144 | #139 | 0 | 0 / 4 | 0 | 0 |
| #142 | #129 | 0 | 0 / 4 | 0 | 0 |
| #141 | #128 | 1 | 0 / 3 | 0 | 0 |
| #140 | #125 | 1 | 0 / 4 | 0 | 0 |
| #92 | #85 | 0 | 0 / 1 | 0 | 0 |
| #84 | #65 | 0 | 0 / 4 | 0 | 0 |
| #70 | #60 | 1 | 1 / 1 | 0 | 0 |
| #20 | #4 | 0 | 0 / 3 | 0 | 0 |
| #19 | #2 | 1 | 1 / 11 | 0 | 0 |
| #13 | #9 | 1 | 5 / 6 | 0 | 0 |
| **All** | | **23** | **23 / 114** | **14** | **19** |

Several tickets left a choice open on purpose and said so. #160 ("Choosing
between them is the task"), #65 ("What needs designing") and #22 ("Open
design questions") are examples. They are classified as under-decided
because they meet the definition, but the ticket's author knew the choice
was open. Those choices cost decisions during the session (B5) or a line in
the description (#160, #65), not findings.

### What differs between the populations

These differences could bias any comparison of A and B:

- **Someone could answer.** On afk-agent the implementing session could ask
  the operator, and recorded 19 decisions taken that way. The afk agent on
  dotfiles runs unattended. dotfiles#346 says "No user to ask", so a gap there
  surfaces later, as a finding or a description bullet. This alone moves cost
  from findings to session decisions in B, and lowers B's ticket-caused
  share.
- **Who implemented.** In B every implementer was Opus, driven interactively.
  In A only dotfiles#346 was the agent; the other four were the operator's own
  sessions. A has one agent-implemented pull request, so it says little about
  the agent's implementer.
- **Who reviewed, and what survives.** B's reviews ran on Opus with the
  `reviewing-changes` skill, and only the operator's follow-ups survive. A's
  reviews ran on `deepseek-v4-pro` through the agent, and are posted whole.
  B's findings are therefore what the operator chose to record. Silently
  dropped findings, and every word count, are missing. The operator's
  follow-up also decides what counts as one point. The prior study found
  numbered findings over-count distinct points by about a quarter, so B's
  per-point counts and A's numbered counts are not the same unit.
- **Who wrote the review prompt.** In B the operator ran the review session
  (two early ones were two-axis). In A the agent's prompt and severity floor
  applied, after dotfiles#324.
- **How far the work was from its ticket.** Most B tickets were filed in
  batches from a resolved map (#102's children on 2026-09-28 between 02:50
  and 11:00) and taken within hours, in the repository whose code they
  describe. A's tickets were dotfiles tickets resting on afk-agent facts, and
  waited days (dotfiles#332: 5 days; dotfiles#301: 8) while afk-agent moved.
  That is where A's staleness came from.
- **Selection.** B counts only pull requests whose review left a trace on
  GitHub: 22 of the 65 that close an issue. A counts every Go-era
  dotfiles pull request with an agent review. Neither is a random sample.
- **Size.** B's pull requests are larger (median about +940 added lines) and
  mostly Go code with tests. A's are Nix and docs. More code gives a review
  more code-caused findings to raise, which also lowers the ticket's share.

### What I could not determine (afk-agent)

- **The reviews themselves.** None was posted, so the findings that were
  raised and let go without a mention, their severities and their words are
  unknown. The counts above are a floor on recorded points.
- **What was asked during the sessions beyond what the descriptions record.**
  "Agreed before building" and "confirmed with the operator" are the
  session's own summaries. The transcripts are not on GitHub.
- **Whether "agreed before building" happened before or after the
  implementer read the code.** That decides whether it was a planning step
  or a mid-implementation interruption.
- **Why 43 of the 65 issue-closing pull requests have no review trace.**
  Possibly no review was run, or it was run and nothing changed. GitHub
  cannot tell these apart.
