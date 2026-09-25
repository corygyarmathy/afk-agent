package implement_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
)

var (
	first  = model.Ref{Provider: "opencode-go", Model: "first"}
	second = model.Ref{Provider: "opencode-go", Model: "second"}
)

// coder is a fake model. Each run does what the next of its turns says, in the
// workspace; a run with no turn left writes nothing and succeeds.
type coder struct {
	mu    sync.Mutex
	turns []func(dir string) error
	asked []opencode.Request

	// issues is the spec file as each run found it, and logs the gate's
	// output.
	issues []string
	logs   []string

	// sessions is the sessions opencode has. A run that names one it does
	// not have fails as opencode does.
	sessions map[string]bool
}

func (m *coder) Run(_ context.Context, req opencode.Request) (opencode.Reply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, req)
	spec, _ := os.ReadFile(filepath.Join(req.Dir, ".git", "afk-issue.md"))
	m.issues = append(m.issues, string(spec))
	log, _ := os.ReadFile(filepath.Join(req.Dir, ".git", "afk-gate.log"))
	m.logs = append(m.logs, string(log))

	if m.sessions == nil {
		m.sessions = map[string]bool{}
	}
	session := req.Session
	if session == "" {
		session = fmt.Sprintf("ses_%d", len(m.asked))
		m.sessions[session] = true
	} else if !m.sessions[session] {
		return opencode.Reply{}, &opencode.SessionGoneError{Session: session}
	}

	if len(m.turns) > 0 {
		turn := m.turns[0]
		m.turns = m.turns[1:]
		if err := turn(req.Dir); err != nil {
			return opencode.Reply{}, err
		}
	}
	return opencode.Reply{Text: "Done.", Session: session}, nil
}

func (m *coder) then(turns ...func(dir string) error) { m.turns = append(m.turns, turns...) }

// commit is a turn that writes a file and commits it.
func commit(name string) func(dir string) error {
	return func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			return err
		}
		if _, err := run(dir, "git", "add", name); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "add "+name)
		return err
	}
}

