# afk-agent

An unattended agent that takes work from a GitHub issue tracker, does it, and leaves a pull request for a human to review and merge.

It exists to spend the computer's time instead of the operator's. End-to-end latency of hours is fine. An unreviewable result is not.

## Shape

A job is a persisted state machine attached to one tracker subject. A transition moves it from one state to the next, and a transition is the only thing that ever runs - nothing waits in-process, so a crash, a restart or a host upgrade costs at most one transition rather than a pipeline. Every transition is also invokable by hand:

```
afk run review --pr 12
```

GitHub owns what is to be done. A local store owns how it is being done - leases, attempts, scheduling, idempotency. Neither duplicates the other, and every outbound side-effect carries an idempotency key, which is what makes crash-anywhere-and-resume safe rather than merely survivable.

Work arrives two ways: an eligibility label on an issue puts it in the queue, and a comment command (`/review`, `/revise`) asks for something specific. Labels are otherwise status the agent writes. Nothing merges on the agent's say-so.

## Building

Go, no dependencies:

```
go build ./...
go test ./...
```

The binary is `cmd/afk`. No transition is implemented yet; `afk help` shows the surface they will be invoked through.

## Status

Early. The design is settled and recorded in [ADR 0001](docs/adr/0001-a-go-state-machine-in-its-own-repository.md); the vocabulary it uses is in [CONTEXT.md](CONTEXT.md). It replaces a working bash prototype that ran for several weeks in [corygyarmathy/dotfiles](https://github.com/corygyarmathy/dotfiles), which is where the NixOS module that packages and configures this agent still lives.
