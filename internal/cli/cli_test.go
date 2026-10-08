package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// withCatalogue puts a transition in front of the command surface for one test.
func withCatalogue(t *testing.T, ts ...transition.Transition) {
	t.Helper()
	reg := transition.MustRegistry(ts...)
	was := catalogue
	catalogue = func(*deps) *transition.Registry { return reg }
	wasReview, wasImplement := reviewDeps, implementDeps
	reviewDeps = func(context.Context, params, store.Store, *tracker) (*review.Deps, error) { return nil, nil }
	implementDeps = func(context.Context, params, store.Store, *tracker) (*implement.Deps, error) { return nil, nil }
	t.Cleanup(func() { catalogue, reviewDeps, implementDeps = was, wasReview, wasImplement })
}

// decides is a transition that decides something and touches nothing: no
// network, no model, no daemon.
func decides(next string) transition.Transition {
	return transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{State: next}, nil
		},
	}
}

func TestMain_ExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		want     int
		stdoutIs string // substring expected on stdout
		stderrIs string // substring expected on stderr
	}{
		{
			name:     "no arguments is a usage error",
			args:     nil,
			want:     ExitUsage,
			stderrIs: "Usage:",
		},
		{
			name:     "unknown command",
			args:     []string{"frobnicate"},
			want:     ExitUsage,
			stderrIs: `unknown command "frobnicate"`,
		},
		{
			name:     "help goes to stdout",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "afk run <transition>",
		},
		{
			name:     "help lists the size signal",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "--size-signal <n>     AFK_SIZE_SIGNAL",
		},
		{
			name:     "help lists the fresh session threshold",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "AFK_FRESH_SESSION_AT",
		},
		{
			name:     "help lists the replay bound",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "--replays <n>         AFK_REPLAYS",
		},
		{
			name:     "help lists the review procedure",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "--review-procedure <url>",
		},
		{
			name:     "help lists the advisory review's severity floor",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "--review-floor <s>    AFK_REVIEW_FLOOR",
		},
		{
			name:     "help lists the advisory review's fold cut",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "--review-fold-cut <n> AFK_REVIEW_FOLD_CUT",
		},
		{
			name:     "help lists the sensitive paths",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "--sensitive <paths>   AFK_SENSITIVE",
		},
		{
			name:     "help lists the eligibility label",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "--eligibility-label <l>",
		},
		{
			name:     "help lists the review-queue limit",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "--review-queue-limit <n>",
		},
		{
			name:     "version",
			args:     []string{"version"},
			want:     ExitOK,
			stdoutIs: Version,
		},
		{
			name:     "run needs a transition",
			args:     []string{"run"},
			want:     ExitUsage,
			stderrIs: "needs a transition name",
		},
		{
			name:     "a flag is not a transition name",
			args:     []string{"run", "--job", "7"},
			want:     ExitUsage,
			stderrIs: "needs a transition name",
		},
		{
			name:     "run needs a subject",
			args:     []string{"run", "review"},
			want:     ExitUsage,
			stderrIs: "one of --job, --issue or --pr",
		},
		{
			name:     "run takes only one subject",
			args:     []string{"run", "review", "--job", "7", "--pr", "12"},
			want:     ExitUsage,
			stderrIs: "only one of --job, --issue or --pr",
		},
		{
			name:     "unknown flag",
			args:     []string{"run", "review", "--branch", "master"},
			want:     ExitUsage,
			stderrIs: "flag provided but not defined",
		},
		{
			name:     "stray positional argument",
			args:     []string{"run", "review", "--pr", "12", "extra"},
			want:     ExitUsage,
			stderrIs: `unexpected argument "extra"`,
		},
		{
			name:     "a subject of the wrong kind",
			args:     []string{"run", "review", "--issue", "12"},
			want:     ExitUsage,
			stderrIs: "review runs review jobs, which are on a pr: use --pr",
		},
		{
			name:     "an unregistered transition names the ones that are",
			args:     []string{"run", "implement", "--job", "01J0"},
			want:     ExitUsage,
			stderrIs: `unknown transition "implement"; known transitions: review`,
		},
		{
			name:     "the store has no default",
			args:     []string{"run", "review", "--pr", "12"},
			want:     ExitUsage,
			stderrIs: "--store is required (or set AFK_STORE)",
		},
		{
			name:     "the lease has no default",
			args:     []string{"run", "review", "--pr", "12", "--store", "/nonexistent/state.db"},
			want:     ExitUsage,
			stderrIs: "--lease is required (or set AFK_LEASE)",
		},
		{
			name:     "a lease that is not a duration",
			args:     []string{"run", "review", "--pr", "12", "--store", "/x", "--lease", "soon"},
			want:     ExitUsage,
			stderrIs: `"soon" is not a duration`,
		},
		{
			name:     "a subject that is not a number",
			args:     []string{"run", "review", "--pr", "twelve", "--store", "/x", "--lease", "1m"},
			want:     ExitUsage,
			stderrIs: `"twelve" is not a pr number`,
		},
		{
			// Half a retry policy is a policy that cannot be applied, and it
			// is better to say so at the command line than at 04:00.
			name:     "a retry interval with no bound",
			args:     []string{"run", "review", "--pr", "12", "--store", "/x", "--lease", "1m", "--retry", "5m"},
			want:     ExitUsage,
			stderrIs: "--retry needs --max-attempts",
		},
		{
			name:     "a retry bound with no interval",
			args:     []string{"run", "review", "--pr", "12", "--store", "/x", "--lease", "1m", "--max-attempts", "3"},
			want:     ExitUsage,
			stderrIs: "--max-attempts needs --retry",
		},
		{
			name:     "work needs its worker count",
			args:     []string{"work", "--store", "/x", "--lease", "1m"},
			want:     ExitUsage,
			stderrIs: "--workers is required (or set AFK_WORKERS)",
		},
		{
			name:     "a resource token capacity that is not a number",
			args:     []string{"work", "--token", "heavy-build=lots"},
			want:     ExitUsage,
			stderrIs: `"lots" is not a positive whole number`,
		},
		{
			// Half a budget observer, like half a retry policy: there is
			// nothing for the threshold to be a threshold of.
			name:     "a budget threshold with nothing to observe",
			args:     append(workArgs(), "--budget-at", "80"),
			want:     ExitUsage,
			stderrIs: "need --budget-key",
		},
		{
			// A pool reuses an observation across jobs, so it has to be told
			// for how long; without it every job dispatched is a request.
			name:     "work needs to be told how long an observation lasts",
			args:     append(workArgs(), "--budget-key", "/nonexistent/key"),
			want:     ExitUsage,
			stderrIs: "--budget-key needs --budget-age",
		},
		{
			// A waiver is nothing without an observation to apply it to.
			name:     "a budget waiver with nothing to observe",
			args:     []string{"budget", "--budget-waive", "monthly=2026-09-27T00:00:00Z"},
			want:     ExitUsage,
			stderrIs: "need --budget-key",
		},
		{
			// Malformed is refused at the command line, before the key file is
			// read, rather than discovered to be wrong when the account is
			// spent.
			name:     "a malformed budget waiver",
			args:     []string{"budget", "--budget-key", "/nonexistent/key", "--budget-waive", "monthly=soon"},
			want:     ExitUsage,
			stderrIs: "is not an RFC 3339 timestamp",
		},
		{
			name:     "budget has nothing to read",
			args:     []string{"budget"},
			want:     ExitUsage,
			stderrIs: "budget needs --budget-key",
		},
		{
			name:     "caught has no repository to read",
			args:     []string{"caught"},
			want:     ExitUsage,
			stderrIs: "caught needs --repo",
		},
		{
			name:     "caught reads through the App",
			args:     []string{"caught", "--repo", "o/n"},
			want:     ExitUsage,
			stderrIs: "--repo needs --app-id",
		},
		{
			// It reads the repository and nothing intake is configured by.
			name:     "caught takes no intake parameter",
			args:     []string{"caught", "--repo", "o/n", "--eligibility-label", "afk"},
			want:     ExitUsage,
			stderrIs: "flag provided but not defined: -eligibility-label",
		},
		{
			name:     "caught takes no argument",
			args:     []string{"caught", "extra"},
			want:     ExitUsage,
			stderrIs: `unexpected argument "extra"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withCatalogue(t, decides("reviewed"))
			var stdout, stderr bytes.Buffer
			got := Main(tt.args, &stdout, &stderr)
			if got != tt.want {
				t.Errorf("exit code = %d, want %d (stderr: %s)", got, tt.want, stderr.String())
			}
			if tt.stdoutIs != "" && !strings.Contains(stdout.String(), tt.stdoutIs) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.stdoutIs)
			}
			if tt.stderrIs != "" && !strings.Contains(stderr.String(), tt.stderrIs) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.stderrIs)
			}
		})
	}
}

// workArgs is a `work` invocation with every required parameter supplied, so a
// test about one of the optional ones fails on that one rather than on the
// first thing missing.
func workArgs() []string {
	return []string{"work", "--store", "/x", "--lease", "1m", "--workers", "1", "--poll", "1s", "--token-wait", "1s"}
}

// The key is read from a file rather than an argument or the environment: an
// argument is visible in `ps` to every process on the host, and an environment
// variable is visible in /proc to anything that can read the process.
func TestTheBudgetKeyIsReadFromItsFile(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "usage-key")
	if err := os.WriteFile(key, []byte("  secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty-key")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		p    params
		want string // substring of the error, or "" for the key itself
	}{
		{name: "read and trimmed", p: params{budgetKey: key, budgetAge: "1m", budgetAt: "80"}},
		{name: "no such file", p: params{budgetKey: filepath.Join(dir, "absent"), budgetAge: "1m"}, want: "--budget-key:"},
		{name: "empty file", p: params{budgetKey: empty, budgetAge: "1m"}, want: "is empty"},
		{name: "a threshold that is not a percentage", p: params{budgetKey: key, budgetAge: "1m", budgetAt: "120"}, want: "is not a percentage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := tt.p.budget()
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if o.Token != "secret" {
				t.Fatalf("token = %q, want the file trimmed", o.Token)
			}
			if o.MaxAge != time.Minute || o.Threshold != 80 {
				t.Fatalf("observer = %+v, want the age and threshold given", o)
			}
		})
	}
}

// No budget parameters is no observer, which is what the dispatcher reads as no
// admission control.
func TestNoBudgetParametersIsNoObserver(t *testing.T) {
	t.Setenv("AFK_BUDGET_KEY", "")
	t.Setenv("AFK_BUDGET_AGE", "")
	t.Setenv("AFK_BUDGET_AT", "")
	t.Setenv("AFK_BUDGET_WAIVE", "")

	var p params
	o, err := p.budget()
	if err != nil {
		t.Fatal(err)
	}
	if o != nil {
		t.Fatalf("observer = %+v, want none", o)
	}
}

// The waiver parameter names a window and the resetsAt it lasts until, may be
// given more than once, and is refused when malformed or when one window is
// waived twice. It is the module that restricts which windows may be waived, so
// nothing here checks the name against a list.
func TestBudgetWaiverParameters(t *testing.T) {
	t.Setenv("AFK_BUDGET_WAIVE", "")
	monthly := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	weekly := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	want := budget.Waivers{{Window: "monthly", Until: monthly}, {Window: "weekly", Until: weekly}}

	// The flag, repeated, keeps them in the order they were given.
	var p params
	fs := flag.NewFlagSet("afk budget", flag.ContinueOnError)
	p.bindBudget(fs)
	if err := fs.Parse([]string{"--budget-waive", "monthly=2026-09-27T00:00:00Z", "--budget-waive", "weekly=2026-09-15T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if got, err := p.waivers(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("waivers() = %+v, %v; want %+v", got, err, want)
	}

	// The environment, comma-separated, for the line the module sets.
	t.Setenv("AFK_BUDGET_WAIVE", "monthly=2026-09-27T00:00:00Z,weekly=2026-09-15T00:00:00Z")
	var env params
	if got, err := env.waivers(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("waivers() = %+v, %v; want %+v", got, err, want)
	}

	for _, tc := range []struct {
		name, value, want string
	}{
		{name: "not window=timestamp", value: "monthly", want: "is not <window>=<RFC3339>"},
		{name: "no window", value: "=2026-09-27T00:00:00Z", want: "names no window"},
		{name: "not a timestamp", value: "monthly=soon", want: "is not an RFC 3339 timestamp"},
		{name: "one window twice", value: "monthly=2026-09-27T00:00:00Z,monthly=2026-10-27T00:00:00Z", want: "is waived twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AFK_BUDGET_WAIVE", tc.value)
			var p params
			if _, err := p.waivers(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// `afk budget` says a waived window is waived, and says of each waiver in force
// whether it names the period its window was observed in - so a waiver for the
// wrong period can be caught by hand before the window is spent.
func TestBudgetOutputReportsEachWaiverAgainstItsPeriod(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	state := budget.State{
		ObservedAt: now,
		Windows: []budget.Window{
			{Name: "weekly", Status: budget.StatusOK, Percent: 41, ResetsAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)},
			{Name: "monthly", Status: budget.StatusRateLimited, Percent: 100, ResetsAt: time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)},
		},
	}
	waivers := budget.Waivers{
		{Window: "monthly", Until: time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)},
		{Window: "weekly", Until: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)},
		{Window: "fortnightly", Until: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)},
		{Window: "rolling", Until: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	}

	var out bytes.Buffer
	if err := writeBudget(&out, state, 0, waivers, now); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		state.String(),
		"start (waived: monthly until 2026-09-27T00:00:00Z)",
		"monthly is waived until 2026-09-27T00:00:00Z: covers this period",
		"weekly is waived until 2026-09-22T00:00:00Z: does not match this period's reset 2026-09-15T00:00:00Z",
		"fortnightly is waived until 2026-09-25T00:00:00Z: no fortnightly window observed",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("afk budget printed:\n%s\nwant:\n%s", out.String(), want)
	}
}

// The acceptance criterion of #2, at the command line: a transition runs with
// no daemon present, named by the tracker subject rather than by a job id. The
// job does not have to exist first - the subject is what names it.
func TestRunAgainstATrackerSubjectWithNoDaemon(t *testing.T) {
	withCatalogue(t, decides("reviewed"))
	path := filepath.Join(t.TempDir(), "state.db")

	var stdout, stderr bytes.Buffer
	got := Main([]string{"run", "review", "--pr", "12", "--store", path, "--lease", "1m"}, &stdout, &stderr)
	if got != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got, ExitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "review-pr-12: review start -> reviewed") {
		t.Errorf("stdout = %q, want it to report what the transition did", stdout.String())
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	job, err := s.Job(context.Background(), "review-pr-12")
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if job.State != "reviewed" {
		t.Errorf("state = %q, want reviewed", job.State)
	}
	if job.Lease != nil {
		t.Errorf("lease = %+v; want it released", job.Lease)
	}
}

// A second invocation names the same job, because the id is derived from the
// kind and the subject rather than allocated. By then the job has moved on, and
// the same state machine applies to a hand-run as to a scheduled one.
func TestASecondRunMeetsTheStateMachine(t *testing.T) {
	withCatalogue(t, decides("reviewed"))
	path := filepath.Join(t.TempDir(), "state.db")
	args := []string{"run", "review", "--pr", "12", "--store", path, "--lease", "1m"}

	var stdout, stderr bytes.Buffer
	if got := Main(args, &stdout, &stderr); got != ExitOK {
		t.Fatalf("first run = %d, want %d (stderr: %s)", got, ExitOK, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := Main(args, &stdout, &stderr); got != ExitError {
		t.Fatalf("second run = %d, want %d", got, ExitError)
	}
	if !strings.Contains(stderr.String(), `is in state "reviewed"`) {
		t.Errorf("stderr = %q, want it to name the state the job is actually in", stderr.String())
	}
}

// The NixOS module sets the parameters once, in the unit's environment, and the
// operator's hand-run inherits the same values rather than a second set.
func TestParametersComeFromTheEnvironmentToo(t *testing.T) {
	withCatalogue(t, decides("reviewed"))
	t.Setenv("AFK_STORE", filepath.Join(t.TempDir(), "state.db"))
	t.Setenv("AFK_LEASE", "1m")

	var stdout, stderr bytes.Buffer
	if got := Main([]string{"run", "review", "--pr", "12"}, &stdout, &stderr); got != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got, ExitOK, stderr.String())
	}
}

func TestRunAgainstAJobThatIsNotThere(t *testing.T) {
	withCatalogue(t, decides("reviewed"))
	path := filepath.Join(t.TempDir(), "state.db")

	var stdout, stderr bytes.Buffer
	got := Main([]string{"run", "review", "--job", "review-pr-99", "--store", path, "--lease", "1m"}, &stdout, &stderr)
	if got != ExitError {
		t.Fatalf("exit code = %d, want %d", got, ExitError)
	}
	if !strings.Contains(stderr.String(), "no such job") {
		t.Errorf("stderr = %q, want it to say the job is not there", stderr.String())
	}
}

// Every transition this build ships is invokable standalone, from its own
// starting state, with no network, no model and no daemon. A transition that
// cannot be is badly factored (#2), so this is a test rather than a convention.
//
// It is vacuous until the first transition lands (#3), which is why
// TestTheStandaloneCheckCatchesATransitionThatCannotRunAlone exists beside it:
// that one proves the check itself works.
func TestEveryTransitionRunsStandalone(t *testing.T) {
	for _, tr := range catalogue(standaloneDeps(t)).All() {
		t.Run(tr.Name, func(t *testing.T) {
			if err := runsStandalone(t, tr); err != nil {
				t.Errorf("%s cannot be run on its own: %v", tr.Name, err)
			}
		})
	}
}

func TestTheStandaloneCheckCatchesATransitionThatCannotRunAlone(t *testing.T) {
	needsSomethingLive := transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{}, context.DeadlineExceeded
		},
	}
	if err := runsStandalone(t, needsSomethingLive); err == nil {
		t.Error("the check passed a transition that cannot run on its own")
	}
	if err := runsStandalone(t, decides("reviewed")); err != nil {
		t.Errorf("the check failed a transition that can: %v", err)
	}
}

// runsStandalone puts a job in the transition's own starting state and runs it
// through the runner, with nothing else present.
func runsStandalone(t *testing.T, tr transition.Transition) error {
	t.Helper()
	ctx := context.Background()

	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	job, err := s.Ensure(ctx, tr.Kind, store.Subject{Type: subjectOf(tr.Kind), Number: 1}, tr.From, time.Now())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	r := &transition.Runner{
		Store:    s,
		Registry: transition.MustRegistry(tr),
		Holder:   "test",
		LeaseTTL: time.Minute,
	}
	_, err = r.Run(ctx, tr.Name, job.ID)
	return err
}

// A usage error belongs on stderr, so that a caller redirecting stdout still
// sees it, and so that nothing parsing stdout is handed a usage message.
func TestMain_UsageErrorsStayOffStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := Main([]string{"run"}, &stdout, &stderr); got != ExitUsage {
		t.Fatalf("exit code = %d, want %d", got, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want it empty", stdout.String())
	}
}

func TestSubject_String(t *testing.T) {
	tests := []struct {
		subject Subject
		want    string
	}{
		{Subject{Job: "01J0"}, "job 01J0"},
		{Subject{Issue: "10"}, "issue 10"},
		{Subject{PR: "12"}, "pr 12"},
		{Subject{}, "no subject"},
	}
	for _, tt := range tests {
		if got := tt.subject.String(); got != tt.want {
			t.Errorf("Subject%+v.String() = %q, want %q", tt.subject, got, tt.want)
		}
	}
}

// The ntfy token is read from a file for the same reason the usage key is: an
// argument is visible in `ps` and an environment variable in /proc.
func TestTheNotifyTokenIsReadFromItsFile(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "ntfy-token")
	if err := os.WriteFile(token, []byte(" tk_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		p     params
		want  string // substring of the error
		token string // the token expected on the notifier
	}{
		{name: "read and trimmed", p: params{notifyURL: "https://ntfy.example/afk", notifyKey: token, tierNotifyAfter: "3"}, token: "tk_secret"},
		{name: "a topic anyone may publish to", p: params{notifyURL: "https://ntfy.example/afk", tierNotifyAfter: "3"}},
		{name: "no such file", p: params{notifyURL: "https://ntfy.example/afk", notifyKey: filepath.Join(dir, "absent"), tierNotifyAfter: "3"}, want: "--notify-key:"},
		{name: "a token with nowhere to publish", p: params{notifyKey: token}, want: "need --notify-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AFK_NOTIFY_URL", "")
			t.Setenv("AFK_NOTIFY_KEY", "")
			t.Setenv("AFK_TIER_NOTIFY_AFTER", "")

			n, err := tt.p.notifier()
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if n.Token != tt.token {
				t.Fatalf("token = %q, want %q", n.Token, tt.token)
			}
			if n.URL != tt.p.notifyURL {
				t.Fatalf("url = %q, want %q", n.URL, tt.p.notifyURL)
			}
		})
	}
}

// No notification parameters is no notifier, which the dispatcher reads as
// nothing being notified. An agent nobody has given a channel to is silent
// rather than broken.
func TestNoNotifyParametersIsNoNotifier(t *testing.T) {
	t.Setenv("AFK_NOTIFY_URL", "")
	t.Setenv("AFK_NOTIFY_KEY", "")
	t.Setenv("AFK_TIER_NOTIFY_AFTER", "")

	var p params
	n, err := p.notifier()
	if err != nil {
		t.Fatal(err)
	}
	if n != nil {
		t.Fatalf("notifier = %+v, want none", n)
	}
}

// The notification parameters reach the binary from the environment too, the
// way the NixOS module sets them in the unit.
func TestTheNotifyURLComesFromTheEnvironmentToo(t *testing.T) {
	t.Setenv("AFK_NOTIFY_URL", "https://ntfy.example/from-the-unit")
	t.Setenv("AFK_NOTIFY_KEY", "")
	t.Setenv("AFK_TIER_NOTIFY_AFTER", "4")

	var p params
	n, err := p.notifier()
	if err != nil {
		t.Fatal(err)
	}
	if n == nil || n.URL != "https://ntfy.example/from-the-unit" || n.TierAfter != 4 {
		t.Fatalf("notifier = %+v, want the URL and the exhaustion count from the environment", n)
	}
}

// The notifier links into the tracker's repository (#169), which is the one
// already configured rather than a parameter of its own.
func TestTheNotifierLinksIntoTheConfiguredRepository(t *testing.T) {
	t.Setenv("AFK_NOTIFY_URL", "https://ntfy.example/afk")
	t.Setenv("AFK_NOTIFY_KEY", "")
	t.Setenv("AFK_TIER_NOTIFY_AFTER", "3")
	t.Setenv("AFK_REPO", "owner/from-the-unit")

	var p params
	n, err := p.notifier()
	if err != nil {
		t.Fatal(err)
	}
	if n.Repo != "owner/from-the-unit" {
		t.Fatalf("Repo = %q, want the repository from AFK_REPO", n.Repo)
	}
}

// How many times a tier is exhausted before the operator hears is the
// deployment's call (#76), so a channel is refused without one rather than
// given a number chosen here.
func TestTheTierExhaustionCountIsRequiredWithAChannel(t *testing.T) {
	tests := []struct {
		name string
		p    params
		want string // substring of the error; empty is a notifier with this count
		n    int
	}{
		{name: "given", p: params{notifyURL: "https://ntfy.example/afk", tierNotifyAfter: "3"}, n: 3},
		{name: "missing", p: params{notifyURL: "https://ntfy.example/afk"}, want: "needs --tier-notify-after"},
		{name: "zero", p: params{notifyURL: "https://ntfy.example/afk", tierNotifyAfter: "0"}, want: "--tier-notify-after:"},
		{name: "not a number", p: params{notifyURL: "https://ntfy.example/afk", tierNotifyAfter: "few"}, want: "--tier-notify-after:"},
		{name: "a count with nowhere to publish", p: params{tierNotifyAfter: "3"}, want: "need --notify-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AFK_NOTIFY_URL", "")
			t.Setenv("AFK_NOTIFY_KEY", "")
			t.Setenv("AFK_TIER_NOTIFY_AFTER", "")

			n, err := tt.p.notifier()
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if n.TierAfter != tt.n {
				t.Fatalf("TierAfter = %d, want %d", n.TierAfter, tt.n)
			}
		})
	}
}

// The App's private key is a file, for the reason every secret here is, and the
// client authenticates as the App built from it (#34).
func TestTheAppKeyIsReadFromItsFile(t *testing.T) {
	dir := t.TempDir()
	rsaKey := testAppKey()
	key := filepath.Join(dir, "app-key.pem")
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})
	if err := os.WriteFile(key, append([]byte("\n"), pemKey...), 0o600); err != nil {
		t.Fatal(err)
	}
	const secret = "ghp_not-a-private-key"
	notAKey := filepath.Join(dir, "token")
	if err := os.WriteFile(notAKey, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		p    params
		want string // substring of the error, or "" for a client
	}{
		{name: "read and parsed", p: params{repo: "corygyarmathy/afk-agent", appID: "Iv23li", appKey: key}},
		{name: "no such file", p: params{repo: "o/n", appID: "1", appKey: filepath.Join(dir, "absent")}, want: "--app-key:"},
		{name: "a file that is not a key", p: params{repo: "o/n", appID: "1", appKey: notAKey}, want: "--app-key: " + notAKey},
		{name: "a repository with no App", p: params{repo: "o/n"}, want: "needs --app-id"},
		{name: "an App with no key", p: params{repo: "o/n", appID: "1"}, want: "needs --app-key"},
		{name: "a key with no App id", p: params{repo: "o/n", appKey: key}, want: "needs --app-id"},
		{name: "an App with no repository", p: params{appID: "1", appKey: key}, want: "need --repo"},
		{name: "a repository that is not owner/name", p: params{repo: "afk-agent", appID: "1", appKey: key}, want: "is not owner/name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AFK_REPO", "")
			t.Setenv("AFK_APP_ID", "")
			t.Setenv("AFK_APP_KEY", "")

			tr, err := tt.p.tracker()
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.want)
				}
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q quotes the key file", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tr.client.Repo != tt.p.repo || tr.app.Repo != tt.p.repo || tr.app.ID != tt.p.appID || !tr.app.Key.Equal(rsaKey) {
				t.Fatalf("app = %+v, want the repository, the id and the key", tr.app)
			}
			if tr.client.Credential != tr.app {
				t.Error("the client does not authenticate as the App")
			}
		})
	}
}

// No tracker parameters is no tracker, which afk work reads as no intake.
func TestNoTrackerParametersIsNoTracker(t *testing.T) {
	t.Setenv("AFK_REPO", "")
	t.Setenv("AFK_APP_ID", "")
	t.Setenv("AFK_APP_KEY", "")

	var p params
	tr, err := p.tracker()
	if err != nil {
		t.Fatal(err)
	}
	if tr != nil {
		t.Fatalf("tracker = %+v, want none", tr)
	}
}

// testAppKey is one App key for the package: generating RSA keys is slow.
var testAppKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

// The login is read from GitHub once per process, whoever asks first, and a
// failure to read it is not kept.
func TestTheLoginIsReadOnce(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/app" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if reads.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"slug":"afk-agent"}`)
	}))
	t.Cleanup(srv.Close)
	tr := &tracker{app: &github.App{ID: "1", Key: testAppKey(), Repo: "o/n", BaseURL: srv.URL}}

	if _, err := tr.Login(context.Background()); err == nil {
		t.Fatal("no error when GitHub is unavailable")
	}
	for range 2 {
		if login, err := tr.Login(context.Background()); err != nil || login != "afk-agent[bot]" {
			t.Fatalf("login = %q, %v, want afk-agent[bot]", login, err)
		}
	}
	if n := reads.Load(); n != 2 {
		t.Errorf("read the login %d times, want twice: once failing, once for good", n)
	}
}

