package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/review"
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
			name:     "budget has nothing to read",
			args:     []string{"budget"},
			want:     ExitUsage,
			stderrIs: "budget needs --budget-key",
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
		{name: "read and trimmed", p: params{notifyURL: "https://ntfy.example/afk", notifyKey: token}, token: "tk_secret"},
		{name: "a topic anyone may publish to", p: params{notifyURL: "https://ntfy.example/afk"}},
		{name: "no such file", p: params{notifyURL: "https://ntfy.example/afk", notifyKey: filepath.Join(dir, "absent")}, want: "--notify-key:"},
		{name: "a token with nowhere to publish", p: params{notifyKey: token}, want: "needs --notify-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AFK_NOTIFY_URL", "")
			t.Setenv("AFK_NOTIFY_KEY", "")

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

	var p params
	n, err := p.notifier()
	if err != nil {
		t.Fatal(err)
	}
	if n == nil || n.URL != "https://ntfy.example/from-the-unit" {
		t.Fatalf("notifier = %+v, want the URL from the environment", n)
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

// Review and intake reach the tracker through the one the command built, so a
// process has one token and one login (ADR 0005 §5).
func TestReviewAndIntakeShareTheCommandsTracker(t *testing.T) {
	for _, env := range []string{"AFK_BUDGET_KEY", "AFK_BUDGET_AGE", "AFK_BUDGET_AT", "AFK_CATALOGUE_AGE", "AFK_REVIEW_NEEDS"} {
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
	}

	deps, err := reviewDeps(context.Background(), p, nil, tr)
	if err != nil {
		t.Fatal(err)
	}
	in, err := newIntake(context.Background(), tr, nil, "holder", time.Minute)
	if err != nil {
		t.Fatal(err)
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
	for _, env := range []string{"AFK_BRANCH_PREFIX", "AFK_GATE", "AFK_GATE_ATTEMPTS", "AFK_IMPLEMENT_TIER", "AFK_IMPLEMENT_NEEDS", "AFK_HAND_BACK_LABEL", "AFK_DENYLIST",
		"AFK_BUDGET_KEY", "AFK_BUDGET_AGE", "AFK_BUDGET_AT", "AFK_CATALOGUE_AGE", "AFK_OPENCODE", "AFK_ENROLMENT", "AFK_MODEL_ATTEMPTS", "AFK_TIER_WAIT"} {
		t.Setenv(env, "")
	}
	tr := &tracker{client: &github.Client{Repo: "o/n"}, login: "afk-agent[bot]"}
	full := params{
		store:         filepath.Join(t.TempDir(), "state.db"),
		opencode:      "/bin/opencode",
		enrolment:     "/etc/afk/enrolment.json",
		modelAttempts: "3",
		tierWait:      "30m",
		branchPrefix:  "afk/",
		gate:          "go test ./...",
		gateAttempts:  "2",
		implementTier: "build",
		handBackLabel: "needs-decision",
		denylist:      ".github/**, flake.lock",
	}

	d, err := implementDeps(context.Background(), full, nil, tr)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tracker != tr.client || d.Login != tr.login {
		t.Errorf("deps = %+v, want the command's client and login", d)
	}
	if d.BranchPrefix != "afk/" || d.Gate != "go test ./..." || d.Attempts != 2 || d.HandBackLabel != "needs-decision" ||
		fmt.Sprint(d.Denylist) != "[.github/** flake.lock]" || d.Token == nil ||
		d.Bound != 3 || d.TierWait != 30*time.Minute || d.Remote != "https://github.com/o/n.git" || d.StateDir != filepath.Dir(full.store) {
		t.Errorf("deps = %+v, want them read from the parameters", d)
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
		{func(p *params) { p.denylist = "src/[a" }, "--denylist"},
	} {
		p := full
		tc.spoil(&p)
		if _, err := implementDeps(context.Background(), p, nil, tr); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("err = %v, want it to contain %q", err, tc.want)
		}
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

// standaloneDeps is every kind's dependencies with nothing live behind them: a
// tracker that answers from memory, and a budget that is limited, so every
// transition reaches a decision from its starting state without a network, a
// model or a checkout.
func standaloneDeps(t *testing.T) *deps {
	return &deps{
		review: &review.Deps{
			Tracker: closedTracker{},
			Resolve: func(context.Context) (model.Candidates, error) {
				return nil, &model.LimitedError{ResetsAt: time.Now().Add(time.Hour)}
			},
			Bound:    1,
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
			TierWait:      time.Hour,
			Gate:          "false",
			Attempts:      1,
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
func (closedTracker) Diff(context.Context, int) (string, error)               { return "", nil }
func (closedTracker) Comments(context.Context, int) ([]github.Comment, error) { return nil, nil }
func (closedTracker) Reactions(context.Context, int64) ([]github.Reaction, error) {
	return nil, nil
}
func (closedTracker) Comment(context.Context, int, string) (github.Comment, error) {
	return github.Comment{}, nil
}
func (closedTracker) React(context.Context, int64, string) error { return nil }
func (closedTracker) Label(context.Context, int, string) error   { return nil }
func (closedTracker) CreatePullRequest(context.Context, github.NewPullRequest) (github.PullRequest, error) {
	return github.PullRequest{}, nil
}

func TestModelChoiceIsReadFromTheParameters(t *testing.T) {
	full := params{opencode: "/bin/opencode", enrolment: "/etc/afk/enrolment.json", reviewTier: "review", modelAttempts: "3", tierWait: "30m"}

	t.Run("resolved", func(t *testing.T) {
		p := full
		p.reviewNeeds = "tool_call, input:image,"
		p.catalogueAge = "24h"
		m, err := p.model()
		if err != nil {
			t.Fatal(err)
		}
		if m.tier != "review" || m.attempts != 3 || m.tierWait != 30*time.Minute || m.catalogueAge != 24*time.Hour {
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, env := range []string{"AFK_OPENCODE", "AFK_ENROLMENT", "AFK_REVIEW_TIER", "AFK_MODEL_ATTEMPTS", "AFK_TIER_WAIT", "AFK_CATALOGUE_AGE", "AFK_REVIEW_NEEDS"} {
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
