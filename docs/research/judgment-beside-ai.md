# Keeping a human's judgment engaged beside an AI reviewer

Research for [#104](https://github.com/corygyarmathy/afk-agent/issues/104), part
of the map [#102](https://github.com/corygyarmathy/afk-agent/issues/102).
Gathered 2026-09-27.

**Question.** What is known about automation bias, complacency and skill decay
when a human works beside automated advice? Which techniques demonstrably keep
the human's judgment engaged? What is specific to AI-assisted code review and
programming, and to deliberate practice?

This reports what the sources say. It does not propose a process; that belongs
to the map's later tickets (#109, #110).

**How to read the confidence labels.**

- **Well-evidenced**: a systematic review or meta-analysis, or several
  independent experiments pointing the same way.
- **Moderate**: one or two controlled studies, or consistent findings from a
  narrow setting (lab tasks, laypeople, short sessions).
- **Speculative**: a single small study, an observational result, a survey of
  self-report, or an author's proposal that has not been tested.

**Sources.** Each claim cites the paper it comes from. For every paper marked
"read", I read the abstract or full text myself: the full text for Parasuraman
& Manzey 2010, Buçinca et al. 2021, and Shen & Tamkin 2026, and the abstract for
the others. Papers marked "not read" are cited from memory or from another
paper's account of them, and are labelled as such.

---

## 1. The phenomena

### Automation bias and complacency are robust, and expertise does not prevent them. **Well-evidenced.**

- Parasuraman & Manzey (2010) reviewed the complacency and automation-bias
  literature across aviation, medicine, process control and military command.
  Their summary says automation bias:
  > "(a) can be found in different settings, (b) occurs in both naive and
  > expert (e.g., pilots) participants, (c) seems to depend on the LOA [level
  > of automation] and the overall reliability of an aid, (d) cannot be
  > prevented by training or explicit instructions to verify the
  > recommendations of an aid, (e) seems to be depend on how accountable users
  > of an aid perceive themselves for overall performance, and (f) can affect
  > decision making in individuals as well as in teams."

  They treat complacency and automation bias as overlapping phenomena, with
  **attention** at the centre of both. The human stops sampling the raw
  information and lets the aid stand in for it. They define the two terms
  differently. Complacency is poorer detection of automation failures, and it
  is found mainly under multi-task load. Automation bias is omission and
  commission errors when the aid is wrong. ([read, full text][pm2010])
- Goddard, Roudsari & Wyatt (2012), a systematic review of 74 studies focused
  on clinical decision support, found these factors affect automation bias:
  - **User factors**: cognitive style and task-specific experience.
  - **Attitudes**: trust and confidence in the aid.
  - **Environment**: workload, task complexity and time pressure.

  The mitigators it lists include training, emphasising user accountability,
  where the advice is placed on screen, updated confidence levels, and
  **"the provision of information versus recommendation"**.
  ([read, abstract][goddard2012])
- Lyell & Coiera (2017), a systematic review of 40 studies, found automation
  bias in **single tasks**, not only when people are multitasking. It showed up
  typically in diagnosis rather than monitoring, and in tasks with **high
  verification complexity**. The authors read it as tied to cognitive load, and
  suggest reducing that load as a mitigation. ([read, abstract][lyell2017])
  This matters for code review, which is a single-task, diagnostic,
  hard-to-verify activity.
- Dratsch et al. (2023) had 27 radiologists read mammograms beside a purported
  AI, which gave a wrong category on 12 of 40 cases. Accuracy fell sharply when
  the AI was wrong, at every level of experience:

  | Radiologists | AI correct | AI wrong |
  |---|---|---|
  | Inexperienced | 79.7% | 19.8% |
  | Very experienced | 82.3% | 45.5% |

  Experience reduced the effect but did not remove it. ([read, abstract][dratsch2023])

### Explanations alone do not fix it, and can make it worse. **Well-evidenced** that they don't reliably help; **moderate** on when they do.

- Bansal et al. (2021) found that AI explanations "increased the chance that
  humans will accept the AI's recommendation, regardless of its correctness."
  ([read, abstract][bansal2021])
- Buçinca et al. (2021) cite the same pattern as their motivation. Their
  "simple explainable AI" conditions had people overrelying on wrong AI
  suggestions. ([read, full text][bucinca2021])
- Vasconcelos et al. (2023) ran 5 studies, N=731. They argue that people
  **strategically** decide whether to engage with an explanation, weighing the
  cost of verifying against the cost of relying on the AI. Explanations reduced
  overreliance only when they actually made verification cheaper. Paying people
  for accuracy also reduced overreliance. ([read, abstract][vasconcelos2023])
- Code-specific: in a 2026 controlled experiment, 86 Python programmers judged
  LLM-generated assertions.
  - They judged correct assertions correctly 74% of the time, but incorrect
    ones only 49% of the time, with similar confidence in both.
  - Natural-language explanations gave no overall benefit.
  - Low-quality explanations lowered accuracy *and* raised confidence.

  ([read, abstract][assertions2026]; this is a recent preprint, so treat it as
  **moderate**.)

### The more the automation does, the worse the human does when it fails. **Well-evidenced.**

- Onnasch et al. (2014), a meta-analysis of 18 experiments, found a trade-off
  as the degree of automation rises. Routine performance and workload improve.
  Failure performance and situation awareness get worse. The sharpest cost comes
  when automation crosses from **supporting information analysis** to
  **supporting action selection**. ([read, abstract][onnasch2014])

  In review terms, the analogy is between an aid that points at things and an
  aid that tells you what to do about them. That analogy is mine, not the
  paper's.
- Parasuraman & Manzey describe a **first-failure effect**. Complacency is
  worst the first time a reliable aid fails, and it partly recovers afterwards.
  Complacency is also *greater* when the automation is highly, but not
  perfectly, reliable. ([read, full text][pm2010])

### Skill decay from working beside automation. **Moderate** in medicine and aviation; **moderate to speculative** for programming.

- **Aviation.** Casner et al. (2014) put 16 airline pilots in a 747 simulator.
  Their hand-flying and instrument-scanning skills were "mostly intact". The
  **cognitive** skills of manual flight had degraded: knowing where the aircraft
  is, what comes next, and recognising failures. Those deficits tracked how
  much pilots let their minds wander while automation flew. The authors
  conclude that retaining cognitive skill "may depend on the degree to which
  pilots remain actively engaged in supervising the automation."
  ([read, abstract][casner2014])
- **Medicine.** Budzyń et al. (2025) studied four Polish centres. In
  colonoscopies done *without* AI, the adenoma detection rate fell from 28.4%
  in the 3 months before AI was introduced to 22.4% in the 3 months after
  (−6.0 points, p=0.0089). This is observational and before/after, so it is
  suggestive rather than causal. ([read, abstract][budzyn2025])
- **Programming, learning a new skill.** Shen & Tamkin (2026) ran a
  pre-registered RCT: 52 developers learning the Trio async library, half with
  a GPT-4o chat assistant. ([read, full text][shen2026])
  - **Learning fell.** The AI group scored 17% lower on the follow-up quiz
    (Cohen's d=0.738, p=0.010).
  - **No time saved.** The AI group was not significantly faster.
  - **Debugging suffered most.** The gap was largest on debugging questions.
    The authors attribute this to the control group hitting, and resolving,
    more errors themselves: a median of 3 errors against 1.
  - **Engagement mattered.** In exploratory analysis, three low-scoring usage
    patterns averaged under 40%: delegating the code to the AI, relying on it
    more and more, and having it debug by iteration. Three patterns that kept
    people cognitively engaged scored 65–86%: asking conceptual questions only,
    asking for explanations alongside generated code, and generating code then
    asking follow-up questions to understand it.
  - **Caveats.** The patterns rest on subgroups of 2–7 people. The authors note
    it was a single one-hour task with a chat interface, not an agentic tool,
    and call their result a lower bound on offloading.
- **Learning, education field experiment.** Bastani et al. (2025, PNAS)
  studied about 1,000 high-school students. Plain GPT-4 access improved grades
  during practice by 48%. When access was later removed, those students scored
  **17% worse** than students who never had access. A tutor prompted with
  learning safeguards "largely mitigated" the harm. ([read, abstract][bastani2025])
- **A counterpoint.** Kazemitabaar et al. (2023) ran a study with 69 novices
  aged 10–17. Codex access did not reduce performance on manual
  code-modification tasks, and retention a week later was slightly (not
  significantly) better. ([read, abstract][kazemitabaar2023]) How much
  scaffolding the task gives, and who the learners are, seem to matter.

### People misjudge how much the AI helps, and how critically they are thinking. **Moderate.**

- METR (2025) ran an RCT: 16 experienced open-source developers, 246 tasks in
  their own repositories. AI tools *increased* completion time by 19%.
  Afterwards, the developers believed AI had *reduced* it by 20%.
  ([read, abstract][metr2025]) This is evidence that the operator's own sense
  of value is not a reliable measure.
- Lee et al. (2025, CHI) surveyed 319 knowledge workers. Higher confidence in
  GenAI was associated with *less* critical thinking; higher self-confidence
  with more. This is self-report and correlational, so it is **speculative** as
  a causal claim. ([MSR page][lee2025]; not read beyond the listing)

---

## 2. Techniques, and how well each is supported

### Commit to a judgment before seeing the automated one. **Moderate.** It reliably reduces anchoring. It does not reliably improve accuracy or produce learning.

- **Fogliato et al. (2022).** 19 veterinary radiologists either saw the AI up
  front, or had to register a provisional answer first.
  - Committing first made them **less likely to agree with the AI "regardless
    of whether the advice is accurate."** This means less anchoring, but also
    rejecting correct advice more often.
  - When they did disagree with the AI, they were less likely to ask a colleague.
  - They rated the AI as less useful.
  - Committing first did **not** lengthen time on task.

  ([read, abstract][fogliato2022])
- **Buçinca et al. (2021)**, "update" condition. Participants decided without
  AI, then saw the AI's suggestion and explanation, and could revise. This was
  one of three cognitive forcing designs. As a group, the three significantly
  reduced overreliance on wrong AI suggestions compared with simple
  explainable AI. ([read, full text][bucinca2021]) The other two were:
  - **on demand**: the AI was hidden until the participant clicked to see it;
  - **wait**: a 30-second delay before the AI appeared.
- **Gajos & Mamykina (2022)**, the key caveat for learning. Deciding first, then
  seeing the AI's recommendation and explanation, **improved decisions but
  produced no learning.** Only the condition that gave an **explanation with no
  recommendation**, so that people had to reach the conclusion themselves,
  produced both better decisions and learning gains. ([read, abstract][gajos2022])

### Cognitive forcing functions. **Moderate** for reducing overreliance in the moment; **weak** for training people to debias themselves.

- **Buçinca et al. (2021)**, N=199 on Mechanical Turk, a nutrition task, a
  simulated AI at 75% accuracy. ([read, full text][bucinca2021])
  - **What improved.** Cognitive forcing "significantly reduced overreliance"
    compared with simple explainable AI.
  - **What it did not do.** It did not eliminate overreliance: human+AI teams
    still did worse than the AI alone. It also did not significantly improve
    overall performance, only performance on the cases where the AI was wrong.
  - **Acceptability cost.** People "performed best in conditions that they
    preferred and trusted the least, and that they rated as the most
    difficult."
  - **Unequal benefit.** The gains held mostly for people high in Need for
    Cognition, meaning those who enjoy effortful thinking.
  - **Proposed, not tested.** The authors suggest deploying forcing functions
    adaptively, "only in a small fraction of cases" where they would help most.
    This is their proposal, not a result.
- **GenAI extension.** A 2026 preprint compared forcing functions for reviewing
  AI-written plans in a writing task. An "Assumption" prompt, which asked for
  argument analysis, reduced overreliance most without adding cognitive load.
  Participants *preferred* a different prompt, "WhatIf". ([read, abstract][cffplans2026])
  **Speculative**: single study, preprint.
- **Medicine: forcing as training.** Sherbino et al. ran two studies, 2011 with
  N=56 and 2014 with N=191. Teaching medical students cognitive forcing
  *strategies*, as a metacognitive habit, **failed to reduce diagnostic error.**
  ([2011, read abstract][sherbino2011]; [2014, not read beyond search listing][sherbino2014])
  Croskerry et al. (2013) advocate forcing functions and debiasing in clinical
  reasoning, but their paper is a review of strategies, not evidence that they
  work. ([read, abstract][croskerry2013])

  Across these sources, two things behave differently. **Forcing built into the
  workflow** has some support. **Forcing taught as a habit** has not held up.
  That is my synthesis of the sources, not a claim any one of them makes.

### Stated reasons and accountability. **Moderate** for accountability; **little direct evidence** for "write down your reason" as a standalone step.

- **Skitka, Mosier & Burdick (2000)**, as reported by Parasuraman & Manzey.
  181 non-pilots were told they would have to justify their performance in a
  debriefing. Those held accountable for **overall performance or accuracy**
  made significantly fewer omission and commission errors, and cross-checked
  the automation more. Those held accountable for **speed**, or for a single
  sub-task, did not.

  Parasuraman & Manzey add that this "may be taken as evidence" for
  accountability but is "not fully conclusive", because the effect might come
  from motivation or goal-setting rather than accountability as such.
  ([read, full text of P&M][pm2010]; Skitka not read directly)
- **Goddard et al. (2012)** list emphasising user accountability as a mitigator.
  ([read, abstract][goddard2012])
- **What does not work.** Parasuraman & Manzey report further studies:
  - A **second crew member** did not reduce automation bias.
  - **Training** did not reduce it.
  - **Explicit prompts to verify**, shown alongside the aid's advice, did not
    reduce it.

  ([read, full text][pm2010]) So "please check the AI" instructions and
  two-person rules have evidence *against* them.
- **Justification requirement.** I found no study that isolates a stated-reason
  requirement for accepting or rejecting AI advice, separate from
  accountability. The self-explanation and generation effects from learning
  science are plausibly related, but I did not fetch them. **Speculative** as
  applied here.

### Experiencing failures, and seeding errors. **Moderate** for experiencing failures; **mixed** for seeding as a training or measurement tool.

- **Bahner, Hüper & Manzey (2008)**, as reported by Parasuraman & Manzey. Some
  operators were only *told* the aid could fail. Others were *exposed* to rare
  failures of its diagnoses during training. The exposed group verified more
  and were less complacent. Even so, both groups verified less than the task
  required. On the aid's first wrong diagnosis in the companion study (Bahner,
  Elepfandt et al.), 18 of 24 participants still made a commission error.
  ([read, full text of P&M][pm2010])
- **Prevalence effect.** Wolfe, Horowitz & Kenner (2005, Nature) found that
  when targets are rare, observers "often fail to notice it when it does
  appear." ([read, abstract][wolfe2005]) This is the underlying case for
  raising the effective rate of findings. In practice, it is the reasoning
  behind the next item.
- **Threat Image Projection** in airport X-ray screening overlays fictional
  threats on real bags. It raises the effective threat rate, gives feedback,
  and measures screener performance. It is deployed in several countries.
  Schwaninger's group reports that covert tests "question the benefits of TIP
  for training", and that unrealistic images limit how well it works.
  ([search listing only, not read][tip])
- **Returning control to the human.** Parasuraman, Mouloua & Molloy (1996)
  handed an automated monitoring task back to participants for 10 minutes in
  the middle of a session. Afterwards, participants detected significantly more
  automation failures once the task was automated again. ([read, abstract][pmm1996])

### Sampling and spot-checks. **Little direct evidence.**

- The only related lab evidence I found is what is above: returning control
  periodically, and seeded targets in screening. I found no study of random
  spot-checking of AI output as a way to maintain vigilance or skill.
  Buçinca et al.'s adaptive "small fraction of cases" is an untested proposal.

### Information rather than recommendation. **Moderate.**

- **Gajos & Mamykina (2022).** An explanation with no recommendation gave both
  better decisions and learning. ([read, abstract][gajos2022])
- **Goddard et al. (2012)** list "provision of information versus
  recommendation" as a design mitigator. ([read, abstract][goddard2012])
- **Onnasch et al. (2014).** The largest failure cost appears when automation
  moves from analysis to action selection. ([read, abstract][onnasch2014])

These three point the same way from different directions.

---

## 3. AI-assisted code review specifically

The direct literature is thin and recent. Most of it measures whether AI
comments get acted on, not whether the human reviewer's judgment stays sharp.

- **Assertions.** The 2026 experiment above found that programmers are poor at
  spotting *wrong* AI-generated assertions (49%), are overconfident, and get no
  help from explanations. Its authors conclude "AI assistance may not improve
  the reliability of code comprehension and review." ([read, abstract][assertions2026])
- **WirelessCar field experiment** (2025). Developers compared AI review shown
  up front with AI review available on demand, and generally preferred the
  AI-led, up-front version. That preference depended on how familiar they were
  with the codebase and how severe the PR was. ([read, abstract][wirelesscar2025])

  This measures **preference**. Buçinca et al. found preference moves opposite
  to overreliance, so preference is not evidence of engaged judgment.
- **GitHub Actions study** (2025). Over 22,000 AI review comments in 178
  repositories: comments were more likely to lead to changes when they were
  concise, contained code snippets, were triggered manually, or came from
  hunk-level tools. ([read, abstract][aireviewactions2025]) This measures
  uptake, not correctness or human engagement.
- **AI-to-AI review** (2026). Closed-loop AI-reviews-AI PRs grew by more than
  two orders of magnitude between 2025-Q1 and 2025-Q3. ([read, abstract][aitoai2026])
  This is descriptive, and relevant to the map's baseline finding: on
  dotfiles#324, the operator approved an AI-drafted triage of an AI review.
- **Gaps.** I found no controlled study of a human reviewer's detection rate
  on real PRs with and without an AI reviewer's findings shown, and none of
  commit-first or forcing functions in code review. Transferring the findings
  above to code review is inference.

---

## 4. Deliberate practice

- **Ericsson, Krampe & Tesch-Römer (1993)** define deliberate practice as
  structured activity designed to improve performance, with immediate feedback,
  time for problem-solving, and repetition to refine. It is distinct from mere
  experience or work. ([not read; cited by Ericsson 2008][ericsson1993])
  Ericsson (2008) restates this for medicine and notes that "observed
  performance does not necessarily correlate with greater professional
  experience." ([read, abstract][ericsson2008])
- **Macnamara, Hambrick & Oswald (2014)**, a meta-analysis. Deliberate practice
  explained this share of the variance in performance:

  | Domain | Variance explained |
  |---|---|
  | Games | 26% |
  | Music | 21% |
  | Sports | 18% |
  | Education | 4% |
  | Professions | **under 1%** |

  They conclude deliberate practice is "important, but not as important as has
  been argued." ([read, abstract][macnamara2014]) Ericsson's side disputes how
  deliberate practice was coded in the studies. **Well-evidenced** that the
  professional-domain effect is small or poorly measured. **Contested** on why.
- **What does carry over.** Ericsson's model has specific ingredients:
  - the learner commits to an attempt;
  - they get prompt feedback on it;
  - the task is at the edge of their ability;
  - they repeat it.

  Several findings above echo those ingredients:
  - Shen & Tamkin: resolving errors yourself drove learning.
  - Gajos & Mamykina: reaching the conclusion yourself drove learning.
  - Parasuraman et al. 1996: periodic manual control restored vigilance.
  - Bahner et al.: experiencing failures reduced complacency.

  Drawing that parallel is my synthesis, not a claim in any one source.
  **Speculative** as a design basis.

---

## 5. Summary: well-evidenced vs speculative

| Claim | Confidence |
|---|---|
| Automation bias and complacency occur in experts too; training or instructions to verify do not prevent them | Well-evidenced (P&M 2010; Goddard 2012; Dratsch 2023) |
| Automation bias happens in single, diagnostic, hard-to-verify tasks, not only under multitask load | Well-evidenced (Lyell & Coiera 2017) |
| Explanations attached to recommendations don't reduce overreliance, and can raise acceptance of wrong advice | Well-evidenced (Bansal 2021; Buçinca 2021; code: assertions 2026) |
| Moving automation from "analyse" to "decide" is where failure costs jump | Well-evidenced (Onnasch 2014) |
| Working beside automation erodes the *cognitive* skills that are not exercised | Moderate (Casner 2014; Budzyń 2025 observational; Shen & Tamkin 2026; Bastani 2025) |
| Committing to a provisional judgment first reduces anchoring on the AI at no time cost | Moderate (Fogliato 2022; Buçinca 2021) |
| Committing first does not by itself produce learning; reaching the conclusion without the AI's answer does | Moderate (Gajos & Mamykina 2022, one paper) |
| Cognitive forcing reduces overreliance, but people dislike it, and it helps high-Need-for-Cognition people most | Moderate (Buçinca 2021, lay task) |
| Teaching forcing *strategies* as a habit reduces error | Not supported (Sherbino 2011, 2014) |
| Accountability for overall accuracy (not speed) reduces automation bias | Moderate (Skitka et al. 2000 via P&M; Goddard 2012) |
| A second reviewer, or "please verify" prompts, reduce automation bias | Not supported (Mosier/Skitka via P&M) |
| Experiencing the aid's failures reduces complacency more than being told it can fail | Moderate (Bahner et al. 2008 via P&M) |
| Periodic manual control restores vigilance | Moderate (Parasuraman et al. 1996, one lab study) |
| Rare targets are missed; seeding raises the effective rate | Moderate for the effect (Wolfe 2005); mixed for seeding as training (TIP) |
| Random spot-checks keep judgment engaged | No direct evidence found |
| Requiring a stated reason, as such, keeps judgment engaged | No direct evidence found |
| People's sense of how much AI helps is unreliable | Moderate (METR 2025) |
| Deliberate practice explains much of professional skill | Weak (Macnamara 2014: <1% in professions; contested) |
| Findings transfer to AI-assisted code review | Speculative: little direct code-review evidence exists |

[pm2010]: https://doi.org/10.1177/0018720810376055
[goddard2012]: https://doi.org/10.1136/amiajnl-2011-000089
[lyell2017]: https://doi.org/10.1093/jamia/ocw105
[dratsch2023]: https://doi.org/10.1148/radiol.222176
[bansal2021]: https://arxiv.org/abs/2006.14779
[bucinca2021]: https://doi.org/10.1145/3449287
[vasconcelos2023]: https://arxiv.org/abs/2212.06823
[assertions2026]: https://arxiv.org/abs/2607.08885
[onnasch2014]: https://doi.org/10.1177/0018720813501549
[casner2014]: https://doi.org/10.1177/0018720814535628
[budzyn2025]: https://doi.org/10.1016/S2468-1253(25)00133-5
[shen2026]: https://arxiv.org/abs/2601.20245
[bastani2025]: https://doi.org/10.1073/pnas.2422633122
[kazemitabaar2023]: https://arxiv.org/abs/2302.07427
[metr2025]: https://arxiv.org/abs/2507.09089
[lee2025]: https://www.microsoft.com/en-us/research/publication/the-impact-of-generative-ai-on-critical-thinking-self-reported-reductions-in-cognitive-effort-and-confidence-effects-from-a-survey-of-knowledge-workers/
[fogliato2022]: https://arxiv.org/abs/2205.09696
[gajos2022]: https://arxiv.org/abs/2202.05402
[cffplans2026]: https://arxiv.org/abs/2601.18033
[sherbino2011]: https://doi.org/10.1080/10401334.2011.536897
[sherbino2014]: https://pubmed.ncbi.nlm.nih.gov/24423999/
[croskerry2013]: https://doi.org/10.1136/bmjqs-2012-001713
[wolfe2005]: https://doi.org/10.1038/435439a
[tip]: https://www.researchgate.net/publication/259382300_Threat_Image_Projection_enhancing_performance
[pmm1996]: https://doi.org/10.1518/001872096778827279
[wirelesscar2025]: https://arxiv.org/abs/2505.16339
[aireviewactions2025]: https://arxiv.org/abs/2508.18771
[aitoai2026]: https://arxiv.org/abs/2608.21311
[ericsson1993]: https://doi.org/10.1037/0033-295X.100.3.363
[ericsson2008]: https://doi.org/10.1111/j.1553-2712.2008.00227.x
[macnamara2014]: https://doi.org/10.1177/0956797614535810