// afk caught reads the agent's login from the App, then counts the catches on
// the heads that login pushed, as the installation reads them.
func TestCaughtCountsWhatTheAppsLoginPushed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.Method + " " + r.URL.Path; p {
		case "GET /app":
			fmt.Fprint(w, `{"slug":"afk-agent"}`)
		case "GET /repos/o/n/installation":
			fmt.Fprint(w, `{"id":1}`)
		case "POST /app/installations/1/access_tokens":
			fmt.Fprint(w, `{"token":"ghs_installation","expires_at":"2099-01-01T00:00:00Z"}`)
		case "GET /repos/o/n/actions/runs":
			if got := r.Header.Get("Authorization"); got != "Bearer ghs_installation" {
				t.Errorf("runs read with %q, want the installation token", got)
			}
			q := r.URL.Query()
			if q.Get("actor") != "afk-agent[bot]" {
				t.Errorf("runs of %q, want the App's login", q.Get("actor"))
			}
			switch q.Get("status") {
			case "":
				fmt.Fprint(w, `{"total_count":12,"workflow_runs":[]}`)
			case "failure":
				fmt.Fprint(w, `{"total_count":1,"workflow_runs":[{"id":7,"head_sha":"abcdef0123456789","head_branch":"afk/7-1","event":"pull_request","conclusion":"failure","created_at":"2026-09-27T10:00:00Z"}]}`)
			default:
				fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
			}
		case "GET /repos/o/n/actions/runs/7/jobs":
			fmt.Fprint(w, `{"jobs":[{"name":"test","conclusion":"failure","html_url":"https://github.com/o/n/job/1"},{"name":"vet","conclusion":"success"}]}`)
		default:
			t.Errorf("unexpected request %s", p)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	app := &github.App{ID: "1", Key: testAppKey(), Repo: "o/n", BaseURL: srv.URL}
	tr := &tracker{client: &github.Client{Repo: "o/n", Credential: app, BaseURL: srv.URL}, app: app}

	var out strings.Builder
	if err := readCaught(context.Background(), tr, true, &out); err != nil {
		t.Fatal(err)
	}
	want := "2026-09-27T10:00:00Z\tabcdef012345\tafk/7-1\ttest\thttps://github.com/o/n/job/1\n" +
		"1\ttest\n" +
		"1 catch on 1 head, from 2026-09-27 to 2026-09-27, of 12 runs afk-agent[bot] started.\n"
	if out.String() != want {
		t.Errorf("got\n%s\nwant\n%s", out.String(), want)
	}
}

