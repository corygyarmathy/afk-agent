# How experienced reviewers review a pull request

Research for [#103](https://github.com/corygyarmathy/afk-agent/issues/103),
part of the map [#102](https://github.com/corygyarmathy/afk-agent/issues/102).
Gathered 2026-09-27.

**Question.** What do established code-review guides and the empirical
literature say a reviewer looks for, in what order, and within what time and
change-size norms? Which findings are well evidenced and which are folklore,
and what is written for reviewers early in their career?

**Lens.** The reader is a soon-to-graduate backend engineer who will review
AI-written pull requests alone. This note reports what the sources say. It
does not design a process; the grilling tickets make those decisions.

## Confidence scale

- **High.** Several independent primary sources agree, at least one of them
  large-scale and empirical.
- **Medium.** One solid empirical source, or several that agree but rest on
  self-report or one organisation.
- **Low.** One source whose method is weak for this claim (vendor data,
  correlation read as cause, an unmeasured number), or practitioner guidance
  with no data behind it.

## Short answer

1. **What reviewers look for.** Google's guide puts design first, then
   functionality, complexity, tests, naming, comments, style, consistency and
   documentation. It asks the reviewer to read every human-written line and to
   look at the change in the context of the whole system [G1]. That list is
   practice, not evidence. The empirical studies measure what review actually
   produces: mostly maintainability ("evolvability") comments. Defect comments
   are a minority, and the defects found are mostly small, local logic errors.
   Deep design or security problems are found rarely [B&B13, M&L09, CGT15].
   *High.*
2. **What drives quality.** Understanding does. Reviewers name understanding
   the change, and above all why it was made, as their main difficulty.
   Reviewers who already know the files write deeper and more useful comments
   [B&B13, BGB15, CGT15, P18]. *High.*
3. **Order.** Google says: read the description and decide whether the change
   should exist, then read the main part and send design comments at once,
   then read the rest in a sensible order [G2]. Empirically, the order matters.
   Files shown later in a review get fewer comments, and a seeded bug is
   missed more often when its file comes last [F22]. Reading the tests first
   changes which problems you find but not how many [S19]. Observed reviewers
   build context first and then inspect [WG25]. *Medium* for the effect of
   order; *low* for any one ideal order.
4. **Size.** Every source says smaller is better. The numbers differ with the
   setting: Google's median change is 24 lines [S18]. Google's guide says about
   100 lines is reasonable and 1000 is usually too large [G3]. The Cisco study
   recommends under 200 and no more than 400 [C06]. Comment usefulness falls
   as the number of files grows [BGB15]; one Microsoft report says the drop
   becomes noticeable at about 20 files [CGT15]. *High* that smaller is better;
   *low* for any exact threshold.
5. **Time and rate.** Google asks for a first response within one business
   day, and says a quick response matters more than a quick finish [G4].
   Measured practice: under an hour to first feedback on small changes at
   Google, a median of about 4 hours for the whole review, and about 3 hours a
   week spent reviewing [S18]. The per-sitting limits come mainly from
   inspection literature and one vendor study: slower than about 300-500 lines
   an hour, and no more than 60-90 minutes a sitting [C06, K&P09]. *Medium*
   that faster reading finds fewer defects; *low* for the exact numbers.
6. **Early-career reviewers.** A reviewer's first comments on unfamiliar code
   are much less useful. About a third are useful the first time, and the rate
   roughly doubles once the reviewer has seen the files before. At Microsoft,
   usefulness rises over a reviewer's first year and then levels off [BGB15,
   CGT15]. The studies recommend keeping new reviewers in review for the
   learning, alongside an experienced reviewer. None of the guides has a
   section written for reviewers early in their career. *Medium-high.*

The rest of this note gives the evidence for each point.

---

## 1. What reviewers look for

### 1.1 The practice guide: Google's engineering-practices review guide

[G1] "What to look for in a code review" lists, in this order: **Design**,
**Functionality**, **Complexity**, **Tests**, **Naming**, **Comments**,
**Style**, **Consistency**, **Documentation**, **Every Line**, **Context**,
**Good Things**. Points that matter for the lens:

- **Functionality** asks whether the change does what the developer intended,
  and whether "what the developer intended [is] good for the users of this
  code". Reviewers should think about edge cases, concurrency and bugs they
  can see by reading. The guide says it is "particularly important" to think
  about functionality when there is "parallel programming … that could
  theoretically cause deadlocks or race conditions".
- **Complexity** asks the reviewer to check "at every level of the CL",
  meaning lines, functions and classes. It warns against over-engineering:
  developers should solve "the problem they know needs to be solved *now*, not
  the problem that the developer speculates *might* need to be solved in the
  future".
- **Tests**: "Tests do not test themselves, and we rarely write tests for our
  tests—a human must ensure that tests are valid". Also ask "will the tests
  actually fail when the code is broken?"
- **Every line**: "don't scan over a human-written class, function, or block
  of code and assume that what's inside of it is okay". If the reviewer cannot
  understand the code, they should ask for it to be made clearer.
- **Style** feedback that isn't mandatory gets the prefix "Nit:".

[G5] "The Standard of Code Review" sets the bar for approval: "reviewers
should favor approving a CL once it is in a state where it definitely improves
the overall code health of the system … even if the CL isn't perfect". It
also says "There is no such thing as 'perfect' code—there is only better
code", and "Technical facts and data overrule opinions and personal
preferences". Design is "almost never a pure style issue or just a personal
preference".

[G6] "How to write code review comments" asks reviewers to label how much a
comment matters: **Nit** (minor; should be done, but won't hugely matter),
**Optional/Consider** (not required), **FYI** (not expected in this change).
It also says "it is the developer's responsibility to fix a CL, not the
reviewer's".

**Evidence status.** This is codified practice from one company. The guide
cites no studies for its list or its ordering. The Google case study [S18]
confirms that Google's review culture works at scale and satisfies
developers, but it does not test this checklist against alternatives.

### 1.2 What reviewers say they want from review

- **Microsoft** [B&B13]: 873 programmers ranked their top motivations.
  "Finding defects" came first for 44% of them and "code improvement" for 39%.
  Alternative solutions, knowledge transfer, team awareness and shared code
  ownership followed. Managers were similar: 44% put finding defects first.
- **Google** [S18] found four themes in interviews: **education, maintaining
  norms, gatekeeping, accident prevention**. Its Finding 1 is that review at
  Google "was introduced … to ensure code readability and maintainability …
  Defect finding is welcomed but not the only focus". Which theme matters most
  depends on who the author and reviewer are to each other (Finding 2).

### 1.3 What review actually produces (well evidenced)

- **Microsoft, 570 comments** [B&B13]: code improvements were the largest
  category (29%). Understanding came second. Defects came fourth (14%), and 65
  of the 78 defect comments were about small logic errors. The authors
  conclude that review "does not result in identifying defects as often as
  project members would like and even more rarely detects deep, subtle, or
  'macro' level issues", and that "Relying on code review in this way for
  quality assurance may be fraught".
- **Microsoft, across the organisation** [CGT15]: "Only about 15% of comments
  provided by reviewers indicate a possible defect, much less a blocking
  defect". Maintainability feedback makes up "at least 50%". Caveat: this is a
  two-page industry talk abstract, not a full paper, and it gives no method.
- **Mäntylä & Lassenius** [M&L09]: across 9 industrial and 23 student reviews,
  about 75% of the defects found did not affect visible functionality. They
  were evolvability defects. (This comes from the abstract; the full text was
  not read.)
- **Google, survey of 44** [S18]: "Only 2 respondents said the comments had
  found a bug".
- **What authors find useful** (Microsoft, 1.5M comments) [BGB15]: authors
  rate comments about functional issues, validation and corner cases as
  useful. Nit-picks count as useful or somewhat useful. Questions asked only
  so the reviewer can understand, praise, and "do it later" remarks are rated
  not useful. About 65% of all comments were useful.

**The trap the studies name.** In [B&B13], a senior developer says: "I've seen
quite a few code reviews where someone commented on formatting while missing
the fact that there were security issues or data model issues". Interviewees
said formatting comments are easier to write, and that reviewers sometimes use
them to avoid doing the harder review.

### 1.4 What reviewers need to know (information needs)

[P18] (900 review threads from OpenStack, Android and Qt, plus interviews)
found seven information needs, listed here from most to least frequent: **N1
suitability of an alternative solution**, **N2 correct understanding**, **N3
rationale**, **N4 code context**, **N5 necessity** (is this part needed?),
**N6 specialised expertise**, **N7 splittable** (should this be separate
changes?). The interviewees ranked importance the same way, except that they
rated "splittable" as important even though it came up rarely. Every
interviewee started from the commit message or the linked ticket, to learn
why the change was made. Questions about necessity tended to come later in a
review.

---

## 2. In what order

### 2.1 Guidance

[G2] "Navigating a CL in review" sets out three steps:

1. **Take a broad view.** "Does the change make sense? Does it have a good
   description?" Decide whether the change should happen at all.
2. **Read the main part.** "Find the file or files that are the 'main' part
   of this CL". If there are major design problems, "send those comments
   immediately", before reviewing the rest.
3. **Read the rest** "in a logical sequence". This is often the order the
   tool shows. Sometimes it helps to "read the tests first".

A second ordering is implied by [G1]: design comes before line-level
concerns, and style is optional (Nit).

### 2.2 Evidence

- **Position bias is real** [F22] (ESEC/FSE 2022, Distinguished Paper). In
  219,476 GitHub pull requests from 138 Java projects, files shown earlier got
  more comments, even after controlling for size and discussion. In a
  controlled experiment with 106 participants, reviewers had "64% lower odds"
  of finding one kind of seeded defect when its file came last rather than
  first. GitHub and Gerrit list files alphabetically by default, so what gets
  scrutinised depends partly on file names.
- **Reading tests first** [S19] (ICSE 2019; 93 developers, over 150
  reviews). Reviewers who read the tests first found the same share of
  production bugs and more test bugs, but fewer maintainability issues in the
  production code. Review time and comment quality did not change. It shifts
  what you find; it does not raise the total.
- **Where reviewers start** [B&B13, observation]. Reviewers who owned the
  files skipped the description and went straight to their files. Those new
  to the code relied on the description, and it helped when it said "what was
  changed and why". Reviewers who started by reading the description closely
  took longer to reach their first comment.
- **How experienced reviewers work** [WG25] (arXiv 2025; 10 experienced
  reviewers, 25 reviews). Review is "opportunistic". It typically begins with
  "a context-building phase, followed by code inspection involving code
  reading, testing, and discussion management". The reviewer compares mental
  models of the expected solution and the ideal one with the actual code. This
  is a preprint with a small sample (only the abstract was read).
- **Author annotations** [C06]. Reviews where the author opened with
  explanatory comments had consistently low defect density. The vendor reads
  this as authors finding their own defects while explaining the change. It
  names the other reading too: annotations may "prime" the reviewer to stop
  looking. The data cannot tell the two apart. This is correlation only.

**Confidence.** *Medium* that order changes what gets found. *Low* for any
particular order being best. Google's "description, then main part, then the
rest" matches the observed context-first pattern [WG25, B&B13], but nobody
has tested it against alternatives.

---

## 3. Change-size norms

| Source | Kind | What it says |
|---|---|---|
| Google eng-practices, "Small CLs" [G3] | Guideline | "100 lines is usually a reasonable size for a CL, and 1000 lines is usually too large, but it's up to the judgment of your reviewer". File spread counts too: "A 200-line change in one file might be okay, but spread across 50 files it would usually be too large". Exceptions: large deletions, and output from an automatic refactoring tool "that you trust completely". |
| Sadowski et al. 2018 (Google, 9M changes) [S18] | Log data | Median change: **24 lines**. Over 35% of changes touch one file, about 90% touch fewer than 10, and over 10% change a single line. Comments per change rise with size, peaking at 12.5 comments around 1250 lines. Past that, changes tend to be generated code or deletions. |
| Rigby & Bird 2013, cited in [S18] | Log data | Medians elsewhere: AMD 44 lines, Lucent 263. Open-source medians run 11-32 lines. |
| Bosu, Greiler & Bird 2015 (Microsoft, 1.5M comments) [BGB15] | Log data + classifier | "The more files that are in a change, the lower the proportion of comments … that will be of value". |
| Czerwonka et al. 2015 (Microsoft) [CGT15] | Industry talk | The drop in usefulness "only starts to be noticeable for reviews with 20 or more changed files". |
| SmartBear/Cisco 2006 [C06] | Vendor case study | "LOC under review should be under 200, not to exceed 400". Defect density was highest below 200 lines. No review over 250 lines produced more than 37 defects per thousand lines. |

**Well evidenced.** Smaller changes get more useful comments per unit of
review, and quicker reviews. Every data source agrees, and so do reviewers'
own reports [P18: "no one will review your code"]. *High.*

**Not well evidenced.** Any exact threshold. Google's 100/1000 is a rule of
thumb. Cisco's 200/400 comes from one vendor study, and it assumes that
defects are spread evenly across lines of code (its footnote 5 says so). The
data is also a random sample of 300 reviews, and the defect-density analysis
has no controls. *Low.*

---

## 4. Time and rate norms

### 4.1 How quickly to respond

- **Google guideline** [G4]: "One business day is the maximum time it should
  take to respond to a code review request". Don't break off focused work to
  review. "It's even more important for the individual responses to come
  quickly than it is for the whole process to happen rapidly". Also "don't
  compromise on the code review standards or quality for an imagined
  improvement in velocity". If a change is too big, ask for it to be split. If
  it can't be split, send design comments first.
- **Measured at Google** [S18]: a median of under an hour to first feedback
  on small changes and about 5 hours on very large ones. The median for the
  whole review is under 4 hours. Over 80% of changes need at most one round of
  addressing comments. 70% are committed within 24 hours of being sent. For
  comparison, [S18] cites medians of 14.7-19.8 hours to approval in Rigby &
  Bird's Microsoft projects and 17.5 hours at AMD. [CGT15] gives about 24
  hours at Microsoft.

### 4.2 Time spent reviewing

- Google: an average of 3.2 hours a week, median 2.6, measured from tool logs
  [S18].
- Microsoft and open source: about 6 hours a week, self-reported (Bosu &
  Carver 2013, cited in [CGT15] and [BGB15]).

### 4.3 Reading rate and length of a sitting

- **SmartBear/Cisco** [C06] concludes: "Inspection rates less than 300
  LOC/hour result in best defect detection. Rates under 500 are still good".
  Also: "Total review time should be less than 60 minutes, not to exceed 90".
  Its headline advice is "review between 100 and 300 lines of code at a time
  and spend 30-60 minutes". It adds: "always spend at least 5 minutes, even on
  a single line of code", because some one-line changes "had ramifications
  throughout the system". Caveats:
  - The rate finding is a correlation, and the data is noisy. The study found
    "no metric that correlated significantly with inspection rate". One
    reviewer's rate had R² = 0.29.
  - The 60-minute limit was **not measured in this study**. The chapter calls
    it a "well-established fact" and points to another essay in the same book.
- **Kemerer & Paulk 2009** [K&P09] (IEEE TSE; 371 and 246 programs from
  Personal Software Process courses). The review rate significantly affects
  how many defects are removed, even after controlling for developer ability.
  About 200 lines an hour or slower was effective, finding nearly two-thirds
  of defects. Caveat: these are course participants reviewing their own code,
  not modern peer review of someone else's change. (This comes from the
  abstract as indexed; the full text was not read.)

**Confidence.** *Medium* that reading faster than a few hundred lines an hour
finds fewer defects. Two independent datasets agree in direction. *Low* for
the specific 60/90-minute and 300/500-line figures.

---

## 5. Early-career reviewers

None of the guides consulted has a section addressed to junior reviewers.
Google's guide is written for all reviewers. Its closest material is on
mentoring: purely educational comments should be marked "Nit" [G5]. The
empirical findings that bear on early-career reviewers:

- **Familiarity with the files dominates** [BGB15]. Reviewers who had
  reviewed a file before were "almost twice more useful (65%-71%) than the
  first time reviewers (32%-37%)". Usefulness kept rising up to about five
  prior reviews of the same file, then levelled off at 70-80%. Having
  previously changed the file helped less (for example 66% to 74% on Azure).
  [CGT15] reports the same curve: 33% useful on first exposure, about 67% by
  the third review, and the long-term average by the fourth.
- **Time in the organisation** [BGB15]. New hires had the lowest usefulness
  in their first three months. It rose most over the first three quarters and
  was "relatively stable after the first year".
- **Why first-time comments score lower** [BGB15]. First-time reviewers "may
  ask questions to understand the implementation, or identify false issues
  based on their incorrect assumption". Authors rate questions and false
  positives as not useful.
- **Studies recommend keeping new reviewers in review** [BGB15]. "We
  recommend including inexperienced reviewers so that they can gain the
  knowledge and experience required", alongside "at least one or two
  experienced reviewers". [B&B13] reports knowledge transfer as a motivation
  that almost every interviewee raised unprompted: "If you do a code review
  and did not learn anything about the area … that was not as good code
  review as it could have been".
- **Understanding unfamiliar code costs more** [B&B13]. 91% of surveyed
  programmers said unfamiliar files take longer to review, and 82% said
  reviewers who know the files give different feedback. That feedback is
  "more likely to find subtle defects, feedback is more conceptual … instead
  of superficial (naming, mechanical style, etc.)". One respondent put it
  this way: "When reviewing a small, unfamiliar change, it is often necessary
  to read through much more code than that being reviewed".
- **Reviewers learn as they review** [S18]. The number of distinct files an
  engineer has seen rises with tenure, and files reviewed make up a large
  share of it. [S18] also reports that authors with under a year at Google
  receive more than twice as many comments per change. That is about authors,
  not reviewers.

**Confidence.** *Medium-high.* The findings are large-scale but come from
Microsoft only, and "useful" is judged by the author through a classifier.
The mechanism behind them (context and familiarity) matches [B&B13] and
[P18].

---

## 6. Well evidenced versus folklore

| Claim | Status | Basis |
|---|---|---|
| Review finds mostly maintainability issues, not defects | **Well evidenced** | [B&B13], [M&L09], [CGT15], [S18] |
| Defects found in review are mostly small, local logic errors; deep design, security and "macro" issues are rarely caught | **Well evidenced** | [B&B13] comment sort and interviews; [BGB15] |
| Understanding the change (and its rationale) is the main difficulty and limits quality | **Well evidenced** | [B&B13], [P18], [BGB15] |
| Reviewers familiar with the code give more useful feedback | **Well evidenced** | [BGB15], [CGT15], [B&B13] |
| Smaller changes are reviewed better and faster | **Well evidenced** | [S18], [BGB15], [C06], [P18] |
| File order in the tool changes what gets scrutinised | **Evidenced** (one strong study) | [F22] |
| Reading faster finds fewer defects | **Evidenced in direction** | [C06], [K&P09] |
| "200-400 LOC per review" | **Weak** | One vendor study [C06]; its own caveats apply |
| "No more than 60-90 minutes per sitting" | **Weak / inherited** | [C06] asserts it and cites another essay; not measured in the Cisco data |
| "200-400 LOC in 60-90 min yields 70-90% defect discovery" | **Folklore** | On SmartBear's marketing page [SB-web], credited to the Cisco study. The Cisco chapter [C06] contains no such figure, and an in-situ study could not measure the share of all defects found. |
| "Two reviewers is optimal" | **Contested** | Rigby & Bird's convergent practice (cited in [S18]); Google's median is one reviewer, and more reviewers there means more comments [S18] |
| "Checklists are the most effective way to eliminate frequently made errors" | **Unsupported here** | SmartBear page, no citation [SB-web]; not tested by any source read for this note |
| Google's "design → functionality → … → style" order is the best order | **Unsupported** | Codified practice [G1, G2]; not tested against alternatives |
| Reading tests first finds more bugs | **False as stated** | [S19]: more test bugs, same production bugs, fewer maintainability findings |

---

## 7. Relevance to the lens (observations, not design)

These points connect the sources to the reader's situation. They are for the
grilling tickets to weigh. They are not recommendations.

- Every source assumes a human author who can answer questions, and gets
  much of its value from the conversation around the change: understanding
  needs [P18], talking in person to explain things [B&B13], authors
  annotating their own changes [C06]. An AI author changes who answers
  "why?". None of the sources studies that.
- The sources agree that what review reliably delivers is maintainability
  and knowledge transfer, while defect finding is limited and shallow. Their
  QA caveat ("may be fraught" [B&B13]) applies with full force to a sole
  reviewer.
- A reviewer new to a codebase starts at the low end of the usefulness curve.
  In the studies, the curve rises quickly with repeated review of the same
  files [BGB15, CGT15].
- Position bias [F22] and the formatting trap [B&B13] are the two named
  failure modes that come from how reviewers pay attention, as opposed to
  what they know.
- Nothing read here predates AI authorship in a way that would make it
  obsolete; it simply does not address AI authorship. I did not search for
  studies of reviewing AI-written changes. That is a gap for this map.

---

## 8. Not checked, and limits

- Kemerer & Paulk [K&P09], Mäntylä & Lassenius [M&L09], McIntosh et al.
  [McI14] and Wurzel Gonçalves et al. [WG25] are cited from abstracts or
  index pages. I did not read their full text.
- [CGT15] is a two-page industry talk abstract. Its numbers have no stated
  method.
- Every "usefulness" figure in [BGB15] comes from a classifier trained on
  author ratings (precision about 89%, recall about 85%).
- All the large quantitative sources come from Google, Microsoft or Cisco
  settings, or from open-source Gerrit projects. None is about a lone
  reviewer.
- No source was consulted on security-specific review, or on how reviewers
  use CI and static-analysis output. Both come up in [S18] and [B&B13] only in
  passing: [S18] says analyser integration lets reviewers "focus on the
  understandability and maintainability of changes, instead of getting
  distracted by trivial comments".
- Related work that exists but was not read: McIntosh et al. 2014 [McI14]
  (low review coverage and participation go with lower quality in Qt, VTK and
  ITK), Baum et al. on change ordering and working memory, and Kononenko et
  al. on how developers judge review quality.

---

## References

- **[G1]** Google, *Engineering Practices: What to look for in a code review.*
  https://google.github.io/eng-practices/review/reviewer/looking-for.html
- **[G2]** Google, *Navigating a CL in review.*
  https://google.github.io/eng-practices/review/reviewer/navigate.html
- **[G3]** Google, *Small CLs.*
  https://google.github.io/eng-practices/review/developer/small-cls.html
- **[G4]** Google, *Speed of Code Reviews.*
  https://google.github.io/eng-practices/review/reviewer/speed.html
- **[G5]** Google, *The Standard of Code Review.*
  https://google.github.io/eng-practices/review/reviewer/standard.html
- **[G6]** Google, *How to write code review comments.*
  https://google.github.io/eng-practices/review/reviewer/comments.html
- **[S18]** Sadowski, Söderberg, Church, Sipko, Bacchelli. *Modern Code
  Review: A Case Study at Google.* ICSE-SEIP 2018.
  https://sback.it/publications/icse2018seip.pdf (doi:10.1145/3183519.3183525)
- **[B&B13]** Bacchelli, Bird. *Expectations, Outcomes, and Challenges of
  Modern Code Review.* ICSE 2013.
  https://www.microsoft.com/en-us/research/wp-content/uploads/2016/02/ICSE202013-codereview.pdf
- **[BGB15]** Bosu, Greiler, Bird. *Characteristics of Useful Code Reviews: An
  Empirical Study at Microsoft.* MSR 2015.
  https://www.microsoft.com/en-us/research/wp-content/uploads/2016/02/bosu2015useful.pdf
- **[CGT15]** Czerwonka, Greiler, Tilford. *Code Reviews Do Not Find Bugs: How
  the Current Code Review Best Practice Slows Us Down.* ICSE-SEIP 2015
  (talk abstract).
  https://www.microsoft.com/en-us/research/wp-content/uploads/2015/05/PID3556473.pdf
- **[C06]** Cohen (SmartBear). *Code Review at Cisco Systems*, chapter of
  *Best Kept Secrets of Peer Code Review*, 2006.
  https://static0.smartbear.co/support/media/resources/cc/book/code-review-cisco-case-study.pdf
- **[SB-web]** SmartBear, *Best Practices for Code Review* (marketing page).
  https://smartbear.com/learn/code-review/best-practices-for-peer-code-review/
- **[P18]** Pascarella, Spadini, Palomba, Bruntink, Bacchelli. *Information
  Needs in Contemporary Code Review.* PACM HCI (CSCW) 2018.
  https://fpalomba.github.io/pdf/Conferencs/C36.pdf (doi:10.1145/3274404)
- **[F22]** Fregnan, Braz, D'Ambros, Çalıklı, Bacchelli. *First Come First
  Served: The Impact of File Position on Code Review.* ESEC/FSE 2022.
  https://arxiv.org/abs/2208.04259
- **[S19]** Spadini et al. *Test-Driven Code Review: An Empirical Study.* ICSE
  2019. https://sback.it/publications/icse2019a.pdf
- **[WG25]** Wurzel Gonçalves, Rani, Storey, Spinellis, Bacchelli. *Code
  Review Comprehension: Reviewing Strategies Seen Through Code Comprehension
  Theories.* arXiv 2503.21455, 2025. https://arxiv.org/abs/2503.21455
- **[K&P09]** Kemerer, Paulk. *The Impact of Design and Code Reviews on
  Software Quality: An Empirical Study Based on PSP Data.* IEEE TSE 35(4),
  2009. https://sites.pitt.edu/~ckemerer/PSP_Data.pdf
- **[M&L09]** Mäntylä, Lassenius. *What Types of Defects Are Really
  Discovered in Code Reviews?* IEEE TSE 35(3), 2009.
  https://doi.org/10.1109/TSE.2008.71
- **[McI14]** McIntosh, Kamei, Adams, Hassan. *The Impact of Code Review
  Coverage and Code Review Participation on Software Quality.* MSR 2014.
  https://doi.org/10.1145/2597073.2597076
