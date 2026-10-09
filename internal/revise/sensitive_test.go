package revise_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
)

// description is an agent's pull request description with sensitive as its
// sensitive line, or none: Go's fixed part, the reminder, and the session's
// part, which a revision never rewrites.
func description(line string) string {
	top := "<!-- afk:implement issue=7 -->\nCloses #7.\n\n"
	if line != "" {
		top += line + "\n\n"
	}
	return top + sensitive.Reminder + " (the agent has no link to the procedure): read #7 first, then this, then the diff from **Start here**.\n\n## Start here\n\nThe widget is renamed.\n"
}

// sensitiveFixture is revisionFixture with one kind of sensitive path, infra,
// and a log that is kept.
func sensitiveFixture(t *testing.T) (*revFixture, *[]string) {
	t.Helper()
	f := revisionFixture(t)
	f.deps.Sensitive = []sensitive.Path{{Label: "infra", Globs: []string{"infra/**"}}}
	var mu sync.Mutex
	var log []string
	f.deps.Log = func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		log = append(log, msg)
	}
	return f, &log
}

// writeOn is a turn that writes each of files, which may be in a directory,
// and commits them with the gate's `ok` file.
func writeOn(files ...string) func(dir string) error {
	return func(dir string) error {
		for _, name := range append(files, "ok") {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
				return err
			}
			if _, err := run(dir, "git", "add", name); err != nil {
				return err
			}
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "write "+strings.Join(files, " "))
		return err
	}
}

// removeOn is a turn that removes name and commits that, with the gate's `ok`
// file.
func removeOn(name string) func(dir string) error {
	return func(dir string) error {
		if _, err := run(dir, "git", "rm", "--quiet", name); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "ok"), []byte("ok\n"), 0o644); err != nil {
			return err
		}
		if _, err := run(dir, "git", "add", "ok"); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "remove "+name)
		return err
	}
}

// appendOn is a step that adds a line to name and commits it.
func appendOn(name string) func(dir string) error {
	return func(dir string) error {
		fh, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := fh.WriteString("one more line\n"); err != nil {
			fh.Close()
			return err
		}
		if err := fh.Close(); err != nil {
			return err
		}
		_, err = run(dir, "git", "commit", "--quiet", "--all", "-m", "append to "+name)
		return err
	}
}

