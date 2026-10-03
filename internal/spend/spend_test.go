package spend_test

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/spend"
)

var (
	flash = model.Ref{Provider: "opencode-go", Model: "deepseek-v4-flash"}
	pro   = model.Ref{Provider: "opencode-go", Model: "deepseek-v4-pro"}
)

// A job that ran on two models shows two lines, each named as enrolled, and
// runs on one model are summed into its line.
func TestAJobThatUsedTwoModelsShowsTwoLines(t *testing.T) {
	var s spend.Spent
	s.Add(context.Background(), flash, nil, opencode.Reply{Cost: 0.01, Tokens: opencode.Tokens{Input: 1000, Output: 200}})
	s.Add(context.Background(), pro, nil, opencode.Reply{Cost: 0.5, Tokens: opencode.Tokens{Input: 120000, CacheRead: 1080000, Output: 30000, Reasoning: 4500}})
	s.Add(context.Background(), flash, nil, opencode.Reply{Cost: 0.02, Tokens: opencode.Tokens{Input: 500, Output: 300}})

	got := s.Footer()
	for _, want := range []string{
		"opencode-go/deepseek-v4-flash · 1.5k in · 500 out · $0.0300",
		"opencode-go/deepseek-v4-pro · 1.2M in (1.1M cached) · 34.5k out · $0.5000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("footer\n%s\nhas no line %q", got, want)
		}
	}
	if strings.Index(got, flash.String()) > strings.Index(got, pro.String()) {
		t.Errorf("footer\n%s\nis not in the order the models were first used", got)
	}
}

// The footer says it is an estimate of the agent's own spend at list price, so
// a reader cannot take it for a bill or for the account's spend.
func TestTheFooterSaysWhatItIs(t *testing.T) {
	var s spend.Spent
	s.Add(context.Background(), flash, nil, opencode.Reply{Cost: 0.01, Tokens: opencode.Tokens{Input: 1}})
	got := s.Footer()
	for _, want := range []string{"this job's own", "estimated at list price", "not a bill", "not the account's spend"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer\n%s\ndoes not say %q", got, want)
		}
	}
	if !strings.HasPrefix(got, spend.Open) || !strings.HasSuffix(got, spend.Close) {
		t.Errorf("footer\n%s\nis not between its hidden lines", got)
	}
}

// A model with no price, from opencode or a catalogue, reports its tokens and
// no figure, rather than a zero that reads as free.
func TestAnUnpricedModelShowsNoFigure(t *testing.T) {
	var s spend.Spent
	s.Add(context.Background(), flash, nil, opencode.Reply{Tokens: opencode.Tokens{Input: 900, Output: 90}})
	got := s.Footer()
	if !strings.Contains(got, "900 in · 90 out · no listed price") {
		t.Errorf("footer\n%s\ndoes not show the tokens with no price", got)
	}
	if strings.Contains(got, "$") {
		t.Errorf("footer\n%s\nshows a figure for a model with no price", got)
	}
}

// prices is a catalogue that lists flash at 1 in, 4 out, 0.1 a cached read
// and nothing for a cache write, per million tokens; lists pro as free; and
// lists nothing else. It counts its lookups.
func prices(asked *int) spend.Prices {
	return func(_ context.Context, ref model.Ref) model.Price {
		*asked++
		switch ref {
		case flash:
			return model.Price{Input: 1, Output: 4, CacheRead: 0.1, Known: true}
		case pro:
			return model.Price{Known: true}
		}
		return model.Price{}
	}
}

// A run opencode reported no cost for is priced from the catalogue's dearest
// band, and the footer says which part of the figure that is. A model the
// catalogue lists as free shows a zero, and one it does not list shows none.
func TestARunOpencodeDidNotPriceIsPricedFromTheCatalogue(t *testing.T) {
	other := model.Ref{Provider: "opencode-go", Model: "unlisted"}
	tokens := opencode.Tokens{Input: 1_000_000, Output: 100_000, Reasoning: 100_000, CacheRead: 1_000_000, CacheWrite: 1_000_000}
	for _, c := range []struct {
		name string
		ref  model.Ref
		runs []opencode.Reply
		want string
	}{
		{"none priced by opencode", flash, []opencode.Reply{{Tokens: tokens}}, "· $2.9000 at the catalogue's dearest price"},
		{"some priced by opencode", flash, []opencode.Reply{{Tokens: tokens}, {Cost: 0.5, Tokens: opencode.Tokens{Input: 1}}}, "· $3.4000, $2.9000 of it at the catalogue's dearest price"},
		{"listed as free", pro, []opencode.Reply{{Tokens: tokens}}, "· $0.0000"},
		{"not listed", other, []opencode.Reply{{Tokens: tokens}}, "· no listed price"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var s spend.Spent
			var asked int
			for _, r := range c.runs {
				s.Add(context.Background(), c.ref, prices(&asked), r)
			}
			if got := s.Footer(); !strings.Contains(got, c.want+"</sub>") {
				t.Errorf("footer\n%s\ndoes not end its line with %q", got, c.want)
			}
			if asked != 1 {
				t.Errorf("the catalogue was asked %d times, want once: for the run opencode did not price", asked)
			}
		})
	}
}

