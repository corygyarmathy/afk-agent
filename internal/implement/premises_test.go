package implement_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/premise"
)

// stopOnGaps is a turn that commits nothing and writes the report of a session
// that stopped on gaps, as the implement skill shapes it.
func stopOnGaps(report string) func(dir string) error {
	return func(dir string) error {
		return os.WriteFile(filepath.Join(dir, ".git", "afk-description.md"), []byte(report), 0o644)
	}
}

// A session that stops on gaps hands back on the issue, with nothing pushed,
// and its questions in full, each with its recommended answer, rather than the
// end of its output. The next step it names is answering with the command. The
// issue is not edited, and the job comes to rest.
func TestASessionThatStopsOnGapsHandsBackItsQuestions(t *testing.T) {
	f := setup(t, newTracker())
	questions := "1. Does `--flag` still exist upstream? Recommended: no, use `--other`, as the head of `flags.go` has it.\n2. Which label? Recommended: `needs-decision`."
	f.model.then(stopOnGaps("Stopped on gaps: nothing changed.\n\n" + questions + "\n"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 {
		t.Fatalf("comments %+v, want one hand-back", posted)
	}
	body := posted[0].Body
	for _, want := range []string{
		"found gaps",
		questions,
		"Answer with `/implement <answers>`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the hand-back has no %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "without committing anything") || strings.Contains(body, "The end of the last output") {
		t.Errorf("the hand-back is the one for a session that committed nothing, not its questions:\n%s", body)
	}
	if f.tr.commentedOn[0] != issue || len(f.tr.labelledOn) != 1 || f.tr.labelledOn[0] != issue {
		t.Errorf("hand-back on %v, labelled %v; want both on the issue", f.tr.commentedOn, f.tr.labelledOn)
	}
	if len(f.tr.opened) != 0 || f.remoteBranches() != "main" {
		t.Errorf("opened %d pull requests, and the remote has %q; want nothing pushed", len(f.tr.opened), f.remoteBranches())
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
}

// A report that does not open with the stop, or that stops and asks nothing,
// is a session that committed nothing, and says so.
func TestAnEmptySessionThatAsksNothingIsNotAStopOnGaps(t *testing.T) {
	for name, report := range map[string]string{
		"no stop line": "## Start here\n\nStopped on gaps: nothing changed.\n\n1. Q? Recommended: A.\n",
		"no questions": "Stopped on gaps: nothing changed.\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, newTracker())
			f.model.then(stopOnGaps(report))
			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			posted := f.tr.byAgent()
			if len(posted) != 1 || !strings.Contains(posted[0].Body, "without committing anything") {
				t.Fatalf("comments %+v, want the hand-back for a session that committed nothing", posted)
			}
		})
	}
}

// Questions over what GitHub takes in a comment are cut at a line, and say so,
// so that the hand-back still lands.
func TestQuestionsTooLongForAHandBackAreCutAtALine(t *testing.T) {
	f := setup(t, newTracker())
	line := "1. " + strings.Repeat("Is it so? ", 50) + "Recommended: yes.\n"
	f.model.then(stopOnGaps("Stopped on gaps: nothing changed.\n\n" + strings.Repeat(line, 200)))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 {
		t.Fatalf("comments %+v, want one hand-back", posted)
	}
	body := posted[0].Body
	if n := len([]rune(body)); n > github.BodyLimit {
		t.Errorf("the hand-back is %d characters, over GitHub's %d", n, github.BodyLimit)
	}
	if !strings.Contains(body, "Recommended: yes.\n\n(Cut here:") {
		t.Errorf("the questions are not cut at a line, with a note saying so:\n%s", body[len(body)-600:])
	}
}

// Work with an acceptance criterion it cannot meet by itself refers to its
// issue rather than closing it, under the issue's title, so that merging it
// does not close an issue with a check still to do.
func TestWorkWithACriterionItCannotMeetRefersToItsIssue(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := describe("## Start here\n\nok:1\n\n## Not verified\n\n- The unit starts on the host.\n")(dir); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, ".git", "afk-unmet.md"), []byte("- The unit starts on the host.\n"), 0o644)
	})

	body := f.opened()
	if !strings.HasPrefix(body, implement.PRMarker(issue)+"\nRefs #7\n") {
		t.Errorf("description opens:\n%s\nwant the link line Refs #7", body[:min(len(body), 200)])
	}
	if strings.Contains(body, "Closes #7") {
		t.Errorf("the description closes the issue:\n%s", body)
	}
	if got := f.tr.opened[0].Title; got != "Reserve a job" {
		t.Errorf("title %q, want the issue's", got)
	}
}