// A review is read from its parameters, and review and intake reach the
// tracker through the one the command built, so a process has one token and
// one login (ADR 0005 §5).
func TestReviewIsReadFromTheParametersAndSharesTheCommandsTracker(t *testing.T) {
	for _, env := range []string{"AFK_BUDGET_KEY", "AFK_BUDGET_AGE", "AFK_BUDGET_AT", "AFK_CATALOGUE_AGE", "AFK_REVIEW_NEEDS", "AFK_EFFECT_ROUNDS", "AFK_HAND_BACK_LABEL", "AFK_REVIEW_FLOOR", "AFK_REVIEW_FOLD_CUT"} {
		t.Setenv(env, "")
	}
	// A login already read, so nothing here reaches a network.
	tr := &tracker{client: &github.Client{Repo: "o/n"}, login: "afk-agent[bot]"}
	p := params{
		store:         filepath.Join(t.TempDir(), "state.db"),
		opencode:      "/bin/opencode",
		enrolment:     "/etc/afk/enrolment.json",
		reviewTier:    "review",
		modelAttempts: "3",
		tierWait:      "30m",
		modelTimeout:  "45m",
		effectRounds:  "4",
		handBackLabel: "needs-decision",
		reviewFloor:   "consider",
		reviewFoldCut: "120",
	}

	deps, err := reviewDeps(context.Background(), p, nil, tr)
	if err != nil {
		t.Fatal(err)
	}
	if deps.Bound != 3 || deps.Rounds != 4 || deps.HandBackLabel != "needs-decision" {
		t.Errorf("deps = %+v, want the attempt bound, the rounds and the hand-back label read from the parameters", deps)
	}
	if deps.Floor != "consider" || deps.FoldCut != 120 || deps.Repo != "o/n" {
		t.Errorf("floor %q, fold cut %d, repo %q; want consider, 120 and the tracker's o/n", deps.Floor, deps.FoldCut, deps.Repo)
	}
	if want := (opencode.Command{Path: "/bin/opencode", Timeout: 45 * time.Minute}); deps.Model != want {
		t.Errorf("model = %+v, want %+v", deps.Model, want)
	}
	for _, tc := range []struct {
		spoil func(*params)
		want  string
	}{
		{func(p *params) { p.effectRounds = "" }, "--effect-rounds is required"},
		{func(p *params) { p.effectRounds = "0" }, "--effect-rounds"},
		{func(p *params) { p.handBackLabel = "" }, "--hand-back-label is required"},
		{func(p *params) { p.reviewFloor = "nit" }, "--review-floor"},
		{func(p *params) { p.reviewFoldCut = "0" }, "--review-fold-cut"},
	} {
		spoilt := p
		tc.spoil(&spoilt)
		if _, err := reviewDeps(context.Background(), spoilt, nil, tr); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("err = %v, want it to contain %q", err, tc.want)
		}
	}
	in, err := newIntake(context.Background(), tr, nil, "holder", time.Minute, intake.Unattended{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Unattended != (intake.Unattended{}) {
		t.Errorf("no eligibility label, and intake takes %+v unattended", in.Unattended)
	}
	if labelled, err := newIntake(context.Background(), tr, nil, "holder", time.Minute, unattended("ready-for-agent")); err != nil || labelled.Unattended != unattended("ready-for-agent") {
		t.Errorf("with an eligibility label, intake takes %+v unattended (%v), want %+v", labelled.Unattended, err, unattended("ready-for-agent"))
	}
	if deps.Tracker != tr.client || in.Tracker != tr.client {
		t.Error("review and intake do not share the command's client")
	}
	if deps.Login != tr.login || in.Login != tr.login {
		t.Errorf("logins %q and %q, want both %q", deps.Login, in.Login, tr.login)
	}
}

// Implementing an issue is read from its parameters, and reaches the tracker
// through the one the command built.
func TestImplementIsReadFromTheParametersAndSharesTheCommandsTracker(t *testing.T) {
	for _, env := range []string{"AFK_BRANCH_PREFIX", "AFK_GATE", "AFK_GATE_ATTEMPTS", "AFK_IMPLEMENT_TIER", "AFK_IMPLEMENT_NEEDS", "AFK_HAND_BACK_LABEL", "AFK_HAND_OFF_LABEL", "AFK_LEASE", "AFK_DENYLIST", "AFK_CI_WAIT", "AFK_CI_CEILING", "AFK_CI_FIXES", "AFK_SIZE_SIGNAL", "AFK_FRESH_SESSION_AT", "AFK_REVIEW_PROCEDURE", "AFK_SENSITIVE", "AFK_EFFECT_ROUNDS", "AFK_REPLAYS",
		"AFK_BUDGET_KEY", "AFK_BUDGET_AGE", "AFK_BUDGET_AT", "AFK_CATALOGUE_AGE", "AFK_OPENCODE", "AFK_ENROLMENT", "AFK_MODEL_ATTEMPTS", "AFK_TIER_WAIT", "AFK_MODEL_TIMEOUT"} {
		t.Setenv(env, "")
	}
	tr := &tracker{client: &github.Client{Repo: "o/n"}, app: &github.App{Repo: "o/n"}, login: "afk-agent[bot]"}
	full := params{
		store:         filepath.Join(t.TempDir(), "state.db"),
		opencode:      "/bin/opencode",
		enrolment:     "/etc/afk/enrolment.json",
		modelAttempts: "3",
		tierWait:      "30m",
		modelTimeout:  "45m",
		branchPrefix:  "afk/",
		gate:          "go test ./...",
		gateAttempts:  "2",
		implementTier: "build",
		handBackLabel: "needs-decision",
		handOffLabel:  "needs-review",
		lease:         "10m",
		denylist:      ".github/**, flake.lock",
		ciWait:        "5m",
		ciCeiling:     "2h",
		ciFixes:       "2",
		sizeSignal:    "400",
		effectRounds:  "4",

		freshSessionAt: "150000",

		reviewProcedure: "https://github.com/o/skills/blob/main/docs/operators-review.md",
		sensitive:       "job store schema = internal/store/**, cmd/migrate/*.go; CI=.github/workflows/**;",
	}

	d, err := implementDeps(context.Background(), full, nil, tr)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tracker != tr.client || d.Login != tr.login {
		t.Errorf("deps = %+v, want the command's client and login", d)
	}
	// A premise in the agent's own repository is read with its own client,
	// and one elsewhere with a client for that repository.
	if own, ok := d.Premises("O/N").(*github.Client); !ok || own != tr.client {
		t.Errorf("a premise in o/n is read through %+v, want the command's client", d.Premises("O/N"))
	}
	if other, ok := d.Premises("up/stream").(*github.Client); !ok || other.Repo != "up/stream" || other.Credential == nil {
		t.Errorf("a premise in up/stream is read through %+v, want a client of its own, as the App", d.Premises("up/stream"))
	}
	if d.BranchPrefix != "afk/" || d.Gate != "go test ./..." || d.Attempts != 2 || d.HandBackLabel != "needs-decision" ||
		fmt.Sprint(d.Denylist) != "[.github/** flake.lock]" || d.Remote.Token == nil ||
		d.HandOffLabel != "needs-review" || d.AskReview == nil ||
		d.CIWait != 5*time.Minute || d.CICeiling != 2*time.Hour || d.CIFixes != 2 || d.SizeSignal != 400 || d.FreshAt != 150000 ||
		d.ReviewProcedure != full.reviewProcedure || d.Repo != "o/n" ||
		d.Bound != 3 || d.Rounds != 4 || d.TierWait != 30*time.Minute || d.Remote.URL != "https://github.com/o/n.git" || d.StateDir != filepath.Dir(full.store) {
		t.Errorf("deps = %+v, want them read from the parameters", d)
	}
	if want := (opencode.Command{Path: "/bin/opencode", Timeout: 45 * time.Minute}); d.Model != want {
		t.Errorf("model = %+v, want %+v", d.Model, want)
	}
	// The remote is never reached from a workspace (#134).
	if want := []string{filepath.Join(filepath.Dir(full.store), "workspaces")}; fmt.Sprint(d.Remote.Untrusted) != fmt.Sprint(want) {
		t.Errorf("the remote refuses %v, want the workspaces %v", d.Remote.Untrusted, want)
	}
	if want := []sensitive.Path{
		{Label: "job store schema", Globs: []string{"internal/store/**", "cmd/migrate/*.go"}},
		{Label: "CI", Globs: []string{".github/workflows/**"}},
	}; fmt.Sprint(d.Sensitive) != fmt.Sprint(want) {
		t.Errorf("sensitive = %v, want %v, in the operator's order", d.Sensitive, want)
	}
	// The sensitive paths are optional: without them no pull request says any.
	none := full
	none.sensitive = ""
	if d, err := implementDeps(context.Background(), none, nil, tr); err != nil || len(d.Sensitive) != 0 {
		t.Errorf("without sensitive paths: deps %+v, err %v; want none and no error", d, err)
	}
	// Zero is a threshold an operator may set: every session is continued.
	always := full
	always.freshSessionAt = "0"
	if d, err := implementDeps(context.Background(), always, nil, tr); err != nil || d.FreshAt != 0 {
		t.Errorf("at zero: deps %+v, err %v; want zero and no error", d, err)
	}
	// The procedure is optional: without it the reminder says it has no link.
	unset := full
	unset.reviewProcedure = ""
	if d, err := implementDeps(context.Background(), unset, nil, tr); err != nil || d.ReviewProcedure != "" {
		t.Errorf("without a procedure: deps %+v, err %v; want no procedure and no error", d, err)
	}

	if _, err := implementDeps(context.Background(), full, nil, nil); err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Errorf("err = %v, want it to name --repo", err)
	}
	for _, tc := range []struct {
		spoil func(*params)
		want  string
	}{
		{func(p *params) { p.branchPrefix = "" }, "--branch-prefix is required"},
		{func(p *params) { p.gate = "" }, "--gate is required"},
		{func(p *params) { p.gateAttempts = "" }, "--gate-attempts is required"},
		{func(p *params) { p.gateAttempts = "none" }, "--gate-attempts"},
		{func(p *params) { p.implementTier = "" }, "--implement-tier is required"},
		{func(p *params) { p.handBackLabel = "" }, "--hand-back-label is required"},
		{func(p *params) { p.denylist = "" }, "--denylist is required"},
		{func(p *params) { p.handOffLabel = "" }, "--hand-off-label is required"},
		{func(p *params) { p.ciWait = "" }, "--ci-wait is required"},
		{func(p *params) { p.ciCeiling = "soon" }, "--ci-ceiling"},
		{func(p *params) { p.ciFixes = "0" }, "--ci-fixes"},
		{func(p *params) { p.sizeSignal = "" }, "--size-signal is required"},
		{func(p *params) { p.sizeSignal = "big" }, "--size-signal"},
		{func(p *params) { p.freshSessionAt = "" }, "--fresh-session-at is required"},
		{func(p *params) { p.freshSessionAt = "-1" }, "--fresh-session-at"},
		{func(p *params) { p.freshSessionAt = "lots" }, "--fresh-session-at"},
		{func(p *params) { p.effectRounds = "" }, "--effect-rounds is required"},
		{func(p *params) { p.denylist = "src/[a" }, "--denylist"},
		{func(p *params) { p.reviewProcedure = "docs/operators-review.md" }, "--review-procedure"},
		{func(p *params) { p.sensitive = "internal/store/**" }, "--sensitive"},
		{func(p *params) { p.sensitive = "=internal/store/**" }, "--sensitive"},
		{func(p *params) { p.sensitive = "store=" }, "--sensitive"},
		{func(p *params) { p.sensitive = "store=src/[a" }, "--sensitive"},
		{func(p *params) { p.sensitive = "store=a/**;store=b/**" }, "--sensitive"},
		{func(p *params) { p.reviewProcedure = "javascript:alert(1)" }, "--review-procedure"},
	} {
		p := full
		tc.spoil(&p)
		if _, err := implementDeps(context.Background(), p, nil, tr); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("err = %v, want it to contain %q", err, tc.want)
		}
	}

	// A revision takes implementing's parameters, and its replay bound of
	// its own.
	full.replays = "2"
	rd, err := reviseDeps(context.Background(), full, nil, tr)
	if err != nil {
		t.Fatal(err)
	}
	if rd.Replays != 2 || rd.Gate != "go test ./..." || fmt.Sprint(rd.Remote.Untrusted) != fmt.Sprint(d.Remote.Untrusted) ||
		rd.SizeSignal != d.SizeSignal || rd.FreshAt != d.FreshAt || rd.AskReview == nil {
		t.Errorf("revise deps = %+v, want them read from the parameters", rd)
	}
	for _, tc := range []struct {
		replays, want string
	}{
		{"", "--replays is required"},
		{"-1", "--replays"},
		{"some", "--replays"},
	} {
		p := full
		p.replays = tc.replays
		if _, err := reviseDeps(context.Background(), p, nil, tr); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("replays %q: err = %v, want it to contain %q", tc.replays, err, tc.want)
		}
	}
	// No replay at all is a bound an operator may set: the first push made
	// during a revision hands it back.
	full.replays = "0"
	if rd, err := reviseDeps(context.Background(), full, nil, tr); err != nil || rd.Replays != 0 {
		t.Errorf("replays 0: deps = %+v, err = %v; want a bound of 0", rd, err)
	}
}