// Sub-agents' cost that could not be read makes the figure the floor it is.
func TestUnreadSubAgentsMakeTheFigureAFloor(t *testing.T) {
	var s spend.Spent
	s.Add(context.Background(), pro, nil, opencode.Reply{Cost: 0.25, Tokens: opencode.Tokens{Input: 10}, SubAgents: 3, Unread: 2})
	if want := "opencode-go/deepseek-v4-pro and its sub-agents · 10 in · 0 out · ≥ $0.2500, with 2 sub-agents' cost unread"; !strings.Contains(s.Footer(), want) {
		t.Errorf("footer\n%s\nhas no line %q", s.Footer(), want)
	}
}

// A job whose runs reported nothing has no footer, not a zero one.
func TestNoSpendReportedIsNoFooter(t *testing.T) {
	var s spend.Spent
	s.Add(context.Background(), flash, nil, opencode.Reply{Text: "done"})
	if got := s.Footer(); got != "" {
		t.Errorf("footer %q for runs that reported nothing, want none", got)
	}
}

// Spent outlives a transition in a job's state file.
func TestSpentRoundTrips(t *testing.T) {
	var s spend.Spent
	s.Add(context.Background(), pro, nil, opencode.Reply{Cost: 0.1, Tokens: opencode.Tokens{Input: 1, Output: 2, Reasoning: 3, CacheRead: 4, CacheWrite: 5}, SubAgents: 1, Unread: 1})
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back spend.Spent
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Footer() != s.Footer() {
		t.Errorf("round trip gave\n%s\nwant\n%s", back.Footer(), s.Footer())
	}
}

func TestWith(t *testing.T) {
	var one, two spend.Spent
	one.Add(context.Background(), flash, nil, opencode.Reply{Cost: 0.01, Tokens: opencode.Tokens{Input: 1}})
	two.Add(context.Background(), flash, nil, opencode.Reply{Cost: 0.02, Tokens: opencode.Tokens{Input: 2}})
	body := "intro\n\n## Start here\n\nsome text\n"

	opened := spend.With(body, one.Footer())
	if want := body + "\n" + one.Footer() + "\n"; opened != want {
		t.Errorf("a body with no footer became\n%q\nwant it appended:\n%q", opened, want)
	}
	if got := spend.With(opened, two.Footer()); got != body+"\n"+two.Footer()+"\n" {
		t.Errorf("the footer was not replaced:\n%q", got)
	}
	held := body + "\n" + spend.Open + "\n" + spend.Close + "\n"
	if got := spend.With(opened, ""); got != held {
		t.Errorf("an empty footer left\n%q\nwant its hidden lines held:\n%q", got, held)
	}
	if got := spend.With(body, ""); got != body {
		t.Errorf("no footer to remove changed the body to %q", got)
	}

	// A session that typed the hidden lines in its part does not move the
	// footer: the agent's are held after it from the start, and the last.
	typed := "## Start here\n\n" + spend.Open + " fake " + spend.Close + "\n"
	for _, start := range []spend.Spent{{}, one} {
		got := spend.With(typed+"\n"+start.Held()+"\n", two.Footer())
		if !strings.HasPrefix(got, typed) || !strings.HasSuffix(got, two.Footer()+"\n") {
			t.Errorf("a typed footer was taken for the agent's:\n%q", got)
		}
	}

	// Text an operator added after the footer is theirs to keep.
	after := opened + "\nan operator's note\n"
	if got := spend.With(after, two.Footer()); !strings.HasSuffix(got, two.Footer()+"\n\nan operator's note\n") {
		t.Errorf("the text after the footer was not kept:\n%q", got)
	}
}

func TestStrip(t *testing.T) {
	var s spend.Spent
	s.Add(context.Background(), flash, nil, opencode.Reply{Cost: 0.01, Tokens: opencode.Tokens{Input: 1}})
	body := "Closes #7.\n\n## Start here\n\nok:1\n"
	for _, footer := range []string{s.Held(), spend.Spent{}.Held()} {
		if got := spend.Strip(body + "\n" + footer + "\n"); got != body {
			t.Errorf("stripped to\n%q\nwant\n%q", got, body)
		}
	}
	if got := spend.Strip(body); got != body {
		t.Errorf("a body with no footer became %q", got)
	}
}

// Nothing computed here is an input to a decision (ADR 0001 §11): none of the
// packages that decide whether, where or when work runs - admission, the
// resolver, the dispatcher, the transition runner - imports this one.
func TestNothingThatDecidesReadsTheSpend(t *testing.T) {
	const self = "github.com/corygyarmathy/afk-agent/internal/spend"
	for _, pkg := range []string{"budget", "model", "dispatch", "transition"} {
		files, err := filepath.Glob(filepath.Join("..", pkg, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no package %s to check", pkg)
		}
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range parsed.Imports {
				if path, _ := strconv.Unquote(imp.Path.Value); path == self {
					t.Errorf("%s imports the spend, which decides nothing", f)
				}
			}
		}
	}
}
