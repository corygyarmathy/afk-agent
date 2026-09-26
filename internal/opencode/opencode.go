//go:build unix

// Package opencode runs one model on one prompt, in one directory, and returns
// what it wrote.
//
// It is the seam between a resolved model and the work it does (ADR 0001 §9,
// §10). The resolver chooses a candidate; this runs it, as `opencode run` in a
// process of its own. A run with no session is a new session, which is how a
// review gets a fresh context by construction rather than by care. A run that
// names one continues it, which is how a retry sees what it is retrying
// against (dotfiles ADR 0004 §6).
//
// It does not choose, retry or make a workspace. Which model to try next is
// model.Candidates.Attempt's decision, and the directory is the caller's. What
// it decides is what kind of failure a run was, because that is what the
// caller's decision turns on:
//
//   - A TransientError is anything opencode reported: an error event, a
//     non-zero exit, a run that said nothing. So is a run still going when
//     its bound runs out, which is opencode stuck on a provider as often as
//     anything. Throttles, provider hiccups, a spent pay-as-you-go balance
//     and a model the provider does not recognise all arrive that way and
//     are not distinguishable through the harness - an unknown model's event
//     reads "Unexpected server error" - so they are not distinguished. The
//     caller retries at the next enrolled model.
//   - A SessionGoneError is a run that named a session opencode does not have
//   - deleted, or lost with its data directory. The caller starts a new one.
//   - A FatalError is a failure this process can see for itself: the binary
//     cannot be run, the workspace is not a directory, or what came back is
//     not an event stream at all. Another model would fail the same way.
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/model"
)

// waitDelay is how long a killed run's pipes are given to close before Wait
// stops waiting for them. Not a parameter: it bounds how long a cancellation
// takes to return, and nothing about the work depends on it.
const waitDelay = 5 * time.Second

// stderrTail is how much of a run's stderr a TransientError keeps. The end is
// the part that says what went wrong.
const stderrTail = 4 << 10

// Command is the opencode binary.
type Command struct {
	// Path is the binary to run. A parameter, from configuration.
	Path string

	// Timeout bounds one run: one still going when it runs out is killed, and
	// fails transiently. A parameter, from configuration, and not the lease's
	// TTL - that wants to be short, so a dead holder's job is taken back
	// quickly, and this long enough for the slowest honest run (#93).
	Timeout time.Duration
}

// Request is one run.
type Request struct {
	Model model.Ref

	// Dir is the workspace the run reads and writes. It must exist.
	Dir string

	// Prompt is the message. It may not begin with "-", which the command
	// line would read as a flag.
	Prompt string

	// Session is the session to continue, or empty for a new one.
	Session string
}

// Reply is what a run that succeeded wrote, and what it cost.
type Reply struct {
	// Text is the text of the run's final step. Text written in earlier steps
	// is narration between tool calls ("let me look at..."), and the answer is
	// what the model wrote once it stopped calling them.
	Text string

	// Cost is the run's cost in dollars, as opencode reports it. It informs;
	// it decides nothing (ADR 0001 §11).
	Cost float64

	// Session is the session the run was in, to continue it later.
	Session string

	Tokens Tokens
}

// Tokens is a run's token usage, summed across its steps.
type Tokens struct {
	Input, Output, Reasoning, CacheRead, CacheWrite int
}

// TransientError is a run opencode reported as failed. Retry at the next
// candidate.
type TransientError struct {
	Model model.Ref
	Err   error
}

func (e *TransientError) Error() string { return fmt.Sprintf("%s: %v", e.Model, e.Err) }
func (e *TransientError) Unwrap() error { return e.Err }

// SessionGoneError is a run that asked to continue a session opencode does not
// have. Nothing ran.
type SessionGoneError struct{ Session string }

func (e *SessionGoneError) Error() string {
	return fmt.Sprintf("opencode has no session %s", e.Session)
}

// sessionGone is what opencode writes to stderr for a session it does not
// have, exiting 1 with nothing on stdout. Seen from opencode 1.18.31. A
// release that words it differently turns this into a TransientError, which
// retries the session at each candidate and then defers: slower, and loud,
// rather than wrong.
const sessionGone = "Session not found"

// FatalError is a run that could not have succeeded on any model.
type FatalError struct{ Err error }

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }

// Run runs one request to completion.
//
// Cancelling ctx kills the run and everything it started - opencode starts
// language servers of its own, and a run abandoned by its transition must not
// leave them behind. The error is then ctx's, and is neither transient nor
// fatal: nothing failed, the caller stopped. A run that outlives Timeout is
// killed the same way, and that is a failure: the run did not finish.
func (c Command) Run(ctx context.Context, req Request) (Reply, error) {
	if err := c.check(req); err != nil {
		return Reply{}, &FatalError{err}
	}

	// The bound on a context of its own, so that its running out can be told
	// apart from the caller's context ending.
	bounded, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	args := []string{"run", "--model", req.Model.String(), "--dir", req.Dir, "--format", "json"}
	if req.Session != "" {
		args = append(args, "--session", req.Session)
	}
	cmd := exec.CommandContext(bounded, c.Path, append(args, req.Prompt)...)
	cmd.Dir = req.Dir

	// Stdin is the null device, and must be. opencode reads a stdin that is
	// not a terminal as more of the message, and a stdin inherited from a
	// service manager or a test harness is an open pipe nobody writes to: the
	// run waits on it forever before it ever reaches the model.
	cmd.Stdin = nil

	// Its own process group, so that a kill reaches the language servers it
	// started as well as opencode itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = waitDelay

	var stderr tail
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Reply{}, &FatalError{err}
	}
	if err := cmd.Start(); err != nil {
		return Reply{}, &FatalError{fmt.Errorf("starting %s: %w", c.Path, err)}
	}

	reply, reported, decodeErr := decode(stdout)
	if decodeErr != nil {
		// Stop reading, so stop the run: nothing it writes after this can be
		// understood either.
		killGroup(cmd.Process.Pid)
	}
	io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	// Whatever it left running goes with it, on success as on failure. The
	// group outlives its leader for as long as anything in it does.
	killGroup(cmd.Process.Pid)

	// Once the bound has run out, Wait blames it for any run whose group the
	// kill reached - including one that had already exited on its own, and
	// left something behind holding its stdout. Whether the run was killed
	// is in how it ended.
	killed := false
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		killed = ws.Signaled()
	}
	if bounded.Err() != nil && !killed {
		waitErr = nil
		if !cmd.ProcessState.Success() {
			waitErr = &exec.ExitError{ProcessState: cmd.ProcessState}
		}
	}

	switch {
	case ctx.Err() != nil:
		return Reply{}, ctx.Err()
	case bounded.Err() != nil && killed:
		// Before the stream is judged: a run killed mid-line leaves half an
		// event, which is the kill's doing rather than opencode's. A run that
		// exited as the bound ran out is judged as it would have been without
		// one.
		return Reply{}, c.transient(req, fmt.Errorf("the run was still going after %s, and was killed", c.Timeout), &stderr)
	case decodeErr != nil:
		return Reply{}, &FatalError{fmt.Errorf("%s did not write an event stream: %w", c.Path, decodeErr)}
	case reported != nil:
		return Reply{}, c.transient(req, reported, &stderr)
	case waitErr != nil && req.Session != "" && reply.Session == "" && strings.Contains(stderr.String(), sessionGone):
		return Reply{}, &SessionGoneError{Session: req.Session}
	case waitErr != nil:
		return Reply{}, c.transient(req, waitErr, &stderr)
	case strings.TrimSpace(reply.Text) == "":
		return Reply{}, c.transient(req, errors.New("the run finished without writing a reply"), &stderr)
	}
	return reply, nil
}

func (c Command) check(req Request) error {
	switch {
	case c.Path == "":
		return errors.New("no opencode binary configured")
	case c.Timeout <= 0:
		return errors.New("no bound on a run configured")
	case req.Model.Provider == "" || req.Model.Model == "":
		return fmt.Errorf("model %q is not provider/model", req.Model)
	case req.Prompt == "":
		return errors.New("empty prompt")
	case strings.HasPrefix(req.Prompt, "-"):
		return errors.New("a prompt beginning with \"-\" would be read as a flag")
	}
	info, err := os.Stat(req.Dir)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace %s is not a directory", req.Dir)
	}
	return nil
}

func (c Command) transient(req Request, err error, stderr *tail) error {
	if s := strings.TrimSpace(stderr.String()); s != "" {
		err = fmt.Errorf("%w\nstderr: %s", err, s)
	}
	return &TransientError{Model: req.Model, Err: err}
}

// event is one line of `opencode run --format json`. Only the fields this
// package reads; testdata/ holds streams recorded from real runs, so a change
// upstream shows up as a failing test rather than as a silently empty reply.
type event struct {
	Type    string `json:"type"`
	Session string `json:"sessionID"`
	Part    struct {
		Text   string  `json:"text"`
		Cost   float64 `json:"cost"`
		Tokens *struct {
			Input     int `json:"input"`
			Output    int `json:"output"`
			Reasoning int `json:"reasoning"`
			Cache     struct {
				Read  int `json:"read"`
				Write int `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
	} `json:"part"`
	Error *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// decode reads the event stream to its end. It returns the reply as far as the
// stream built one, the first error the run reported, and an error of its own
// if a line was not an event.
//
// Event types it does not read - tool calls, and whatever a later release adds
// - are skipped rather than refused. An unknown type is upstream saying more,
// and a line that is not JSON is upstream saying something else entirely.
func decode(r io.Reader) (Reply, error, error) {
	var (
		reply    Reply
		step     []string
		reported error
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			return reply, reported, fmt.Errorf("line %q: %w", truncate(line), err)
		}
		if ev.Type == "" {
			return reply, reported, fmt.Errorf("line %q has no event type", truncate(line))
		}
		if reply.Session == "" {
			reply.Session = ev.Session
		}
		switch ev.Type {
		case "step_start":
			step = step[:0]
		case "text":
			step = append(step, ev.Part.Text)
		case "step_finish":
			reply.Cost += ev.Part.Cost
			if t := ev.Part.Tokens; t != nil {
				reply.Tokens.Input += t.Input
				reply.Tokens.Output += t.Output
				reply.Tokens.Reasoning += t.Reasoning
				reply.Tokens.CacheRead += t.Cache.Read
				reply.Tokens.CacheWrite += t.Cache.Write
			}
		case "error":
			if reported == nil {
				reported = reportedError(ev)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return reply, reported, err
	}
	reply.Text = strings.Join(step, "\n")
	return reply, reported, nil
}

func reportedError(ev event) error {
	if ev.Error == nil {
		return errors.New("opencode reported an error and did not say what")
	}
	if ev.Error.Data.Message == "" {
		return fmt.Errorf("opencode reported %s", ev.Error.Name)
	}
	return fmt.Errorf("opencode reported %s: %s", ev.Error.Name, ev.Error.Data.Message)
}

// killGroup kills a process group. A group that is already gone is not an
// error: that is the outcome being asked for.
func killGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func truncate(b []byte) string {
	const n = 120
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// tail is an io.Writer that keeps the last stderrTail bytes written to it.
type tail struct{ buf []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - stderrTail; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.buf) }
