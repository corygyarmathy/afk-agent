package implement_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/review"
)

// cut is a turn that cuts big's work to its tests and `ok`, one changed line
// and five of tests, gives the piece a title, and says what is left.
func cut(dir string) error {
	if _, err := run(dir, "git", "rm", "--quiet", "job.go"); err != nil {
		return err
	}
	if _, err := run(dir, "git", "commit", "--quiet", "-m", "leave job.go for the rest"); err != nil {
		return err
	}
	return piece("Reserve a job: the tests first", "Make the tests pass: job.go reserves the job.")(dir)
}

// piece is a turn that writes the description of a first piece, titled, and
// what is left.
func piece(title, rest string) func(dir string) error {
	return func(dir string) error {
		desc := title + "\n\n## Start here\n\njob_test.go:1, the tests the rest makes pass.\n"
		if err := os.WriteFile(filepath.Join(dir, ".git", "afk-description.md"), []byte(desc), 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, ".git", "afk-remainder.md"), []byte(rest+"\n"), 0o644)
	}
}

// nothing is a turn that changes nothing: a session that finds no coherent
// first piece, and leaves the branch as it is.
func nothing(string) error { return nil }

// Over the size signal, the work goes back to its session once to be cut to a
// first coherent piece, in the session that wrote it, once the work as it was
// is kept on a branch of its own. What is left is filed as an issue of its
// own, blocked by the issue and labelled for nobody, and then the piece opens
// as part of the issue, under the session's title, naming it.
func TestWorkOverTheSizeSignalIsCutToItsFirstPiece(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.SizeSignal = 10
	f.model.then(big, cut)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 2 {
		t.Fatalf("%d model runs, want the work and one cut", len(f.model.asked))
	}
	again := f.model.asked[1]
	if again.Session != "ses_1" {
		t.Errorf("the cut ran in session %q, want the one that wrote the work", again.Session)
	}
	for _, want := range []string{"12 changed lines", "5 of tests", "size signal of 10", "first coherent piece", ".git/afk-remainder.md", "title", "`afk/7-1-whole`", "switch or delete", "not a heading"} {
		if !strings.Contains(again.Prompt, want) {
			t.Errorf("the cut's prompt does not say %q:\n%s", want, again.Prompt)
		}
	}

	if len(f.tr.opened) != 1 {
		t.Fatalf("%d pull requests opened, want 1", len(f.tr.opened))
	}
	pr := f.tr.opened[0]
	if pr.Title != "Reserve a job: the tests first" {
		t.Errorf("the pull request's title is %q, want the session's for the piece", pr.Title)
	}
	if strings.Contains(pr.Body, "Closes #7") || !strings.Contains(pr.Body, "Part of #7") {
		t.Errorf("the pull request does not say it is part of #7:\n%s", pr.Body)
	}
	if strings.Contains(pr.Body, "Reserve a job: the tests first") {
		t.Errorf("the title is in the body:\n%s", pr.Body)
	}

	if n, r, ok := review.PartOf(pr.Body); !ok || n != issue || r != 201 {
		t.Errorf("the review reads the link line as part of #%d, the rest #%d, %v: want #7 and #201:\n%s", n, r, ok, pr.Body)
	}
	if !f.onRemote("afk/7-1-whole", "job.go") || f.onRemote("afk/7-1", "job.go") {
		t.Errorf("want the work as it was kept on afk/7-1-whole, and the piece without job.go on afk/7-1")
	}

	if len(f.tr.filed) != 1 {
		t.Fatalf("%d issues filed, want the rest filed once", len(f.tr.filed))
	}
	rest := f.tr.filed[0]
	for _, want := range []string{"Make the tests pass: job.go reserves the job.", "`afk/7-1`", "`afk/7-1-whole`", "#7"} {
		if !strings.Contains(rest.Body, want) {
			t.Errorf("the rest's body does not say %q:\n%s", want, rest.Body)
		}
	}
	if !strings.Contains(rest.Title, "#7") || !strings.Contains(rest.Title, "Reserve a job") {
		t.Errorf("the rest's title is %q, want it to name #7 and its title", rest.Title)
	}
	if got := f.tr.blocked[rest.Number]; len(got) != 1 || got[0] != issue {
		t.Errorf("the rest is blocked by %v, want #7 alone", got)
	}
	is, err := f.tr.Issue(t.Context(), rest.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(is.Labels) != 0 {
		t.Errorf("the rest carries %v, want no label: whether it is worked is the operator's", is.Labels)
	}

	if body := f.tr.prs[0].Body; body != pr.Body || !strings.Contains(body, "Part of #7. The rest is #201.") {
		t.Errorf("the pull request does not open naming the rest, or was edited since:\n%s", body)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Errorf("job in %q, want %q", j.State, implement.Watching)
	}
	if posted := f.tr.byAgent(); len(posted) != 0 {
		t.Errorf("the agent said %+v, want nothing", posted)
	}
}

// One cut round, and no more: a piece still over the signal is pushed and
// handed back on the issue, as work that was never cut is. Nothing is filed.
func TestACutStillOverTheSignalIsHandedBack(t *testing.T) {
	grow := func(dir string) error { return commitLines("more.go", 5)(dir) }
	for name, turn := range map[string]func(string) error{"declined": nothing, "still over": grow} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.deps.SizeSignal = 10
			f.model.then(big, turn, cut)

			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			if len(f.model.asked) != 2 {
				t.Errorf("%d model runs, want the work and one cut", len(f.model.asked))
			}
			if len(f.tr.opened) != 0 || len(f.tr.filed) != 0 {
				t.Errorf("%d pull requests and %d issues, want none", len(f.tr.opened), len(f.tr.filed))
			}
			if _, err := run(f.remote, "git", "rev-parse", "refs/heads/afk/7-1"); err != nil {
				t.Errorf("the branch was not pushed: %v", err)
			}
			if !f.onRemote("afk/7-1-whole", "job.go") {
				t.Errorf("the work as it was before the cut is not kept on afk/7-1-whole")
			}
			posted := f.tr.byAgent()
			if len(posted) != 1 || f.tr.commentedOn[0] != issue {
				t.Fatalf("comments %+v on %v, want one hand-back on the issue", posted, f.tr.commentedOn)
			}
			for _, want := range []string{"size signal of 10", "cut", "open one from it by hand", "`afk/7-1-whole`"} {
				if !strings.Contains(posted[0].Body, want) {
					t.Errorf("the hand-back does not say %q:\n%s", want, posted[0].Body)
				}
			}
			if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
				t.Errorf("job = %+v, want it at rest", j)
			}
		})
	}
}

