# afk-agent

An unattended agent that takes work from a GitHub issue tracker, does it, and leaves a pull request for a human to review and merge.

It exists to spend the computer's time instead of the operator's. End-to-end latency of hours is fine. An unreviewable result is not.

## Shape

A job is a persisted state machine attached to one tracker subject (ADR 0001 §2). A transition moves it from one state to the next, and a transition is the only thing that ever runs - nothing waits in-process, so a crash, a restart or a host upgrade costs at most one transition rather than a pipeline. Every transition is also invokable by hand (ADR 0001 §4):

```
afk run review --pr 12
```

The pool that runs transitions unattended is `afk work`. It has two independent limits (ADR 0001 §8): how many transitions may execute at once, and how many may hold a given resource token - a named permit for a host constraint, so that eight network-bound reviews can overlap while two builds cannot. Parameters have no defaults in the code; the NixOS module that packages this agent supplies them, and `afk help` lists them.

The budget is observed, never estimated (ADR 0001 §11). Usage is read from the provider's own endpoint, which sees interactive use of the account as well as the agent's; being rate limited defers work to the timestamp the provider gave, and approaching a limit stops new jobs starting without interrupting jobs in flight. `afk budget` reads it by hand.

Two things interrupt the operator, and nothing else does (ADR 0001 §13): a job that failed and came to rest needing a human, and a budget window the provider says is spent. A pull request ready for review, a job handed back and a red CI run are states queried when the operator chooses to look. The happy path is silent. Notifications publish to an ntfy topic supplied as a parameter; without one, nothing is notified.

GitHub owns what is to be done. A local store owns how it is being done - leases, attempts, scheduling, idempotency (ADR 0001 §5). Neither duplicates the other, and every outbound side-effect carries an idempotency key, which is what makes crash-anywhere-and-resume safe rather than merely survivable.

Work arrives two ways: an eligibility label on an issue puts it in the queue, and a comment command (`/review`, `/revise`) asks for something specific. Labels are otherwise status the agent writes. Nothing merges on the agent's say-so.

## Building

Go. One dependency, the job store's SQLite driver, vendored in-tree
([ADR 0004](docs/adr/0004-the-job-store-is-sqlite-the-one-vendored-dependency.md)),
so a clone builds without fetching anything:

```
go build ./...
go test ./...
```

The binary is `cmd/afk`. The runner, the worker pool and the command surface are there; no transition is registered in it yet, so `afk run` will tell you it knows none. `afk help` shows the surface they will be invoked through.

## Status

Early. The design is settled and recorded in [ADR 0001](docs/adr/0001-a-go-state-machine-in-its-own-repository.md); the vocabulary it uses is in [CONTEXT.md](CONTEXT.md). [AGENTS.md](AGENTS.md) is the house style, for a person or an agent working here. It replaces a working bash prototype that ran for several weeks in [corygyarmathy/dotfiles](https://github.com/corygyarmathy/dotfiles), which is where the NixOS module that packages and configures this agent still lives.
