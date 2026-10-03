package implement_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/spend"
)

const procedure = "https://github.com/o/skills/blob/main/docs/operators-review.md"

// held is the end of a description opened by sessions that reported no
// spend: the footer's hidden lines, with nothing between them.
const held = "\n" + spend.Open + "\n" + spend.Close + "\n"

// describe is a turn that writes the description file and commits the work.
func describe(text string) func(dir string) error {
	return func(dir string) error {
		if err := commit("ok")(dir); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, ".git", "afk-description.md"), []byte(text), 0o644)
	}
}

// opened drives the job to its pull request, and returns the description.
func (f *fixture) opened() string {
	f.t.Helper()
	if errs := f.drive(); len(errs) != 0 {
		f.t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Watching {
		f.t.Fatalf("job in %q, want %q", j.State, implement.Watching)
	}
	if len(f.tr.opened) != 1 {
		f.t.Fatalf("%d pull requests opened, want 1", len(f.tr.opened))
	}
	return f.tr.opened[0].Body
}

// The description is the marker, the link line and the reminder, then the
// session's sections in their order whatever order it wrote them in. A
// section with nothing in it, or only "none", is left out, and so is one the
// prompt did not name. A file:line in the session's part links to the pushed
// head.
func TestTheDescriptionIsGoFixedPartsThenTheSessionsSectionsInOrder(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.Repo, f.deps.ReviewProcedure = "o/n", procedure
	f.model.then(describe(strings.Join([]string{
		"Preamble the prompt did not ask for.",
		"",
		"## Not verified",
		"",
		"- Needs a host run.",
		"",
		"## What changed",
		"",
		"- Every file, one by one.",
		"",
		"## Start here",
		"",
		"ok:1 - where the behaviour lives.",
		"",
		"## Where the ticket didn’t decide",
		"",
		"- Took the skill's default.",
		"",
		"## Recipe",
		"",
		"None.",
		"",
	}, "\n")))

	body := f.opened()
	head, _ := run(f.workspace(), "git", "rev-parse", "HEAD")
	want := implement.PRMarker(7) + "\n" +
		"Closes #7\n\n" +
		"> **Your review** ([procedure](" + procedure + ")): read #7 first, then this, then the diff from **Start here**. Do your own reading before you open the advisory review. End by merging, sending back in your own words, or closing with one line why.\n\n" +
		"## Start here\n\n" +
		"[ok:1](https://github.com/o/n/blob/" + head + "/ok#L1) - where the behaviour lives.\n\n" +
		"## Where the ticket didn't decide\n\n" +
		"- Took the skill's default.\n\n" +
		"## Not verified\n\n" +
		"- Needs a host run.\n" +
		held
	if body != want {
		t.Errorf("description:\n%s\nwant:\n%s", body, want)
	}
}

// A Closes pull request keeps the issue's title, which describes the job it
// closes, whatever the file's first line says. That line is not in the body
// either way: it is a title, and only a Part of pull request takes it.
func TestAClosesPullRequestKeepsTheIssuesTitle(t *testing.T) {
	for name, text := range map[string]string{
		"titled":   "The first piece\n\n## Start here\n\nok:1 - where the behaviour lives.\n",
		"untitled": "## Start here\n\nok:1 - where the behaviour lives.\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.model.then(describe(text))

			body := f.opened()
			if got := f.tr.opened[0].Title; got != "Reserve a job" {
				t.Errorf("title %q, want the issue's, %q", got, "Reserve a job")
			}
			if strings.Contains(body, "The first piece") {
				t.Errorf("the title line is in the body:\n%s", body)
			}
			if !strings.Contains(body, "## Start here\n\nok:1 - where the behaviour lives.\n") {
				t.Errorf("the session's part is not in the body:\n%s", body)
			}
		})
	}
}

