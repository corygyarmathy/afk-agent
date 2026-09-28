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
A job rescheduled to wait out something outside it, rather than to retry an error. What waiting for a rate-limit window is: the provider says when it reopens, and the job is due then. Where nothing says when - an exhausted tier, or a limit reported with no reset - the job is due after a wait the deployment configured, which is a guess rather than a timestamp. The opposite of a park - a deferred job comes back on its own.
_Avoid_: Backoff, sleep, snooze

**Park**:
A job left in its persisted state with nothing scheduled - no lease, no next run - resting until something reschedules it. How a job waits for an operator rather than for time: a due job comes back on its own, a parked job does not. About the scheduling, not about holding: a parked job is one whose lease has been given back _and_ whose re-entry nothing scheduled.
_Avoid_: Stuck, suspended, dropped

**Stay**:
A transition that ran and decided to leave its job in the same state, scheduled again. Distinct from an attempt: a run that returned an error is an attempt and not a stay - it decided nothing - and a stay is not an attempt, so waiting does not spend the retries an error needs. A job counts both since it entered its state - neither breaks the other's count - and a move to another state starts both again. The count is what picks the candidate model, because the one stay a model-running transition makes is a model that failed transiently; an error that is not the model's, such as the tracker or a clone, leaves the next run on the same candidate.
_Avoid_: Retry, skip, candidate index

**Episode**:
One stretch of a job's model tier staying exhausted: from the first time every candidate in the tier fails transiently until the job next gets past the model or parks. Deferring and resuming to try the tier again are inside it. What the operator is told about once, when it has gone on long enough, rather than once per deferral. Counted by the pool and kept in the store, with whether it has been told, so a restart carries on counting it and does not tell it again.
_Avoid_: Outage, incident, streak

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
An instruction from a human to the agent, issued as a comment on an issue or a pull request (`/implement`, `/review`, `/revise`). The only way a human asks the agent to _do_ something; a comment the agent wrote is never a command. Every command has the same shape - a verb, a subject, an author with write access, optional instructions - and is claimed with a 👀 on the comment and answered at most once. Labels carry status the agent writes, with one exception - the eligibility label on an issue, which the agent reads.
_Avoid_: Trigger, directive

**Request**:
Something asking for a job's work: a command, or another job making this one due, as `implement` does for the `review` of its pull request. Each request is claimed with a 👀 on what asked - the command comment, or what the asking job posted - and the work says which asked. Unattended work taken through the eligibility label is not a request: nobody asked.
_Avoid_: Trigger, invocation

**Eligibility label**:
The issue label that opts an issue in to unattended work: the agent may take it with nobody asking, producing the same job a command would. The single label the agent reads rather than writes, and a queue filter rather than an instruction: it says this issue _may_ be worked, never that a particular thing should happen to it. Not needed for work to start - a command starts work on any issue.
_Avoid_: Trigger label, ready label

**Gate**:
A check a job must pass before it may proceed. Distinct from the advisory review, which advises and never blocks.
_Avoid_: Check, validation

**Size signal**:
The changed non-test lines a pull request may have before its size needs a decision, as the agent counts them on the commits: generated, vendored and lock files and files deleted whole are left out, and tests are counted beside it. Not a gate: crossing it fails nothing, it asks for a decision instead of a silent large pull request. Only a command's own instructions override it.
_Avoid_: Size limit, size gate, PR budget

**Advisory review**:
The agent's report on a pull request: advice to the operator, never a gate, never a decision.
_Avoid_: Review (bare), AI review

**Finding**:
One numbered item in an advisory review, carrying a severity. Advice only: it is owed no answer, and it enters a send-back only when the operator cites it.
_Avoid_: Comment, issue, nit

**Operator's review**:
The operator reading a pull request and deciding it: merge, send back, or close. The only review that decides anything.
_Avoid_: Review (bare), approval

**Send-back**:
An operator's review that ends with points to address, written in the operator's own words. The only instruction `/revise` acts on. It may cite advisory findings one at a time, but it is never an answer to them: the operator owes no reply to any finding, and a reaction, an approval or a resolved thread decides nothing.
_Avoid_: Change request, triage, feedback

**Revision**:
What one `/revise` produces for one send-back: commits added on top of the head the send-back was written against, never rewriting it, and one reply saying which points were done. Does only the send-back's points; anything else it noticed is a suggested follow-up in the reply, owed no answer.
_Avoid_: Round, iteration, fix

**Sensitive path**:
A path the operator has named as deserving closer reading. A pull request that touches one says which, and the operator's review reads those files line by line. One-sided: nothing is ever marked as safe to skim. Named in the operator's configuration, never assessed by a model.
_Avoid_: Risk level, high-risk, critical

**Hand-off**:
The agent declaring a pull request ready for human review. A signal, not a control: nothing merges on it.
_Avoid_: Completion, ready state

**Review queue**:
The pull requests the operator's review is owed or about to be: those handed off and still open, and the `implement` work taken but not yet handed off. A sent-back pull request leaves it while its revision is in flight and rejoins when the revision hands off; a hand-back is never in it. When it is full the agent takes no issue through the eligibility label, and nothing else waits. Not admission: it is not a budget decision, and it never holds a job that already exists.
_Avoid_: Backlog, throttle, inbox

**Hand-back**:
The agent declaring it cannot proceed and returning the subject to the human, with what it tried.
_Avoid_: Stuck path, failure

**Owed**:
What a transition has decided to say on the tracker (a claim, a reply to a command, a hand-back's comment and label) and has not yet seen there. The job does not move on from something owed until the tracker shows it: an owed thing lost is made again, never given up on quietly.
_Avoid_: Pending, outbox, queued

**Effect**:
Something a transition does outside its job's own commit - a push, a pull request, a comment, a reaction, a label, another job made due - performed only after the transition's state change is committed, and under an idempotency key that commit reserved. May be lost between the two; is never made twice under one key.
_Avoid_: Side effect, action, write

**Round**:
One making of an effect, under a key of its own. An effect that never shows up is made again in the next round, a bounded number of times; a key spent on a round is never used again, whether its effect landed, failed or was lost. Distinct from an attempt: a round is counted by the keys an effect has used, an attempt by the runs of a transition that returned an error.
_Avoid_: Retry, try, attempt

**Fix**:
A red CI run sent back to the session that wrote the branch, to be fixed and pushed again. Bounded, and counted once for each head CI failed on.
_Avoid_: CI round, fix round

**Resource token**:
A named, capacity-limited permit a transition must hold to run, expressing a host constraint rather than a logical one. A transition that builds holds the heavy-build token; one that calls an API holds nothing.
_Avoid_: Semaphore, slot, concurrency limit

**Enrolled model**:
A model a human has explicitly admitted to a quality tier. The resolver may only ever choose among enrolled models; capability and price are fetched, eligibility never is.
_Avoid_: Available model, configured model

**Tier**:
A quality level a human assigns to enrolled models, and which a job kind requires. The unit in which quality floors are expressed.
_Avoid_: Class, level, grade