// A session told the rule may stop at a first piece by itself. Its pull request
// is part of the issue too, and what it says is left is filed: the issue must
// not be closed by the piece. It needs no cut.
func TestAPieceTheSessionStoppedAtIsPartOfTheIssue(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := commit("ok")(dir); err != nil {
			return err
		}
		return piece("Reserve a job: the ok file", "Reserve the job itself.")(dir)
	})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("%d model runs, want no cut", len(f.model.asked))
	}
	if len(f.tr.opened) != 1 || f.tr.opened[0].Title != "Reserve a job: the ok file" || !strings.Contains(f.tr.opened[0].Body, "Part of #7") {
		t.Fatalf("opened %+v, want one pull request, part of #7, under the piece's title", f.tr.opened)
	}
	if len(f.tr.filed) != 1 || !strings.Contains(f.tr.filed[0].Body, "Reserve the job itself.") {
		t.Errorf("filed %+v, want the rest once", f.tr.filed)
	}
}

// A pull request that is the whole job closes the issue, and files nothing.
// Asked for as one pull request, what the session says is left is not read:
// the person who asked wanted it whole.
func TestTheWholeJobFilesNothing(t *testing.T) {
	for name, command := range map[string]string{"unattended": "", "one pull request asked for": "/implement don't split"} {
		t.Run(name, func(t *testing.T) {
			tr := newTracker()
			if command != "" {
				commanded(tr, 1, command)
			}
			f := setup(t, tr)
			turn := commit("ok")
			if command != "" {
				turn = func(dir string) error {
					if err := commit("ok")(dir); err != nil {
						return err
					}
					return piece("A title", "Something else.")(dir)
				}
			}
			f.model.then(turn)

			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			if len(f.tr.opened) != 1 || !strings.Contains(f.tr.opened[0].Body, "Closes #7") || f.tr.opened[0].Title != "Reserve a job" {
				t.Errorf("opened %+v, want one pull request closing #7 under its title", f.tr.opened)
			}
			if len(f.tr.filed) != 0 {
				t.Errorf("filed %+v, want nothing", f.tr.filed)
			}
		})
	}
}

