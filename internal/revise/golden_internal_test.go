package revise

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/spend"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// TestWriteGoldenProgress writes testdata's progress files with the progress
// type and save as they are before delivery (#212): every field set, so every
// key is in the file. Run once, with AFK_WRITE_GOLDEN=1, and removed after.
func TestWriteGoldenProgress(t *testing.T) {
	if os.Getenv("AFK_WRITE_GOLDEN") == "" {
		t.Skip("writes the golden progress files; AFK_WRITE_GOLDEN=1 to run")
	}
	p := progress{
		Progress: work.Progress{
			Nonce:     "8c41d2e7f0a95b13",
			Branch:    "feature",
			Base:      "fed456",
			Into:      "main",
			Head:      "2222222222222222222222222222222222222222",
			Pushed:    "2222222222222222222222222222222222222222",
			PushedAt:  time.Date(2026, 9, 28, 11, 0, 0, 0, time.UTC),
			Session:   "ses_1",
			LastInput: 120000,
			Attempts:  1,
			Failure:   "FAIL: no ok\n",
			Why:       "FAIL: no ok",
			Fixes:     1,
			FixedHead: "3333333333333333333333333333333333333333",
			Spent: spend.Spent{Lines: []spend.Line{{
				Model: "anthropic/claude-sonnet", Input: 1000, Output: 200, Reasoning: 50, CacheRead: 300, CacheWrite: 40,
				Cost: 0.25, Listed: 0.5, Priced: true,
			}}},
		},
		Read:               "abc123",
		Points:             []int64{1, 2},
		PullRequestReviews: []int64{3},
		Reply:              "Renamed Foo to Bar.\n",
		Replays:            1,
		Measured:           true,
		Lines:              120,
		Tests:              80,
		Sensitive:          []sensitive.Touched{{Label: "job store schema", Files: []string{"internal/store/schema.sql"}}},
	}
	d := &Deps{StateDir: t.TempDir()}
	write := func(name string, p progress) {
		if err := d.save("job", p); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(d.work().ProgressPath("job"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("testdata", name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("progress.json", p)
	p.Correction = &correction.Correction{
		Reviewed: "2222222222222222222222222222222222222222",
		Review:   900,
		Findings: []correction.Finding{{Number: 1, Axis: "Standards", Text: "breaks the rule in AGENTS.md"}, {Number: 2, Axis: "Correctness", Text: "the file is empty"}},
		Given:    true,
		Failed:   "The gate still failed after 3 attempts.",
		From:     1,
	}
	write("progress-correction.json", p)
}
