# ADR 0002: Skills are forked into a repository we own, and vendored into each consumer

- **Status:** Proposed
- **Date:** 2026-09-12
- **Related Artefacts:**
    - Source: [`corygyarmathy/skills`](https://github.com/corygyarmathy/skills), the canonical home this decision creates.
    - Derived from: [`mattpocock/skills`](https://github.com/mattpocock/skills), MIT, whose copies seeded it.
    - Also consumed by: `corygyarmathy/dotfiles`, which vendored the originals and is expected to adopt this.
    - Vocabulary: `CONTEXT.md` in this repository.

## Context

The prototype's prompts invoke skills by name and stop if one is missing: a
review that is not the `code-review` skill's, reported as if it were, is worse
than no review, because nothing downstream can tell the difference. This
repository had none, so an agent pointed at it would be a capable model with no
instructions.

The skills that would fill the gap already existed, vendored into
`corygyarmathy/dotfiles` from `mattpocock/skills` and recorded in a
`skills-lock.json` holding each skill's upstream path and a hash of its upstream
content. That file answers one question: has upstream moved away from the copy
here? It is the right question for an installed dependency and the wrong one
here, because these skills are worth editing. Three examples were in hand before
anything was decided. `code-review` tells the user to run a setup skill that was
never installed. `ADR-FORMAT.md` prescribes a one-paragraph ADR with optional
sections, contradicting both `docs/adr/TEMPLATE.md` and the never-amend-an-
accepted-ADR discipline this project actually holds to. `diagnosing-bugs` orders
its feedback-loop ladder around a web application, putting curl and a headless
browser above fuzzing, persisted-state replay and concurrency stress - the three
that a state machine with durable state and a worker pool will actually need.

Under a lock file that measures distance from upstream, each of those fixes
registers as drift. The mechanism is not neutral about whether they happen; it
discourages them.

There is a second problem the first one hides. There are now two repositories
wanting the same skills and there will be more. Whatever answers "who owns this
text" has to also answer "how does the third repository get it", and a copy in
each repository with no common ancestor answers neither.

## Decision

**1. The skills are forked, not installed.** `corygyarmathy/skills` is the
canonical home. Its history begins with copies from `mattpocock/skills`, the MIT
notice travels with them, and the provenance is stated once in that repository's
`NOTICE`. There is no sync back to upstream and no drift measured against it. An
improvement made upstream is adopted by reading it and deciding, which is the
same act as any other edit.

**2. Each consumer keeps a committed copy.** Harnesses discover skills from the
directory they are launched in, so a skill that is not in the repository is not
available to an unattended agent working a fresh clone of it - on a CI runner,
or on a host that has never seen the canonical repository. The copy lives in
`.agents/skills/`, with a symlink per skill in `.claude/skills/` for harnesses
that look there. A symlinked directory *of* skills is not followed, which is why
the links are per-skill.

**3. `skills-lock.json` pins the source commit and hashes the vendored copy.**
The same file name as before and a different question: not "has upstream moved
away from us", but "is what is checked in here still what we pinned". The hash
covers the whole skill directory, since a reference file beside `SKILL.md` is as
load-bearing as the skill body. `verify` reports drift in all three directions -
a vendored copy edited in place, a pinned skill missing, a skill present but
unpinned - and `pull` re-vendors at the pinned commit.

**4. Local divergence is allowed, but not silently.** A consumer may edit its
vendored copy. `verify` will say so, and the next `pull` will overwrite it. The
mechanism has an opinion about visibility and none about whether the two copies
agree, which is the opposite of what it replaces.

**5. The vendoring tool is a Go program in the canonical repository, run
straight from the module.** `go run github.com/corygyarmathy/skills/cmd/vendor-skills@latest`
needs nothing checked in here, which removes a bootstrap problem rather than
solving one: a script has to be present before it can vendor anything, so it has
to be vendored itself, and then it has to detect its own staleness. The tool is
deliberately not pinned by the lock. What must be reproducible is the vendored
content, and that is pinned by commit and checked by hash, so a tool that wrote
the wrong thing is caught by the next `verify` rather than trusted.

**6. Which skills a repository vendors is its own choice.** This one takes
`implement`, `tdd`, `code-review`, `domain-modeling`, `codebase-design` (which
`tdd` invokes by name for the seam vocabulary) and `diagnosing-bugs`. The
fleet-specific skills in `dotfiles` are not brought across.

## Consequences

**Positive**

- Editing a skill to fit how this project actually works is the intended act
  rather than measurable drift, and there is one place to make the edit.
- A third repository is a `pull` against a pinned commit, not a decision about
  where the text lives.
- Every repository is self-contained: a fresh clone on a machine that has never
  seen the canonical repository still has its skills.
- The provenance is stated once, in the repository that holds the code, rather
  than re-derived per consumer from a hash of someone else's file.

**Negative**

- A third repository to maintain, with its own tracker and its own history, for
  what is ultimately a pile of markdown.
- The fork forgoes upstream improvements unless someone goes and reads them. The
  lock file it replaces would at least have said that upstream had moved.
- A vendored copy plus a lock file is machinery. Two consumers do not obviously
  need it, and it will feel like overhead until the third.
- `pull` now needs a Go toolchain where a script needed none. That costs
  nothing in this repository and little in a Nix one, but it is a real
  requirement placed on any future consumer.
- `dotfiles` now carries copies with the old provenance until it adopts this,
  so there is a window in which the same skill has two lock formats in two
  repositories.

## Alternatives considered

- **Keep `skills-lock.json` pointed at `mattpocock/skills`.** No new repository,
  and upstream improvements stay visible. Rejected because it is the mechanism
  that makes the three edits above look like defects, and those edits are the
  point.
- **Let the two repositories diverge outright**, with no shared home and no
  mechanism. Cheapest today. Rejected because the two copies would be equal and
  unrelated, so the third consumer has no non-arbitrary source to copy from, and
  a fix made in one is invisible in the other.
- **`dotfiles` stays canonical, and this repository vendors from it.** No new
  repository, and one home. Rejected because it makes the agent's own repository
  depend on the fleet configuration it was deliberately split out of (ADR 0001),
  for text that has nothing to do with NixOS.
- **Install the skills user-wide via home-manager**, so every repository on the
  fleet sees them with nothing checked in. Zero drift by construction, and no
  vendoring at all. Rejected because it works only on hosts built from that
  configuration: a CI runner, a fresh clone or a container gets no skills, and
  the per-project discovery the prototype's prompts depend on stops being what
  carries them.
- **A shell script, vendored into each consumer.** What this replaced. Rejected
  on both counts that matter: the bootstrap above is pure self-inflicted
  machinery, and the first cut of it shipped a cleanup trap that referenced a
  variable scoped to the function that set it - the class of defect shell is
  good at hiding and that nothing but running it would have caught.
- **A git submodule instead of a vendored copy.** Keeps one copy with a pinned
  commit, which is most of what is wanted. Rejected for the shape of the thing
  being pinned: a submodule brings all of the skills or none, cannot be edited
  locally without a second repository's worth of ceremony, and puts a
  non-obvious clone step in front of an agent that will be working a fresh
  checkout unattended.
