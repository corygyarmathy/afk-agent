package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/notify"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// This file is where configuration enters the binary, and it holds no defaults
// on purpose.
//
// Counts, intervals and thresholds belong to the NixOS module that configures
// this agent rather than to the code (AGENTS.md, ADR 0001), and a default is a
// value chosen here - the fact that it can be overridden does not make it
// anyone else's decision. So every parameter is a flag with an environment
// fallback and no default: the module sets the environment once in the unit,
// and the operator's hand-run inherits the same values rather than a second set
// invented in Go.
//
// The exception is a policy that is safe to be absent. There is no retry
// interval unless one is configured, and a failed job then parks rather than
// retrying on an interval this file picked.
const paramUsage = `Parameters have no defaults. Each is a flag or the environment variable beside it,
and the NixOS module that packages this agent sets them:

  --store <path>        AFK_STORE         the job store file            (required)
  --lease <duration>    AFK_LEASE         how long a lease is held      (required)
  --retry <duration>    AFK_RETRY         when a failed job re-enters
  --max-attempts <n>    AFK_MAX_ATTEMPTS  attempts before a job parks

afk work also takes:

  --workers <n>         AFK_WORKERS       transitions executing at once (required)
  --poll <duration>     AFK_POLL          wait when nothing is due      (required)
  --token-wait <dur>    AFK_TOKEN_WAIT    wait for a resource token     (required)
  --token <name>=<n>    AFK_TOKENS        resource token capacity, repeatable

Budget observation, for afk work and afk budget:

  --budget-key <path>   AFK_BUDGET_KEY    file holding the usage API key
  --budget-age <dur>    AFK_BUDGET_AGE    how long an observation is reused
                                          (required by afk work)
  --budget-at <pct>     AFK_BUDGET_AT     percentage that stops new jobs

Notification, for afk work:

  --notify-url <url>    AFK_NOTIFY_URL    ntfy topic to publish to
  --notify-key <path>   AFK_NOTIFY_KEY    file holding the ntfy token

Command intake, for afk intake and afk work:

  --repo <owner/name>   AFK_REPO          repository commands are read from
  --tracker-key <path>  AFK_TRACKER_KEY   file holding the GitHub token

Without --repo afk work reads no commands, and runs only the jobs already in the
store.

Without --retry and --max-attempts a failed job parks: it keeps its state, is
scheduled for nothing, and waits for an operator.

Without --budget-key there is no admission control: work runs into the
provider's limits and they arrive as transient failures.

Without --notify-url nothing is notified. Two conditions reach the operator when
it is set - a job that parked after a failure, and a budget window the provider
says is spent - and nothing else does.`

// params collects the configuration flags, before they are resolved against the
// environment.
type params struct {
	store       string
	lease       string
	retry       string
	maxAttempts string

	workers   string
	poll      string
	tokenWait string
	tokens    tokenCapacity

	budgetKey string
	budgetAge string
	budgetAt  string

	notifyURL string
	notifyKey string

	repo       string
	trackerKey string
}

func (p *params) bindStore(fs *flag.FlagSet) {
	fs.StringVar(&p.store, "store", "", "path to the job store (AFK_STORE)")
}

func (p *params) bindLease(fs *flag.FlagSet) {
	fs.StringVar(&p.lease, "lease", "", "how long a lease is held (AFK_LEASE)")
}

func (p *params) bindBackoff(fs *flag.FlagSet) {
	fs.StringVar(&p.retry, "retry", "", "when a failed job re-enters (AFK_RETRY)")
	fs.StringVar(&p.maxAttempts, "max-attempts", "", "attempts before a job parks (AFK_MAX_ATTEMPTS)")
}

// bindBudget binds budget observation.
//
// The key is a path to a file rather than the key itself. Every other parameter
// here is a flag or an environment variable and this one is neither: an
// argument is visible in `ps` to every process on the host, and an environment
// variable is visible in /proc to anything that can read the process. A path is
// safe in both, and it is also the shape the secret already arrives in - sops
// writes a file, and systemd's LoadCredential hands over a path.
func (p *params) bindBudget(fs *flag.FlagSet) {
	fs.StringVar(&p.budgetKey, "budget-key", "", "file holding the usage API key (AFK_BUDGET_KEY)")
	fs.StringVar(&p.budgetAge, "budget-age", "", "how long an observation is reused (AFK_BUDGET_AGE)")
	fs.StringVar(&p.budgetAt, "budget-at", "", "percentage of a window that stops new jobs (AFK_BUDGET_AT)")
}

