# Where a job's tokens go

Research for [#183](https://github.com/corygyarmathy/afk-agent/issues/183),
part of the map [#182](https://github.com/corygyarmathy/afk-agent/issues/182).
Gathered 2026-10-07.

**Question.** For the `implement`, `review` and `revise` jobs `afk-agent` has
run on `homelab01`, where do the tokens go? Break a job's input and output down
by what produced them, using what opencode records per session. How much is
context the model did not need, and how much is repeated across a review's
sub-agents? Can context-narrowing levers matter here at all?

## What this note could not measure

**No `afk-agent` job's session was read.** The service's opencode store is
under `HOME=/var/lib/afk-agent` (the unit's `Environment=HOME=`). That
directory is `drwx------ afk-agent:afk-agent`, and `sudo` on `homelab01` needs a
password, so this research could not read it. The read-only limit on the host
ruled out any other way in. The service's journal records transitions only
(`review-run reviewing -> posting`), not tokens. The two review comments the
service has posted (dotfiles
[#318](https://github.com/corygyarmathy/dotfiles/pull/318#issuecomment-5969431669),
[#353](https://github.com/corygyarmathy/dotfiles/pull/353#issuecomment-5969824966))
carry an older footer with no token counts. Each says
`≥ $0.17` / `≥ $0.12` "with 3 sub-agents' cost unread", so it doesn't give the
total either.

**What was measured instead** is the same opencode release (1.18.33, the
host's `AFK_OPENCODE`), the same provider, the same skills and the same repos,
run interactively on the operator's workstation. Its store holds four
comparable session trees:

| proxy | session | model | shape |
| --- | --- | --- | --- |
| A | `Reviewing PR #270` (dotfiles) | deepseek-v4-pro | review with 2 sub-agents, then some fixing and a user turn |
| B | `Review PR #153 changes` (afk-agent) | deepseek-v4.1-flash | `reviewing-changes` with 3 sub-agents (spec+standards, correctness, approach), the job's shape |
| C | `Implement issue #146` (afk-agent) | deepseek-v4.1-flash | implement, 179 steps |
| D | `Issue #39 implementation, branch, and PR` (afk-agent) | deepseek-v4.1-flash | implement, 93 steps |

Only A ran on the enrolled `deepseek-v4-pro`. They differ from the jobs in
three ways. A human was in the loop for one or two turns. Review proxies read
the diff with `git diff` where the job reads `.git/afk-pr.diff`. And there is
no `revise` proxy. The mechanisms below come from how opencode and the
DeepSeek API work, so they carry over to the jobs. The percentages are a
sample of four and should be read as ranges, not as the jobs' figures.

To get the real figures, the operator runs this on the host. It is read-only
and opens the store with `mode=ro`:

```bash
sudo nix shell nixpkgs#python3 -c python3 docs/research/job-tokens.py \
  /var/lib/afk-agent/.local/share/opencode/opencode-stable.db --list
sudo nix shell nixpkgs#python3 -c python3 docs/research/job-tokens.py \
  /var/lib/afk-agent/.local/share/opencode/opencode-stable.db <root session id>
```

The store's file name is `opencode-stable.db` on the workstation and was not
seen on the host: `ls` the directory first.

## What opencode records

opencode 1.18.33 keeps everything in one SQLite store
(`~/.local/share/opencode/opencode-stable.db`), in tables `session`, `message`
and `part`, read here with `.schema`:

- **`session`** has the totals: `cost`, `tokens_input`, `tokens_output`,
  `tokens_reasoning`, `tokens_cache_read`, `tokens_cache_write`, plus
  `parent_id`, which makes a sub-agent's session a child of the one whose
  `task` tool call started it.
- **`part`** has each step. A `step-start`/`step-finish` pair brackets one
  model call, and `step-finish` carries that call's `tokens`
  (`input`, `output`, `reasoning`, `cache.read`, `cache.write`) and `cost`. Between
  the two sit the step's `reasoning`, `text` and `tool` parts. A `tool` part
  keeps the call's `input` (file path, command) and its full `output`.
- `opencode export <session>` writes the same thing as JSON. `afk-agent`
  already reads it for the spend footer (`internal/opencode/opencode.go`,
  `subAgents`).

There is no per-tool token count. opencode records characters for each tool
output and tokens for each step.

## Method

A step's prompt is `input + cache.read + cache.write`: the whole context,
re-sent. Context grows from step *k* to *k+1* by what step *k* generated (its
text, tool-call arguments and reasoning) plus what its tools returned.
[`job-tokens.py`](job-tokens.py) takes the growth, subtracts the step's own
output and reasoning, and splits the rest across that step's tool outputs by
character count. A chunk that enters before step *j* of *N* is paid for
*N − j + 1* times. Each chunk is labelled by the tool, and `bash` calls are
classed by their command (`gh`, `git diff`, `cat`/`sed`, `grep`/`find`, `nix`,
`go test` and so on).

Two observations make this work:

- **Reasoning is re-sent.** On steps with almost no tool output, growth equals
  output plus reasoning to within a few tokens (for example 2,708 = 68 out +
  2,626 reasoning + 14). The DeepSeek API requires it: with tool calls, "the
  `reasoning_content` of all previous turns should be passed back to the API …
  If your code does not correctly pass back `reasoning_content`, the API will
  return a 400 error"
  ([DeepSeek, thinking mode](https://api-docs.deepseek.com/guides/thinking_mode)).
- **The model's own output comes back as cache reads. Tool output is fresh
  input once.** A session's uncached input matches its baseline plus its tool
  output, not its whole growth. Priced on that basis at the catalogue's list
  prices, the estimate comes within 2% of the recorded cost on deepseek-v4-pro
  (A: $0.393 against $0.400) and 8–12% under it on flash. The flash cost
  columns are therefore approximate. The token columns are not estimated
  beyond the per-step split.

The prices are from `opencode models opencode-go --verbose`, in USD per
million: deepseek-v4-pro 0.66 in, 1.98 out, **0.022 cache read**;
deepseek-v4.1-flash 0.15 / 0.60 / 0.003. A cache read costs 1/30 to 1/50 of
fresh input. That matters because the Go subscription window is "one
account-wide dollar budget" ([`budget.md`](../agents/budget.md)): list-price
dollars are what count against it, not tokens.

## Findings

### 1. Almost all input is cache reads, so tokens and dollars rank differently

Across the four trees, 97–99% of input tokens are cache reads (A: 5.06M of
5.14M in the parent). Total input is roughly *steps × average context*: C's
179 steps grow the context from 14k to 300k and add up to 36M input tokens. In
dollars, the share is far smaller than the raw count suggests.

### 2. The breakdown

Share of each tree's input tokens, and of its estimated cost:

| origin | A review, v4-pro (tok / $) | B review, flash | C implement, flash | D implement, flash |
| --- | --- | --- | --- | --- |
| baseline: system prompt, tools, AGENTS.md, prompt, skill | 18.6% / 11.9% | 18.9% / 8.0% | 7.1% / 4.5% | 9.6% / 5.8% |
| file reads (`read`, `cat`/`sed`) | 29.7% / 20.0% | 33.6% / 17.1% | 41.6% / 27.3% | 50.9% / 31.9% |
| search and listing (`grep`, `find`, `ls`, glob) | 5.2% / 3.5% | 5.4% / 3.2% | 5.3% / 3.5% | 3.8% / 2.4% |
| diff (`git diff`/`show`/`log`) | 9.4% / 7.7% | 7.3% / 3.7% | 0.4% / 0.3% | 0.7% / 1.4% |
| `gh` | 1.5% / 0.8% | 1.0% / 0.4% | 3.9% / 2.5% | 0.7% / 0.4% |
| builds and tests (`nix`, `go test`) | 0.3% / 0.3% | 0.1% / 0.1% | 0.5% / 0.5% | 0.5% / 0.5% |
| sub-agent results returned | 1.1% / 0.6% | 0.8% / 0.3% | – | – |
| model's own output, re-sent | 8.2% / 2.7% | 5.9% / 1.2% | 15.7% / 7.7% | 9.7% / 3.9% |
| model's reasoning, re-sent | 25.2% / 8.4% | 22.8% / 4.7% | 24.5% / 12.1% | 23.4% / 9.2% |
| other | 0.9% / 0.8% | 4.2% / 2.3% | 0.9% / 0.8% | 0.8% / 0.8% |
| **generating output** | – / 9.7% | – / 9.1% | – / 17.0% | – / 16.2% |
| **generating reasoning** | – / 33.7% | – / 49.8% | – / 23.7% | – / 27.5% |

What the table shows:

- **Reasoning is the largest single cost.** Generating it is 24–50% of a
  tree's dollars, and re-sending it is another 5–12%. Together that is 36–55%.
  Narrowing context does not touch it: the API requires the re-send, and the
  generation is the model thinking. Only fewer steps, or a model or setting
  that thinks less, reduces it.
- **File reads are the largest share of input**: 30–51% of tokens and 17–32%
  of dollars. Adding search and listing gives 35–55% of tokens and 20–34% of
  dollars. This is the only large share that a context-narrowing lever (Jev's
  surgical search, ranged reads) acts on.
- **Tool output from builds, tests and `gh` is negligible**: under 1% of
  dollars for builds and tests, and under 3% for `gh`. The output is short and
  arrives late in the session. This was not seen on a dotfiles `nix flake
  check` failure, whose log could be long. No proxy had one.
- **The baseline is 11–14k tokens per session.** A no-tool "pong" sub-agent
  costs 10,730 input + 1,792 cache read
  ([`internal/opencode/testdata/task-child.json`](../../internal/opencode/testdata/task-child.json)).
  It is 5–12% of dollars, more in short sessions.

### 3. Repetition across a review's sub-agents

- **Each sub-agent pays its own baseline**: about 11–14k tokens, re-sent on
  every step it takes. In B, the three sub-agents' baselines are 41k tokens
  added and 1.65M paid, **11.4% of the tree's input tokens**. In A, the two
  sub-agents' baselines are 5.1%.
- **Files read by more than one session are a small share.** In A, 6 files
  were read in two or three sessions, about 16k tokens more than a single read
  each. In B it was 6 files and about 22k. Each sub-agent reads the diff
  itself: 7–9% of a review tree's input tokens go to diffs. In the job that is
  `.git/afk-pr.diff` read once per sub-agent.
- **The sub-agents are where a review's reasoning is.** In B, the three
  sub-agents generated 144k of the tree's 174k reasoning tokens. A review's
  cost scales with the number of sub-agents mainly through reasoning, not
  through repeated context.

### 4. Context the model did not need

The session record can bound this. It cannot settle it:

- **Re-reading a file already in context** costs 0–7% of a session's input
  tokens (A's parent 3.1%, A's spec sub-agent 5.7%, B's spec+standards 1.8%,
  C 1.0%, D 1.1%).
- **Repeated across sub-agents**: the baselines (5–11%) and duplicate reads
  (about 1%) above.
- **Whether a file read was needed is not recorded anywhere.** The `read` tool
  returns whole files by default, at about 2.4k tokens a read in C and 2.9k in
  D. Deciding how much of a read the model used would need a judgement on each
  read that this research did not make. The ceiling for any lever that narrows
  reads is the file-read and search share: 35–55% of input tokens and 20–34%
  of dollars. A lever that halved what reads return would save something like
  10–17% of a job's dollars, and somewhat more, because a smaller context also
  cuts every later re-send. That number is an inference from the table, not
  something measured.

## Answer for the map

Context-narrowing levers can matter, but they compete with reasoning. On the
enrolled DeepSeek models, 36–55% of a job's list-price dollars are reasoning,
generated and then re-sent as the API requires. Narrowing cannot reach that
part. File reads and search are the largest part narrowing can reach: 35–55%
of input tokens, but only 20–34% of dollars, because 97–99% of input is
cache reads at 1/30 the price of fresh input. Builds, tests and `gh` output
are noise. In a review, each sub-agent's own baseline (about 11% of the tree's
input with three sub-agents) and its own reasoning are the repeated cost. The
repeated file reads are not. These figures come from interactive proxies on
the same opencode, models, skills and repos, not from `afk-agent`'s own
sessions, which were unreadable without root on `homelab01`.
[`job-tokens.py`](job-tokens.py) produces the same breakdown from the
service's store when run there.

## Sources

- opencode 1.18.33's store schema (`.schema session|message|part` on
  `opencode-stable.db`) and its session data, read with `mode=ro`.
- `opencode models opencode-go --verbose` (opencode 1.18.33) for prices.
- [DeepSeek API, thinking mode](https://api-docs.deepseek.com/guides/thinking_mode),
  on passing `reasoning_content` back with tool calls.
- `afk-agent`'s own code and docs:
  [`internal/opencode/opencode.go`](../../internal/opencode/opencode.go)
  (how runs and sub-agent sessions are read),
  [`docs/agents/review.md`](../agents/review.md) (the review's diff file and
  sub-agents), [`docs/agents/budget.md`](../agents/budget.md) (the window is a
  dollar budget), [`docs/agents/spend.md`](../agents/spend.md) (the footer).
- `homelab01`: `systemctl cat afk-agent` (the service's `HOME` and opencode),
  `ls -ld /var/lib/afk-agent` (`drwx------ afk-agent`), and
  `journalctl -u afk-agent` (transitions only).