// A dependency that is there already is not added again, and neither is one
// the operator removed once it was seen: running the open transition once more
// after all of it is done files nothing, blocks nothing and edits nothing.
func TestTheRestIsFiledAndBlockedOnce(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.SizeSignal = 10
	f.model.then(big, cut)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	body := f.tr.prs[0].Body

	f.setState(implement.Opening)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.tr.filed) != 1 || len(f.tr.blocked[201]) != 1 || len(f.tr.opened) != 1 || f.tr.prs[0].Body != body {
		t.Errorf("filed %d, blockers %v, opened %d, body changed %v: want each once and nothing edited", len(f.tr.filed), f.tr.blocked[201], len(f.tr.opened), f.tr.prs[0].Body != body)
	}

	delete(f.tr.blocked, 201)
	f.setState(implement.Opening)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.tr.blocked[201]) != 0 {
		t.Errorf("the rest is blocked by %v again, want the operator's removal to stand", f.tr.blocked[201])
	}
}

// A rest the operator closes as soon as it is filed is still the rest: it is
// read back closed, and not filed again.
func TestARestClosedAtOnceIsNotFiledAgain(t *testing.T) {
	f := setup(t, newTracker())
	f.tr.closeFiled = true
	f.deps.SizeSignal = 10
	f.model.then(big, cut)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.tr.filed) != 1 {
		t.Fatalf("%d issues filed, want the rest once", len(f.tr.filed))
	}
	if len(f.tr.opened) != 1 || !strings.Contains(f.tr.opened[0].Body, "The rest is #201.") {
		t.Errorf("opened %+v, want the piece naming #201", f.tr.opened)
	}
}

// A dependency that never lands fails nothing: the piece opens all the same,
// and its description says the rest is not blocked, for the operator to add by
// hand.
func TestARestThatCannotBeBlockedIsNotedOnThePullRequest(t *testing.T) {
	f := setup(t, newTracker())
	f.tr.failBlocks = f.deps.Rounds
	f.deps.SizeSignal = 10
	f.model.then(big, cut)

	for _, err := range f.drive() {
		if !errors.Is(err, errBlock) {
			t.Fatalf("error %v, want only the failed dependencies", err)
		}
	}
	if len(f.tr.blocked[201]) != 0 {
		t.Errorf("the rest is blocked by %v, want nothing: every try failed", f.tr.blocked[201])
	}
	if len(f.tr.opened) != 1 {
		t.Fatalf("%d pull requests opened, want the piece", len(f.tr.opened))
	}
	if body := f.tr.opened[0].Body; !strings.Contains(body, "Part of #7. The rest is #201.\n\n#201 could not be made blocked by #7") {
		t.Errorf("the description does not say the rest is not blocked:\n%s", body)
	}
	if j := f.now(); j.State != implement.Watching {
		t.Errorf("job in %q, want %q", j.State, implement.Watching)
	}
}

// A cut is one bounded round, like a fix: the gate's attempts start again for
// it, whatever the work spent before it was sent back.
func TestACutGetsTheGatesAttemptsAfresh(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.SizeSignal = 10
	f.deps.Attempts = 2
	unfinished := func(dir string) error {
		for _, turn := range []func(string) error{commitLines("job.go", 11), commitLines("job_test.go", 5)} {
			if err := turn(dir); err != nil {
				return err
			}
		}
		return nil
	}
	f.model.then(unfinished, commit("ok"), redCut, fixCut)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 4 {
		t.Errorf("%d model runs, want the work, its fix, the cut and its fix", len(f.model.asked))
	}
	if len(f.tr.opened) != 1 || f.tr.opened[0].Title != "Reserve a job: the tests first" {
		t.Errorf("opened %+v, want the piece", f.tr.opened)
	}
	if posted := f.tr.byAgent(); len(posted) != 0 {
		t.Errorf("the agent said %+v, want nothing", posted)
	}
}