// bindNotify binds the operator's interrupt channel.
//
// The token is a path for the same reason the usage key is: an argument is
// visible in `ps` and an environment variable in /proc, and the secret already
// arrives as a file from sops or systemd's LoadCredential.
func (p *params) bindNotify(fs *flag.FlagSet) {
	fs.StringVar(&p.notifyURL, "notify-url", "", "ntfy topic to publish to (AFK_NOTIFY_URL)")
	fs.StringVar(&p.notifyKey, "notify-key", "", "file holding the ntfy token (AFK_NOTIFY_KEY)")
}

func (p *params) bindPool(fs *flag.FlagSet) {
	fs.StringVar(&p.workers, "workers", "", "transitions executing at once (AFK_WORKERS)")
	fs.StringVar(&p.poll, "poll", "", "wait when nothing is due (AFK_POLL)")
	fs.StringVar(&p.tokenWait, "token-wait", "", "wait for a resource token (AFK_TOKEN_WAIT)")
	p.tokens = tokenCapacity{}
	fs.Var(p.tokens, "token", "resource token capacity as <name>=<n>, repeatable (AFK_TOKENS)")
}

// required resolves a parameter from its flag, then its environment variable,
// and fails naming both.
func required(value, flagName, env string) (string, error) {
	if v := optional(value, env); v != "" {
		return v, nil
	}
	return "", usagef("--%s is required (or set %s)", flagName, env)
}

// optional resolves a parameter that may legitimately be absent.
func optional(value, env string) string {
	if value != "" {
		return value
	}
	return os.Getenv(env)
}

func duration(value, flagName string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, usagef("--%s: %q is not a duration", flagName, value)
	}
	if d <= 0 {
		return 0, usagef("--%s: %s is not a positive duration", flagName, d)
	}
	return d, nil
}

func count(value, flagName string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0, usagef("--%s: %q is not a positive whole number", flagName, value)
	}
	return n, nil
}

func (p *params) storePath() (string, error) {
	return required(p.store, "store", "AFK_STORE")
}

func (p *params) leaseTTL() (time.Duration, error) {
	v, err := required(p.lease, "lease", "AFK_LEASE")
	if err != nil {
		return 0, err
	}
	return duration(v, "lease")
}

// backoff builds the retry policy, or returns nil for "there is none".
//
// The two parameters go together: an interval with no bound retries forever,
// and a bound with no interval has nothing to wait. Requiring both is how a
// half-configured policy fails at the command line rather than at 04:00.
func (p *params) backoff() (transition.Backoff, error) {
	retry := optional(p.retry, "AFK_RETRY")
	attempts := optional(p.maxAttempts, "AFK_MAX_ATTEMPTS")
	switch {
	case retry == "" && attempts == "":
		return nil, nil
	case retry == "":
		return nil, usagef("--max-attempts needs --retry: there is no interval to re-enter on")
	case attempts == "":
		return nil, usagef("--retry needs --max-attempts: a retry with no bound never stops")
	}

	d, err := duration(retry, "retry")
	if err != nil {
		return nil, err
	}
	limit, err := count(attempts, "max-attempts")
	if err != nil {
		return nil, err
	}
	return func(n int) (time.Time, bool) {
		if n >= limit {
			return time.Time{}, false
		}
		return time.Now().Add(d), true
	}, nil
}

