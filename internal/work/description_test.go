package work_test

import (
	"context"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/spend"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// editor keeps the last description it was given.
type editor struct {
	body  string
	edits int
}

func (e *editor) EditPullRequest(_ context.Context, _ int, body string) error {
	e.edits++
	e.body = body
	return nil
}

// described is an agent's pull request description with line as its
// sensitive line, or none, and start as its Start here section.
func described(line, start string) string {
	top := "Closes #7.\n\n"
	if line != "" {
		top += line + "\n\n"
	}
	return top + sensitive.Reminder + ": read #7 first.\n\n## Start here\n\n" + start + "\n"
}

// The edit starts from the description as it is when the edit is made, not as
// it was read when the edit was decided: what the operator wrote in between is
// kept.
func TestRedescribeKeepsAnEditMadeAfterItWasDecided(t *testing.T) {
	infra := []sensitive.Touched{{Label: "infra", Files: []string{"infra/main.tf"}}}
	line := "**Sensitive:** infra (`infra/main.tf`)"
	for _, c := range []struct {
		name  string
		now   string
		want  string
		edits int
	}{
		{"the operator's edit is kept", described("", "The widget is renamed. The operator's note."), described(line, "The widget is renamed. The operator's note."), 1},
		{"the line is right by then", described(line, "The widget is renamed."), "", 0},
		{"the reminder is gone by then", "Closes #7.\n\nThe operator's own description.\n", "", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := storetest.Open(t)
			ed := &editor{}
			pr := github.PullRequest{Number: 12, Body: described("", "The widget is renamed.")}
			reread := func(context.Context) (string, bool, error) { return c.now, true, nil }
			nolog := func(string, ...any) {}

			effect, ok, err := work.Redescribe(context.Background(), s, 3, ed, reread, pr, "feature", "abc123", infra, nil, nolog)
			if err != nil || !ok {
				t.Fatalf("Redescribe = %v, %v; want an edit", ok, err)
			}
			if err := effect.Do(context.Background()); err != nil {
				t.Fatal(err)
			}
			if ed.edits != c.edits {
				t.Fatalf("the description was edited %d times, want %d", ed.edits, c.edits)
			}
			if c.edits > 0 && ed.body != c.want {
				t.Errorf("the description is\n%s\nwant\n%s", ed.body, c.want)
			}
		})
	}
}

// A pull request gone by the time the edit is made is not edited.
func TestRedescribeDoesNotEditAPullRequestThatIsGone(t *testing.T) {
	s := storetest.Open(t)
	ed := &editor{}
	pr := github.PullRequest{Number: 12, Body: described("", "The widget is renamed.")}
	reread := func(context.Context) (string, bool, error) { return "", false, nil }
	infra := []sensitive.Touched{{Label: "infra", Files: []string{"infra/main.tf"}}}

	effect, ok, err := work.Redescribe(context.Background(), s, 3, ed, reread, pr, "feature", "abc123", infra, nil, func(string, ...any) {})
	if err != nil || !ok {
		t.Fatalf("Redescribe = %v, %v; want an edit", ok, err)
	}
	if err := effect.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ed.edits != 0 {
		t.Errorf("the description was edited %d times, want none:\n%s", ed.edits, strings.TrimSpace(ed.body))
	}
}

// The footer is brought to what the job has spent alongside the sensitive
// line, and a nil spend - a revision's push, whose spend is its reply's -
// leaves the implement job's footer as it is.
func TestRedescribeBringsTheFooterUpToDate(t *testing.T) {
	var was, now spend.Spent
	ref := model.Ref{Provider: "opencode-go", Model: "first"}
	was.Add(ref, opencode.Reply{Cost: 0.01, Tokens: opencode.Tokens{Input: 1}})
	now.Add(ref, opencode.Reply{Cost: 0.03, Tokens: opencode.Tokens{Input: 3}})
	opened := described("", "The widget is renamed.") + "\n" + was.Held() + "\n"

	for _, c := range []struct {
		name  string
		spent *spend.Spent
		want  string
		ok    bool
	}{
		{"spent since", &now, described("", "The widget is renamed.") + "\n" + now.Footer() + "\n", true},
		{"nothing since", &was, "", false},
		{"a revision's push", nil, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := storetest.Open(t)
			ed := &editor{}
			pr := github.PullRequest{Number: 12, Body: opened}
			reread := func(context.Context) (string, bool, error) { return opened, true, nil }

			effect, ok, err := work.Redescribe(context.Background(), s, 3, ed, reread, pr, "feature", "abc123", nil, c.spent, func(string, ...any) {})
			if err != nil || ok != c.ok {
				t.Fatalf("Redescribe = %v, %v; want %v", ok, err, c.ok)
			}
			if !ok {
				return
			}
			if err := effect.Do(context.Background()); err != nil {
				t.Fatal(err)
			}
			if ed.body != c.want {
				t.Errorf("the description is\n%s\nwant\n%s", ed.body, c.want)
			}
		})
	}
}
