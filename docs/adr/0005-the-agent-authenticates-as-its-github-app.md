# ADR 0005: The agent authenticates as its GitHub App, and reads who it is from GitHub

- **Status:** Proposed
- **Date:** 2026-09-13
- **Related Artefacts:**
    - Implements: [`corygyarmathy/dotfiles` ADR 0006](https://github.com/corygyarmathy/dotfiles/blob/HEAD/docs/adr/0006-the-runner-is-a-github-app.md) - the runner's identity is a GitHub App. That ADR decides _who_ the agent is; this one decides how the binary holds that identity.
    - Specified by: issue #34, "GitHub App authentication: mint installation tokens, and know the agent's login without GET /user".
    - Constrains: [`corygyarmathy/dotfiles#281`](https://github.com/corygyarmathy/dotfiles/issues/281), the NixOS module, which supplies the App's id and private key.

**Amended 2026-09-26 (git reads as the App too; #65).** The last negative
consequence below said the review's `git` fetch sent no credentials, so a
private repository could not be reviewed, and the same was true of
`implement`'s clone and its reads of the remote's branches. Every `git` process
that reaches the tracker's repository now carries an installation token, as the
push already did: minted as that process starts, and given to it only in its
environment, as an HTTP header scoped to the repository's URL, with the agent
user's global and system `git` configuration shut out. Nothing of it is written
to a file, so a clone leaves nothing of it in the workspace the model reads, and
no token is held across a model run to expire in it. `internal/git`'s `Remote`
is the one place this is done, for every job kind that reaches the repository.

## Context

The agent's first tracker client authenticated with a static bearer token read
once at startup, and learned its own login from `GET /user`. Neither survives
the identity it actually runs as. What the host holds for a GitHub App is a
private key; what the API accepts is an installation token minted from it, and
that token lives one hour. `afk work` runs for days, so a token expiring under a
running process is the normal case rather than an edge. And `GET /user` answers
an installation token with `403 Resource not accessible by integration`
(measured in dotfiles ADR 0006), so the agent could not start.

The login is not incidental. Intake and the review tell the agent's own
comments and 👀 claims from everyone else's by comparing authors against it. A
login that is wrong in either direction breaks the design quietly: the agent's
own review comments read as someone else's, and its claims stop reading as
answers.

Where minting lives, how expiry is noticed and where the login comes from were
left to this repository by #34. They are decisions rather than parameters,
because reversing any of them sends you back to the alternatives below.

## Decision

**1. The agent authenticates as a GitHub App installation, from the App's
private key, and in no other way.** There is no static-token mode beside it. A
transition invoked by hand and one invoked by the pool are the same thing (ADR
0001 §4), and that includes whose name is on what they write.

**2. Installation tokens are minted inside the process, and replaced before
they expire, never by restarting it.** A token is minted when a request first
needs one, reused while it has comfortably more life left than a request takes,
and minted again after that. The check is made on every request against the
expiry GitHub returned, rather than on a timer. A 401 discards the token held,
so a revoked token is not presented again; the request that met it fails and is
not replayed by the client, because whether to try again is the transition's
decision. The timings involved - the JWT's lifetime and backdating, and how
early a token is replaced - are constants in code rather than parameters: the
first two are GitHub's rules for a JWT it will accept, and the third has one
right answer that no operator's choice improves on.

**3. The agent reads its own login from GitHub, as the App: `GET /app`, whose
slug and `[bot]` is the account its comments and reactions carry.** It is not
configured and it is not read from `GET /user`. Read once per process, at
startup, and a failed read is not remembered.

**4. The installation is found from the repository, and a token reaches only
that repository.** The installation id is looked up with the App's JWT rather
than configured, and looked up again if minting reports it gone. Each token is
minted for the one repository the agent is working; the App is installed on
more than one.

**5. A process holds one identity.** Each command builds the tracker - the
client, the App and its login - once, and everything that reaches the tracker
is handed that one. A process therefore holds one token and reads its login
once, however many job kinds it runs.

**6. Secrets arrive as file paths, and are read once at startup.** An argument
is visible in `ps` to every process on the host, and an environment variable in
`/proc` to anything that can read the process; a path is safe in both, and it is
the shape the secret already arrives in, from sops or systemd's
`LoadCredential`. Read once, so a rotated secret is a restart, which is what the
module does when a secret changes. This holds for the App's private key, the
usage API key and the ntfy token alike. The installation tokens of §2 are not
secrets the host is given - they are derived from one - so §6 does not bind
them.

## Consequences

**Positive**

- `afk work` runs past any number of token expiries without a restart, so an
  in-flight review is never killed to renew a credential.
- A leaked installation token is a one-hour problem on one repository.
- Nothing the module supplies can disagree with GitHub about who the agent is or
  where it is installed. The configuration is an id and a key file.
- Adding a job kind costs no new credential handling: it takes the command's
  tracker.
- Signing the JWT is the standard library, so ADR 0004's one-dependency bar
  holds.

**Negative**

- Starting a command that reaches the tracker now needs GitHub reachable, for
  the login, before the first transition runs. A GitHub outage at startup is a
  failed start rather than a pool that waits.
- A revoked token costs the one request that discovers it, and a reinstalled App
  costs two before its new installation is found. Both surface as transient
  failures of whichever transition met them.
- The host clock matters. GitHub refuses a JWT that expires more than ten minutes
  ahead of its own clock, so by that rule a host clock running more than about a
  minute ahead has every mint refused. This follows from GitHub's documented
  rule and was not observed.
- An operator running `afk` by hand needs the App's key, as the unit does. There
  is no personal-token shortcut for a quick look.
- The JWT and token lifecycle is code this repository owns and must keep
  correct, where a static token was a header.
- ~~The review's `git` fetch is untouched by this decision and still sends no
  credentials, so a private repository still cannot be reviewed.~~ No longer
  true: see the amendment of 2026-09-26.

## Alternatives considered

- **Mint outside the binary and restart the unit on a timer.** The binary could
  have kept reading one token at startup. Rejected: it kills whatever
  transition is running roughly hourly, and a review's model run can take most
  of that hour (dotfiles#281), to cover a gap that belongs in the binary.
- **Mint outside the binary into a file, and re-read the file per request.** No
  restarts. Rejected: two schedules that must agree - the timer's and the
  token's - and a live credential kept on disk, while the binary still needs the
  App's key anyway to read its login.
- **A fine-grained PAT on a machine account.** Rejected in dotfiles ADR 0006, for
  the reasons given there.
- **Keep the static token as a second mode, beside the App.** Rejected: two
  authentication paths to test, and a token belonging to another account
  carries a different login, so a hand-run with one would read the App's own
  comments as someone else's commands.
- **The login as a parameter**, which dotfiles ADR 0006 sketched for the
  prototype. Rejected: `GET /app` makes it derivable, and a mistyped value fails
  in the quiet way *Context* describes rather than at startup.
- **The installation id as a parameter.** Rejected: one more value to keep in
  step with GitHub, and a reinstall would need a configuration change and a
  restart where a lookup needs neither.
- **Replace a token only when a request is refused.** Simpler, and it would
  work. Rejected: every hour's first request would fail by design, and the
  failure would land on whichever transition happened to be running.
- **Retry a refused request inside the client.** Rejected: whether a failed
  request is tried again is the transition's decision, and a client that retried
  would do it underneath the runner's idempotency keys.
- **A client per consumer, each with its own App.** What the first cut did.
  Rejected: every job kind added would add a token cache expiring on its own
  schedule and another read of the login.
- **A JWT library.** Rejected against ADR 0004's bar: RS256 signing is a few
  lines of the standard library.