func (p *params) poolConfig() (workers int, poll, tokenWait time.Duration, capacity map[string]int, err error) {
	v, err := required(p.workers, "workers", "AFK_WORKERS")
	if err != nil {
		return 0, 0, 0, nil, err
	}
	if workers, err = count(v, "workers"); err != nil {
		return 0, 0, 0, nil, err
	}

	if v, err = required(p.poll, "poll", "AFK_POLL"); err != nil {
		return 0, 0, 0, nil, err
	}
	if poll, err = duration(v, "poll"); err != nil {
		return 0, 0, 0, nil, err
	}

	if v, err = required(p.tokenWait, "token-wait", "AFK_TOKEN_WAIT"); err != nil {
		return 0, 0, 0, nil, err
	}
	if tokenWait, err = duration(v, "token-wait"); err != nil {
		return 0, 0, 0, nil, err
	}

	capacity = map[string]int{}
	for name, n := range p.tokens {
		capacity[name] = n
	}
	if len(capacity) == 0 {
		for _, pair := range strings.Split(os.Getenv("AFK_TOKENS"), ",") {
			if pair = strings.TrimSpace(pair); pair == "" {
				continue
			}
			name, n, perr := parseToken(pair)
			if perr != nil {
				return 0, 0, 0, nil, usagef("AFK_TOKENS: %v", perr)
			}
			capacity[name] = n
		}
	}
	return workers, poll, tokenWait, capacity, nil
}

// tokenCapacity is the repeatable --token flag: `--token heavy-build=1`.
type tokenCapacity map[string]int

func (t tokenCapacity) String() string {
	pairs := make([]string, 0, len(t))
	for name, n := range t {
		pairs = append(pairs, fmt.Sprintf("%s=%d", name, n))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func (t tokenCapacity) Set(v string) error {
	name, n, err := parseToken(v)
	if err != nil {
		return err
	}
	if _, dup := t[name]; dup {
		return fmt.Errorf("resource token %q given twice", name)
	}
	t[name] = n
	return nil
}

func parseToken(v string) (string, int, error) {
	name, capacity, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return "", 0, fmt.Errorf("%q is not <name>=<capacity>", v)
	}
	n, err := strconv.Atoi(capacity)
	if err != nil || n < 1 {
		return "", 0, fmt.Errorf("resource token %q: %q is not a positive whole number", name, capacity)
	}
	return name, n, nil
}

// percent resolves a percentage parameter. Bounded at both ends: a threshold
// above 100 never fires, and one at or below zero is "no threshold" spelled as
// a number, which is a way to leave admission with nothing to stop new work
// short of an actual limit without meaning to. Absence is how it is turned off.
func percent(value, flagName string) (float64, error) {
	v, err := strconv.ParseFloat(value, 64)
	if err != nil || v <= 0 || v > 100 {
		return 0, usagef("--%s: %q is not a percentage between 0 and 100", flagName, value)
	}
	return v, nil
}

// budget builds the budget observer, or returns nil for "there is no admission
// control".
//
// The key is what decides whether there is one at all. A zero age means every
// call fetches, which is right for a one-shot `afk budget` and wrong for a
// worker pool - so the pool demands one of its own rather than this function
// demanding it of every caller.
//
// The threshold is genuinely optional. Without it the only thing that stops new
// work is a window that is actually limited, which is a coherent configuration
// rather than a half-made one.
func (p *params) budget() (*budget.Observer, error) {
	key := optional(p.budgetKey, "AFK_BUDGET_KEY")
	age := optional(p.budgetAge, "AFK_BUDGET_AGE")
	at := optional(p.budgetAt, "AFK_BUDGET_AT")

	if key == "" {
		if age != "" || at != "" {
			return nil, usagef("--budget-age and --budget-at need --budget-key: there is nothing to observe")
		}
		return nil, nil
	}

	var (
		maxAge time.Duration
		err    error
	)
	if age != "" {
		if maxAge, err = duration(age, "budget-age"); err != nil {
			return nil, err
		}
	}

	var threshold float64
	if at != "" {
		if threshold, err = percent(at, "budget-at"); err != nil {
			return nil, err
		}
	}

	token, err := readKey(key, "budget-key")
	if err != nil {
		return nil, err
	}
	return &budget.Observer{Token: token, MaxAge: maxAge, Threshold: threshold}, nil
}

// notifier builds the notification channel, or returns nil for "nothing is
// notified".
//
// The URL is what decides whether there is one, and the token is genuinely
// optional: an ntfy topic that anyone may publish to is a coherent deployment,
// and an empty token sends no Authorization header rather than a broken one. A
// token with no URL is the half-made configuration worth refusing - it is a
// secret read for a channel that does not exist.
func (p *params) notifier() (*notify.Notifier, error) {
	url := optional(p.notifyURL, "AFK_NOTIFY_URL")
	key := optional(p.notifyKey, "AFK_NOTIFY_KEY")

	if url == "" {
		if key != "" {
			return nil, usagef("--notify-key needs --notify-url: there is nowhere to publish to")
		}
		return nil, nil
	}

	var (
		token string
		err   error
	)
	if key != "" {
		if token, err = readKey(key, "notify-key"); err != nil {
			return nil, err
		}
	}
	return &notify.Notifier{URL: url, Token: token}, nil
}

// requireBudgetAge is the check a caller that reuses an observation across jobs
// makes before building the observer.
//
// Here rather than in budget() because `afk budget` legitimately has no age -
// a single hand-run has nothing to reuse - and before it because a
// half-configured observer is the operator's mistake and should be reported as
// one, ahead of anything that depends on the key file being readable.
func (p *params) requireBudgetAge() error {
	if optional(p.budgetKey, "AFK_BUDGET_KEY") != "" && optional(p.budgetAge, "AFK_BUDGET_AGE") == "" {
		return usagef("--budget-key needs --budget-age (or set AFK_BUDGET_AGE)")
	}
	return nil
}

// readKey reads the API key from its file.
//
// Read once, at startup, rather than before each request. A key that is rotated
// under a running process is a restart, which is what the NixOS module does when
// the secret changes; re-reading it on every fetch would trade that for a file
// the agent must be able to read for as long as it runs.
//
// The error names the path and never the contents: a key that is empty or
// unreadable is a thing to report, and a key that is wrong is reported by the
// endpoint as a 401.
func readKey(path, flagName string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--%s: %w", flagName, err)
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", usagef("--%s: %s is empty", flagName, path)
	}
	return key, nil
}

