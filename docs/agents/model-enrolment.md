# Model enrolment

The two files model choice reads, and what has to be in them. The decision they
implement is [ADR 0001 §9,
§10](../adr/0001-a-go-state-machine-in-its-own-repository.md); the code is
[`internal/model`](../../internal/model).

## The enrolment file

Written by the NixOS module that configures this agent. It is the whole of
eligibility: a model absent from it is never a candidate, whatever the catalogue
says about it.

```json
{
  "tiers": [
    {
      "name": "planning",
      "models": ["opencode-go/glm-5.3", "anthropic/claude-opus-5"]
    },
    {
      "name": "implementation",
      "models": ["opencode-go/glm-5.3-flash", "opencode-go/deepseek-v4.1-flash"]
    }
  ]
}
```

- `tiers` is an array, and its order is preserved.
- `name` is any non-empty string. Tier names are not known to the code and are
  not ordered; the agent never moves a job from one tier to another.
- `models` is `provider/model`, matching the keys of
  [`https://models.dev/api.json`](https://models.dev/api.json). Model ids may
  contain a slash - `orcarouter/qwen/qwen3.5-plus` is provider `orcarouter`,
  model `qwen/qwen3.5-plus`.
- Order within `models` is the order the agent tries them.

Refused at load, rather than at 04:00:

- no tiers, or a tier with no name
- a tier with no models
- the same tier name twice, or the same model twice within one tier
- a model reference that is not `provider/model`
- any field the schema above does not list

The same model may appear in more than one tier.

## The catalogue cache

`internal/model.Source` keeps the last good copy of
`https://models.dev/api.json` in the state directory, beside the job store and
not inside it. Delete it and the next run fetches again. If upstream is
unreachable or returns something that is not a catalogue, the cached copy is
used and its age is reported; the cache is written only after the response has
parsed.

## Which models the Go subscription covers

The `opencode-go` provider in the catalogue is the OpenCode Go subscription's
endpoint, `https://opencode.ai/zen/go/v1` - the same base as the usage endpoint
in [#5](https://github.com/corygyarmathy/afk-agent/issues/5). Enrolling only
from that provider keeps work on the subscription. Models from other providers
are reached pay-as-you-go and are bounded by the account balance, not by this
agent ([ADR 0001 §12](../adr/0001-a-go-state-machine-in-its-own-repository.md)).

## Where the values live

Tier names, tier membership, the retry bound, the catalogue's maximum age and
each job kind's requirements are parameters. They belong to the NixOS module in
[`corygyarmathy/dotfiles`](https://github.com/corygyarmathy/dotfiles), not to
this repository: [`domain.md`](domain.md).
