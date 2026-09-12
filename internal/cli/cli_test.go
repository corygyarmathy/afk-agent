package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestMain_ExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		want     int
		stdoutIs string // substring expected on stdout
		stderrIs string // substring expected on stderr
	}{
		{
			name:     "no arguments is a usage error",
			args:     nil,
			want:     ExitUsage,
			stderrIs: "Usage:",
		},
		{
			name:     "unknown command",
			args:     []string{"frobnicate"},
			want:     ExitUsage,
			stderrIs: `unknown command "frobnicate"`,
		},
		{
			name:     "help goes to stdout",
			args:     []string{"help"},
			want:     ExitOK,
			stdoutIs: "afk run <transition>",
		},
		{
			name:     "version",
			args:     []string{"version"},
			want:     ExitOK,
			stdoutIs: Version,
		},
		{
			name:     "run needs a transition",
			args:     []string{"run"},
			want:     ExitUsage,
			stderrIs: "needs a transition name",
		},
		{
			name:     "a flag is not a transition name",
			args:     []string{"run", "--job", "7"},
			want:     ExitUsage,
			stderrIs: "needs a transition name",
		},
		{
			name:     "run needs a subject",
			args:     []string{"run", "review"},
			want:     ExitUsage,
			stderrIs: "one of --job, --issue or --pr",
		},
		{
			name:     "run takes only one subject",
			args:     []string{"run", "review", "--job", "7", "--pr", "12"},
			want:     ExitUsage,
			stderrIs: "only one of --job, --issue or --pr",
		},
		{
			name:     "unknown flag",
			args:     []string{"run", "review", "--branch", "master"},
			want:     ExitUsage,
			stderrIs: "flag provided but not defined",
		},
		{
			name:     "stray positional argument",
			args:     []string{"run", "review", "--pr", "12", "extra"},
			want:     ExitUsage,
			stderrIs: `unexpected argument "extra"`,
		},
		{
			name:     "a well-formed invocation reaches the runner",
			args:     []string{"run", "review", "--pr", "12"},
			want:     ExitError,
			stderrIs: `transition "review" is not implemented (pr 12)`,
		},
		{
			name:     "the ADR's job-id form is accepted",
			args:     []string{"run", "implement", "--job", "01J0"},
			want:     ExitError,
			stderrIs: `transition "implement" is not implemented (job 01J0)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := Main(tt.args, &stdout, &stderr)
			if got != tt.want {
				t.Errorf("exit code = %d, want %d (stderr: %s)", got, tt.want, stderr.String())
			}
			if tt.stdoutIs != "" && !strings.Contains(stdout.String(), tt.stdoutIs) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.stdoutIs)
			}
			if tt.stderrIs != "" && !strings.Contains(stderr.String(), tt.stderrIs) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.stderrIs)
			}
		})
	}
}

// A usage error belongs on stderr, so that a caller redirecting stdout still
// sees it, and so that nothing parsing stdout is handed a usage message.
func TestMain_UsageErrorsStayOffStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := Main([]string{"run"}, &stdout, &stderr); got != ExitUsage {
		t.Fatalf("exit code = %d, want %d", got, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want it empty", stdout.String())
	}
}

func TestSubject_String(t *testing.T) {
	tests := []struct {
		subject Subject
		want    string
	}{
		{Subject{Job: "01J0"}, "job 01J0"},
		{Subject{Issue: "10"}, "issue 10"},
		{Subject{PR: "12"}, "pr 12"},
		{Subject{}, "no subject"},
	}
	for _, tt := range tests {
		if got := tt.subject.String(); got != tt.want {
			t.Errorf("Subject%+v.String() = %q, want %q", tt.subject, got, tt.want)
		}
	}
}