// fail is a turn that fails as a throttled provider does.
func fail(ref model.Ref) func(string) error {
	return func(string) error {
		return &opencode.TransientError{Model: ref, Err: errors.New("429 Too Many Requests")}
	}
}

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// bareRemote is a bare repository with one commit on main, standing in for
// the tracker's repository. Offline: the remote is a directory.
func bareRemote(t *testing.T) string {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "afk", "GIT_AUTHOR_EMAIL": "afk@example.invalid",
		"GIT_COMMITTER_NAME": "afk", "GIT_COMMITTER_EMAIL": "afk@example.invalid",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	seed := filepath.Join(dir, "seed")
	for _, step := range [][]string{
		{"git", "init", "--quiet", "--bare", "--initial-branch=main", remote},
		{"git", "clone", "--quiet", remote, seed},
	} {
		if _, err := run(dir, step[0], step[1:]...); err != nil {
			t.Fatal(err)
		}
	}
	if err := commit("README")(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := run(seed, "git", "push", "--quiet", "origin", "HEAD:main"); err != nil {
		t.Fatal(err)
	}
	return remote
}

// drive runs whatever transition the job's state calls for until the job comes
// to rest, defers, or reaches a state nothing runs from yet, the way the pool
// would. It returns the errors the transitions returned along the way.
func (f *fixture) drive() []error {
	f.t.Helper()
	var errs []error
	for range 30 {
		job := f.now()
		t, ok := f.reg.Next(job.Kind, job.State)
		if !ok {
			return errs
		}
		if _, err := f.run.Run(context.Background(), t.Name, job.ID); err != nil {
			errs = append(errs, err)
		}
		job = f.now()
		if job.NextRunAt.IsZero() || job.State == implement.Deferred {
			return errs
		}
	}
	f.t.Fatalf("the job never came to rest; it is in %q", f.now().State)
	return errs
}

func (f *fixture) workspace() string {
	return filepath.Join(f.deps.StateDir, "workspaces", f.job.ID)
}

// remoteBranches is the branches on the remote.
func (f *fixture) remoteBranches() string {
	f.t.Helper()
	out, err := run(f.remote, "git", "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// A session that commits work the gate passes goes on to the push, from a new
// branch, with its commits in the workspace - and has been told what to do.
func TestWorkThatPassesTheGateIsReadyToPush(t *testing.T) {
	tr := newTracker(github.Comment{ID: 1, Login: "alice", Association: "OWNER", Body: "/implement keep it small\nand test it"})
	tr.body = "Jobs are reserved before they run."
	tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f := setup(t, tr)
	f.model.then(commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}

	ws := f.workspace()
	if branch, _ := run(ws, "git", "symbolic-ref", "--short", "HEAD"); branch != "afk/7-1" {
		t.Errorf("the workspace is on %q, want afk/7-1", branch)
	}
	if n, _ := run(ws, "git", "rev-list", "--count", "origin/main..HEAD"); n != "1" {
		t.Errorf("%s commits on the branch, want 1", n)
	}

	if len(f.model.asked) != 1 {
		t.Fatalf("the model was asked %d times, want 1", len(f.model.asked))
	}
	req := f.model.asked[0]
	if req.Model != first || req.Session != "" || req.Dir != ws {
		t.Errorf("request = %+v, want a new session on the first candidate in the workspace", req)
	}
	for _, want := range []string{"#7", "`implement` skill", "afk/7-1", f.deps.Gate, ".git/afk-issue.md"} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("the prompt does not contain %q:\n%s", want, req.Prompt)
		}
	}
	for _, want := range []string{"# Issue #7: Reserve a job", "Jobs are reserved before they run.", "keep it small\nand test it"} {
		if !strings.Contains(f.model.issues[0], want) {
			t.Errorf("the spec does not contain %q:\n%s", want, f.model.issues[0])
		}
	}
}

// A failing gate goes back to the session that wrote the commit, with what
// the gate said, and the fix lands on the same branch.
func TestAFailingGateGoesBackToTheSameSession(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("attempt"), commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	if len(f.model.asked) != 2 {
		t.Fatalf("the model was asked %d times, want 2", len(f.model.asked))
	}
	retry := f.model.asked[1]
	if retry.Session != "ses_1" {
		t.Errorf("the retry ran in session %q, want the first run's, ses_1", retry.Session)
	}
	if !strings.Contains(retry.Prompt, "failed") || !strings.Contains(retry.Prompt, ".git/afk-gate.log") {
		t.Errorf("the retry prompt does not say the gate failed and where its output is:\n%s", retry.Prompt)
	}
	if !strings.Contains(f.model.logs[1], "FAIL: no ok") {
		t.Errorf("the gate's output was not left for the session: %q", f.model.logs[1])
	}
	if n, _ := run(f.workspace(), "git", "rev-list", "--count", "origin/main..HEAD"); n != "2" {
		t.Errorf("%s commits on the branch, want both sessions' on the one branch", n)
	}
}

// Running out of attempts hands back on the issue, pushes nothing, and leaves
// nothing behind.
func TestRunningOutOfAttemptsHandsBackOnTheIssue(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("a"), commit("b"), commit("c"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 3 {
		t.Errorf("the model was asked %d times, want the 3 attempts", len(f.model.asked))
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 {
		t.Fatalf("%d comments, want one hand-back", len(posted))
	}
	for _, want := range []string{"without opening a pull request", "3 attempts", "FAIL: no ok", "Nothing was pushed", "`/implement` again"} {
		if !strings.Contains(posted[0].Body, want) {
			t.Errorf("the hand-back does not say %q:\n%s", want, posted[0].Body)
		}
	}
	if fmt.Sprint(f.tr.labels) != "[needs-decision]" {
		t.Errorf("labels %v, want the hand-back label once", f.tr.labels)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
	if b := f.remoteBranches(); b != "main" {
		t.Errorf("the remote has %q, want nothing pushed", b)
	}
	if _, err := os.Stat(f.workspace()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the workspace was left behind: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.deps.StateDir, "progress")); len(entries) != 0 {
		t.Errorf("progress was left behind: %v", entries)
	}

	// A later /implement that fails again says so again, although nothing
	// was pushed and the branch name is free for it to take once more.
	f.tr.comments = append(f.tr.comments, command(2))
	f.restart()
	f.model.then(commit("a"), commit("b"), commit("c"))
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if posted := f.tr.byAgent(); len(posted) != 2 {
		t.Errorf("%d hand-backs after a second failed run, want 2", len(posted))
	}
}

// A session that commits nothing has nothing to push, and another attempt
// would be spending money on the same answer.
func TestASessionThatCommitsNothingHandsBack(t *testing.T) {
	f := setup(t, newTracker())

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want 1", len(f.model.asked))
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "without committing anything") {
		t.Fatalf("comments %+v, want one hand-back saying nothing was committed", posted)
	}
}

// Uncommitted changes to tracked files are a failure the session is told
// about: the gate reads the commits, which is what would be pushed.
func TestUncommittedChangesGoBackToTheSession(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := commit("a")(dir); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "a"), []byte("changed\n"), 0o644)
	}, func(dir string) error {
		if _, err := run(dir, "git", "commit", "--quiet", "--all", "-m", "change a"); err != nil {
			return err
		}
		return commit("ok")(dir)
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	if !strings.Contains(f.model.asked[1].Prompt, "uncommitted") || !strings.Contains(f.model.logs[1], "M a") {
		t.Errorf("the session was not told what was uncommitted:\n%s\n%s", f.model.asked[1].Prompt, f.model.logs[1])
	}
}

