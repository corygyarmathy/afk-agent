package implement_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
)

// commitAt is a turn that commits a file at path, making its directories.
func commitAt(path string) func(dir string) error {
	return func(dir string) error {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
			return err
		}
		return commit(path)(dir)
	}
}

// both is a turn that takes each of turns in order.
func both(turns ...func(string) error) func(string) error {
	return func(dir string) error {
		for _, turn := range turns {
			if err := turn(dir); err != nil {
				return err
			}
		}
		return nil
	}
}

var sensitivePaths = []sensitive.Path{
	{Label: "job store schema", Globs: []string{"store/**"}},
	{Label: "CI", Globs: []string{"ci/*.yml"}},
	{Label: "docs", Globs: []string{"docs/**"}},
}

// A pull request that touches a sensitive path says which, after the link
// line, with each label the operator gave in the operator's order and the
// files it matched. A label nothing matched is not named.
func TestAPullRequestTouchingASensitivePathSaysSo(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.ReviewProcedure = procedure
	f.deps.Sensitive = sensitivePaths
	f.model.then(both(commitAt("ci/build.yml"), commitAt("store/schema.sql"), commitAt("store/b/x.go"), commitAt("ok")))

	body := f.opened()
	want := implement.PRMarker(7) + "\n" +
		"Closes #7\n\n" +
		"**Sensitive:** job store schema (`store/b/x.go`, `store/schema.sql`), CI (`ci/build.yml`)\n\n" +
		"> **Your review** ([procedure](" + procedure + ")): read #7 first, then this, then the diff. Do your own reading before you open the advisory review. End by merging, sending back in your own words, or closing with one line why.\n"
	if body != want {
		t.Errorf("description:\n%s\nwant:\n%s", body, want)
	}
}

// A pull request that touches none carries no line, and neither does one
// when the operator named no sensitive paths.
func TestAPullRequestTouchingNoSensitivePathSaysNothing(t *testing.T) {
	for name, list := range map[string][]sensitive.Path{"none touched": sensitivePaths, "none named": nil} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.deps.Sensitive = list
			f.model.then(both(commitAt("src/x.go"), commitAt("ok")))
			if body := f.opened(); strings.Contains(body, "Sensitive") {
				t.Errorf("description:\n%s\nwant no sensitive line", body)
			}
		})
	}
}

// The line is recomputed on every push. A fix that newly touches a sensitive
// path adds it to the description of the pull request already open, and the
// session's part, which is written once, is left as it was.
func TestALaterPushThatTouchesASensitivePathAddsIt(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.Sensitive = sensitivePaths
	f.model.then(describe("## Start here\n\nok:1\n"), commitAt("store/schema.sql"))
	f.tr.checks = red()

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Reviewing {
		t.Fatalf("job in %q, want %q", j.State, implement.Reviewing)
	}
	opened := f.tr.opened[0].Body
	if strings.Contains(opened, "Sensitive") {
		t.Fatalf("opened with a sensitive line before one was touched:\n%s", opened)
	}
	want := strings.Replace(opened, "Closes #7\n\n", "Closes #7\n\n**Sensitive:** job store schema (`store/schema.sql`)\n\n", 1)
	if got := f.tr.prs[0].Body; got != want {
		t.Errorf("description after the fix:\n%s\nwant:\n%s", got, want)
	}
	if !strings.HasSuffix(f.tr.prs[0].Body, "## Start here\n\nok:1\n") {
		t.Errorf("the session's part changed:\n%s", f.tr.prs[0].Body)
	}
}

// An edit that does not land is made again. One that never does is logged
// and costs the work nothing: the description is orientation, not the gate.
func TestAnEditThatNeverLandsIsLoggedAndTheWorkGoesOn(t *testing.T) {
	for name, c := range map[string]struct {
		fails int
		log   bool
	}{"lands the second time": {1, false}, "never lands": {99, true}} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.deps.Sensitive = sensitivePaths
			f.model.then(commit("ok"), commitAt("store/schema.sql"))
			f.tr.checks = red()
			f.tr.failEdits = c.fails

			errs := f.drive()
			for _, err := range errs {
				if !errors.Is(err, errEdit) {
					t.Fatalf("errors: %v, want only the failed edits", errs)
				}
			}
			if j := f.now(); j.State != implement.Reviewing {
				t.Fatalf("job in %q, want %q", j.State, implement.Reviewing)
			}
			got := strings.Contains(f.tr.prs[0].Body, "**Sensitive:** job store schema")
			if got == c.log {
				t.Errorf("sensitive line on the pull request = %v, want %v:\n%s", got, !c.log, f.tr.prs[0].Body)
			}
			var logged []string
			for _, l := range f.logged {
				if strings.Contains(l, "sensitive") {
					logged = append(logged, l)
				}
			}
			if c.log != (len(logged) == 1) || len(logged) > 1 {
				t.Errorf("logged %q, want a line: %v", logged, c.log)
			}
		})
	}
}

// manyUnder is a turn that commits n files under dir in one commit, each
// with a long name, so that listing them takes more than GitHub's limit.
func manyUnder(dir string, n int) func(string) error {
	return func(ws string) error {
		if err := os.MkdirAll(filepath.Join(ws, dir), 0o755); err != nil {
			return err
		}
		for i := range n {
			name := fmt.Sprintf("%s-%03d", strings.Repeat("x", 240), i)
			if err := os.WriteFile(filepath.Join(ws, dir, name), []byte("x\n"), 0o644); err != nil {
				return err
			}
		}
		if _, err := run(ws, "git", "add", dir); err != nil {
			return err
		}
		_, err := run(ws, "git", "commit", "--quiet", "-m", "add "+dir)
		return err
	}
}

// Files that would take the description over GitHub's limit are counted for
// each label rather than listed, and the session's part, which the operator
// needs more than a list the diff repeats, is kept.
func TestSensitiveFilesOverGitHubsLimitAreCounted(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.Sensitive = sensitivePaths
	f.deps.SizeSignal = 100000
	f.model.then(both(manyUnder("store", 300), commitAt("ci/build.yml"), describe("## Start here\n\nok:1\n")))

	body := f.opened()
	if !strings.Contains(body, "Closes #7\n\n**Sensitive:** job store schema (300 files), CI (1 file)\n\n> **Your review**") {
		t.Errorf("description:\n%.600s\nwant the sensitive paths counted", body)
	}
	if !strings.HasSuffix(body, "## Start here\n\nok:1\n") {
		t.Errorf("the session's part was set aside:\n%.600s", body)
	}
	var logged []string
	for _, l := range f.logged {
		if strings.Contains(l, "counted rather than listed") {
			logged = append(logged, l)
		}
	}
	if len(logged) != 1 {
		t.Errorf("logged %q, want one line saying the paths were counted", f.logged)
	}
}

// The same holds for a later push: the edit counts the files rather than
// asking GitHub every round for a description it refuses.
func TestALaterPushOverGitHubsLimitIsCounted(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.Sensitive = sensitivePaths
	f.deps.SizeSignal = 100000
	f.model.then(describe("## Start here\n\nok:1\n"), manyUnder("store", 300))
	f.tr.checks = red()

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	opened := f.tr.opened[0].Body
	want := strings.Replace(opened, "Closes #7\n\n", "Closes #7\n\n**Sensitive:** job store schema (300 files)\n\n", 1)
	if got := f.tr.prs[0].Body; got != want {
		t.Errorf("description after the fix:\n%.600s\nwant:\n%.600s", got, want)
	}
}
