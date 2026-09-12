# ADR 0001: The agent is a Go state machine in its own repository

- **Status:** Proposed
- **Date:** 2026-09-11
- **Related Artefacts:**
    - Replaces: the bash prototype in `corygyarmathy/dotfiles`, `modules/services/afk-agent/`, whose shape is recorded in that repository's [ADR 0004](https://github.com/corygyarmathy/dotfiles/blob/master/docs/adr/0004-afk-agent-runs-self-hosted-with-a-harness-split.md) and [ADR 0007](https://github.com/corygyarmathy/dotfiles/blob/master/docs/adr/0007-the-pull-request-opens-before-the-review.md). Those decisions are amended, not deleted: they remain the correct record of why the prototype was built the way it was.
    - Packaged by: the NixOS module in `corygyarmathy/dotfiles`, which owns this design's parameters - worker counts, thresholds, intervals, tier membership, budget ceilings. This document states decisions only.
    - Vocabulary: `CONTEXT.md` in this repository.

## Context

A bash prototype ran for several weeks and proved the idea: an unattended runner that claims a ticket, implements it, opens a pull request, watches CI, reviews its own work in a fresh context, and hands off to a human. It measurably removed the operator's cognitive load - no copying output between sessions, no hand-written prompts, no remembering which skill to invoke.

It also surfaced the limits of its own shape. The runner is roughly 3,300 lines of shell across numbered stage files sharing thirty globals, executed as one long linear process per ticket. Three consequences follow from that shape rather than from any individual bug. Unexpected state anywhere in a multi-hour process ends the whole process. A phase cannot be invoked on its own - reviewing a pull request means running the pipeline that produces one. And work state is held entirely in GitHub labels and comments, which cannot express "held by a process that may have died", producing a family of duplicate-side-effect defects that no amount of careful shell fixes.

Meanwhile the budget picture turned out to be different from what the prototype assumed. OpenCode Go's usage endpoint reports one account-wide dollar budget across three independent windows, with no per-model dimension: when the budget is spent, every model is spent at once. Per-model limits exist only as throughput throttles, which produce transient failures rather than exhaustion. A design built to fail over between models on budget grounds would have been solving a problem that does not exist, while missing the one that does.

## Decision

Parameters are excluded here by convention - counts, thresholds, intervals, tier membership, ceilings and label names live in the NixOS module that configures this agent. See [`docs/agents/domain.md`](../agents/domain.md) for why.

**1. The agent is written in Go, in its own repository.** Go for a type system and a compiler that finds a class of error before the program runs unattended at 04:00, and for a concurrency model that makes a supervised worker pool ordinary rather than clever. Its own repository because it has a different cadence from a fleet configuration - millisecond unit tests, its own CI, its own release tags - and because a program that will eventually work on its own tracker needs a tracker of its own. The `dotfiles` repository continues to package and configure it as a NixOS module, consumed as a flake input.

**2. A job is a persisted state machine, and a transition is the unit of execution.** A job is durable, resumable, and attached to exactly one tracker subject. Nothing executes for longer than one transition. This is the decision that replaces the linear pipeline, and every other structural property follows from it: a crash, a restart or a host upgrade costs at most one transition; a transition is testable from a persisted fixture state with no live process; and a transition invoked by hand is the same thing as one invoked by the scheduler.

**3. Waiting is never in-process.** A job that must wait - for CI, for a rate-limit window, for a human - persists its state and schedules re-entry. There is no sleeping process holding a job. Polling remains the trigger, as it was in the prototype; webhooks may later change *when* re-entry fires without changing anything else here.

**4. Every transition is invokable standalone, with no daemon present.** Not a debugging affordance but a structural rule: a transition that cannot be run on its own from its persisted state is badly factored. This is simultaneously the on-demand command surface, the integration test entry point, and the operator's recovery tool.

**5. State ownership is split, and every outbound side-effect carries an idempotency key.** GitHub owns work state - what is to be done, what was done, what a human said. The local store owns run state - leases, attempt counts, scheduling, budget observations, idempotency history. Neither duplicates the other, so there is nothing to reconcile. Recovery is re-deriving intent from GitHub and deduplicating side-effects by key. The alternative to the key is not "slightly more duplicate comments"; it is that crash-anywhere-and-resume cannot be made safe at all.

**6. The local store is disposable and is not backed up.** Everything in it is either re-derivable from GitHub or cheap to lose. The one exception, idempotency history, degrades to a possible duplicate comment rather than to corruption. Stated as a decision rather than an omission because the day someone wants to back it up is the day decision 5 has been violated, and that should be a loud signal rather than a quiet backup job.

**7. A claim and a lease are different things.** A claim is the tracker-visible marker that work is taken: public, durable, no owner, no expiry. A lease is a local, exclusive, expiring hold by the process executing a transition. The prototype conflated them, which is why a dead run's leftovers could be re-picked and replied to twice. One word per concept.

