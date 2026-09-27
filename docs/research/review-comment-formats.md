# Review comment formats that cut noise

Research for [#106](https://github.com/corygyarmathy/afk-agent/issues/106), part
of the map [#102](https://github.com/corygyarmathy/afk-agent/issues/102).
Gathered 2026-09-27. This reports what the sources say; it is not a process
design.

**Question.** What conventions exist for labelling and ranking review comments,
and what is known - empirically where possible - about the number and length of
comments a recipient can usefully act on? Which conventions help the recipient
decide quickly what matters?

## Summary

| # | Finding | Confidence |
|---|---------|------------|
| 1 | Every established convention separates "must change before merge" from "optional", and they converge on the same few tiers. The split is the load-bearing part; the vocabulary differs. | High |
| 2 | Only a minority of human review comments concern defects (about 15% at Microsoft; a 75:25 maintainability-to-functional ratio across OSS, industry and academic data). Most review output is, by nature, the "optional" tier. | High |
| 3 | Even among humans, a third or more of comments are not useful to the author (Microsoft 64-68% useful; other datasets 55-77%). Usefulness falls as change size (files) grows. | High |
| 4 | Automated/LLM review comments are acted on less than human ones in every study that compares them, and noise is the complaint practitioners raise. Rates range from ~1-19% (open GitHub Actions) through ~39-40% (Atlassian, Google) to ~74% (one industrial deployment). | High on direction, medium on any single number |
| 5 | The deployments that got acceptance up did it by *not posting*: suppressing whole categories of non-actionable finding, confidence thresholds, actionability filters, and a "not useful" feedback loop, against an explicit target (Google: 80% useful; Tricorder: <10% effective false positives). | High |
| 6 | Concise, code-carrying, line-anchored comments are acted on more than long prose or file/PR-level comments. | Medium (one large correlational study plus survey statements) |
| 7 | No source found gives an evidence-based number of comments, or characters, a recipient can act on. What exists is indirect: volume and acceptance move in opposite directions, and deployed tools settle at roughly 2-4 comments per PR. | Low - this is a gap, not a finding |

## 1. Labelling and ranking conventions

### Conventional Comments

[conventionalcomments.org](https://conventionalcomments.org/) proposes a
prefix grammar:

```
<label> [decorations]: <subject>

[discussion]
```

Labels: `praise`, `nitpick`, `suggestion`, `issue`, `todo`, `question`,
`thought`, `chore`, `note` (with `typo`, `polish`, `quibble` offered as
alternatives). Decorations: `(blocking)`, `(non-blocking)`, `(if-minor)` - the
last meaning "resolve only if the change turns out to be minor". `nitpick` is
defined as non-blocking and `note` as always non-blocking.

The site's rationale is qualitative: adding a label makes "the intention
clear" and "the tone dramatically changes", and labels "prompt the reviewer to
give more actionable comments". No evidence is offered beyond that.

Note the two independent axes: the *label* says what kind of comment it is, the
*decoration* says whether it gates. A `suggestion (blocking)` and an
`issue (non-blocking)` are both legal.

### Google engineering practices

[How to write code review comments](https://google.github.io/eng-practices/review/reviewer/comments.html),
"Label comment severity":

> Nit: This is a minor thing. Technically you should do it, but it won't hugely
> impact things.
> Optional (or Consider): I think this may be a good idea, but it's not
> strictly required.
> FYI: I don't expect you to do this in this CL, but you may find this
> interesting to think about for the future.

The unmarked default is therefore "required". The same guide asks reviewers to
explain *why*, and to comment on good work too.
[The Standard of Code Review](https://google.github.io/eng-practices/review/reviewer/standard.html)
adds the reason severity labels exist: reviewers "should favor approving a CL
once it is in a state where it definitely improves the overall code health …
even if the CL isn't perfect", and a polish point should be prefixed "Nit:"
"to let the author know that it's just a point of polish that they could
choose to ignore".
[Speed of Code Reviews](https://google.github.io/eng-practices/review/reviewer/speed.html)
defines "LGTM with comments": approve while leaving unresolved comments when
they "don't *have* to be addressed" or are minor, and "specify which of these
options they intend, if it is not otherwise clear".

Google's tool encodes the same split as state rather than text. In Critique,
"Unresolved comments represent action items for the change author to
definitely address. Resolved comments include optional or informational
comments that may not require any action"
([Sadowski et al., ICSE-SEIP 2018](https://sback.it/publications/icse2018seip.pdf), §3).

### Netlify feedback ladder

[Netlify, 2020](https://www.netlify.com/blog/2020/03/05/feedback-ladders-how-we-encode-code-reviews-at-netlify/)
- five rungs, each with an emoji so the rank is visible at a glance:

| Rung | Meaning |
|------|---------|
| ⛰ Mountain | Blocking and requires immediate action |
| 🧗 Boulder | Blocking - must be fixed before approval, not necessarily immediately |
| ⚪ Pebble | Non-blocking, but requires future action |
| ⏳ Sand | Non-blocking, requires future consideration |
| 🌫 Dust | Non-blocking, "take it or leave it" |

Two blocking rungs and three non-blocking rungs. The non-blocking rungs are
distinguished by *what happens later* (a follow-up is owed, worth considering,
or nothing), not by how important the reviewer thinks the point is.

### GitLab, Chromium, GitHub

- **GitLab** [code review guidelines](https://docs.gitlab.com/development/code_review/)
  adopt Conventional Comments ("Use the Conventional Comment format to convey
  intent. Mark non-mandatory suggestions as `(non-blocking:)`"), tell
  reviewers to "communicate which ideas you feel strongly about and those you
  don't", and say "when only non-blocking suggestions remain, move the MR to
  the next stage rather than waiting".
- **Chromium** [Respectful Code Reviews](https://github.com/chromium/chromium/blob/main/docs/cr_respect.md)
  has no severity vocabulary, but: "Find an end" (don't iterate a review
  towards perfection; "if there are bigger refactorings to be done, move them
  to a new CL"), "Don't bikeshed" ("ask yourself if this decision *really*
  matters in the long run"), and "Explain why".
- **GitHub** review states carry a coarse blocking marker for the whole review:
  *Comment* ("general feedback without explicitly approving … or requesting
  additional changes"), *Approve*, *Request changes* ("feedback that must be
  addressed before the pull request can be merged")
  ([GitHub docs](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/reviewing-changes-in-pull-requests/reviewing-proposed-changes-in-a-pull-request)).
  It is per review, not per comment.

### How the conventions line up

| Gates merge | Conventional Comments | Google | Netlify | Current `reviewing-changes` skill |
|---|---|---|---|---|
| Yes, now | `issue (blocking)` / `todo` / `chore` | (unmarked) | Mountain | **blocker** |
| Yes | `… (blocking)` | (unmarked) | Boulder | **should-fix** |
| No, follow-up owed | `… (non-blocking)` | FYI | Pebble | - |
| No, consider | `suggestion (non-blocking)`, `thought` | Optional / Consider | Sand | **consider** |
| No, take or leave | `nitpick`, `(if-minor)` | Nit | Dust | **consider** |
| Not a finding | `question`, `praise`, `note` | FYI | - | (Correctness "question") |

Mapping is the author's reading of the definitions quoted above, not a
published crosswalk. Observations, with no recommendation implied:

- All four conventions agree that the blocking/non-blocking line is the one the
  recipient must be able to see first. Where there are more tiers, they sit
  *below* that line.
- Conventional Comments and Netlify keep "what kind of comment" separate from
  "does it gate"; Google folds both into one prefix.
- None of these sources says anything about ranking *across* reviewers or
  concerns; they rank within one reviewer's comments. The empirical sources
  below also say nothing on whether a global rank helps or hurts.
- `question` is its own type in Conventional Comments. Bosu et al. (below)
  found questions were rated *not useful* by authors, and useful comments were
  those that triggered a change.

## 2. What makes a review comment useful: human reviews

### Bosu, Greiler & Bird, MSR 2015 (Microsoft)

[Characteristics of Useful Code Reviews: An Empirical Study at Microsoft](https://www.microsoft.com/en-us/research/wp-content/uploads/2016/02/bosu2015useful.pdf).
Interviews rating 145 comments, a validated classifier (precision ~89%, recall
~85%), then ~1.5M comments from 190,050 reviews across five projects.

- **Usefulness density** (share of a review's comments the author finds
  useful) was 64-68% in every project. About a third of human review comments
  were not useful.
- Authors rated **functional defects** and validation/corner-case findings
  *useful*. **Nit-picks** (indentation, style, naming, typos) were split
  between "somewhat useful" and "useful" - "resolving nit-picking issues may
  not be essential, but … help long-term project maintenance".
- Rated **not useful**: questions asked to understand the implementation,
  praise, false positives, and "work that needed to occur in the future …
  If immediate actions based on these comments are not foreseeable, authors
  rated such comments as not useful."
- The strongest signal of a useful comment was that it **triggered a change**
  within a line of it; "Won't fix" status was the second. 92% of "Won't fix"
  comments were not useful.
- **Larger changes get less useful comments**: usefulness density falls as the
  number of files grows (Fig. 8); "reviewers may opt for cursory review of
  large changesets". Build and config files got fewer useful comments than
  source.
- Reviewers who had reviewed the file before were roughly twice as useful
  (65-71%) as first-time reviewers (32-37%).

### Other human-review studies

- **Czerwonka, Greiler & Tilford, ICSE 2015** (Microsoft),
  [Code Reviews Do Not Find Bugs](https://www.microsoft.com/en-us/research/wp-content/uploads/2015/05/PID3556473.pdf):
  "Only about 15% of comments provided by reviewers indicate a possible
  defect, much less a blocking defect … feedback related to the long-term code
  maintainability … comprises … at least 50% of all."
- **Beller et al., MSR 2014**,
  [Modern Code Reviews in Open-Source Projects: Which Problems Do They Fix?](http://sback.it/publications/msr2014.pdf):
  a 75:25 ratio of maintainability-related to functional changes, "strikingly
  similar" to earlier industry and academic data; 7-35% of review comments are
  discarded.
- **Rahman, Roy & Kula, MSR 2017**,
  [Predicting Usefulness of Code Review Comments](https://arxiv.org/pdf/1807.04485)
  (1,116 comments, Samsung): 55.5% useful, 44.5% not. Useful comments share
  more vocabulary with the changed code and mention code elements; a
  readability difference was not statistically significant.
- **Turzo & Bosu, EMSE 2024**,
  [What Makes a Code Review Useful to OpenDev Developers?](https://arxiv.org/pdf/2302.11686)
  (2,500 comments, survey of 238): ~77% of comments scored above "not useful";
  only 19% were functional. Usefulness depends on "linguistic characteristics
  such as comprehensibility and politeness" as well as technical content;
  respondents said reviews "must be concise, understandable, and
  explanatory". (Caution: the model's "comment volume" variable is the ratio
  of *source-code comment lines* in the file, not the number of review
  comments; it says nothing about review-comment count.)
- **Sadowski et al., ICSE-SEIP 2018**,
  [Modern Code Review: A Case Study at Google](https://sback.it/publications/icse2018seip.pdf):
  human comments per change rise with change size, "reaching a peak of 12.5
  comments per change for changes of about 1250 lines". Static analysis in
  review lets reviewers avoid "getting distracted by trivial comments (e.g.,
  about formatting)".

## 3. Automated and LLM review: acceptance and noise

| Study | Setting | Comments per PR | Acted on |
|---|---|---|---|
| [Cihan et al., ICSE-SEIP 2025](https://arxiv.org/abs/2412.18531) | Beko, Qodo PR-Agent, 3 projects | bot 3.65 vs human 0.31 | 73.8% "Resolved", 21.3% "Won't fix" (project range 55-90%) |
| [RovoDev, ICSE-SEIP 2026](https://arxiv.org/abs/2601.01129) | Atlassian, 2,000+ repos, 54k comments | 2.1 (after filtering) | 38.7% led to a code change, vs 44.45% for human comments |
| [AutoCommenter, AIware 2024](https://arxiv.org/abs/2405.13565) | Google, best-practice comments | posted on ~1-4% of changed files | ~40% resolution (estimated); "useful ratio" 54% → 66% after suppression, target 80% |
| [Sun et al., arXiv 2025 / TSE](https://arxiv.org/abs/2508.18771) | 16 GitHub Actions, 178 repos, 22k comments | - | AI 0.9-19.2% vs human ~60% |
| [Chowdhury et al., arXiv 2026](https://arxiv.org/abs/2604.03196) | AIDev dataset, 98 closed agent-only PRs | 1-8 | 60.2% of those PRs had <30% "signal"; 12 of 13 agents averaged <60% |

The rates are not comparable across rows: "resolved" in Cihan is an
author-applied label (the authors note it may be applied loosely), RovoDev and
Sun measure subsequent code change, AutoCommenter uses thumbs/“Please fix”
clicks. Chowdhury et al. is a small keyword-classified preprint and the weakest
row. The direction - automated below human - holds in every study that
measures both.

What the sources say about noise specifically:

- **Cihan et al.**: 26.2% of comments were not acted on; "these comments could
  be too trivial, unrelated or not a problem for the context of the pull
  request. In any way, developers spend time on them." Respondents: "makes
  suggestions that fix code blocks that are not in the scope of the task";
  "sometimes the mistakes it thinks it finds are not mistakes at all";
  out-of-scope suggestions "could slow down reviews and create distractions".
  Average PR closure time rose from 5h52m to 8h20m. One respondent warned of
  over-reliance: reviewers may assume "if any other issue exists, the bot would
  have written it".
- **RovoDev** names the failure modes it filters for: comments that are "vague
  (e.g., 'Needs improvement'), nitpicking without context (e.g., 'Add a blank
  line here'), unfocused or off-topic". It runs an LLM-as-judge
  factual-correctness check and a ModernBERT actionability check before
  posting; the actionability check alone improved the acted-on measure by 20
  percentage points.
- **AutoCommenter**: the useful ratio plateaued at 54% against an 80% target.
  A rater study found non-useful comments came from guidelines too broad to
  act on, poor summaries, contested practices, systematic model errors, and
  low-value corrections ("a missing period at the end of a sentence … is often
  allowed by human reviewers"). Suppressing 17 non-actionable guideline
  categories raised the useful ratio to 66% (developer) and 74% (rater);
  further suppression and summary fixes reached 80%. "A simple suppression
  mechanism was sufficient to strongly improve user acceptance to over 80%
  without major sacrifices in efficacy."
- **Sun et al.**: manually triggered comments were addressed more than
  automatic ones (e.g. 12.8% vs 6.8% for one action); "the massive feedback
  resulting from unconditional triggering might reduce developers'
  willingness to respond" (the authors' interpretation, not a measured
  effect).

### Precedent from static analysis

Automated review comments inherit a longer literature on analyser warnings.

- Google's Tricorder holds code-review checks to **under 10% effective false
  positives**, where an effective false positive is any result after which
  "developers did not take some positive action" - including a correct
  warning the developer did not understand or thought unimportant.
  "Before we established clear feedback channels, many developers would just
  ignore analysis results they did not understand." Checks focus on newly
  introduced issues; results carry a "Not useful" button, and "analyzers with
  high 'Not useful' click rates are fixed or disabled"
  ([Software Engineering at Google, ch. 20](https://abseil.io/resources/swe-book/html/ch20.html);
  [Sadowski et al. 2018](https://sback.it/publications/icse2018seip.pdf)).
- Johnson et al., ICSE 2013,
  [Why Don't Software Developers Use Static Analysis Tools to Find Bugs?](https://www.researchgate.net/publication/261192385_Why_don't_software_developers_use_static_analysis_tools_to_find_bugs)
  (20 developer interviews): false positives and how warnings are presented
  are the main barriers.
- Anthropic's own
  [`code-review` plugin](https://github.com/anthropics/claude-code/blob/main/plugins/code-review/commands/code-review.md)
  instructs "If you are not certain an issue is real, do not flag it. False
  positives erode trust and waste reviewer time", excludes "pedantic nitpicks
  that a senior engineer would not flag", and re-validates each finding in a
  separate agent before posting. (Vendor practice, not evidence.)

## 4. Number and length

**Length.** The one large quantitative result: in Sun et al., shorter comments
were more likely to be addressed (text length ρ = −0.24), and comments that
were mostly code - especially multi-line suggested code - much more so (code
ratio ρ = 0.89; addressing rates rose "noticeably when Code Text Ratio exceeded
0.5"). Line/hunk-anchored comments beat file- and PR-level ones (6.5-19.2% vs
0.9-4.2%). Survey and interview sources agree in words: "concise,
understandable, and explanatory" (Turzo & Bosu); warnings should be "easy to
fix" and carry "guidance as to how the issue might indeed be fixed"
(Tricorder). Rahman et al. found no significant readability effect.

**Number.** No source found sets or measures a ceiling on how many comments a
recipient can act on. What exists:

- Humans at Google leave more comments on bigger changes, peaking around 12.5
  per ~1,250-line change; at Beko humans left 0.31 per PR.
- Deployed LLM reviewers settle at 2.1 (RovoDev, after filtering) to 3.65
  (Beko) per PR; AutoCommenter posts on a few percent of changed files.
- Every deployment that improved acceptance did so by posting less - by
  category suppression, confidence thresholds, or actionability filters - and
  reported little loss (AutoCommenter: "without major sacrifices in efficacy").
- Bosu et al. and Turzo & Bosu tie usefulness to *change* size, not to comment
  count.

Anything more specific - "no more than N findings" - would be a design choice,
not something these sources support.

## 5. The dotfiles#324 baseline against these sources

The advisory review on
[dotfiles#324](https://github.com/corygyarmathy/dotfiles/pull/324) posted 18
findings (3 should-fix, 15 consider, plus 1 question) in ~8k characters, under
four axis headings, with no ordering across axes. The operator's triage reply
(read from the PR, counted by hand):

- Acted on: 7 of 18 (~39%) - all 3 should-fix (one partly), 4 of 15 consider.
- Declined with a rebuttal: 11, including all 5 Standards findings (all
  baseline smells, all `consider`).

That acted-on share sits where RovoDev's does (38.7%) and below Bosu et al.'s
human usefulness density (64-68%). One PR is an anecdote, not a measurement.
Every declined finding still cost a written rebuttal, because the skill
requires that "a finding is never dropped silently" - the "developers spend
time on them" cost Cihan et al. describe.

## Gaps

- No controlled study found comparing comment formats (labelled vs unlabelled,
  Conventional Comments vs not) on how fast or how well authors triage. The
  case for labels is practitioner rationale.
- No study found on a per-review ceiling for comment count or total length.
- No study found on AI-reviewed, AI-authored changes triaged by one operator -
  the setting on #102. The closest is Cihan et al.'s over-reliance quote and
  the #102 baseline itself.
- Not checked: Chromium's and Mozilla's internal severity vocabularies beyond
  the public page quoted; Microsoft's internal CodeFlow guidance.
