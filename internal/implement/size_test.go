package implement_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/intake"
)

// commitLines is a turn that writes a file of n lines and commits it.
func commitLines(name string, n int) func(dir string) error {
	return func(dir string) error {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "%s %d\n", name, i)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
			return err
		}
		if _, err := run(dir, "git", "add", name); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "add "+name)
		return err
	}
}

// big is a turn whose work passes the gate at 12 changed lines, and 5 of
// tests.
func big(dir string) error {
	for _, turn := range []func(string) error{commitLines("job.go", 11), commitLines("job_test.go", 5), commit("ok")} {
		if err := turn(dir); err != nil {
			return err
		}
	}
	return nil
}

// commanded is an /implement with instructions, for the job to claim.
func commanded(tr *tracker, id int64, body string) {
	tr.comments = append(tr.comments, github.Comment{ID: id, Login: "alice", Association: "OWNER", Body: body})
}

// Over the size signal, the work is pushed and no pull request is opened. The
// issue is told the counts, the signal and the branch, so the work is kept
// and a human decides what becomes of it.
func TestWorkOverTheSizeSignalIsPushedAndHandedBack(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.SizeSignal = 10
	f.model.then(big)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.tr.opened) != 0 {
		t.Errorf("%d pull requests opened, want none", len(f.tr.opened))
	}
	at, err := run(f.remote, "git", "rev-parse", "refs/heads/afk/7-1")
	if err != nil {
		t.Fatalf("the branch was not pushed: %v", err)
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 || f.tr.commentedOn[0] != issue {
		t.Fatalf("comments %+v on %v, want one hand-back on the issue", posted, f.tr.commentedOn)
	}
	for _, want := range []string{"without opening a pull request", "12 changed lines", "5 changed lines of tests", "size signal of 10", "open one from it by hand", "`afk/7-1` is on the remote at `" + at[:12] + "`"} {
		if !strings.Contains(posted[0].Body, want) {
			t.Errorf("the hand-back does not say %q:\n%s", want, posted[0].Body)
		}
	}
	if strings.Contains(posted[0].Body, "Nothing was pushed") {
		t.Errorf("the hand-back says nothing was pushed:\n%s", posted[0].Body)
	}
	if fmt.Sprint(f.tr.labels) != "[needs-decision]" || f.tr.labelledOn[0] != issue {
		t.Errorf("labels %v on %v, want the hand-back label on the issue", f.tr.labels, f.tr.labelledOn)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
}

// The command's own instructions are the only override: the person who asked
// for one pull request has consented to its size in advance.
func TestOverTheSizeSignalWithOnePullRequestAskedForOpensIt(t *testing.T) {
	tr := newTracker()
	commanded(tr, 1, "/implement one PR, don't split")
	f := setup(t, tr)
	f.deps.SizeSignal = 10
	f.model.then(big)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching || len(f.tr.opened) != 1 {
		t.Errorf("job in %q with %d pull requests, want %q with 1", j.State, len(f.tr.opened), implement.Watching)
	}
	if posted := f.tr.byAgent(); len(posted) != 0 {
		t.Errorf("the agent said %+v, want nothing", posted)
	}
}

// The override is this job's own command's. One an earlier job claimed says
// nothing about work nobody has commanded since.
func TestAnOlderCommandDoesNotOverrideTheSizeSignal(t *testing.T) {
	tr := newTracker()
	commanded(tr, 1, "/implement one PR, don't split")
	tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f := setup(t, tr)
	f.deps.SizeSignal = 10
	f.model.then(big)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.tr.opened) != 0 {
		t.Errorf("%d pull requests opened, want none", len(f.tr.opened))
	}
	if strings.Contains(f.model.asked[0].Prompt, "one pull request, whatever its size") {
		t.Errorf("the session was told to make one pull request:\n%s", f.model.asked[0].Prompt)
	}
	if posted := f.tr.byAgent(); len(posted) != 1 || !strings.Contains(posted[0].Body, "size signal of 10") {
		t.Errorf("the agent said %+v, want the size hand-back", posted)
	}
}

// At the signal or under it, nothing changes: tests do not count towards it.
func TestWorkAtTheSizeSignalOpensItsPullRequest(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.SizeSignal = 12
	f.model.then(big)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching || len(f.tr.opened) != 1 {
		t.Errorf("job in %q with %d pull requests, want %q with 1", j.State, len(f.tr.opened), implement.Watching)
	}
}

// The session is told the rule and the signal before it starts, so it can stop
// at a coherent first piece by itself and give the piece a title - unless the
// person who asked wants one pull request, which it is told instead.
func TestThePromptCarriesTheSizeRule(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		want, not     []string
	}{
		{"unattended", "", []string{"one concern, reviewable in one sitting", "400 changed lines", "coherent first piece", "title for the piece"}, []string{"one pull request, whatever its size"}},
		{"commanded", "/implement keep it tidy", []string{"one concern, reviewable in one sitting", "400 changed lines", "title for the piece"}, []string{"one pull request, whatever its size"}},
		{"one pull request asked for", "/implement do not split this", []string{"one pull request, whatever its size"}, []string{"400 changed lines", "title for the piece"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker()
			if tc.command != "" {
				commanded(tr, 1, tc.command)
			}
			f := setup(t, tr)
			f.model.then(commit("ok"))
			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			prompt := f.model.asked[0].Prompt
			for _, want := range tc.want {
				if !strings.Contains(prompt, want) {
					t.Errorf("the prompt does not say %q:\n%s", want, prompt)
				}
			}
			for _, not := range tc.not {
				if strings.Contains(prompt, not) {
					t.Errorf("the prompt says %q:\n%s", not, prompt)
				}
			}
		})
	}
}

func TestOnePullRequestIsAskedForInTheInstructions(t *testing.T) {
	for _, tc := range []struct {
		instructions string
		want         bool
	}{
		{"one PR, don't split", true},
		{"Don't split it.", true},
		{"dont split", true},
		{"do not split", true},
		{"Do  not\nsplit, please", true},
		{"don’t split", true},
		{"", false},
		{"split it up", false},
		{"one PR", false},
		{"don't splitter", false},
		{"I don't mind: split", false},
	} {
		if got := implement.Whole(tc.instructions); got != tc.want {
			t.Errorf("Whole(%q) = %v, want %v", tc.instructions, got, tc.want)
		}
	}
}
