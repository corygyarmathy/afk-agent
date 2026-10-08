package correction_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/spend"
)

const (
	reviewed  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	corrected = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fix       = "cccccccccccccccccccccccccccccccccccccccc"
)

// posted is an advisory review as the agent posts it: the wrapper, then the
// skill's report, then the spend footer. Finding 2's reproduction holds a
// fenced line that looks like a finding, and one that looks like a heading.
func posted() string {
	return "<details>\n<summary>Advisory review of <code>aaaaaaaaaaaa</code>. Open it after your own reading.</summary>\n\n" +
		review.Marker(reviewed) + "\nThis review does not gate or block merging. Asked for by the implement job for #7, once CI was green.\n\n" +
		"Standards reviewed with Spec.\n\n" +
		"## Standards\n\n" +
		"1. **should-fix** — `AGENTS.md:12`: a parameter is hard-coded. Fix: take it from the module.\n\n" +
		"## Spec\n\n" +
		"No findings at or above `should-fix`.\n\n" +
		"## Correctness\n\n" +
		"2. **blocker** — `internal/x/x.go:40`: an empty name panics.\n\n" +
		"<details><summary>Reproduction</summary>\n\n" +
		"```go\n3. not a finding\n## Not a heading\nfunc TestEmpty(t *testing.T) { x.Do(\"\") }\n```\n\n" +
		"```\npanic: index out of range\n```\n\n" +
		"</details>\n\n" +
		"3. **should-fix** (also Spec) — `internal/x/x.go:52`: the error is dropped.\n\n" +
		"Q1 — Should an empty name be refused rather than defaulted?\n\n" +
		"## Approach\n\n" +
		"4. **should-fix** — `internal/x/x.go:10`: duplicates `y.Do`.\n\n" +
		spend.Open + "\n<sub>What this job's own model runs cost</sub>\n" + spend.Close + "\n\n</details>\n"
}

// The correctable findings are the ones under Correctness and Standards,
// whatever their severity, each with all of its text and none of the next
// thing: not a question, not another axis, not the footer. A fenced
// reproduction is part of its finding, whatever it holds.
func TestTheCorrectableFindingsAreCorrectnessAndStandards(t *testing.T) {
	got := correction.Correctable(posted())
	var numbers []int
	for _, f := range got {
		numbers = append(numbers, f.Number)
	}
	if len(got) != 3 || numbers[0] != 1 || numbers[1] != 2 || numbers[2] != 3 {
		t.Fatalf("findings %v, want 1, 2 and 3", numbers)
	}
	if got[0].Axis != "Standards" || got[1].Axis != "Correctness" || got[2].Axis != "Correctness" {
		t.Errorf("axes %q, %q, %q", got[0].Axis, got[1].Axis, got[2].Axis)
	}
	if !strings.Contains(got[1].Text, "func TestEmpty") || !strings.Contains(got[1].Text, "panic: index out of range") || !strings.HasSuffix(got[1].Text, "</details>") {
		t.Errorf("finding 2 lost its reproduction:\n%s", got[1].Text)
	}
	if strings.Contains(got[2].Text, "Q1") {
		t.Errorf("finding 3 runs into the question:\n%s", got[2].Text)
	}
	if strings.Contains(got[0].Text, "## Spec") || strings.Contains(got[0].Text, "No findings") {
		t.Errorf("finding 1 runs into the next axis:\n%s", got[0].Text)
	}
}

// A review whose findings are all advice for the operator asks for no
// correction, and nor does one with no findings.
func TestAReviewWithNothingCorrectableAsksForNoCorrection(t *testing.T) {
	for name, body := range map[string]string{
		"approach and spec only": "<details>\n" + review.Marker(reviewed) + "\n\n## Standards\n\nNone.\n\n## Spec\n\n1. **blocker** — missing.\n\n## Correctness\n\nNothing at or above the floor.\n\n## Approach\n\n2. **should-fix** — sketch.\n\n</details>\n",
		"no findings":            "<details>\n" + review.Marker(reviewed) + "\nLooks sound.\n</details>\n",
	} {
		if _, ok := correction.Begin(github.Comment{ID: 900, Body: body}, reviewed); ok {
			t.Errorf("%s: a correction was begun", name)
		}
	}
	c, ok := correction.Begin(github.Comment{ID: 900, Body: posted()}, reviewed)
	if !ok || c.Reviewed != reviewed || c.Review != 900 || len(c.Findings) != 3 {
		t.Errorf("Begin = %+v, %v; want the review's three findings, at its head", c, ok)
	}
}