// A file that says only "none" is no criterion.
func TestAnUnmetFileThatSaysNoneClosesTheIssue(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(func(dir string) error {
		if err := describe("## Start here\n\nok:1\n")(dir); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, ".git", "afk-unmet.md"), []byte("None.\n"), 0o644)
	})
	if body := f.opened(); !strings.Contains(body, "\nCloses #7\n") {
		t.Errorf("description:\n%s\nwant it to close the issue", body)
	}
}

// premises is a fixture repository a premise is read from, counting the reads.
type premises struct {
	reads int
}

func (r *premises) Issue(_ context.Context, n int) (github.Issue, error) {
	r.reads++
	return github.Issue{Number: n, State: "closed", Title: "The decision", Body: "Decided."}, nil
}

func (r *premises) Comments(context.Context, int) ([]github.Comment, error) { return nil, nil }

func (r *premises) DefaultBranch(context.Context) (string, error) { return "main", nil }

func (r *premises) File(context.Context, string, string) ([]byte, error) {
	r.reads++
	return []byte("package flags\n"), nil
}

// The issue's premises are fetched into the workspace before the session
// starts, and the prompt says where. They are fetched once for the workspace:
// a retry reads what the first run read.
func TestAnIssuesPremisesAreFetchedBeforeTheSessionStarts(t *testing.T) {
	tr := newTracker()
	tr.body = "## What to build\n\nA thing.\n\n## Premises\n\n- Decided in #195.\n- The flag is in https://github.com/up/stream/blob/abc/flags.go.\n"
	f := setup(t, tr)
	f.deps.Repo = "o/n"
	repos := map[string]*premises{}
	f.deps.Premises = func(repo string) premise.Reader {
		if repos[repo] == nil {
			repos[repo] = &premises{}
		}
		return repos[repo]
	}
	var index string
	f.model.then(func(dir string) error {
		b, err := os.ReadFile(filepath.Join(dir, ".git", premise.Dir, premise.Index))
		index = string(b)
		if err != nil {
			return err
		}
		// Committed without the file the gate wants, so the work goes back
		// to the session.
		return commit("a")(dir)
	}, commit("ok"))

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	for _, want := range []string{"1. #195", "o/n#195, an issue, closed", "2. https://github.com/up/stream/blob/abc/flags.go", "the same as at the linked revision"} {
		if !strings.Contains(index, want) {
			t.Errorf("the index the session read has no %q:\n%s", want, index)
		}
	}
	if !strings.Contains(f.model.asked[0].Prompt, ".git/afk-premises/") || !strings.Contains(f.model.asked[0].Prompt, "`index.md`") {
		t.Errorf("the prompt does not say where the premises are:\n%s", f.model.asked[0].Prompt)
	}
	if len(f.model.asked) < 2 {
		t.Fatalf("the model was asked %d times, want a retry", len(f.model.asked))
	}
	if got := repos["o/n"].reads + repos["up/stream"].reads; got != 3 {
		t.Errorf("%d reads of the premises' sources over the first run and its retry, want 3: once each", got)
	}
}

// An issue with no Premises section fetches nothing, and the prompt says
// nothing of premises.
func TestAnIssueWithNoPremisesFetchesNothing(t *testing.T) {
	tr := newTracker()
	tr.body = "Fix https://github.com/o/n/blob/abc/a.go."
	f := setup(t, tr)
	f.deps.Repo = "o/n"
	f.deps.Premises = func(repo string) premise.Reader {
		t.Errorf("a premise read from %s", repo)
		return &premises{}
	}
	f.model.then(commit("ok"))
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if _, err := os.Stat(filepath.Join(f.workspace(), ".git", premise.Dir)); err == nil {
		t.Error("the premises directory was made")
	}
	if strings.Contains(f.model.asked[0].Prompt, "afk-premises") {
		t.Errorf("the prompt names premises:\n%s", f.model.asked[0].Prompt)
	}
}

// The first session is told what a gap means with nobody to ask. A session
// sent back to fix its work is not: it has committed already.
func TestOnlyTheFirstSessionIsToldToStopOnAGap(t *testing.T) {
	f := setup(t, newTracker())
	f.model.then(commit("a"), commit("ok"))
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) < 2 {
		t.Fatalf("the model was asked %d times, want a retry", len(f.model.asked))
	}
	const gap = "means committing nothing"
	if !strings.Contains(f.model.asked[0].Prompt, gap) {
		t.Errorf("the first prompt does not say what a gap means:\n%s", f.model.asked[0].Prompt)
	}
	if strings.Contains(f.model.asked[1].Prompt, gap) {
		t.Errorf("the retry is told to stop on a gap:\n%s", f.model.asked[1].Prompt)
	}
}
