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

**Command**:
An instruction from a human to the agent, issued as a pull request comment (`/review`, `/revise`). The only imperative channel: every request for the agent to _do_ something is a command. Labels carry status the agent writes, with one exception - the eligibility label on an issue, which is how work enters the queue at all and which the agent reads.
_Avoid_: Trigger, directive

**Eligibility label**:
The issue label that admits an issue to the agent's queue. The single label the agent reads rather than writes, and a queue filter rather than an instruction: it says this issue _may_ be worked, never that a particular thing should happen to it.
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
