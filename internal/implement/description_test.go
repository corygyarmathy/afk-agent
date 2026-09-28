package implement_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/implement"
)

const procedure = "https://github.com/o/skills/blob/main/docs/operators-review.md"

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
		"- Needs a host run.\n"
	if body != want {
		t.Errorf("description:\n%s\nwant:\n%s", body, want)
	}
}

// With no file, or one with no Start here, the pull request opens with Go's
// parts only: the diff is still reviewable. It is not a gate failure.
func TestADescriptionWithNoStartHereIsGosPartsOnly(t *testing.T) {
	for name, turn := range map[string]func(string) error{
		"missing":  commit("ok"),
		"headless": describe("## Not verified\n\n- Needs a host run.\n"),
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.deps.ReviewProcedure = procedure
			f.model.then(turn)

			body := f.opened()
			want := implement.PRMarker(7) + "\n" +
				"Closes #7\n\n" +
				"> **Your review** ([procedure](" + procedure + ")): read #7 first, then this, then the diff. Do your own reading before you open the advisory review. End by merging, sending back in your own words, or closing with one line why.\n"
			if body != want {
				t.Errorf("description:\n%s\nwant:\n%s", body, want)
			}
			if f.now().State != implement.Watching || len(f.model.asked) != 1 {
				t.Errorf("the work took %d sessions, want the one: a description is not the gate", len(f.model.asked))
			}
		})
	}
}

// Without --review-procedure the reminder says it has no link, rather than
// linking nowhere.
func TestWithNoProcedureTheReminderSaysSo(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))

	body := f.opened()
	if strings.Contains(body, "](") {
		t.Errorf("the description links something with no procedure configured:\n%s", body)
	}
	if !strings.Contains(body, "> **Your review** (the agent has no link to the procedure): read #7 first") {
		t.Errorf("the reminder does not say there is no procedure linked:\n%s", body)
	}
}

// The description file is read as a regular file only: a link out of the
// workspace would publish whatever it points at.
func TestALinkedDescriptionFileIsNotRead(t *testing.T) {
	f := setup(t, newTracker())
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("## Start here\n\nhunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.model.then(func(dir string) error {
		if err := commit("ok")(dir); err != nil {
			return err
		}
		return os.Symlink(secret, filepath.Join(dir, ".git", "afk-description.md"))
	})

	if body := f.opened(); strings.Contains(body, "hunter2") || strings.Contains(body, "## Start here") {
		t.Errorf("the description read through the link:\n%s", body)
	}
}

// A description left by a session that never finished is not the next
// session's: a fresh start begins with none.
func TestAFreshStartDoesNotInheritADescription(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, ".git", "afk-description.md"), []byte("## Start here\n\nstale\n"), 0o644); err != nil {
			return err
		}
		return fail(first)(dir)
	}, commit("ok"))

	if body := f.opened(); strings.Contains(body, "stale") {
		t.Errorf("the description is the unfinished session's:\n%s", body)
	}
}

// The description is written once, when the pull request opens. Opening again
// finds it open and writes nothing, whatever the file says by then.
func TestOpeningAgainDoesNotRewriteTheDescription(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(describe("## Start here\n\nfirst\n"))
	was := f.opened()

	if err := os.WriteFile(filepath.Join(f.workspace(), ".git", "afk-description.md"), []byte("## Start here\n\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.setState(implement.Opening)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.tr.opened) != 1 || f.tr.opened[0].Body != was {
		t.Errorf("%d pull requests opened, the first with:\n%s\nwant one, with:\n%s", len(f.tr.opened), f.tr.opened[0].Body, was)
	}
}

// The prompt asks for the description as a file under the named headings,
// says how long, and what not to write. It no longer asks for a reply.
func TestThePromptAsksForTheDescriptionFile(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("ok"))
	f.opened()

	prompt := f.model.asked[0].Prompt
	for _, want := range []string{
		".git/afk-description.md", "## Start here", "## Where the ticket didn't decide", "## Not verified", "## Recipe",
		"one screen", "file by file", "restate the issue", "tests pass", "self-rating", "hand-checks",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not contain %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Reply with a short summary") {
		t.Errorf("the prompt still asks for a summary reply:\n%s", prompt)
	}
}