// bindTracker binds the tracker: the repository commands are read from, and the
// token that reads them.
//
// The token is a path for the reason the usage key is: an argument is visible
// in `ps` and an environment variable in /proc, and the secret already arrives
// as a file from sops or systemd's LoadCredential.
func (p *params) bindTracker(fs *flag.FlagSet) {
	fs.StringVar(&p.repo, "repo", "", "repository commands are read from, owner/name (AFK_REPO)")
	fs.StringVar(&p.trackerKey, "tracker-key", "", "file holding the GitHub token (AFK_TRACKER_KEY)")
}

// tracker is the tracker client, or nil if no repository is configured.
//
// A repository with no token is refused rather than read anonymously. Intake
// has to know the agent's own login to tell its comments and its claims from
// anyone else's, and only an authenticated request can say whose that is.
func (p *params) tracker() (*github.Client, error) {
	repo := optional(p.repo, "AFK_REPO")
	key := optional(p.trackerKey, "AFK_TRACKER_KEY")

	if repo == "" {
		if key != "" {
			return nil, usagef("--tracker-key needs --repo: there is no repository to read")
		}
		return nil, nil
	}
	if owner, name, ok := strings.Cut(repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, usagef("--repo: %q is not owner/name", repo)
	}
	if key == "" {
		return nil, usagef("--repo needs --tracker-key: intake reads the agent's own login, which takes a token")
	}
	token, err := readKey(key, "tracker-key")
	if err != nil {
		return nil, err
	}
	return &github.Client{Repo: repo, Token: token}, nil
}

// intake is command intake over the configured tracker, or nil if there is no
// tracker. It asks the tracker who the agent is, once, rather than on every
// pass.
func (p *params) intake(ctx context.Context, st store.Store, holder string, lease time.Duration) (*intake.Intake, error) {
	client, err := p.tracker()
	if err != nil || client == nil {
		return nil, err
	}
	login, err := client.Login(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the agent's own login: %w", err)
	}
	return &intake.Intake{
		Tracker:  client,
		Store:    st,
		Commands: commands(),
		Login:    login,
		Holder:   holder,
		LeaseTTL: lease,
	}, nil
}
