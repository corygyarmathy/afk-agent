# AFK Agent

An unattended agent that takes work from a GitHub issue tracker, does it, and leaves a pull request for a human to review and merge. It exists to spend the computer's time instead of the operator's: end-to-end latency of hours is acceptable, an unreviewable result is not.

## Language

**Job**:
A durable, resumable piece of agent work attached to exactly one tracker subject - an issue or a pull request. Carries a kind and a persisted state.
_Avoid_: Run, pipeline, session

**Job kind**:
What a job is for: `implement`, `review`, or `revise`. Determines which transitions the job may take and what it requires of a model.
_Avoid_: Lane, phase, mode

**Transition**:
One unit of execution that moves a job from one persisted state to the next. The only thing that runs; a job is never "in progress" between transitions.
_Avoid_: Stage, step, phase

**Claim**:
The tracker-visible marker that a subject has been taken by the agent. Public, durable, and readable by a human in a browser.
_Avoid_: Assignment, lock

**Lease**:
A local, exclusive, expiring hold on a job, held by the process executing a transition. Distinct from a claim: a claim says the work is taken, a lease says a live process is holding it _right now_.
_Avoid_: Lock, claim

**Defer**:
A job rescheduled to an absolute timestamp something else gave, rather than to an interval this agent chose. What waiting for a rate-limit window is: the provider says when it reopens, and the job is due then. The opposite of a park - a deferred job comes back on its own.
_Avoid_: Backoff, sleep, snooze

**Park**:
A job left in its persisted state with nothing scheduled - no lease, no next run - resting until something reschedules it. How a job waits for an operator rather than for time: a due job comes back on its own, a parked job does not. About the scheduling, not about holding: a parked job is one whose lease has been given back _and_ whose re-entry nothing scheduled.
_Avoid_: Stuck, suspended, dropped

**Budget observation**:
One reading of the account's usage, taken from the provider's own endpoint. Account-wide and dollar-denominated, across independent windows, and it sees interactive use as well as the agent's. Never a number this agent accumulated: the agent observes its budget and does not estimate it.
_Avoid_: Ledger, estimate, quota, usage tracking

**Admission**:
Whether the worker pool may start a new job now, decided from a budget observation. Not a gate: a gate is a check one job must pass on its way through the state machine, and admission is about work starting at all. It never interrupts a job already in flight, and it never applies to a hand-invocation.
_Avoid_: Throttle, gate, rate limiting

**Waiver**:
An operator's permission for work to continue through one spent budget window, until that window next resets. Spends the pay-as-you-go balance, and is never granted by the agent itself.
_Avoid_: Override, bypass, failover

**Command**:
An instruction from a human to the agent, issued as a comment on an issue or a pull request (`/implement`, `/review`, `/revise`). The only imperative channel: every request for the agent to _do_ something is a command. Every command has the same shape - a verb, a subject, an author with write access, optional instructions - and is claimed with a 👀 on the comment and answered at most once. Labels carry status the agent writes, with one exception - the eligibility label on an issue, which the agent reads.
_Avoid_: Trigger, directive

**Eligibility label**:
The issue label that opts an issue in to unattended work: the agent may take it with nobody asking, producing the same job a command would. The single label the agent reads rather than writes, and a queue filter rather than an instruction: it says this issue _may_ be worked, never that a particular thing should happen to it. Not needed for work to start - a command starts work on any issue.
_Avoid_: Trigger label, ready label

**Gate**:
A check a job must pass before it may proceed. Distinct from the review, which advises and never blocks.
_Avoid_: Check, validation

**Hand-off**:
The agent declaring a pull request ready for human review. A signal, not a control: nothing merges on it.
_Avoid_: Completion, ready state

**Hand-back**:
The agent declaring it cannot proceed and returning the subject to the human, with what it tried.
_Avoid_: Stuck path, failure

**Resource token**:
A named, capacity-limited permit a transition must hold to run, expressing a host constraint rather than a logical one. A transition that builds holds the heavy-build token; one that calls an API holds nothing.
_Avoid_: Semaphore, slot, concurrency limit

**Enrolled model**:
A model a human has explicitly admitted to a quality tier. The resolver may only ever choose among enrolled models; capability and price are fetched, eligibility never is.
_Avoid_: Available model, configured model

**Tier**:
A quality level a human assigns to enrolled models, and which a job kind requires. The unit in which quality floors are expressed.
_Avoid_: Class, level, grade
