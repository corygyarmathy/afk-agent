# ADR 0003: `master` is protected by a ruleset, and nothing bypasses it

- **Status:** Proposed
- **Date:** 2026-09-12
- **Related Artefacts:**
    - Enforces: [ADR 0001](0001-a-go-state-machine-in-its-own-repository.md) §15, merge stays a human act.
    - Requires: the `go ci` job in [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml) (#7).
    - Reference and procedures: [`docs/agents/branch-protection.md`](../agents/branch-protection.md).
    - Precedent: the `protect-main` ruleset in `corygyarmathy/dotfiles`, and its ADR 0001.

## Context

`master` was unprotected. ADR 0001 §15 says merge stays a human act, including
in this repository, and §1 anticipates this agent eventually opening pull
requests against its own tracker. Neither was enforced by anything: a push to
`master` would have been accepted, from the operator's credential or from the
token the agent will hold.

The agent is the reason this is urgent rather than tidy. It is an unattended
process with a write credential against the repository that defines it, and ADR
0001 §15 records why that matters - an agent that can merge to itself can break
its own delivery path and then be unable to ship the fix. The protection has to
hold against a non-human actor that will be running at 03:00 with no one
watching, which is a different requirement from protecting a human from a typo.

Two things constrain the shape. There is one human, so any rule that requires a
second person is a rule that blocks every merge. And the gate worth requiring
now exists: #7 put `gofmt`, `go build`, `go vet` and the offline test run behind
a single status check.

`dotfiles` has had protected-master-plus-gate since its own ADR 0001, with no
bypass, and has since learned things about the mechanism that are worth
inheriting rather than rediscovering.

## Decision

**1. Enforcement is a repository ruleset, not legacy branch protection.** One
ruleset named `protect-master`, over `~DEFAULT_BRANCH` and `refs/heads/master`.
Same mechanism as `dotfiles`, so what is learned about rulesets on one
repository applies to the other.

**2. Nothing reaches `master` except through a pull request, and the pull
request must be green.** The `go ci` context is required, with the strict policy,
so a branch merges only after it has been up to date with `master` and passed
there. Force-pushes and branch deletion are refused outright.

**3. Zero required approving reviews.** The pull request is required; an approval
on it is not. GitHub refuses an author's approval of their own pull request, so
on a one-human repository a required approval adds no second reader - it makes
every merge impossible and the only way through is to weaken the rule, which is
the habit this ADR exists to prevent. What the requirement buys is that every
change arrives as a reviewable diff with a green gate, and that the merge is an
act a human performs deliberately.

**4. There are no bypass actors, and the operator is not an exception.** The list
is empty, so the API reports `current_user_can_bypass: never` for the operator,
for repository-admin rights, and for every token. A bypass that exists is a
bypass that gets used at 23:00, and a bypass granted to the operator is in
practice granted to anything holding the operator's credential - which on this
repository will include the agent. The cost is accepted: the only way past the
rule is to disable the ruleset in the web UI and re-enable it afterwards, as a
deliberate and visible act.

**5. The required status check is a job name, so the two are a pair.** The
context is the `name:` of the job in `ci.yml`. Renaming the job blocks every
merge, silently, with no bypass to get out from under it, until the ruleset is
updated to match. The job name is deliberately short and dull for this reason.

**6. This configuration is repository state, and the repository cannot hold
it.** It lives on GitHub; it is not a parameter of the NixOS module that
configures the agent (ADR 0001, *Decision*), because it is not the agent's
behaviour being configured. This ADR is the decision and
[`docs/agents/branch-protection.md`](../agents/branch-protection.md) is the
reference and the procedures. There is no third copy and no file to apply.

## Consequences

**Positive**

- ADR 0001 §15 is enforced rather than stated, against the agent's credential as
  much as the operator's.
- The agent can be given a write token and pointed at this tracker without the
  one configuration worth refusing outright - an agent opening pull requests into
  an unprotected default branch.
- Every change to `master` has a diff, a gate result and a deliberate human
  merge, which is also what makes the commit history worth reading.
- The recovery path has friction proportional to its risk, and friction is
  visible in the ruleset's enforcement status rather than invisible in a flag on
  someone's push.

**Negative**

- Nothing mechanically requires a second reader, so review quality rests on the
  operator, and the rule cannot tell a considered merge from a rubber stamp.
- The recovery path for a wedged `master` requires disabling the ruleset, which
  means there is a window where `master` is unprotected and a step that is easy
  to forget. `dotfiles` has already recorded forgetting it as the real risk in
  that procedure.
- A required check that is a job name is a cross-repository naming dependency
  with a silent, total failure mode.
- The ruleset API's `PUT` replaces a whole ruleset rather than patching it, so an
  automated edit is a rewrite. This is why the procedure prefers the UI, and it
  means the configuration is read back rather than managed.

## Alternatives considered

- **Legacy branch protection** (`branches/master/protection`). Rejected: the
  older mechanism, it does not compose when a second rule is eventually needed,
  and `dotfiles` is on rulesets - one mechanism across both repositories is worth
  more than either choice on its merits.
- **The operator as a bypass actor.** The obvious convenience, and the reason
  this ADR has a §4. Rejected because the agent will hold a credential that acts
  as the repository owner, so "the operator may bypass" is not a rule about a
  human - a fine-grained token acts as the owner, which is how `dotfiles`
  discovered the same thing from the other direction in its ADR 0005.
- **One required approving review.** Rejected on mechanics rather than
  principle: GitHub will not accept the author's own approval, so with one human
  it blocks every merge. If a second reviewer ever exists, this is the first
  thing to revisit.
- **`require_last_push_approval`, or `CODEOWNERS`.** Both need a reviewer who is
  not the author to be useful, so they reduce to the option above.
- **Rely on the agent's token scope instead of a rule.** A token without merge
  rights cannot merge, so the protection would be unnecessary. Rejected because
  it puts the invariant in a credential's configuration rather than the
  repository's, where it is invisible to anyone reading this repository,
  re-granted by accident the next time a token is rotated, and silently absent
  for the operator's own credential.