// With no file, one with no Start here, one that cannot be read, or one too
// long for GitHub to take, the pull request opens with Go's parts only: the
// diff is still reviewable. It is not a gate failure, and a file the agent
// set aside is logged, so that the operator can tell it from no file.
func TestADescriptionWithNoStartHereIsGosPartsOnly(t *testing.T) {
	for name, c := range map[string]struct {
		turn func(t *testing.T) func(string) error
		log  string
	}{
		"missing": {func(*testing.T) func(string) error { return commit("ok") }, ""},
		"headless": {func(*testing.T) func(string) error {
			return describe("### Start here\n\nok:1\n\n## Not verified\n\n- Needs a host run.\n")
		}, `no "## Start here" section`},
		"unreadable": {unreadable, "could not be read"},
		"too long": {func(*testing.T) func(string) error {
			return describe("## Start here\n\n" + strings.Repeat("x", 70000) + "\n")
		}, "over GitHub's 65536 characters"},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.deps.ReviewProcedure = procedure
			f.model.then(c.turn(t))

			body := f.opened()
			want := implement.PRMarker(7) + "\n" +
				"Closes #7\n\n" +
				"> **Your review** ([procedure](" + procedure + ")): read #7 first, then this, then the diff. Do your own reading before you open the advisory review. End by merging, sending back in your own words, or closing with one line why.\n" +
				held
			if body != want {
				t.Errorf("description:\n%s\nwant:\n%s", body, want)
			}
			if f.now().State != implement.Watching || len(f.model.asked) != 1 {
				t.Errorf("the work took %d sessions, want the one: a description is not the gate", len(f.model.asked))
			}
			var logged []string
			for _, l := range f.logged {
				if strings.Contains(l, "agent's parts only") {
					logged = append(logged, l)
				}
			}
			switch {
			case c.log == "" && len(logged) != 0:
				t.Errorf("logged %q with no file to set aside", logged)
			case c.log != "" && (len(logged) != 1 || !strings.Contains(logged[0], c.log) || !strings.Contains(logged[0], "implement-issue-7:")):
				t.Errorf("logged %q, want one line for the job saying %q", logged, c.log)
			}
		})
	}
}

// unreadable is a turn that leaves a description the agent cannot read. A
// file's mode does not stop root, nor a user namespace's root, which is how
// scripts/offline-test.sh may run: there the case is skipped.
func unreadable(t *testing.T) func(string) error {
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(probe, nil, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(probe); err == nil {
		t.Skip("a file's mode does not stop a read here")
	}
	return func(dir string) error {
		if err := describe("## Start here\n\nok:1\n")(dir); err != nil {
			return err
		}
		return os.Chmod(filepath.Join(dir, ".git", "afk-description.md"), 0o000)
	}
}

// A fix after the pull request opened is not asked for the description: it
// is written once, and a change to the file would reach nobody. Nor is a new
// session that takes over the fix from one that is gone.
func TestAFixIsNotAskedForTheDescription(t *testing.T) {
	for name, forget := range map[string]bool{"continued": false, "new session": true} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.model.then(func(dir string) error {
				if forget {
					f.model.sessions = map[string]bool{}
				}
				return describe("## Start here\n\nok:1\n")(dir)
			}, commit("fix"))
			f.tr.checks = red()

			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			fix := f.model.asked[len(f.model.asked)-1]
			if !strings.Contains(fix.Prompt, "CI failed") {
				t.Fatalf("the last run was not the fix:\n%s", fix.Prompt)
			}
			if forget && fix.Session != "" {
				t.Fatalf("the fix continued session %q, want a new one", fix.Session)
			}
			if !strings.Contains(f.model.asked[0].Prompt, "afk-description.md") {
				t.Errorf("the first run was not asked for the description:\n%s", f.model.asked[0].Prompt)
			}
			if strings.Contains(fix.Prompt, "afk-description.md") {
				t.Errorf("the fix was asked for the description:\n%s", fix.Prompt)
			}
		})
	}
}

// The prompt asks for the skill's closing report as the description file,
// rather than as a reply.
func TestThePromptAsksForTheDescriptionFile(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	f.opened()

	prompt := f.model.asked[0].Prompt
	for _, want := range []string{"closing report", ".git/afk-description.md"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not contain %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Reply with a short summary") {
		t.Errorf("the prompt still asks for a summary reply:\n%s", prompt)
	}
}
