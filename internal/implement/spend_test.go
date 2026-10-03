package implement_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/spend"
)

// The description ends with what the job spent, a line for each model, the
// run that failed on the first included (#22). A fix CI sent back is spent
// after the pull request opened, and the footer is brought up to date with
// the push it ends in. Which candidate ran when is as it would be with
// nothing spent.
func TestTheDescriptionSaysWhatTheJobSpent(t *testing.T) {
	f := setup(t, newTracker())
	f.model.cost = 0.01
	f.model.then(fail(first), describe("## Start here\n\nok:1\n"), commitAt("x"))
	f.tr.checks = red()

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != implement.Reviewing {
		t.Fatalf("job in %q, want %q", j.State, implement.Reviewing)
	}
	var asked []model.Ref
	for _, r := range f.model.asked {
		asked = append(asked, r.Model)
		if !r.Cost {
			t.Errorf("a run on %s did not ask for its sub-agents' cost", r.Model)
		}
	}
	if fmt.Sprint(asked) != fmt.Sprint([]model.Ref{first, second, first}) {
		t.Errorf("ran on %v, want first, second, and first again for the fix", asked)
	}

	opened := f.tr.opened[0].Body
	for _, want := range []string{
		"<br>\nopencode-go/first · 1k in · 100 out · $0.0100<br>\nopencode-go/second · 1k in · 100 out · $0.0100</sub>",
		"not the account's spend",
	} {
		if !strings.Contains(opened, want) {
			t.Errorf("the description opened without %q:\n%s", want, opened)
		}
	}
	if !strings.Contains(opened, "## Start here\n\nok:1\n\n"+spend.Open) || !strings.HasSuffix(opened, spend.Close+"\n") {
		t.Errorf("the footer is not last, after the session's part:\n%s", opened)
	}

	now := f.tr.prs[0].Body
	if want := "<br>\nopencode-go/first · 2k in · 200 out · $0.0200<br>\nopencode-go/second · 1k in · 100 out · $0.0100</sub>"; !strings.Contains(now, want) {
		t.Errorf("the description after the fix does not say %q:\n%s", want, now)
	}
	if strings.Count(now, spend.Open) != 1 {
		t.Errorf("the description has %d footers, want one:\n%s", strings.Count(now, spend.Open), now)
	}
}

// A job that could not finish still spent, and its hand-back says what.
func TestAHandBackSaysWhatTheJobSpent(t *testing.T) {
	f := setup(t, newTracker())
	f.model.cost = 0.01

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "Nothing was pushed") {
		t.Fatalf("comments %+v, want the hand-back on the issue", posted)
	}
	if want := "opencode-go/first · 1k in · 100 out · $0.0100</sub>"; !strings.Contains(posted[0].Body, want) {
		t.Errorf("the hand-back does not say %q:\n%s", want, posted[0].Body)
	}
}
