# Where the Go-era tickets fell short

Research for [#194](https://github.com/corygyarmathy/afk-agent/issues/194),
part of the map [#182](https://github.com/corygyarmathy/afk-agent/issues/182).
Gathered 2026-10-08, read-only, from GitHub and the operator's local Claude
Code session transcripts. Extends
[Operator load on the Go-era pull requests](operator-load-go-era.md).

**Question.** On the Go-era pull requests, how often did the issue the work
started from fall short, in which way, and what did each case ask of the
operator? A shortfall is **stale** (a predecessor merged, or a decision landed
elsewhere, and changed what the issue assumed), **under-decided** (the issue
left a choice open that the implementer had to make), **wrong** (the issue
asked for something that did not hold) or **too big** (more than one concern,
or more than one sitting). This is evidence, not a recommendation.

There are two populations, reported separately:

- **A. dotfiles.** `corygyarmathy/dotfiles` pull requests that the afk agent
  reviewed (and in one case wrote), from 4 tickets. These are written
  `dotfiles#N`.
- **B. afk-agent.** This repository's own pull requests: 43, from 46
  tickets. An interactive Claude Opus session implemented each ticket, and a
  separate Opus session reviewed it with `reviewing-changes`. The reviews
  were never posted to GitHub. They were recovered from the operator's
  session transcripts (`~/.claude/projects/-home-coryg-git-afk-agent/`).
  Bare `#N` means this repository.

## Short answer

1. **The tickets fell short in both populations, in different ways.**
   - dotfiles: 15 shortfalls on 4 tickets, spread across the kinds (stale 5,
     under-decided 5, too big 3, wrong 2).
   - afk-agent: 37 shortfalls on 43 pull requests, mostly under-decided
     (stale 2, under-decided 21, wrong 10, too big 4).
2. **The ticket caused about one finding in five on afk-agent, against two in
   five on dotfiles.** Compared on reviews of the same shape (the full
   four-axis report, answered finding by finding):

   | | Ticket-caused findings | Ticket-caused questions |
   |---|---|---|
   | dotfiles (3 reviews) | 14 of 32 (44%); 11 (34%) strictly | 2 of 4 |
   | afk-agent (25 reviews) | 26 of 147 (18%); 23 (16%) strictly | 4 of 59 |
   | Combined | 40 of 179 (22%); 34 (19%) strictly | 6 of 63 |

   Across every afk-agent review shape, including the earlier ones that
   counted `consider` findings and the stack review, it is 43 of about 340
   (13%).
3. **On afk-agent, the operator let nothing go silently.** In the 25
   two-step reviews, the operator answered every one of 147 findings by
   number, in the review session, before any follow-up reached GitHub. 141
   were fixed as recommended or in a variant the operator named. 6 (4%) were
   declined, deferred or found wrong on checking. The GitHub follow-up
   comments record fewer than that. #141's follow-up names 3 of its 8
   findings, and #70's names 1 of its 12.
4. **"Agreed before building" means at the start, before any code.** In the
   six implementing sessions read, the operator's prompt invited questions.
   The session asked 1 to 3 questions within 1 to 3 minutes of that prompt,
   before its first edit. The gaps were caught while the operator was still
   present, not mid-implementation.
5. **Staleness was a dotfiles problem.** Of the 7 stale shortfalls, 5 were on
   dotfiles, where tickets rested on afk-agent and waited days for it to
   move. afk-agent tickets were mostly filed in batches and taken within
   hours. The two stale cases there are #22, three weeks old by the time it
   was taken, and #40, whose decisions #49 had reversed.
6. **"Wrong" was more common on afk-agent once the reviews were read.** The
   GitHub record showed 5 wrong shortfalls; the transcripts show 10. Several
   are a ticket asking for something the reviewer pushed back on and the
   operator then agreed to drop:
   - #126's "default 400" broke the no-defaults rule.
   - #168 asked for a `checks/` convention, which the operator declined in
     favour of an attribute.
   - #127's link order did not hold.
   - #72's premise about imports was false.
7. **The biggest afk-agent costs crossed repositories.**
   - **#132** assumed a skill input that did not exist. That cost three
     findings, skills#19 and a re-vendor.
   - **#168 and #126** each needed a dotfiles change first: dotfiles#358 and
     dotfiles#329, the latter set by dotfiles#339.
   - **#63's review** flagged that the dotfiles module did not yet set the
     new required flags. The operator left that to dotfiles#321, which then
     went stale against it (S1).

## Side by side

| | A. dotfiles | B. afk-agent | Combined |
|---|---|---|---|
| Pull requests (tickets) | 5 (4) + 1 hand-back | 43 (46) | 48 (50) |
| Implementer | the afk agent (1), the operator's own sessions (4) | interactive Opus session that asked first | |
| Reviewer | afk agent on `deepseek-v4-pro`, posted | Opus with `reviewing-changes`, not posted | |
| Shortfalls | 15 | 37 | 52 |
| stale | 5 | 2 | 7 |
| under-decided | 5 | 21 | 26 |
| wrong | 2 | 10 | 12 |
| too big | 3 | 4 | 7 |
| Ticket-caused findings (same-shape reviews) | 14 / 32 (44%) | 26 / 147 (18%) | 40 / 179 (22%) |
| Ticket-caused questions | 2 / 4 | 4 / 59 | 6 / 63 |
| Findings let go with no answer | unknown on 2 of 4 PRs | 0 of 147 | |
| Decisions asked before implementing | 0 (unattended) | 20 recorded, on 7 PRs | |
| Issues rewritten or amended because of a shortfall | 2 | 4 (#128, #145, #72, #149) | 6 |
| Follow-up issues the shortfall caused | 3 | 4 (skills#19, #139, #154, dotfiles#358) | 7 |

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

**GitHub.** I listed every pull request in this repository: 87 of them, all
merged, numbered #11 to #191. For each I read its closing references, comments,
submitted reviews, body and files. For each issue it closes, I read the body,
edit history, comments, blockers and cross-references.

**Transcripts.** I also read the operator's Claude Code session transcripts
in `~/.claude/projects/-home-coryg-git-afk-agent/`. That is 153 top-level
sessions from 2026-09-12 on; no worktree sessions sit in sibling
directories. Today's two wayfinder sessions were left out. I used `jq` and
scripts, never whole files.

**Finding the reviews.** A review session is one that runs
`reviewing-changes`, either as a skill call or as the `/reviewing-changes`
command, or that is asked to "review PR #N". Its report is the session's
longest assistant text with the four axis headings. The operator's
answers are the messages typed after it.

**Matching a session to a pull request:**
- Most sessions name the pull request in the command arguments.
- The reviews of #140–#144 and #150 ran on the branch before the pull request
  was opened. They name the starting commit and the issue, and the same
  session then opened the pull request.
- #159 has two sessions. The first stopped before reviewing anything, because
  of a tool fault; the second is the review.

Every match below is high confidence: the session names the pull request, or
the branch and issue, and goes on to push to that pull request.

**What I take from a review.** I counted its numbered findings and their
severities, its questions and its words. For each finding I asked whether
its stated cause is the issue's text or the code. I then checked the
operator's typed answer to each finding: fixed, fixed differently, declined,
deferred, or not mentioned. "Let go" means not mentioned. Findings and
transcript text are paraphrased here, never quoted beyond a few words.

**What I did not read.** I did not read sub-agent transcripts, or tool
results beyond the report.

### The population

**Selection.** A pull request qualifies when it closes an issue and either
GitHub or the transcripts hold a review of it.

**43 qualify:**
- **19 of the earlier 22**, with their review now read from a transcript:
  #20, #70, #84, #92, #140–#142, #144, #150, #155–#157, #159, #163, #164,
  #166, #167, #171 and #175.
- **2 of the earlier 22 with no review session:** #19 and #153. They keep
  their GitHub-only counts.
- **22 of the 43 that previously showed no review trace,** because a review
  session exists for them: #54, #59, #71, #75, #77, #78, #81, #90, #95–#97,
  #101, #137, #138, #143, #151, #152, #158, #162, #177, #178 and #180.

**#13 leaves the population.** Its session shows that the "review" its
follow-up answered was the operator's own reading, typed into the
implementing session. No advisory review was run. Its row from the GitHub
pass (5 points on `AGENTS.md`) was the operator's own points, not a
reviewer's.

The other 21 issue-closing pull requests have neither a review session nor a
review trace.

**Also reviewed, but outside the population.** #55–#57 were reviewed in the
stack review with #54 and #59, but they close no issue. #174 also closes no
issue. One #56 finding is a clear stale ticket: #51 still said "a plain
push" after #40 and ADR 0001 had moved to a leased push. It is not counted.

**Four review shapes:**

| Shape | PRs | What the transcript holds | Comparable to dotfiles? |
|---|---|---|---|
| **Two-step** (2026-09-28 onward) | 25 | The full four-axis report at a `should-fix` floor. The operator then answers each finding by number, and the session applies the answers. | Yes. This is the shape used for the headline. |
| **Review-and-fix** (09-26 and 09-27) | 13 | The reviewer was told to fix what clearly needed it and hand back the judgement calls. Severities include `consider`. The text read is often the post-fix summary, so counts are approximate. | No. The operator delegated the findings. |
| **Stack review** (09-25) | 2 (#54, #59) | One report for #54–#59. The operator's answers are not in the session; the findings became issues #58 and #60–#65. | Counts yes, answers no. |
| **Plain review** (09-12) | 1 (#20) | "Review this PR", with no skill. The same session fixed what the operator approved. | Partly. |

### Per pull request

| PR | Ticket | Review | Date | Findings (blockers) | Questions | Words | Ticket-caused findings / questions | Answered by the operator |
|---|---|---|---|---|---|---|---|---|
| #13 | #9 | none (operator's own reading) | 09-12 | - | - | - | - | left the population |
| #19 | #2 | two-axis, not in transcripts | 09-12 | 11 | 0 | - | 1 / 0 | GitHub follow-up only |
| #20 | #4 | plain review, reviewer fixed | 09-12 | 5 | 0 | 828 | 0 / 0 | all fixed on request |
| #54 | #49 | stack review (#54–#59) | 09-25 | 14 | 2 | 2869 (#54–#59) | 2 / 0 | not in session; became issues |
| #59 | #50–#53 | stack review (#54–#59) | 09-25 | 18 | 1 | (with #54) | 2 / 0 | not in session; became issues |
| #70 | #60 | review-and-fix | 09-26 | 12 | 1 | 962 | 5 / 0 | delegated |
| #71 | #61 | review-and-fix | 09-26 | 11 | 1 | 967 | 0 / 0 | delegated |
| #75 | #62 | review-and-fix | 09-26 | 14 | 0 | 797 | 0 / 0 | delegated |
| #77 | #63 | review-and-fix | 09-26 | 19 | 0 | 667 | 1 / 0 | 6 calls answered |
| #78 | #64 | review-and-fix | 09-26 | 8 | 0 | 485 | 0 / 0 | delegated |
| #81 | #72 | review-and-fix | 09-26 | 8 (1) | 0 | 568 | 3 / 0 | 2 calls answered |
| #84 | #65 | review-and-fix | 09-26 | 16 | 3 | 704 | 0 / 0 | delegated |
| #90 | #76 | review-and-fix | 09-26 | 15 | 0 | 607 | 0 / 0 | delegated |
| #92 | #85 | review-and-fix | 09-26 | 13 | 0 | 564 | 0 / 0 | delegated |
| #95 | #87 | review-and-fix (partial report) | 09-26 | 4 | 0 | 610 | 0 / 0 | delegated |
| #96 | #91 | review-and-fix | 09-26 | 11 | 1 | 604 | 0 / 0 | delegated |
| #97 | #93 | review-and-fix | 09-26 | 8 | 0 | 577 | 1 / 0 | 3 calls answered |
| #101 | #98 | review-and-fix | 09-27 | not countable | 0 | 638 | 0 / 0 | 4 calls answered |
| #137 | #126 | two-step | 09-28 | 9 | 3 | 889 | 2 / 0 | 9/9 (1 deferred, 1 disagreed) |
| #138 | #124 | two-step | 09-28 | 4 | 2 | 563 | 0 / 0 | 4/4 |
| #140 | #125 | two-step | 09-28 | 8 | 2 | 886 | 0 / 0 | 8/8 |
| #141 | #128 | two-step | 09-28 | 8 | 2 | 903 | 2 / 0 | 8/8 |
| #142 | #129 | two-step | 09-28 | 7 | 1 | 884 | 0 / 0 | 7/7 |
| #143 | #130 | two-step | 09-28 | 9 | 3 | 1004 | 1 / 1 | 9/9 |
| #144 | #139 | two-step | 09-28 | 3 | 2 | 550 | 0 / 1 | 3/3 |
| #150 | #127 | two-step | 09-28 | 10 (1) | 3 | 1018 | 1 / 0 | 10/10 |
| #151 | #86 | two-step | 09-28 | 8 | 5 | 990 | 0 / 0 | 8/8 |
| #152 | #99 | two-step | 09-28 | 6 | 4 | 763 | 1 / 0 | 6/6 (1 to a follow-up) |
| #153 | #145 | not in transcripts | 09-28 | 6 | 4 | - | 2 / 0 | GitHub follow-up only |
| #155 | #39 | two-step | 09-28 | 9 (3) | 1 | 822 | 3 / 0 | 9/9 |
| #156 | #146 | two-step | 09-28 | 13 (1) | 3 | 1155 | 2 / 0 | 13/13 |
| #157 | #148 | two-step | 09-28 | 5 | 4 | 994 | 1 / 1 | 5/5 |
| #158 | #134 | two-step | 09-29 | 9 (3) | 1 | 793 | 2 / 0 | 9/9 |
| #159 | #147 | two-step | 09-29 | 3 | 4 | 937 | 0 / 0 | 3/3 |
| #162 | #161 | two-step | 09-29 | 1 | 2 | 632 | 0 / 0 | 1/1 |
| #163 | #149 | two-step | 09-29 | 6 | 3 | 828 | 0 / 0 | 6/6 |
| #164 | #160 | two-step | 09-29 | 1 | 2 | 557 | 0 / 0 | 1/1 |
| #166 | #165 | two-step | 09-30 | 4 | 0 | 750 | 2 / 0 | 4/4 |
| #167 | #133 | two-step | 09-30 | 6 (1) | 2 | 932 | 2 / 0 | 6/6 (blocker found wrong) |
| #171 | #22 | two-step | 10-03 | 10 (1) | 2 | 952 | 2 / 0 | 10/10 |
| #175 | #132 | two-step | 10-04 | 5 | 3 | 835 | 3 / 0 | 5/5 |
| #177 | #169 | two-step | 10-04 | 0 | 2 | 561 | 0 / 0 | - |
| #178 | #168 | two-step | 10-04 | 3 (2) | 1 | 636 | 2 / 1 | 3/3 (2 declined) |
| #180 | #176 | two-step | 10-04 | 0 | 2 | 674 | 0 / 0 | - |

On the 25 two-step reviews: 147 findings (12 blockers, 135 should-fix) and 59
questions, in 20,508 words, about 820 per review. The ticket caused 26
findings (18%) and 4 questions. Across all shapes, the ticket caused 43 of
about 340 findings (13%). The review-and-fix shape counts `consider`
findings, which makes its denominator larger.

### What the operator let go

**On the 25 two-step reviews, nothing was let go silently.** Every finding
got a typed answer, usually "Fix as recommended", within minutes of the
report and in the same session. That session then fixed them and wrote the
GitHub follow-up. Of 147 findings:
- 141 were fixed as recommended, or in a variant the operator named;
- 2 were declined (#178's two blockers, which the operator answered by
  asking for an attribute rather than a convention);
- 2 were deferred (#137's flag default, to a dotfiles pull request; #152's
  sub-agent models, to #154);
- 1 was disagreed with in favour of a different home for the reasoning
  (#137's finding 5);
- 1 was found wrong on checking (#167's blocker about the reaction login).

**The GitHub follow-up undercounts.** It records what changed, not what was
raised. Against the transcripts, the earlier GitHub-only counts were a floor:

| PR | GitHub follow-up | Review |
|---|---|---|
| #70 | 1 point | 12 findings |
| #92 | 1 point | 13 findings |
| #84 | 4 points | 16 findings |
| #142 | 4 points | 7 findings |
| #140 | 4 points | 8 findings |
| #141 | 3 points | 8 findings |
| #171 | 7 points | 10 findings |

**Questions were answered too, but less decisively.** The operator often
asked the session to check, or asked what the question meant. "What's the
question here?" comes up on #151, #152 and #157.

**The review-and-fix shape** delegated what was clear to the reviewer, so a
let-go rate cannot be read off it. The judgement calls it handed back were
all answered (#77, #81, #97, #101). **The stack review's** answers happened
outside the session, as issues.

### When the decisions were made

The pull requests' "agreed before building" and "confirmed with the
operator" decisions were all taken at the start of the implementing
session. I read the implementing sessions for #132, #22, #133, #149, #160
and #145:

- The operator's prompt asked the session to "ask me for any significant
  decisions or judgement calls".
- The session asked 1 to 3 questions, 1 to 3 minutes after that prompt,
  before its first edit.
- Where recorded, the first edit followed 2 to 11 minutes later.

So the operator met these gaps while still at the keyboard, at planning
time, and not as interruptions mid-implementation. #160's open choice ("Choosing between them is the
task") was also put this way, as one question, which makes it a decision
rather than a description line (B23). I did not read the implementing
sessions for #146, or for the 22 pull requests that joined.

### The shortfalls

| # | Ticket → PR | Kind | What fell short | What it asked of the operator | Rewrites and follow-ups | Catchable before implementing, by |
|---|---|---|---|---|---|---|
| B1 | #132 → #175 | under-decided | It never said what a delta review's spec is. | Finding 4; the operator chose the send-back as the spec. | None. | When writing. |
| B2 | #132 → #175 | wrong | "The whole PR as context" assumed `reviewing-changes` takes a context diff. | Findings 3 and 5. | skills#19 filed; #179 re-vendored. | When writing, from the skill's inputs. |
| B3 | #132 → #175 | under-decided | Three design choices. | 3 questions at the start. | None. | When writing. |
| B4 | #22 → #171 | stale | "Price comes from the catalogue". By 10-03 the review used opencode's reported cost. | The session's own choice, then finding 5; the operator chose the catalogue as the fallback. | Description left out of date on 3 points. | A check when taken (3 weeks old). |
| B5 | #22 → #171 | under-decided | Its three "Open design questions". | 3 questions at the start (5 decisions per the description). | None. | When writing; the ticket said they were open. |
| B6 | #22 → #171 | under-decided | "A test should be able to say so" said nothing about how. | Finding 10. | None. | When writing. |
| B7 | #133 → #167 | under-decided | Nothing covered a review written on an old head. | Findings 4 and 5. | None. | When writing. |
| B8 | #133 → #167 | under-decided | How the two forms order, and "in flight" across them. | 2 questions at the start. | None. | When writing. |
| B9 | #133 → #167 | too big | Over the size signal. | 1 question at the start ("one PR"). | None. | At planning. |
| B10 | #165 → #166 | wrong | Step 3's premise (a replay and a second reply) was false. | Findings 2 and 3, and finding 1 in part. | #165 not edited. | When writing, from `work.Watch`. |
| B11 | #149 → #163 | under-decided | Where the review's request is claimed. | None after the rewrite. | Rewritten 14 minutes after filing, before any work. | Caught before implementing. |
| B12 | #149 → #163 | under-decided | Reply strictness, a hand-back after the reply, and when `afk work` builds revise. | 3 questions at the start (4 per the description). | None. | When writing. |
| B13 | #146 → #156 | under-decided | Which tier a revision draws from. | Finding 7: implement's tier, no revise tier. | None. It is the fact dotfiles#332 went stale against (S6). | When writing. |
| B14 | #146 → #156 | under-decided | The range "the denylist runs before every push" covers. | Finding 3. | None. | When writing. |
| B15 | #39 → #155 | under-decided | It flagged the `resetsAt` drift but left the matching rule open. | Findings 3, 6 and 7, all three blockers. | None. | When writing; the ticket names the problem. |
| B16 | #145 → #153 | wrong | "Claiming takes the hand-off label off" did not hold for a refusal. | Findings 2 and 6 (GitHub only). | Amended by a comment on #145. | When writing. |
| B17 | #145 → #153 | under-decided | "In flight", several commands, and "a branch the App can push to". | 3 questions at the start. | None. | When writing. |
| B18 | #127 → #150 | wrong | Its order (the rest issue links the PR) forced an open-then-edit with a third set of rounds. | Finding 10, which pushes back on the spec; the operator agreed. | #127 not edited; the PR followed the new order. | When writing. |
| B19 | #128 → #141 | too big | It also covered a revision's push, which did not exist yet. | Findings 3 and 8. | #128 edited to move that to #131; it became #148. | When writing; caught during review, before the PR opened. |
| B20 | #125 → #140 | under-decided (close call) | It did not carry #111's first-line title rule. | A below-floor note. | #139 filed and made #127's blocker. | When writing, from #111. |
| B21 | #60 → #70 | wrong | Its fix (skip a subject whose `updated_at` has not moved) misses same-second comments, reactions and later collaborators. | 5 findings, 2 of them should-fix (one root). | None. | When writing, by probing `updated_at`. |
| B22 | #2 → #19 | under-decided (close call) | "Nothing sleeps holding a job" against a token wait. | 1 Spec point (GitHub only). | None. | When writing. |
| B23 | #160 → #164 | under-decided | It left the fix open on purpose. | 1 question at the start. | None. | When writing; deliberate. |
| B24 | #148 → #157 | wrong (close call) | It designed for "a point may ask for a rebase", which #146 hands back. | Finding 3 and Q4; the operator kept the fresh fetch. | None. | When writing, from #146. |
| B25 | #139 → #144 | under-decided (close call) | Whether title length is #139's job or #127's. | Q2; answered "here". | None. | When writing. |
| B26 | #49 → #54 | too big | "Claims every unanswered `/implement` … after a kill" needs #58's fix. | A Spec should-fix. | Turned into issues. | At planning. |
| B27 | #40 → #54 | stale | #40's "Decisions already taken" still named the read #49 had reversed. | A Spec pushback (consider). | None recorded. | A check when taken. |
| B28 | #50–#53 → #59 | too big | The stack closes #40, whose kill criterion waits on #58. | A Spec should-fix. | None recorded. | At planning. |
| B29 | #53 → #59 | under-decided (close call) | A job-made request contradicted the glossary's "only imperative channel". | A Standards should-fix. | None traced. | When writing. |
| B30 | #63 → #77 | wrong | An immediate hand-back on a 403 cannot be done as written: the error arrives after the commit. | A judgement call; the operator accepted the rounds-then-hand-back. | None. | When writing. |
| B31 | #72 → #81 | wrong | The premise that `implement` imports `intake` only for `Armer` was false. | A literal-reading blocker and 2 should-fix; a judgement call. | #72's acceptance amended (3 edits). | When writing, by grepping the imports. |
| B32 | #93 → #97 | under-decided | Where the bound lives, and that it is not tied to the lease. | A judgement call (ADR or not). | Noted in ADR 0001. | When writing. |
| B33 | #126 → #137 | wrong | "`--size-signal` exists (default 400)" broke the no-defaults rule. | Findings 1 and 8; deferred to the module. | dotfiles#329. | When writing, from `AGENTS.md`. |
| B34 | #130 → #143 | under-decided | Its queue left out revisions in flight, and was unclear on parked jobs. | Finding 8 and Q2. | None. | When writing. |
| B35 | #99 → #152 | under-decided | A 35-word question as the ticket. | Finding 6 (pushback on the spec). | #154 filed. | When writing. |
| B36 | #134 → #158 | under-decided (close call) | It misses a push that landed but reads as unpushed. | Findings 5 and 8. | None. | When writing. |
| B37 | #168 → #178 | wrong | It asked for `checks/` in the test-directory list, which the operator then declined in favour of an attribute. | Findings 1 and 2 (blockers, declined), and Q1. | dotfiles#358 filed. | When writing. |

#### By kind

| Kind | Count | Rows | Ticket-caused findings |
|---|---|---|---|
| stale | 2 | B4, B27 | 2 |
| under-decided | 21 | B1, B3, B5–B8, B11–B15, B17, B20, B22, B23, B25, B29, B32, B34–B36 | 16 |
| wrong | 10 | B2, B10, B16, B18, B21, B24, B30, B31, B33, B37 | 21 |
| too big | 4 | B9, B19, B26, B28 | 4 |
| **All** | **37** | | **43 findings, and 4 questions** |

The ticket-caused finding counts per row add to 43. They come from the
per-pull-request table, summed over the rows each pull request's findings
belong to.

### What differs between the populations

These differences could bias any comparison of A and B:

- **The implementer asked first.** In B the operator's prompt invited
  questions, and the session asked them before any code. That turned 20 ticket
  gaps into decisions at the start, which never became findings. The afk
  agent on dotfiles runs unattended, so the same gaps arrive as findings and
  description bullets.
- **The operator answered the review in the same sitting.** In B the operator
  read the four-axis report in a terminal and answered by number. In A the
  review is a GitHub comment, owed no answer, and on 2 of 4 pull requests
  nothing records what was done with it.
- **Who reviewed.** B's reviewer was Opus running `reviewing-changes` in a
  separate session that the operator started. A's was the afk agent on
  `deepseek-v4-pro`. Both use the same skill family. B's reviews average 820
  words, against 800 on A.
- **Review shape changed over time in B.** Only the 25 two-step reviews are
  like A's. The earlier shapes either delegated the findings or reviewed a
  stack at once.
- **How far the work was from its ticket.** Most B tickets were filed in
  batches from a resolved map and taken within hours, in the repository whose
  code they describe. A's tickets rested on afk-agent facts and waited days.
- **Size.** B's pull requests are larger (median about +940 lines) and are
  Go with tests. More code gives a review more code-caused findings to raise,
  which lowers the ticket's share.
- **Selection.** B now includes every issue-closing pull request with a
  review on GitHub or in a transcript: 43 of 65. A includes every Go-era
  dotfiles pull request with an agent review.

### What I could not determine (afk-agent)

- **Reviews of #19 and #153.** Neither is in the transcripts, in this
  project's directory or any other. Their counts remain the GitHub floor.
- **Sub-agent findings below the floor.** The reports aggregate what each
  axis found above the floor. Findings the sub-agents dropped, and the
  orchestrator's merging of duplicates, sit in sub-agent transcripts I did
  not read.
- **What happened to the stack review's findings** outside the session,
  beyond the issues filed afterwards.
- **The review-and-fix shape's full finding lists.** Several of those
  sessions report after fixing, so their counts are approximate, and #101's
  is not countable.
- **The implementing sessions for #146 and the 22 joined pull requests.** I
  did not read them, so their start-of-session questions are not counted
  (the 20 above are a floor).
- **The 21 issue-closing pull requests with no review session.** It is not
  known whether they were reviewed some other way, or not at all.
