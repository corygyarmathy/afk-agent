package work

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// GateTail is how much of the gate's output is kept for the session that has
// to fix it. The end is the part that says what failed. Not a parameter: it
// bounds a file the model reads, and nothing about the work depends on it.
const GateTail = 64 << 10

// gateWaitDelay is how long a cancelled gate's pipes are given to close before
// Wait stops waiting for them. Not a parameter, for the reason opencode's own
// is not: it bounds how long a cancellation takes to return.
const gateWaitDelay = 5 * time.Second

// GateState is what the local gate found on a workspace's commits.
type GateState int

const (
	// GatePassed is a gate that exited zero on the commits.
	GatePassed GateState = iota
	// GateLost is a workspace that is not there, so there is nothing to gate.
	GateLost
	// GateSwitched is a workspace on a branch other than the one the work is
	// for.
	GateSwitched
	// GateEmpty is a workspace with no commit on top of the point the work is
	// counted from.
	GateEmpty
	// GateDirty is uncommitted changes to tracked files, which the gate would
	// not read.
	GateDirty
	// GateFailed is a gate that exited non-zero with attempts left.
	GateFailed
	// GateExhausted is a gate that exited non-zero with the attempts spent.
	GateExhausted
)

// GateResult is what Check decided, and the pieces a kind words its hand-back
// with. The gate's own failure and why are already in the progress Check was
// given.
type GateResult struct {
	State GateState

	// Branch is the branch the workspace is on, for GateSwitched.
	Branch string

	// Output is what to hand back: the gate's output, or the uncommitted
	// changes the gate did not read.
	Output string

	// Attempts is how many times the gate has failed after this check.
	Attempts int
}

// Check runs the local gate over the workspace's commits and counts a failure
// against the progress's attempt bound. It is the loop both kinds run: the
// kind decides what to say about the result, and saves its own progress.
//
// A revision's gate and an implement's differ only in where an empty workspace
// leaves the job and in the words a hand-back uses; the counting, the branch
// check and the gate itself are one.
func (w Workspace) Check(ctx context.Context, jobID string, p *Progress, command string, attempts int) (GateResult, error) {
	if !w.Exists(jobID) {
		return GateResult{State: GateLost}, nil
	}
	ws := w.Dir(jobID)

	if branch, err := BranchOf(ctx, ws); err != nil {
		return GateResult{}, err
	} else if branch != p.Branch {
		return GateResult{State: GateSwitched, Branch: branch}, nil
	}

	// A fix's work is what it adds to the agent's last push. An amend or a
	// rebase of that push counts; the same head again would push nothing.
	since := p.Base
	if p.Pushed != "" {
		since = p.Pushed
	}
	made, err := Commits(ctx, ws, since)
	if err != nil {
		return GateResult{}, err
	}
	if made == 0 {
		return GateResult{State: GateEmpty}, nil
	}

	if dirty, err := Uncommitted(ctx, ws); err != nil {
		return GateResult{}, err
	} else if dirty != "" {
		p.Why = fmt.Sprintf("The local gate, `%s`, was not run: the session left changes to tracked files uncommitted, and the gate reads commits.", command)
		p.Failure = "git status --porcelain --untracked-files=no:\n" + dirty + "\n"
	} else {
		// Untracked files go before the gate runs rather than fail it
		// unread: a session runs the gate itself, and what that leaves
		// behind is not the work. A file the work needed but nobody added
		// goes too, and the gate says so.
		if _, err := inWorkspace(ctx, ws, "clean", "--quiet", "--force", "-d"); err != nil {
			return GateResult{}, err
		}
		passed, output, err := RunGate(ctx, ws, command)
		if err != nil {
			return GateResult{}, err
		}
		if passed {
			p.Failure, p.Why = "", ""
			return GateResult{State: GatePassed, Attempts: p.Attempts}, nil
		}
		p.Why = fmt.Sprintf("The local gate, `%s`, failed on the work: it exited non-zero.", command)
		p.Failure = output
	}

	p.Attempts++
	if p.Attempts >= attempts {
		return GateResult{State: GateExhausted, Output: p.Failure, Attempts: p.Attempts}, nil
	}
	return GateResult{State: GateFailed, Output: p.Failure, Attempts: p.Attempts}, nil
}

// RunGate runs the gate command in the workspace. It reports whether the gate
// passed, and the end of what it wrote. An error is a gate that could not be
// run at all, which is not the work's fault.
func RunGate(ctx context.Context, dir, gate string) (bool, string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", gate)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out

	// Its own process group, as opencode's run has, so that a cancellation
	// reaches what the gate started - a test binary, a server it spun up -
	// and not just the shell, and Wait is not left holding a pipe open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = gateWaitDelay
	err := cmd.Run()
	if cmd.Process != nil {
		// Whatever it left running goes with it, pass or fail.
		killGroup(cmd.Process.Pid)
	}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return false, "", ctx.Err()
	case errors.As(err, &exit):
		return false, Tail(out.String(), GateTail), nil
	case err != nil:
		return false, "", fmt.Errorf("running the gate: %w", err)
	}
	return true, "", nil
}

// killGroup kills a process group. One already gone is not an error.
func killGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// Tail is the last n bytes of s, which is how a gate's output is kept: the end
// is the part that says what failed.
func Tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
