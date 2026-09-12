package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// withCatalogue puts a transition in front of the command surface for one test.
func withCatalogue(t *testing.T, ts ...transition.Transition) {
	t.Helper()
	reg := transition.MustRegistry(ts...)
	was := catalogue
	catalogue = func() *transition.Registry { return reg }
	t.Cleanup(func() { catalogue = was })
}

// review is a transition that decides something and touches nothing: no
// network, no model, no daemon.
func review(next string) transition.Transition {
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
			name:     "budget has nothing to read",
			args:     []string{"budget"},
			want:     ExitUsage,
			stderrIs: "budget needs --budget-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withCatalogue(t, review("reviewed"))
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

	var p params
	o, err := p.budget()
	if err != nil {
		t.Fatal(err)
	}
	if o != nil {
		t.Fatalf("observer = %+v, want none", o)
	}
}

// The acceptance criterion of #2, at the command line: a transition runs with
// no daemon present, named by the tracker subject rather than by a job id. The
// job does not have to exist first - the subject is what names it.
func TestRunAgainstATrackerSubjectWithNoDaemon(t *testing.T) {
	withCatalogue(t, review("reviewed"))
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
	withCatalogue(t, review("reviewed"))
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
	withCatalogue(t, review("reviewed"))
	t.Setenv("AFK_STORE", filepath.Join(t.TempDir(), "state.db"))
	t.Setenv("AFK_LEASE", "1m")

	var stdout, stderr bytes.Buffer
	if got := Main([]string{"run", "review", "--pr", "12"}, &stdout, &stderr); got != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got, ExitOK, stderr.String())
	}
}

func TestRunAgainstAJobThatIsNotThere(t *testing.T) {
	withCatalogue(t, review("reviewed"))
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
	for _, tr := range catalogue().All() {
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
	if err := runsStandalone(t, review("reviewed")); err != nil {
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

	typ := store.SubjectPR
	if tr.Kind == store.KindImplement {
		typ = store.SubjectIssue
	}
	job, err := s.Ensure(ctx, tr.Kind, store.Subject{Type: typ, Number: 1}, tr.From, time.Now())
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