// A corrected review names both heads in its summary, keeps its own marker,
// and collapses each corrected finding to a line linking its commit, with the
// finding folded beneath it. A correctable finding no commit names stays, as
// advice a correction was attempted on, and the rest of the review is as it
// was.
func TestACorrectedReviewLinksEachCorrection(t *testing.T) {
	c, _ := correction.Begin(github.Comment{ID: 900, Body: posted()}, reviewed)
	got := correction.Amended(posted(), c, corrected, map[int]string{2: fix}, "o/n")

	for _, want := range []string{
		"<summary>Advisory review of <code>aaaaaaaaaaaa</code>; corrected to <code>bbbbbbbbbbbb</code>, checked by reproductions and CI, not re-reviewed. Open it after your own reading.</summary>",
		review.Marker(reviewed),
		correction.Marker(c, corrected),
		"https://github.com/o/n/compare/" + reviewed + "..." + corrected,
		`<details><summary>2. Corrected in <a href="https://github.com/o/n/commit/` + fix + `"><code>cccccccccccc</code></a>.</summary>`,
		"func TestEmpty",
		"1. **should-fix** — `AGENTS.md:12`: a parameter is hard-coded. Fix: take it from the module.\n\n_Correction attempted: no commit corrected this, so it is advice._",
		"3. **should-fix** (also Spec) — `internal/x/x.go:52`: the error is dropped.\n\n_Correction attempted: no commit corrected this, so it is advice._",
		"4. **should-fix** — `internal/x/x.go:10`: duplicates `y.Do`.\n\n" + spend.Open,
		"Q1 — Should an empty name be refused",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the edited review does not have %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "Correction attempted") != 2 {
		t.Errorf("want only findings 1 and 3 marked:\n%s", got)
	}
}

// A failed correction leaves the summary and every finding as they were,
// each correctable one marked as a correction that failed, and says why.
func TestAFailedCorrectionLeavesTheFindingsAsAdvice(t *testing.T) {
	c, _ := correction.Begin(github.Comment{ID: 900, Body: posted()}, reviewed)
	c.Failed = "CI still failed after 2 fixes."
	got := correction.Amended(posted(), c, reviewed, nil, "o/n")

	for _, want := range []string{
		"<summary>Advisory review of <code>aaaaaaaaaaaa</code>. Open it after your own reading.</summary>",
		correction.Marker(c, reviewed),
		"attempted and failed: CI still failed after 2 fixes. The pull request is back at `aaaaaaaaaaaa`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the edited review does not have %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "_Correction attempted, failed: this is advice._"); n != 3 {
		t.Errorf("%d findings marked failed, want 3:\n%s", n, got)
	}
	if strings.Contains(got, "Corrected in") {
		t.Errorf("a failed correction links a commit:\n%s", got)
	}
}

// Which commit corrected which finding is read from the commits' trailers: the
// newest commit that names a finding is the one it links.
func TestEachFindingLinksTheNewestCommitThatNamesIt(t *testing.T) {
	dir := t.TempDir()
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "afk", "GIT_AUTHOR_EMAIL": "afk@example.invalid",
		"GIT_COMMITTER_NAME": "afk", "GIT_COMMITTER_EMAIL": "afk@example.invalid",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		t.Setenv(k, v)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "f")
		git("commit", "--quiet", "-m", msg)
		return git("rev-parse", "HEAD")
	}
	git("init", "--quiet")
	base := commit("the work\n\nCorrects: advisory 9")
	first := commit("test the empty name\n\nCorrects: advisory 2")
	second := commit("take the parameter\n\nCorrects: advisory 1, advisory 2\ncorrects: Advisory 3")

	got, err := correction.Commits(context.Background(), dir, base, second)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{1: second, 2: second, 3: second}
	if len(got) != len(want) || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("Commits = %v, want %v (and nothing from before the reviewed head)", got, want)
	}
	if got, _ := correction.Commits(context.Background(), dir, base, first); got[2] != first {
		t.Errorf("to %s, finding 2 is %q, want %s", first, got[2], first)
	}
}
