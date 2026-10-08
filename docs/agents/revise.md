# Revise

What `/revise` does, what it needs on the host, and how to run one by hand.
The decisions are [ADR 0001](../adr/0001-a-go-state-machine-in-its-own-repository.md)
§2-§5, §10 and §14, as for implement. The spec is
[#131](https://github.com/corygyarmathy/afk-agent/issues/131) and its
sub-issues #145, #146, #134, #147, #148 and #149, and the submitted-review form
is [#133](https://github.com/corygyarmathy/afk-agent/issues/133). The transitions are
[`internal/revise`](../../internal/revise). The workspace, the relay, the local
gate and its retries, the denylist, the leased push, the replay, the CI watch
and its fixes, and the hand-back on a pull request are
[`internal/work`](../../internal/work); the wait for the review and the
hand-off label are [`internal/handoff`](../../internal/handoff). Both are
shared with [`implement.md`](implement.md).

## What it does

`/revise` on an open pull request, from an account with write access that is
not the agent's, is a **send-back**: the points after the word, on the same
line or below it, in the operator's own words. It comes in two forms:

- **A comment** starting `/revise`. Its points are its body after the word.
- **A submitted review** whose body starts `/revise`. Its points are its body
  after the word, then each of its line comments, in order, with their file
  and line. What the review says (approve, request changes, comment) decides
  nothing. A line comment outside that review is never a point.

Both are claimed with a 👀 on the command itself: on the comment, or on the
review. Each is answered at most once, keyed on its own id. It produces commits on top of
the head the send-back was written against, one reply answering it, an
advisory review of the new head that claims the reply, and then the hand-off
label again, in that order. The agent never merges, and never rewrites what
the operator read.

Every unanswered `/revise` with points, in either form, is part of one
send-back, in the order they were written: a review goes after every comment
written no later than it was submitted. What cannot be revised gets one reply
saying so, and nothing else: a pull request whose branch is not in the
repository (a fork's), a command written while a revision was in flight, a
review written on a commit that is no longer the pull request's head, or with a
line comment written on one, and a command with no points.
A closed pull request's commands are claimed and nothing more.

| transition | from | does |
| --- | --- | --- |
| `revise` | `start` | Reacts 👀 to every unanswered `/revise`, comment or review (the claim), replies to each that cannot be revised, and, when there are points, takes the hand-off label off. |
| `revise-claimed` | `claiming` | Reads the claims, replies and label back, and makes any that are missing again. Once all are there, on to the revision, or rests. |
| `revise-run` | `revising` | Clones the repository, brings in the send-back's head through the relay, and runs one enrolled model of the implement tier on the points. After a gate or CI failure it continues the session that wrote the commits, with the failure, unless that session has [grown too long](implement.md#a-session-too-long-to-continue). Hands back if the branch was deleted, or pushed over, since the send-back. |
| `revise-gate` | `gating` | The agent runs the local gate itself, as implement does. A session that rewrote the head the send-back was written against hands back. |
| `revise-push` | `pushing` | The denylist, then the size and the [sensitive paths](implement.md#sensitive-paths) measured against the base branch's current tip, then the push under a lease pinned to the head the revision was built on. |
| `revise-pushed` | `pushed` | Reads the push back from the remote, and brings the description's sensitive line up to it. Someone else's push during the revision sends it to be replayed. |
| `revise-replay` | `replaying` | The revision's own commits, on top of the other push, gated again and pushed under a lease pinned to it, up to `--replays` times. |
| `revise-watch` | `watching` | Reads CI on the pushed head, as implement does. Red goes back to the revision's session, until `--ci-fixes` runs out. Green owes [the reply](#the-reply). |
| `revise-replied` | `replying` | Reads the reply back, and posts it again under the next key if it is not there. Once it is, on to the review. |
| `revise-review` | `reviewing` | Makes the pull request's `review` job due, and waits for its review of the new head, as implement does. Hands back if someone else pushed, or the review job parked. Rests if the review job handed its review back: that hand-back is the pull request's. |
| `revise-hand-off` | `handing-off` | Applies the hand-off label, and reads it back until it is there. Out of rounds, hands back. |
| `revise-handed-back` | `handing-back` | Reads the hand-back's comment and label back, and makes whichever is missing again. Once both are there, the job rests. |
| `revise-resume` | `deferred` | Tries the tier again from its first model. |

There is no cap on rounds: the next `/revise` after a hand-off is a new
send-back, against the head the operator then read.

## The reply

One comment on the pull request answering every command of the send-back,
posted once CI is green on the revision's final head, and before the advisory
review ([#149](https://github.com/corygyarmathy/afk-agent/issues/149)). The
code is
[`internal/revise/revise_reply.go`](../../internal/revise/revise_reply.go).

- **The agent's part** is a compare link from the head the send-back was
  written against to the new head ("changes since your review"), and one line
  when the pull request is now over `--size-signal`. The size is measured at
  the push ([`implement.md`](implement.md#the-description)); over it is a
  note, never a cut or a hand-back.
- **The session's part** is `.git/afk-reply.md`, under these headings, in
  this order, each left out when it is empty: **Points**, each identified by
  a short quote or, for a line comment, by its link, and each `done` with
  its commit's full SHA or `not done` with one line why; **Suggested
  follow-ups**, owed no answer; **Not verified**. The agent orders them, and
  posts nothing else the session wrote: no narration, no "tests pass", no
  self-rating, and no HTML comment, so no marker.
- **The spend footer** comes last: what the revision's runs cost, CI fixes
  and failed runs included ([`spend.md`](spend.md)). A hand-back carries it
  too.
- **Posted once, and never edited.** It is read back by a marker naming the
  new head, beside one marker for each command it answers. A later `/revise`
  written before the reply or a hand-back is read as written while the
  revision was in flight, and refused.

## The review, and the hand-off

The review is the `review` job the revise job makes due for the new head,
never a `/review` comment (ADR 0001 §14). It reviews the revision's delta
([`review.md`](review.md)). Its request is claimed with a 👀 on the reply,
which the revise job posted, never on the description. The review links the
reply it claimed. The hand-off label goes back on once the review is there.

A **hand-back** is a comment saying what stopped the revision, and the
hand-back label. The pull request stays open, with the branch as the revision
left it. Before the reply, the hand-back lists the points done so far, from the
session's reply file. After the reply, it links the reply, which is never
edited. A review that fails after the
reply is posted is a hand-back too: the review job's own, when its post never
appears, or the revision's, when the review job parks.

A claim, a reply, a hand-back and the label are each read back before the job
moves on ([`internal/owed`](../../internal/owed)), for the reason
[`implement.md`](implement.md#what-it-does) gives.

## What it needs on the host

What implement needs ([`implement.md`](implement.md#what-it-needs-on-the-host)):
git, sh, a commit identity, opencode and the implement tier's credentials, the
GitHub App's permissions, and the heavy-build token, which `revise-run` and
`revise-gate` hold. A send-back issued as a review adds three requests to the
App's: listing a pull request's reviews and one review's line comments over
REST, and the 👀 on a review and reading its reactions over GraphQL. They
need Pull requests: write, which the App already has for implement. Observed
on this repository on 2026-10-02, with tokens minted below the installation's
grants:

| request | accepts | observed |
| --- | --- | --- |
| listing a pull request's reviews | Pull requests: read | served with Metadata only (a public repository) |
| listing one review's line comments | Pull requests: read | served with Metadata only (a public repository) |
| reading a review's reactions (GraphQL) | no header | served with Metadata only (a public repository) |
| the 👀 on a review (GraphQL `addReaction`) | no header | refused (`FORBIDDEN`) with Metadata, Pull requests: read, or Issues: write; accepted with Pull requests: write |

It uses no skill of its own: the prompt is
[`internal/revise/revise.md`](../../internal/revise/revise.md).

The state directory is the directory holding `--store`. Beside the store, a
revision keeps `send-backs/<job>.json` (the head, branch and points the claim
took), `workspaces/<job>` and `relays/<job>.git`, `progress/<job>.json` (the
head read, the head pushed, the session and its reply file, gate attempts,
fixes and replays, the size and sensitive paths at the last push, and what
its sessions have spent),
`owed/<job>.json` (what is being read back) and `notes/<job>.json` (the last
error of a push, a review request or a label). All of it is disposable. Lost
before the push, the revision starts over from the send-back, or is handed
back if the send-back went too. Lost after it, the revision is handed back.

## Parameters

`afk help` lists them, and the NixOS module sets them
([`domain.md`](domain.md)). A revision runs on implement's parameters
([`implement.md`](implement.md#parameters)): the same gate, attempts,
denylist, sensitive paths, size signal, CI bounds, fresh session threshold,
hand-off label and tier.
There is no revise tier of its own. It requires one parameter of its own:

- `--replays` is how many times a revision is replayed onto a push someone
  else made during it before the next such push hands it back. `0` hands back
  at the first.

`afk work` runs revise jobs and answers `/revise` only when implement jobs
run too, and `--replays` is set. Without either it says so on stderr, and
leaves `/revise` unanswered.

## Running one by hand

Every step runs with no daemon present (ADR 0001 §4). With the parameters in
the environment:

```bash
afk run revise          --pr 12   # claim; -> claiming
afk run revise-claimed  --pr 12   # -> revising, once the claims are read back
afk run revise-run      --pr 12   # the model; -> gating
afk run revise-gate     --pr 12   # -> pushing, or back to revising
afk run revise-push     --pr 12   # -> pushed
afk run revise-pushed   --pr 12   # -> watching
afk run revise-watch    --pr 12   # -> replying, once CI is green
afk run revise-replied  --pr 12   # -> reviewing, once the reply is read back
afk run revise-review   --pr 12   # makes review-pr-12 due; run the review, then again
afk run revise-hand-off --pr 12   # -> start, not scheduled: done
```

The review in between is the review job's own transitions, from `afk run
review --pr 12` ([`review.md`](review.md)). A hand-back moves the job to
`handing-back`, and `afk run revise-handed-back --pr 12` rests it once the
comment and the label are both there.
