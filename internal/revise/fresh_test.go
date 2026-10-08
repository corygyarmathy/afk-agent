package revise_test

import (
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/revise"
)

// A revision's session whose last turn was over the threshold is not
// continued for a fix: a fresh session is given the send-back and what CI
// said, and its fix lands on top of the revision's push (#192).
func TestAnOutgrownRevisionSessionIsNotContinued(t *testing.T) {
	f := setupRevision(t)
	f.deps.FreshAt = 1000
	f.model.lastInput = 1001
	f.model.then(
		reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."),
		reviseOn("fix.txt", "## Points\n\n- \"Rename Foo\" done, and fixed."),
	)
	var first string
	f.tr.checks = redOn(&first)

	job := f.drive()
	if job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s\n%s", job.State, revise.Replying, f.handBack())
	}
	if len(f.model.asked) != 2 {
		t.Fatalf("the model was asked %d times, want 2", len(f.model.asked))
	}
	fix := f.model.asked[1]
	if fix.Session != "" {
		t.Errorf("the fix continued session %q, want a fresh one", fix.Session)
	}
	for _, want := range []string{".git/afk-send-back.md", "CI failed", "`test`", ".git/afk-gate.log", ".git/afk-reply.md"} {
		if !strings.Contains(fix.Prompt, want) {
			t.Errorf("the fresh session's prompt does not say %q:\n%s", want, fix.Prompt)
		}
	}
	if !strings.Contains(f.gateLog(), "--- FAIL: TestBar") {
		t.Errorf("CI's output was not left for the fresh session:\n%s", f.gateLog())
	}
	if at := f.remoteHead(); at == first || !f.ancestor(first, at) {
		t.Errorf("the fix %s is not pushed on top of the revision's push %s", at, first)
	}
	var fresh []string
	for _, l := range f.logs {
		if strings.Contains(l, "fresh session") {
			fresh = append(fresh, l)
		}
	}
	if len(fresh) != 1 || !strings.Contains(fresh[0], "ses_1") || !strings.Contains(fresh[0], "1001") || !strings.Contains(fresh[0], "1000") {
		t.Errorf("logs = %q, want one line saying ses_1 at 1001 was over 1000", f.logs)
	}
}

// At the threshold, the revision's session is continued.
func TestARevisionSessionAtTheThresholdIsContinued(t *testing.T) {
	f := setupRevision(t)
	f.deps.FreshAt = 1000
	f.model.lastInput = 1000
	f.model.then(
		reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."),
		reviseOn("fix.txt", "## Points\n\n- \"Rename Foo\" done, and fixed."),
	)
	var first string
	f.tr.checks = redOn(&first)

	if job := f.drive(); job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s\n%s", job.State, revise.Replying, f.handBack())
	}
	if len(f.model.asked) != 2 || f.model.asked[1].Session != "ses_1" {
		t.Errorf("requests = %+v, want the fix in the revision's session, ses_1", f.model.asked)
	}
}