// What the implement kind logs - what CI caught that the local gate did not -
// reaches stderr from a hand-run and from the pool alike. Nothing else says
// it.
func TestImplementLogsReachStderr(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		// untilSignal is a command that runs until it is interrupted.
		untilSignal bool
	}{
		{"run", []string{"run", "implement", "--issue", "7"}, false},
		{"work", []string{"work", "--workers", "1", "--poll", "1s", "--token-wait", "1s", "--branch-prefix", "afk/"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withLoggingImplement(t, tc.untilSignal)
			args := append(tc.args, "--store", filepath.Join(t.TempDir(), "state.db"), "--lease", "1m")
			var stdout, stderr bytes.Buffer
			if got := Main(args, &stdout, &stderr); got != ExitOK {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", got, ExitOK, stderr.String())
			}
			if !strings.Contains(stderr.String(), "a catch\n") {
				t.Errorf("stderr = %q, want the implement kind's log line", stderr.String())
			}
		})
	}
}

// withLoggingImplement puts an implement kind in front of the command surface
// whose catalogue writes one line to the Log it was given. With interrupt, it
// then interrupts the process, which is how `work` stops.
func withLoggingImplement(t *testing.T, interrupt bool) {
	t.Helper()
	withCatalogue(t)
	was, wasImplement := catalogue, implementDeps
	implementDeps = func(context.Context, params, store.Store, *tracker) (*implement.Deps, error) {
		return &implement.Deps{}, nil
	}
	catalogue = func(d *deps) *transition.Registry {
		if d != nil && d.implement != nil && d.implement.Log != nil {
			d.implement.Log("a catch")
		}
		if interrupt && d != nil {
			self, err := os.FindProcess(os.Getpid())
			if err == nil {
				err = self.Signal(os.Interrupt)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		return transition.MustRegistry(transition.Transition{
			Name: "implement", Kind: store.KindImplement, From: "start",
			Run: func(context.Context, transition.In) (transition.Result, error) {
				return transition.Result{State: "done"}, nil
			},
		})
	}
	t.Cleanup(func() { catalogue, implementDeps = was, wasImplement })
}

// What intake takes with nobody asking is the eligibility label, held at the
// review-queue limit if there is one. Without the flag there is no limit. A
// limit counts the pull requests carrying the hand-off label, so it needs one.
func TestTakingIsReadFromTheParameters(t *testing.T) {
	for _, env := range []string{"AFK_ELIGIBILITY_LABEL", "AFK_REVIEW_QUEUE_LIMIT", "AFK_HAND_OFF_LABEL"} {
		t.Setenv(env, "")
	}
	for _, tc := range []struct {
		name string
		p    params
		want intake.Unattended
		err  string
	}{
		{name: "nothing", p: params{}},
		{name: "no limit", p: params{eligibilityLabel: "ready-for-agent", handOffLabel: "ready-for-review"}, want: unattended("ready-for-agent")},
		{
			name: "a limit",
			p:    params{eligibilityLabel: "ready-for-agent", reviewQueueLimit: "3", handOffLabel: "ready-for-review"},
			want: func() intake.Unattended {
				u := unattended("ready-for-agent")
				u.Queue = intake.ReviewQueue{Limit: 3, Label: "ready-for-review", Revise: store.KindRevise}
				return u
			}(),
		},
		{name: "a limit with no hand-off label", p: params{eligibilityLabel: "ready-for-agent", reviewQueueLimit: "3"}, err: "--hand-off-label"},
		{name: "a limit of none", p: params{eligibilityLabel: "ready-for-agent", reviewQueueLimit: "0", handOffLabel: "l"}, err: "--review-queue-limit"},
		{name: "a limit that is not a number", p: params{eligibilityLabel: "ready-for-agent", reviewQueueLimit: "three", handOffLabel: "l"}, err: "--review-queue-limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.p.taking()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Errorf("err = %v, want it to contain %q", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("taking = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// afk intake and afk work both take issues, so both take the limit, and afk
// intake the hand-off label it counts by. Bound on afk work alone, a hand-run
// pass could take every eligible issue at once.
func TestIntakeAndWorkBothTakeTheLimit(t *testing.T) {
	for _, env := range []string{"AFK_REPO", "AFK_APP_ID", "AFK_APP_KEY", "AFK_STORE", "AFK_LEASE"} {
		t.Setenv(env, "")
	}
	for _, args := range [][]string{
		{"intake", "--review-queue-limit", "0", "--hand-off-label", "l", "--eligibility-label", "e", "--repo", "o/n", "--app-id", "1", "--app-key", "/nonexistent"},
		{"work", "--review-queue-limit", "0", "--hand-off-label", "l", "--eligibility-label", "e"},
	} {
		var stdout, stderr bytes.Buffer
		Main(args, &stdout, &stderr)
		if strings.Contains(stderr.String(), "not defined") {
			t.Errorf("afk %s: %s", args[0], stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	code := Main([]string{"intake", "--review-queue-limit", "0", "--eligibility-label", "e", "--hand-off-label", "l", "--repo", "o/n", "--app-id", "1", "--app-key", "/nonexistent"}, &stdout, &stderr)
	if code != ExitUsage || !strings.Contains(stderr.String(), "--review-queue-limit") {
		t.Errorf("exit = %d, stderr:\n%s\nwant a usage error naming --review-queue-limit", code, stderr.String())
	}
}

// afk intake is nothing without a repository, and says so before it opens
// anything.
func TestIntakeNeedsARepository(t *testing.T) {
	t.Setenv("AFK_REPO", "")
	t.Setenv("AFK_APP_ID", "")
	t.Setenv("AFK_APP_KEY", "")

	var stdout, stderr bytes.Buffer
	code := Main([]string{"intake", "--store", filepath.Join(t.TempDir(), "state.db"), "--lease", "1m"}, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--repo") {
		t.Errorf("stderr does not name --repo:\n%s", stderr.String())
	}
}

// Every command's job kind starts at a state some transition runs from, or the
// command makes due a job the pool can only park.
func TestEveryCommandStartsWhereATransitionRuns(t *testing.T) {
	reg := catalogue(nil)
	for _, cmd := range commands() {
		if _, ok := reg.Next(cmd.Kind, cmd.Start); !ok {
			t.Errorf("%s starts %s jobs in %q, and no transition runs from there", cmd.Word, cmd.Kind, cmd.Start)
		}
		if want := subjectOf(cmd.Kind); cmd.On != want {
			t.Errorf("%s is read on a %s, and makes %s jobs, which are on a %s", cmd.Word, cmd.On, cmd.Kind, want)
		}
	}
}

// A pool with no implement configured does not answer /implement.
func TestAPoolAnswersOnlyTheCommandsItCanRun(t *testing.T) {
	reviewOnly := catalogue(&deps{review: standaloneDeps(t).review})
	var words []string
	for _, c := range runnable(commands(), reviewOnly) {
		words = append(words, c.Word)
	}
	if strings.Join(words, " ") != "/review" {
		t.Errorf("commands %v, want only /review", words)
	}
	// Nor, with no revise configured, /revise: it takes a replay bound
	// implementing does not.
	all := standaloneDeps(t)
	words = nil
	for _, c := range runnable(commands(), catalogue(&deps{review: all.review, implement: all.implement})) {
		words = append(words, c.Word)
	}
	if strings.Join(words, " ") != "/review /implement" {
		t.Errorf("commands %v, want /review and /implement", words)
	}
	if got := runnable(commands(), catalogue(standaloneDeps(t))); len(got) != len(commands()) {
		t.Errorf("a pool with every kind answers %d of %d commands", len(got), len(commands()))
	}
}

// Unattended work is the job /implement makes, on an issue, and a pool that
// cannot run it takes nothing unattended.
func TestUnattendedWorkIsTheJobImplementMakes(t *testing.T) {
	u := unattended("ready-for-agent")
	var cmd intake.Command
	for _, c := range commands() {
		if c.Word == implement.Word {
			cmd = c
		}
	}
	if u.Label != "ready-for-agent" || u.Kind != cmd.Kind || u.Start != cmd.Start || subjectOf(u.Kind) != store.SubjectIssue {
		t.Errorf("unattended = %+v, want %s's %s job from %q, on an issue", u, cmd.Word, cmd.Kind, cmd.Start)
	}
	if takeable(u, catalogue(&deps{review: standaloneDeps(t).review})) {
		t.Error("a pool with no implement configured takes issues unattended")
	}
	if !takeable(u, catalogue(standaloneDeps(t))) {
		t.Error("a pool with implement configured takes no issue unattended")
	}
}

// standaloneDeps is every kind's dependencies with nothing live behind them: a
// tracker that answers from memory, and a budget that is limited, so every
// transition reaches a decision from its starting state without a network, a
// model or a checkout.
func standaloneDeps(t *testing.T) *deps {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	return &deps{
		review: &review.Deps{
			Tracker: closedTracker{},
			Resolve: func(context.Context) (model.Candidates, error) {
				return nil, &model.LimitedError{ResetsAt: time.Now().Add(time.Hour)}
			},
			Bound:    1,
			Rounds:   1,
			TierWait: time.Hour,
			Login:    "afk-bot",
			StateDir: t.TempDir(),
		},
		implement: &implement.Deps{
			Tracker: closedTracker{},
			Login:   "afk-bot",
			Resolve: func(context.Context) (model.Candidates, error) {
				return nil, &model.LimitedError{ResetsAt: time.Now().Add(time.Hour)}
			},
			BranchPrefix:  "afk/",
			Bound:         1,
			Rounds:        1,
			TierWait:      time.Hour,
			Gate:          "false",
			Attempts:      1,
			HandBackLabel: "needs-decision",
			StateDir:      t.TempDir(),
		},
		revise: &revise.Deps{
			Tracker: closedTracker{},
			Store:   st,
			Login:   "afk-bot",
			Repo:    "o/n",
			Resolve: func(context.Context) (model.Candidates, error) {
				return nil, &model.LimitedError{ResetsAt: time.Now().Add(time.Hour)}
			},
			Bound:         1,
			Rounds:        1,
			TierWait:      time.Hour,
			Gate:          "false",
			Attempts:      1,
			HandOffLabel:  "ready-for-review",
			HandBackLabel: "needs-decision",
			StateDir:      t.TempDir(),
		},
	}
}

// closedTracker is an issue and a pull request that are closed and have
// nothing on them.
type closedTracker struct{}

func (closedTracker) PullRequest(_ context.Context, n int) (github.PullRequest, error) {
	return github.PullRequest{Number: n, State: "closed", HeadSHA: "abc"}, nil
}
func (closedTracker) Issue(_ context.Context, n int) (github.Issue, error) {
	return github.Issue{Number: n, State: "closed"}, nil
}
func (closedTracker) OpenPullRequests(context.Context) ([]github.PullRequest, error) {
	return nil, nil
}
func (closedTracker) Compare(context.Context, string, string) (string, error) { return "", nil }
func (closedTracker) Comments(context.Context, int) ([]github.Comment, error) { return nil, nil }
func (closedTracker) Reactions(context.Context, int64) ([]github.Reaction, error) {
	return nil, nil
}
func (closedTracker) Comment(context.Context, int, string) (github.Comment, error) {
	return github.Comment{}, nil
}
func (closedTracker) React(context.Context, int64, string) error { return nil }
func (closedTracker) Label(context.Context, int, string) error   { return nil }
func (closedTracker) Unlabel(context.Context, int, string) error { return nil }
func (closedTracker) IssueReactions(context.Context, int) ([]github.Reaction, error) {
	return nil, nil
}
func (closedTracker) ReactToIssue(context.Context, int, string) error { return nil }
func (closedTracker) CheckRuns(context.Context, string) ([]github.CheckRun, error) {
	return nil, nil
}
func (closedTracker) RequiredChecks(context.Context, string) ([]string, error) {
	return nil, nil
}

func (closedTracker) EditPullRequest(context.Context, int, string) error { return nil }
func (closedTracker) EditComment(context.Context, int64, string) error   { return nil }
func (closedTracker) PullRequestReviews(context.Context, int) ([]github.PullRequestReview, error) {
	return nil, nil
}
func (closedTracker) LineComments(context.Context, int, int64) ([]github.LineComment, error) {
	return nil, nil
}
func (closedTracker) PullRequestReviewReactions(context.Context, string) ([]github.Reaction, error) {
	return nil, nil
}
func (closedTracker) ReactToPullRequestReview(context.Context, string, string) error { return nil }
func (closedTracker) CreatePullRequest(context.Context, github.NewPullRequest) (github.PullRequest, error) {
	return github.PullRequest{}, nil
}
func (closedTracker) IssuesBy(context.Context, string) ([]github.Issue, error) { return nil, nil }
func (closedTracker) CreateIssue(context.Context, github.NewIssue) (github.Issue, error) {
	return github.Issue{}, nil
}
func (closedTracker) BlockedBy(context.Context, int) ([]github.Issue, error) { return nil, nil }
func (closedTracker) AddBlockedBy(context.Context, int, int64) error         { return nil }

// The floor and the fold cut are the skill's inputs, passed through (#124).
// Unset is not an error: the skill's own defaults hold, and none is chosen here.
func TestTheReviewsFloorAndFoldCutAreReadFromTheParameters(t *testing.T) {
	t.Setenv("AFK_REVIEW_FLOOR", "")
	t.Setenv("AFK_REVIEW_FOLD_CUT", "")

	rp, err := (&params{reviewFloor: "consider", reviewFoldCut: "120"}).review()
	if err != nil || rp.floor != "consider" || rp.foldCut != 120 {
		t.Errorf("review() = %+v, %v; want consider and 120", rp, err)
	}
	if rp, err := (&params{}).review(); err != nil || rp.floor != "" || rp.foldCut != 0 {
		t.Errorf("unset: review() = %+v, %v; want neither, and no error", rp, err)
	}
	t.Setenv("AFK_REVIEW_FLOOR", "blocker")
	t.Setenv("AFK_REVIEW_FOLD_CUT", "30")
	if rp, err := (&params{}).review(); err != nil || rp.floor != "blocker" || rp.foldCut != 30 {
		t.Errorf("from the environment: review() = %+v, %v; want blocker and 30", rp, err)
	}

	for _, tc := range []struct {
		p    params
		want string
	}{
		{params{reviewFloor: "nit"}, "--review-floor"},
		{params{reviewFoldCut: "small"}, "--review-fold-cut"},
		{params{reviewFoldCut: "0"}, "--review-fold-cut"},
	} {
		if _, err := tc.p.review(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("review(%+v) = %v, want an error naming %s", tc.p, err, tc.want)
		}
	}
}

func TestModelChoiceIsReadFromTheParameters(t *testing.T) {
	full := params{opencode: "/bin/opencode", enrolment: "/etc/afk/enrolment.json", reviewTier: "review", modelAttempts: "3", tierWait: "30m", modelTimeout: "45m"}

	t.Run("resolved", func(t *testing.T) {
		p := full
		p.reviewNeeds = "tool_call, input:image,"
		p.catalogueAge = "24h"
		m, err := p.model()
		if err != nil {
			t.Fatal(err)
		}
		if m.tier != "review" || m.attempts != 3 || m.tierWait != 30*time.Minute || m.timeout != 45*time.Minute || m.catalogueAge != 24*time.Hour {
			t.Errorf("model = %+v", m)
		}
		if fmt.Sprint(m.needs) != "[tool_call input:image]" {
			t.Errorf("needs = %v, want [tool_call input:image]", m.needs)
		}
	})

	for _, tc := range []struct {
		name  string
		spoil func(*params)
		want  string
	}{
		{"no opencode", func(p *params) { p.opencode = "" }, "--opencode is required"},
		{"no enrolment", func(p *params) { p.enrolment = "" }, "--enrolment is required"},
		{"no tier", func(p *params) { p.reviewTier = "" }, "--review-tier is required"},
		{"no attempt bound", func(p *params) { p.modelAttempts = "" }, "--model-attempts is required"},
		{"an attempt bound that is not a count", func(p *params) { p.modelAttempts = "some" }, "--model-attempts"},
		{"no tier wait", func(p *params) { p.tierWait = "" }, "--tier-wait is required"},
		{"no bound on a run", func(p *params) { p.modelTimeout = "" }, "--model-timeout is required"},
		{"a bound on a run that is not a duration", func(p *params) { p.modelTimeout = "long" }, "--model-timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, env := range []string{"AFK_OPENCODE", "AFK_ENROLMENT", "AFK_REVIEW_TIER", "AFK_MODEL_ATTEMPTS", "AFK_TIER_WAIT", "AFK_MODEL_TIMEOUT", "AFK_CATALOGUE_AGE", "AFK_REVIEW_NEEDS"} {
				t.Setenv(env, "")
			}
			p := full
			tc.spoil(&p)
			if _, err := p.model(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