// What a session leaves untracked - the gate's own output, say - is cleaned
// away before the gate runs, rather than failing the work unread.
func TestUntrackedLeftoversDoNotFailTheWork(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := commit("ok")(dir); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "coverage.out"), nil, 0o644)
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching || len(f.model.asked) != 1 {
		t.Fatalf("job in %q after %d runs, want %q after 1", j.State, len(f.model.asked), implement.Watching)
	}
	if _, err := os.Stat(filepath.Join(f.workspace(), "coverage.out")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the leftover is still in the workspace: %v", err)
	}
}

// A file the work needs but the session never added is cleaned away too, and
// the gate, run without it, says what is missing.
func TestAnUnaddedFileFailsTheGate(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := commit("a")(dir); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "ok"), nil, 0o644)
	}, commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching || len(f.model.asked) != 2 {
		t.Fatalf("job in %q after %d runs, want %q after 2", j.State, len(f.model.asked), implement.Watching)
	}
	if !strings.Contains(f.model.logs[1], "FAIL: no ok") {
		t.Errorf("the gate's output was not left for the session: %q", f.model.logs[1])
	}
}

// A session that leaves the branch hands back at once. Its commits are not
// where the push would take them from, and letting the next run start over
// would let the gate's attempts start over with it.
func TestASessionThatLeavesTheBranchHandsBack(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if _, err := run(dir, "git", "switch", "--quiet", "--create", "elsewhere"); err != nil {
			return err
		}
		return commit("ok")(dir)
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("the model was asked %d times, want 1", len(f.model.asked))
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "left `afk/7-1` for `elsewhere`") {
		t.Fatalf("comments %+v, want one hand-back saying the session left the branch", posted)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
}

// A session that is gone - a rebuilt host - is not the end of the job: a new
// session is given the failure instead.
func TestAMissingSessionFallsBackToANewOneGivenTheFailure(t *testing.T) {
	f := setup(t, newTracker())
	// The first run makes a session, and then opencode forgets it. A turn
	// runs under the model's lock, so it may touch the sessions directly.
	f.model.then(func(dir string) error {
		f.model.sessions = map[string]bool{}
		return commit("attempt")(dir)
	}, commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	if len(f.model.asked) != 3 {
		t.Fatalf("the model was asked %d times, want a first run, a resume that found no session, and a new one", len(f.model.asked))
	}
	fresh := f.model.asked[2]
	if fresh.Session != "" || !strings.Contains(fresh.Prompt, "not the first attempt") || !strings.Contains(fresh.Prompt, ".git/afk-gate.log") {
		t.Errorf("request = %+v, want a new session told about the failure", fresh)
	}
}

// A workspace lost before the gate - a wiped state directory - starts the work
// over. Nothing was pushed, so nothing is lost but a model run.
func TestALostWorkspaceStartsTheWorkOver(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := commit("ok")(dir); err != nil {
			return err
		}
		return os.RemoveAll(filepath.Join(f.deps.StateDir, "workspaces"))
	}, commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching || len(f.model.asked) != 2 {
		t.Fatalf("job in %q after %d runs, want %q after 2", j.State, len(f.model.asked), implement.Watching)
	}
	if f.model.asked[1].Session != "" {
		t.Errorf("the second run continued session %q, want a new one", f.model.asked[1].Session)
	}
}