**8. Concurrency is worker parallelism plus resource tokens.** Two independent limits rather than one global serial constraint: how many transitions may execute at once, and named capacity-limited permits a transition must hold to run. Most transitions are network-bound and cheap; a few build or test and can exhaust a host that has no swap. One mechanism expresses both honestly, and extends to a future constraint without a redesign. This supersedes `dotfiles` ADR 0004 §8 for this agent.

**9. Model choice is a pure resolver over enrolled models.** Each job kind declares what it requires - capabilities, a quality tier, a ceiling. Each model is enrolled by a human into a tier. The resolver maps requirements, the fetched capability catalogue and observed budget state to an ordered candidate list, and may only ever choose among enrolled models. Capability data and price are fetched; eligibility never is, so a newly published model cannot become eligible on its own. Pure, because a resolver that needs the network to be tested will not be tested.

**10. Transient failure retries at the next enrolled model in the same tier.** Throughput throttles and provider hiccups are not distinguishable from one another through the harness, and need not be: the response to all of them is the same. Same tier, so the quality floor is never silently crossed. Bounded, then the job is deferred rather than handed back, so a bad few minutes at a provider does not generate work for the operator. Exhausting a tier is a human-facing event.

**11. The budget is observed, never estimated.** Usage is read from the provider's own endpoint, which sees interactive use as well as the agent's. A locally kept ledger of the agent's own spend answers the wrong question and was already removed once from the prototype for that reason; it may inform a notification, never a decision. A rate-limited status in any window defers work; approaching a limit stops new work from starting without interrupting work in flight.

**12. Pay-as-you-go spend is bounded by a vendor-enforced ceiling, not by code.** Whichever control the provider offers - a capped balance, a spending limit - the ceiling is enforced on the provider's side of the boundary, where it is impossible to undercount and where it accounts for interactive use as well as the agent's. Which control implements it is configuration, not a decision. The agent holds no opinion about the ceiling and does not meter against it: an in-agent cap would be an estimate at exactly the point where being wrong costs money, and it would be blind to spend the agent did not make. The consequence accepted knowingly: exhaustion arrives as an indistinguishable transient failure, and is handled as one.

**13. Notification is reserved for what the operator must act on.** Failures requiring human intervention and budget exhaustion. Not a pull request ready for review, not a job handed back, not a red CI run: those are states the operator queries when they choose to look. The happy path is silent by design - notifying on success spends the one resource this agent exists to protect.

**14. A comment command is the only imperative channel.** Commands map to job kinds through a registry, so adding one is an entry and a kind rather than a change to a pipeline. Labels are status the agent writes, with a single exception: the eligibility label on an issue, which is how work enters the queue and is a filter rather than an instruction. One imperative channel means one place where "have I already acted on this?" is answered.

**15. Merge stays a human act, including in this repository.** Inherited from `dotfiles` ADR 0004 §9 and restated because this agent will eventually open pull requests against its own tracker. An agent that can merge to itself can break its own delivery path and then be unable to ship the fix.

## Consequences

**Positive**

- The failure unit shrinks from a multi-hour pipeline to a single transition, which is the literal form of the prototype's central complaint.
- Every transition is testable from a fixture state without a network, a daemon or a model.
- A new command is a registry entry, not a new path through a pipeline.
- Host upgrades and restarts stop being hazards to in-flight work.

**Negative**

- A state machine with durable state and idempotency keys is more machinery than a shell pipeline, and most of that machinery earns its place only under failure. It will feel like overhead for as long as nothing goes wrong.
- Two repositories mean a flake input to bump between "fixed" and "running", adding a hop the single-repository prototype did not have.
- Scheduled re-entry makes "what is happening right now" less immediately visible than a running process was: the answer moves from `systemctl status` to a query against the store.
- The agent cannot help build its own replacement until the prototype's runner is taught to work a second repository, or the work is done by hand.

## Alternatives considered

- **A behaviour-preserving port of the bash runner to Go**, as originally scoped, keeping the existing test harness green throughout. Rejected: it would carry the linear pipeline into the new language, and the linear pipeline is the actual defect. The safety of a like-for-like port is real, but it buys type checking while preserving the shape that produces the failures.
- **Keeping the agent in `dotfiles`.** Simpler by every short-term measure - no flake input, no second CI, no migration. Rejected because the tracker is the work queue: an agent sharing a tracker with the fleet it maintains cannot distinguish its own backlog from its operator's, and the separation is cheapest to establish before there are issues to migrate.
- **A durable job queue where a waiting task waits inside itself.** Familiar, and less machinery than a state machine. Rejected: it reintroduces the long-lived process, and with it the property that a restart during a CI watch loses the work.
- **Separate processes per component**, for genuine fault isolation. Rejected as the wrong trade at this scale: a supervised worker pool over a durable store recovers a panicking transition just as well, for one deployment unit instead of several, on a single host serving a single operator.
- **Per-model budget failover**, as the prototype's operator originally specified. Rejected on evidence rather than on design grounds: the provider's budget has no per-model dimension, so there is no per-model exhaustion to fail over from.
