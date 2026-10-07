# What Jev can do through the opencode console

Research for [#184](https://github.com/corygyarmathy/afk-agent/issues/184),
part of the map [#182](https://github.com/corygyarmathy/afk-agent/issues/182).
Gathered 2026-10-07.

**Question.** What is Jev as the opencode console offers it: what it is, how it
is called, and what it costs against the Opencode Go subscription? Which of its
levers (search tools in place of context dumps, model routing, output and
correctness validation) could serve `afk-agent`? For each one, can we get it
with what we already run, and where in a job would it act?

**Lens.** This note collects ideas. It does not recommend adopting anything.
The measure is **operator load** (`CONTEXT.md`), and spend is a constraint on
it.

## Sources and trust

| Source | What it is | Trust |
| --- | --- | --- |
| [opencode console: Models, §Jev](https://opencode.ai/v2/docs/console/models/#jev) | opencode's own page: the endpoint, model ids and prices | High, for what opencode serves and charges |
| [opencode console: Go](https://opencode.ai/v2/docs/console/go/) | The Go model list and its usage limits | High |
| `https://opencode.ai/zen/v1/models`, `https://opencode.ai/zen/go/v1/models`, `https://models.dev/api.json` | Live model lists, fetched 2026-10-07 | High, as of that date |
| [docs.typesafe.ai](https://docs.typesafe.ai/llms.txt): Models, Jev with coding agents, Jev 1.13 jaggedness, Confidence, API, patterns and cookbooks | TypeSafe documents its own model | High for the API's shape and price. Medium for capability: the vendor documents its own limits, which counts in its favour, but it also wrote every result shown |
| [TypeSafe launch post](https://typesafe.ai/blog/introducing-system-one-models-and-jev) | Vendor announcement, 2026-09-15 | Low for performance claims, which the vendor makes about its own product. The post says so itself |
| [dzhng/jevgrep](https://github.com/dzhng/jevgrep) README and `evals/results/` | The most-starred jevgrep (about 2.3k stars). It is a third-party tool, not from TypeSafe, and the repo includes its own evaluation | Medium for how it works. Low for its savings figure: one run of ten tuned tasks |
| eesel.ai, orcarouter, cloudprice and similar write-ups | Marketing summaries | Not used for any claim |

## Short answer

1. **What Jev is.** Jev is a classifier, not a chat model. TypeSafe calls it a
   "System One" model. You send a `state` (text or JSON) and a map of typed
   questions. It returns typed answers: a `noul` (the probability that a yes/no
   statement is true), a `choice` (one option from a set), or a `score` (a
   rating against an ordered rubric). Choice and Score answers also carry a
   probability distribution and a `confidence`. Jev does not generate text,
   write code or call tools
   ([System One](https://docs.typesafe.ai/concepts/system-one.md)). TypeSafe
   says plainly that it "is **not** a drop-in replacement for the LLM behind
   Claude Code, Cursor, opencode" ([Jev with coding
   agents](https://docs.typesafe.ai/introduction/coding-agents.md)).
2. **How it is called.** You send `POST https://opencode.ai/zen/v1/systemone`
   with your Console API key and `"model": "jev-1.13"` or `"jev-1.13-free"`.
   This is not the chat-completions endpoint. opencode's own page lists no AI
   SDK package for it ([console
   Models](https://opencode.ai/v2/docs/console/models/#jev)). So `opencode
   run` cannot use Jev as its model, and the enrolment file cannot name it:
   Jev is missing from `models.dev`, and it is not a `provider/model` that
   opencode runs. The only ways in are an HTTP call from our own code, or a
   tool that a model running in a job calls.
3. **What it costs against Go.** It costs nothing from the Go allowance,
   because Jev is not part of Go. Go's model list does not include it, and
   neither does `zen/go/v1/models` (43 ids, none of them Jev). It is a pay-as-you-go
   Console model: $0.042 per million input tokens, output free.
   `jev-1.13-free` costs nothing "for a limited time" (console Models,
   §Pricing and §Free models). TypeSafe's own price for `jev-1.13.0` is the
   same ([Models](https://docs.typesafe.ai/models.md)). It would draw on the
   Console balance, which `docs/agents/budget.md` does not observe, and which
   the account's monthly limit at the provider bounds (ADR 0001 §12). At list
   price the amounts are tiny. A 25k-token request costs about $0.001, against
   roughly $0.80 for one of our reviews.
4. **The levers, in short.** Each one is a pattern that TypeSafe or a third
   party built around Jev. None is a feature of the console. All three can be
   approximated with what we already run, by asking an enrolled Go model
   instead. What Jev would add is calibrated probabilities, cost low enough to
   ignore, and speed, and none of these has been shown on code-review work.
   The validation lever acts on the reviewer's findings, which is the map's
   first target. It is the best fit for operator load and the one most limited
   by Jev's documented weak spots.

## Jev's constraints that matter here

From [Models](https://docs.typesafe.ai/models.md) and [Jev 1.13
jaggedness](https://docs.typesafe.ai/model-jaggedness/jev-1.13.md), the latter
last reviewed 2026-10-02:

- **Context.** A request holds at most 64k tokens. Within that, the `state`
  plus the longest single question must fit in 32k. A large pull request's diff
  does not fit in one request.
- **Billing.** You pay per input token, and the state is ingested once per
  request, however many questions it carries. TypeSafe's [parallel
  questions](https://docs.typesafe.ai/cookbooks/parallel_questions.md) cookbook
  reports that one batched call was 12.2x cheaper than separate calls.
- **Weak spots, as the vendor lists them:** literal reading, counting and
  arithmetic, comparing dates, **indirection** ("a property of a property or
  something that requires multiple hops of reasoning costs accuracy"), a
  **large state full of irrelevant detail**, adversarial content in the state
  ("does not treat it as hostile by default"), bias towards the first Choice
  option, and generating text. It handles high-level languages better than
  low-level code.
- **Rate limits.** 100k tokens/s and 80 requests/s, "adjusting dynamically" and
  liable to change without notice.
- **Versions.** The `jev-latest` alias moves when a new version ships. Thresholds
  tuned against one version should pin that version.
- **Data.** TypeSafe says Jev is not trained on customer requests. opencode's
  privacy section lists models whose data may be used during a free period.
  `jev-1.13-free` is not on that list, so opencode's default zero-retention
  statement covers it, as far as that page says.

## The levers

### 1. Search tools in place of context dumps

**What it is.** [jevgrep](https://github.com/dzhng/jevgrep) (`jg`) is a
Node CLI with an agent skill. A coding agent asks it a question in plain
language ("Where is authentication checked before a request reaches a
handler?"). Jev judges which folders, files and declarations are relevant, and
`jg` returns file locations and exact source excerpts with line numbers. The
model then reads less to find its place. OpenCode Zen is one of its supported
providers. TypeSafe's own documents cover the same idea from the other side.
The [Line-by-line search](https://docs.typesafe.ai/cookbooks/semantic_find.md)
cookbook scores line ids against a query, and [Re-ranking](https://docs.typesafe.ai/cookbooks/rerank_typesafe.md)
reranks a BM25 shortlist. The jaggedness page also warns *against* the dump,
for Jev itself: a large irrelevant state costs accuracy.

**Evidence.** jevgrep's own evaluation
([total-cost-2026-09-28](https://github.com/dzhng/jevgrep/blob/main/evals/results/total-cost-2026-09-28.md))
ran ten tuned Python SWE-bench tasks with `gpt-5.6-sol`. It solved 8 of 10 both
with and without `jg`, and total cost including Jev was 25.8% lower ($5.66
against $7.62). Jev was about a quarter of that total. The authors call it a
single run each on tuned tasks, "not a holdout or a variance estimate". In a
later release, combined cost was 2-3% *higher* than that run. Read it as
plausible, not as established.

**What we already run.** opencode's built-in `grep` and `glob` tools use
ripgrep ([opencode Tools](https://opencode.ai/docs/tools/)), and LSP is
available behind an experimental flag. So the model in a job can already
search instead of reading everything. The dumps we do make are deliberate and
small. `review-run` writes the diff to `.git/afk-pr.diff` and the description
and linked issues to `.git/afk-pr-spec.md`
([`review.md`](../agents/review.md)). The diff is what the review is about, so
it is not a dump in the sense jevgrep means.

**Where it would act.** It would act mainly in the **implementer**, where an
unfamiliar repository has to be found before it can be changed. It would act
less in the **reviewer**, whose starting point is the diff and which only
reaches outside it for context. Adopting it means a Node tool on `homelab01`
from the NixOS module, and a skill. Under ADR 0002 that skill is vendored from
`corygyarmathy/skills`. A Go-model equivalent needs nothing new, because the
built-in tools are already there. What Jev adds is relevance ranking: finding
code by what it does when there is no symbol to grep for.

**Operator load.** Indirect at best. The claim is about the agent's cost and
the same correctness, not about what the operator reads.

### 2. Model routing

**What it is.** TypeSafe's [intent
routing](https://docs.typesafe.ai/patterns/intent-routing.md) and
[confidence-gated
routing](https://docs.typesafe.ai/patterns/confidence-routing.md) patterns put
a cheap classifier in front of the expensive handlers. A Choice or Score with a
confidence decides which of deterministic code, a specialist LLM or a human
gets the request. A low confidence sends it to the safer path, and riskier
actions need a higher confidence. The [SDE
cascade](https://docs.typesafe.ai/cookbooks/sde_cascade.md) cookbook is the
same idea applied to output: a small model goes first, and a stronger model
runs only if a check fires (lever 3).

**What we already run.** Routing in `afk-agent` is static. A job kind requires
a tier. Within a tier, models are tried in the order the enrolment file gives,
and a model that fails transiently passes the job to the next one. "The agent
never moves a job from one tier to another"
([`model-enrolment.md`](../agents/model-enrolment.md); ADR 0001 §9, §10).
Nothing picks a tier per subject. The closest thing that judges a subject is
the **size signal**, and it asks the operator for a decision rather than
routing.

**Where it would act.** At **tier routing**, before a job runs. A ticket's
text, scored for difficulty or ambiguity with a confidence, could pick
between a cheap and a strong tier. It could also send an unclear ticket back
before any implementer runs, which is lever (i) on the map, "the ticket,
before the agent starts". Choosing a tier per subject would change ADR 0001
§9-10, which makes it a decision rather than a tweak. An enrolled Go model
could make the same classification inside the subscription. What it would
lack is calibrated probabilities: the vendor's argument is that a confidence
an LLM states about itself is overconfident
([Confidence](https://docs.typesafe.ai/confidence.md); launch post).

**Operator load.** It lowers operator load if it stops unready tickets from
becoming pull requests. A routing mistake turns into a weak pull request or a
needless hand-back, which adds load.

### 3. Output and correctness validation

**What it is.** A cheap, independent verifier asks narrow yes/no questions
about another model's output, with "bad" framed as `true`, one question per
item, combined in code with `max`. Code then keeps, drops or escalates each
item ([SDE cascade](https://docs.typesafe.ai/cookbooks/sde_cascade.md),
Appendix A; [Double-checking
citations](https://docs.typesafe.ai/cookbooks/citation_check.md); [Guardrails
for LLMs](https://docs.typesafe.ai/cookbooks/llm_guardrails.md)). The cascade's
cost/quality frontier comes from TypeSafe's internal results on 100 extraction
prompts. It is vendor data, and its chart predates the current price.

**What we already run.** Nothing checks the reviewer's output before it is
posted. `review-post` posts the `reviewing-changes` report as written. The
four-axis report already runs parallel sub-agents
([`review.md`](../agents/review.md)), so a verifying pass is the same kind of
thing as what runs today. It could be one more enrolled-model pass in the
skill, inside the Go window.

**Where it would act.** On the **reviewer**, between `review-run` and
`review-post`. For each finding, ask whether its cited lines are in the diff,
whether the quoted evidence supports its claim, and whether it is about a line
the pull request changed. Findings that fail would be dropped or marked. This
targets "the reviewer's finding quality and evidence" (lever iii), and fewer
unsupported findings is directly less for the operator to read and decide.
The same check fits the **implementer's** pull request description:
does each claim match the diff? Two cautions come from Jev's own documentation:

- The checks it is good at are **grounding** checks: is this claim supported
  by this text? Whether a reported bug is real is multi-hop reasoning about
  code, the "indirection" it says it handles badly. Some grounding checks
  belong in code, not in any model. "Is the cited line in the diff" is a parse,
  and the jaggedness page says to keep what code can compute in code.
- The state limit is 32k tokens, so each finding's question would carry only
  the diff hunk it cites, not the whole pull request. That also suits the
  vendor's advice against irrelevant state.

**Operator load.** This is the closest of the three to the measure. The open
question is whether a Jev-class verifier's probabilities separate real findings
from noise on review findings. That is an empirical question that no source
here answers.

## What I could not determine

- **Whether a Go subscription key reaches `zen/v1/systemone`, and what is
  billed.** opencode's page says to "use your OpenCode Console API key". I did
  not call the endpoint, so I have not confirmed that the operator's key
  works there, or whether `jev-1.13-free` needs a positive Console balance.
- **How long the free variant lasts.** It is "limited time", with no date given.
- **How Jev performs on code review findings, or on anything to do with code
  correctness.** TypeSafe's published workflow evaluations, cookbooks and
  launch post are about extraction, classification, routing and moderation.
  jevgrep measures retrieval for a coding agent, not verification. I found no
  primary source that measures Jev as a verifier of review findings.
- **The size of our pull requests' diffs in tokens.** I did not measure it, so
  I cannot say how often one would exceed the 32k state limit.
- **Independent replication** of any performance number in this note. Every
  figure comes from the vendor, or from a tool author measuring their own tool.