// A transient failure tries the next candidate, and an exhausted tier defers.
func TestATransientFailureTriesTheNextModelThenDefers(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(fail(first), commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if got := []model.Ref{f.model.asked[0].Model, f.model.asked[1].Model}; got[0] != first || got[1] != second {
		t.Errorf("tried %v, want first then second", got)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Errorf("job in %q, want %q", j.State, implement.Watching)
	}

	g := setup(t, newTracker())
	g.model.then(fail(first), fail(second))
	if errs := g.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := g.now(); j.State != implement.Deferred || !j.NextRunAt.Equal(now.Add(g.deps.TierWait)) {
		t.Errorf("job = %+v, want it deferred by the tier wait", j)
	}
	if _, err := g.run.Run(context.Background(), "implement-resume", g.job.ID); err != nil {
		t.Fatal(err)
	}
	if j := g.now(); j.State != implement.Implementing || j.Attempts != 0 {
		t.Errorf("job = %+v, want it back at the work from the first model", j)
	}
}

// What a session did before it failed transiently is not left for the next
// model, which is given the first-run prompt and would not know it was there.
func TestATransientFailureLeavesNothingForTheNextModel(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := commit("partial")(dir); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "scratch"), nil, 0o644); err != nil {
			return err
		}
		return fail(first)(dir)
	}, commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	ws := f.workspace()
	if n, _ := run(ws, "git", "rev-list", "--count", "origin/main..HEAD"); n != "1" {
		t.Errorf("%s commits on the branch, want only the second model's", n)
	}
	for _, name := range []string{"partial", "scratch"} {
		if _, err := os.Stat(filepath.Join(ws, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was left for the next model: %v", name, err)
		}
	}
}

// A limited budget defers to the reset the provider gave.
func TestALimitedBudgetDefersToTheReset(t *testing.T) {
	f := setup(t, newTracker())
	reset := now.Add(3 * time.Hour)
	f.deps.Resolve = func(context.Context) (model.Candidates, error) {
		return nil, &model.LimitedError{ResetsAt: reset}
	}
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Deferred || !j.NextRunAt.Equal(reset) {
		t.Errorf("job = %+v, want it deferred to %s", j, reset)
	}
	if len(f.model.asked) != 0 {
		t.Error("a model ran on a limited budget")
	}
}

// A branch the agent already pushed for the issue is never reused.
func TestABranchAlreadyOnTheRemoteIsNotReused(t *testing.T) {
	f := setup(t, newTracker())
	if _, err := run(f.remote, "git", "branch", "afk/7-1", "main"); err != nil {
		t.Fatal(err)
	}
	f.model.then(commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if branch, _ := run(f.workspace(), "git", "symbolic-ref", "--short", "HEAD"); branch != "afk/7-2" {
		t.Errorf("the workspace is on %q, want afk/7-2", branch)
	}
}

func TestInstructionsAreWhatFollowsTheWord(t *testing.T) {
	for body, want := range map[string]string{
		"/implement":                        "",
		"  /implement  \n":                  "",
		"/implement keep it small":          "keep it small",
		"/implement\nuse the store\nthanks": "use the store\nthanks",
	} {
		if got := implement.Instructions(github.Comment{Body: body}); got != want {
			t.Errorf("Instructions(%q) = %q, want %q", body, got, want)
		}
	}
}