// onRemote runs steps in a clone of the fixture's remote, with branch checked
// out, and returns the commit branch is at in the clone once they ran. Each
// step is a git command's arguments, or a turn.
func (f *revFixture) onRemote(branch string, steps ...any) string {
	f.t.Helper()
	dir := f.t.TempDir()
	if _, err := run(dir, "git", "clone", "--quiet", f.remote, dir); err != nil {
		f.t.Fatal(err)
	}
	if _, err := run(dir, "git", "switch", "--quiet", branch); err != nil {
		f.t.Fatal(err)
	}
	for _, step := range steps {
		var err error
		switch s := step.(type) {
		case []string:
			_, err = run(dir, "git", s...)
		case func(string) error:
			err = s(dir)
		}
		if err != nil {
			f.t.Fatal(err)
		}
	}
	out, err := run(dir, "git", "rev-parse", "HEAD")
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// sendBack has the pull request at head, as the operator left it, and sends it
// back and claims it.
func (f *revFixture) sendBack(head string) {
	f.t.Helper()
	f.head = head
	f.tr.PullRequests[12].HeadSHA = head
	f.tr.Say(12, send(1, "/revise Rename Foo to Bar."))
	f.claim()
}

// size is the whole pull request's size the revision kept for its reply.
func (f *revFixture) size() (lines, tests int) {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.deps.StateDir, "progress", jobID()+".json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var p struct {
		Lines int `json:"lines"`
		Tests int `json:"tests"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		f.t.Fatal(err)
	}
	return p.Lines, p.Tests
}

// A revision's push that newly touches a sensitive path puts it in the
// description's sensitive line, and leaves the rest of the description as it
// was.
func TestARevisionThatTouchesASensitivePathNamesIt(t *testing.T) {
	f, _ := sensitiveFixture(t)
	f.tr.PullRequests[12].Body = description("")
	f.sendBack(f.head)
	f.model.then(writeOn("infra/main.tf"))

	if job := f.step(revise.Watching); job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Watching)
	}
	want := description("**Sensitive:** infra (`infra/main.tf`)")
	if f.tr.PullRequests[12].Body != want {
		t.Errorf("the description is\n%s\nwant\n%s", f.tr.PullRequests[12].Body, want)
	}
}

// A revision that stops touching a sensitive path takes it off the line, and
// the line goes when nothing is left on it.
func TestARevisionThatStopsTouchingASensitivePathDropsIt(t *testing.T) {
	f, _ := sensitiveFixture(t)
	head := f.onRemote("feature", writeOn("infra/main.tf"), []string{"push", "--quiet", "origin", "feature"})
	f.tr.PullRequests[12].Body = description("**Sensitive:** infra (`infra/main.tf`)")
	f.sendBack(head)
	f.model.then(removeOn("infra/main.tf"))

	if job := f.step(revise.Watching); job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Watching)
	}
	if want := description(""); f.tr.PullRequests[12].Body != want {
		t.Errorf("the description is\n%s\nwant\n%s", f.tr.PullRequests[12].Body, want)
	}
}

// A pull request rebased onto a newer base before it is sent back is measured
// from where it now meets its base, and so is one whose base moved on since:
// neither the sensitive line nor the size counts the base's commits as the pull
// request's.
func TestARebasedPullRequestIsMeasuredAgainstItsBasesTip(t *testing.T) {
	f, _ := sensitiveFixture(t)
	f.tr.PullRequests[12].Body = description("")
	// The base moves on with a sensitive path, the operator rebases the pull
	// request onto it, and then the base moves on again.
	f.onRemote("main", writeOn("infra/old.tf"), []string{"push", "--quiet", "origin", "main"})
	head := f.onRemote("feature",
		[]string{"fetch", "--quiet", "origin"},
		[]string{"rebase", "--quiet", "origin/main"},
		[]string{"push", "--quiet", "--force", "origin", "feature"})
	f.onRemote("main", writeOn("infra/new.tf"), appendOn("README"), []string{"push", "--quiet", "origin", "main"})
	f.sendBack(head)
	f.model.then(writeOn("infra/main.tf"))

	if job := f.step(revise.Watching); job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Watching)
	}
	if want := description("**Sensitive:** infra (`infra/main.tf`)"); f.tr.PullRequests[12].Body != want {
		t.Errorf("the description is\n%s\nwant\n%s", f.tr.PullRequests[12].Body, want)
	}
	// The pull request's own: feature.txt, and the revision's infra/main.tf,
	// a line each. The base's ok came in with the rebase, and the revision's
	// is the same file. The README line the base added since is not the pull
	// request's, and nor are the files it added.
	if lines, tests := f.size(); lines != 2 || tests != 0 {
		t.Errorf("the size kept is %d lines and %d of tests, want 2 and 0", lines, tests)
	}
}

// An edit of the description that never lands is logged, and the revision
// goes on.
func TestAnEditThatNeverLandsIsLoggedAndTheRevisionGoesOn(t *testing.T) {
	f, log := sensitiveFixture(t)
	f.tr.PullRequests[12].Body = description("")
	f.tr.FailOn("EditPullRequest", errors.New("502 Bad Gateway"))
	f.sendBack(f.head)
	f.model.then(writeOn("infra/main.tf"))

	if job := f.step(revise.Watching); job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s: an edit that never lands costs the revision nothing", job.State, revise.Watching)
	}
	if got := f.tr.Writes["EditPullRequest"]; got != f.deps.Rounds {
		t.Errorf("the description was edited %d times, want %d", got, f.deps.Rounds)
	}
	if f.tr.PullRequests[12].Body != description("") {
		t.Errorf("the description changed:\n%s", f.tr.PullRequests[12].Body)
	}
	if f.handedBack() {
		t.Error("the revision was handed back")
	}
	said := strings.Join(*log, "\n")
	if !strings.Contains(said, "never showed them") {
		t.Errorf("nothing logged says the edit never landed:\n%s", said)
	}
}

// A pull request into a branch other than the default is measured against
// that branch, and the diff its session is given starts there too: the
// commits the branch has and the default branch lacks are not the pull
// request's.
func TestAPullRequestIntoAnotherBranchIsMeasuredAgainstIt(t *testing.T) {
	f, _ := sensitiveFixture(t)
	f.tr.PullRequests[12].Body = description("")
	f.tr.PullRequests[12].BaseRef = "release"
	f.onRemote("main", []string{"switch", "--quiet", "-c", "release"}, writeOn("infra/old.tf"), []string{"push", "--quiet", "origin", "release"})
	head := f.onRemote("feature",
		[]string{"fetch", "--quiet", "origin"},
		[]string{"rebase", "--quiet", "origin/release"},
		[]string{"push", "--quiet", "--force", "origin", "feature"})
	f.sendBack(head)
	var given string
	f.model.then(func(dir string) error {
		b, err := os.ReadFile(filepath.Join(dir, ".git", "afk-send-back.md"))
		given = string(b)
		if err != nil {
			return err
		}
		return writeOn("infra/main.tf")(dir)
	})

	if job := f.step(revise.Watching); job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Watching)
	}
	if strings.Contains(given, "infra/old.tf") {
		t.Errorf("the diff the session was given has release's own infra/old.tf:\n%s", given)
	}
	if want := description("**Sensitive:** infra (`infra/main.tf`)"); f.tr.PullRequests[12].Body != want {
		t.Errorf("the description is\n%s\nwant\n%s", f.tr.PullRequests[12].Body, want)
	}
	if lines, tests := f.size(); lines != 2 || tests != 0 {
		t.Errorf("the size kept is %d lines and %d of tests, want 2 and 0", lines, tests)
	}
}

// A push that cannot be measured - here, the pull request was retargeted onto
// a branch that is not there - is logged and goes on: the revision is pushed,
// and the sensitive line is left as it was.
func TestAPushThatCannotBeMeasuredIsLoggedAndGoesOn(t *testing.T) {
	f, log := sensitiveFixture(t)
	f.tr.PullRequests[12].Body = description("")
	f.sendBack(f.head)
	f.model.then(func(dir string) error {
		f.tr.PullRequests[12].BaseRef = "gone"
		return writeOn("infra/main.tf")(dir)
	})

	if job := f.step(revise.Watching); job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s: a measure that fails costs the revision nothing", job.State, revise.Watching)
	}
	if f.handedBack() {
		t.Error("the revision was handed back")
	}
	if at := f.onRemote("feature"); at == f.head {
		t.Error("the revision was not pushed")
	}
	if got := f.tr.Writes["EditPullRequest"]; got != 0 {
		t.Errorf("the description was edited %d times, want none: what the push touches is not known", got)
	}
	said := strings.Join(*log, "\n")
	if !strings.Contains(said, "was not measured") {
		t.Errorf("nothing logged says the push was not measured:\n%s", said)
	}
}