// A cut that fails - its gate red to the last attempt, or nothing left
// committed - is handed back on the issue, and the work it was cut from is
// not lost with its workspace: it was kept before the cut, and the hand-back
// says where.
func TestACutThatFailsHandsBackTheWorkItWasCutFrom(t *testing.T) {
	reset := func(dir string) error {
		_, err := run(dir, "git", "reset", "--quiet", "--hard", "origin/HEAD")
		return err
	}
	for name, turns := range map[string][]func(string) error{
		"red gate":          {big, redCut, nothing, nothing},
		"nothing committed": {big, reset},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.deps.SizeSignal = 10
			f.model.then(turns...)

			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			if len(f.tr.opened) != 0 || len(f.tr.filed) != 0 {
				t.Errorf("%d pull requests and %d issues, want none", len(f.tr.opened), len(f.tr.filed))
			}
			if b := f.remoteBranches(); b != "afk/7-1-whole\nmain" {
				t.Errorf("the remote has %q, want the work kept on afk/7-1-whole, and no cut pushed", b)
			}
			if !f.onRemote("afk/7-1-whole", "job.go") {
				t.Errorf("afk/7-1-whole is not the work as it was")
			}
			posted := f.tr.byAgent()
			if len(posted) != 1 || f.tr.commentedOn[0] != issue {
				t.Fatalf("comments %+v on %v, want one hand-back on the issue", posted, f.tr.commentedOn)
			}
			if body := posted[0].Body; !strings.Contains(body, "`afk/7-1-whole`") || strings.Contains(body, "Nothing was pushed") {
				t.Errorf("the hand-back does not say where the work is kept:\n%s", body)
			}
		})
	}
}

// A branch that is the whole of another try's work takes its number, as the
// branch itself would: the next try's cut keeps its own whole beside it.
func TestALeftoverWholeTakesItsBranchNumber(t *testing.T) {
	f := setup(t, newTracker())
	if _, err := run(f.remote, "git", "branch", "afk/7-1-whole", "main"); err != nil {
		t.Fatal(err)
	}
	f.model.then(commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.tr.opened) != 1 || f.tr.opened[0].Head != "afk/7-2" {
		t.Errorf("opened %+v, want one from afk/7-2", f.tr.opened)
	}
}

// A whole branch the agent did not push is not pushed over: the work is not
// cut, and is pushed and handed back as over the signal, as it was before
// there was a cut.
func TestAWholeBranchSomeoneElsePushedIsLeftAlone(t *testing.T) {
	f := setup(t, newTracker())
	f.deps.SizeSignal = 10
	var theirs string
	f.model.then(func(dir string) error {
		// Made while the work is under way, after the branch was chosen.
		out, err := run(f.remote, "git", "rev-parse", "main")
		if err != nil {
			return err
		}
		theirs = out
		if _, err := run(f.remote, "git", "branch", "afk/7-1-whole", "main"); err != nil {
			return err
		}
		return big(dir)
	}, cut)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 1 {
		t.Errorf("%d model runs, want the work and no cut", len(f.model.asked))
	}
	if at, _ := run(f.remote, "git", "rev-parse", "afk/7-1-whole"); at != theirs {
		t.Errorf("afk/7-1-whole is at %s, want %s, where its owner left it", at, theirs)
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "size signal of 10") {
		t.Fatalf("comments %+v, want the size hand-back", posted)
	}
}

// redCut is a cut that leaves the gate red: it takes `ok` away with job.go.
func redCut(dir string) error {
	if _, err := run(dir, "git", "rm", "--quiet", "job.go", "ok"); err != nil {
		return err
	}
	_, err := run(dir, "git", "commit", "--quiet", "-m", "cut to the tests")
	return err
}

// fixCut is the fix of redCut's gate, and the piece's description.
func fixCut(dir string) error {
	if err := commit("ok")(dir); err != nil {
		return err
	}
	return piece("Reserve a job: the tests first", "Make the tests pass: job.go reserves the job.")(dir)
}

// onRemote reports whether branch on the remote has a file called name.
func (f *fixture) onRemote(branch, name string) bool {
	f.t.Helper()
	_, err := run(f.remote, "git", "cat-file", "-e", "refs/heads/"+branch+":"+name)
	return err == nil
}
