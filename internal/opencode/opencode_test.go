//go:build linux

package opencode_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
)

// The environment the test hands the fake opencode.
const (
	envMode   = "AFK_OPENCODE_FAKE"
	envStream = "AFK_OPENCODE_STREAM"
	envExit   = "AFK_OPENCODE_EXIT"
	envStderr = "AFK_OPENCODE_STDERR"
	envRecord = "AFK_OPENCODE_RECORD"
	envPids   = "AFK_OPENCODE_PIDS"
)

var ref = model.Ref{Provider: "opencode-go", Model: "muse-spark-1.3-contributor"}

// fake returns a Command whose binary is this test binary, standing in for
// opencode in the given mode. A shell script in between, because opencode's
// arguments are `run --model ...` and the test binary needs `-test.run` first;
// it execs, so the pid the Command starts is the helper's own.
func fake(t *testing.T, mode string, env map[string]string) opencode.Command {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode")
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run='^TestHelperIsOpencode$' -- \"$@\"\n", os.Args[0])
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envMode, mode)
	for k, v := range env {
		t.Setenv(k, v)
	}
	return opencode.Command{Path: path}
}

// fixture is a stream recorded from a real `opencode run --format json`.
func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// stream writes an event stream of the test's own to a file.
func stream(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func request(t *testing.T) opencode.Request {
	return opencode.Request{Model: ref, Dir: t.TempDir(), Prompt: "Reply with the single word: ok"}
}

// A run that succeeded, recorded from the real binary on 2026-09-13.
func TestAReplyIsReadFromARecordedRun(t *testing.T) {
	c := fake(t, "replay", map[string]string{envStream: fixture(t, "ok.jsonl"), envExit: "0"})

	got, err := c.Run(context.Background(), request(t))
	if err != nil {
		t.Fatal(err)
	}
	want := opencode.Reply{
		Text:   "ok",
		Cost:   0.000644486,
		Tokens: opencode.Tokens{Input: 6367, Output: 11, Reasoning: 14, CacheRead: 1393},
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// An unknown model, recorded from the real binary. The stream says only
// "Unexpected server error", which is why a failure opencode reports is not
// picked apart: it is transient, and the caller moves to the next candidate.
func TestAnUnknownModelIsTransient(t *testing.T) {
	c := fake(t, "replay", map[string]string{envStream: fixture(t, "model-not-found.jsonl"), envExit: "1"})

	_, err := c.Run(context.Background(), request(t))
	var te *opencode.TransientError
	if !errors.As(err, &te) {
		t.Fatalf("got %v, want a TransientError", err)
	}
	if te.Model != ref {
		t.Errorf("the error names %s, want %s", te.Model, ref)
	}
	if !strings.Contains(err.Error(), "Unexpected server error") {
		t.Errorf("error %q does not say what opencode reported", err)
	}
}

func TestFailuresOpencodeReportsAreTransient(t *testing.T) {
	for _, tc := range []struct {
		name, exit, stderr, want string
		lines                    []string
	}{
		{
			name: "a non-zero exit with nothing on stdout", exit: "2",
			stderr: "429 Too Many Requests", want: "429 Too Many Requests",
		},
		{
			name: "a run that finished without a reply", exit: "0", want: "without writing a reply",
			lines: []string{
				`{"type":"step_start","part":{}}`,
				`{"type":"step_finish","part":{"reason":"stop","cost":0.001}}`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{envExit: tc.exit, envStderr: tc.stderr, envStream: stream(t, tc.lines...)}
			c := fake(t, "replay", env)

			_, err := c.Run(context.Background(), request(t))
			var te *opencode.TransientError
			if !errors.As(err, &te) {
				t.Fatalf("got %v, want a TransientError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// Failures another model would meet just the same.
func TestFailuresThisProcessCanSeeAreFatal(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	os.WriteFile(notADir, nil, 0o644)

	for _, tc := range []struct {
		name string
		cmd  func(t *testing.T) opencode.Command
		req  func(t *testing.T) opencode.Request
	}{
		{
			name: "output that is not an event stream",
			cmd: func(t *testing.T) opencode.Command {
				return fake(t, "replay", map[string]string{envExit: "0", envStream: stream(t, "<html>502 Bad Gateway</html>")})
			},
		},
		{
			name: "a binary that is not there",
			cmd: func(t *testing.T) opencode.Command {
				return opencode.Command{Path: filepath.Join(t.TempDir(), "no-opencode-here")}
			},
		},
		{
			name: "no binary configured",
			cmd:  func(t *testing.T) opencode.Command { return opencode.Command{} },
		},
		{
			name: "a workspace that is not a directory",
			req: func(t *testing.T) opencode.Request {
				r := request(t)
				r.Dir = notADir
				return r
			},
		},
		{
			name: "a prompt the command line would read as a flag",
			req: func(t *testing.T) opencode.Request {
				r := request(t)
				r.Prompt = "--auto"
				return r
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := opencode.Command{Path: "/nonexistent"}
			if tc.cmd != nil {
				c = tc.cmd(t)
			}
			req := request(t)
			if tc.req != nil {
				req = tc.req(t)
			}
			_, err := c.Run(context.Background(), req)
			var fe *opencode.FatalError
			if !errors.As(err, &fe) {
				t.Fatalf("got %v, want a FatalError", err)
			}
			var te *opencode.TransientError
			if errors.As(err, &te) {
				t.Errorf("%v is both fatal and transient", err)
			}
		})
	}
}

// Text written before the last step is narration between tool calls. The
// reply is what the model wrote once it stopped calling them, and the cost is
// every step's.
func TestTheReplyIsTheFinalStep(t *testing.T) {
	c := fake(t, "replay", map[string]string{envExit: "0", envStream: stream(t,
		`{"type":"step_start","part":{}}`,
		`{"type":"text","part":{"text":"Let me read the diff."}}`,
		`{"type":"tool_use","part":{"tool":"read"}}`,
		`{"type":"step_finish","part":{"reason":"tool-calls","cost":0.25,"tokens":{"input":100,"output":10,"reasoning":0,"cache":{"read":5,"write":1}}}}`,
		`{"type":"step_start","part":{}}`,
		`{"type":"text","part":{"text":"The change is sound."}}`,
		`{"type":"text","part":{"text":"One nit: a missing test."}}`,
		`{"type":"step_finish","part":{"reason":"stop","cost":0.5,"tokens":{"input":200,"output":20,"reasoning":3,"cache":{"read":7,"write":0}}}}`,
	)})

	got, err := c.Run(context.Background(), request(t))
	if err != nil {
		t.Fatal(err)
	}
	want := opencode.Reply{
		Text:   "The change is sound.\nOne nit: a missing test.",
		Cost:   0.75,
		Tokens: opencode.Tokens{Input: 300, Output: 30, Reasoning: 3, CacheRead: 12, CacheWrite: 1},
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// The command line is opencode's, the run happens in the workspace, and stdin
// is closed. opencode reads a stdin that is not a terminal as more of the
// message, so an inherited pipe nobody writes to hangs the run before it
// reaches the model - found by doing exactly that while recording testdata/.
func TestTheRunIsInTheWorkspaceWithNothingOnStdin(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record.json")
	c := fake(t, "record", map[string]string{envRecord: record, envStream: fixture(t, "ok.jsonl")})
	req := request(t)

	if _, err := c.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	var got recorded
	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"run", "--model", ref.String(), "--dir", req.Dir, "--format", "json", req.Prompt}
	if fmt.Sprint(got.Args) != fmt.Sprint(wantArgs) {
		t.Errorf("args %q\nwant %q", got.Args, wantArgs)
	}
	if got.Cwd != req.Dir {
		t.Errorf("ran in %s, want the workspace %s", got.Cwd, req.Dir)
	}
	if got.Stdin != "" {
		t.Errorf("stdin carried %q, want nothing", got.Stdin)
	}
}

// Cancelling kills the run and what it started, and the test proves it by
// looking for the processes afterwards rather than by trusting the kill.
func TestCancellingKillsTheRunAndWhatItStarted(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	c := fake(t, "hang", map[string]string{envPids: pids})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Run(ctx, request(t))
		done <- err
	}()

	started := waitForPids(t, pids)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want context.Canceled: a stop is not a failure of the run", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	for _, pid := range started {
		gone(t, pid)
	}
}

// A run that succeeds takes what it started with it. opencode starts language
// servers, and a review that finished must not leave them running for the
// life of the worker.
func TestAFinishedRunLeavesNothingBehind(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	c := fake(t, "orphan", map[string]string{envPids: pids, envStream: fixture(t, "ok.jsonl")})

	if _, err := c.Run(context.Background(), request(t)); err != nil {
		t.Fatal(err)
	}
	for _, pid := range waitForPids(t, pids) {
		gone(t, pid)
	}
}

type recorded struct {
	Args  []string
	Cwd   string
	Stdin string
}

func waitForPids(t *testing.T, path string) []int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			var pids []int
			for _, f := range strings.Fields(string(b)) {
				pid, err := strconv.Atoi(f)
				if err != nil {
					t.Fatal(err)
				}
				pids = append(pids, pid)
			}
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the fake opencode never started")
	return nil
}

// gone fails unless pid has stopped running. A zombie has stopped: it is dead
// and waiting for whoever inherited it to reap it.
func gone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return
		}
		// The state follows the parenthesised command name.
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 && strings.HasPrefix(string(b[i+1:]), " Z") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("process %d is still running", pid)
}

// TestHelperIsOpencode is not a test. It is the fake opencode, run as its own
// process by fake().
func TestHelperIsOpencode(t *testing.T) {
	mode := os.Getenv(envMode)
	if mode == "" {
		t.Skip("run as the fake opencode, not directly")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(helper(mode, args))
}

func helper(mode string, args []string) int {
	switch mode {
	case "replay":
		fmt.Fprint(os.Stderr, os.Getenv(envStderr))
		return replay()
	case "record":
		cwd, _ := os.Getwd()
		stdin, _ := io.ReadAll(os.Stdin)
		b, _ := json.Marshal(recorded{Args: args, Cwd: cwd, Stdin: string(stdin)})
		if err := os.WriteFile(os.Getenv(envRecord), b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return replay()
	case "hang":
		startChild()
		time.Sleep(time.Hour)
		return 0
	case "orphan":
		startChild()
		return replay()
	case "sleep":
		time.Sleep(time.Hour)
		return 0
	}
	fmt.Fprintln(os.Stderr, "unknown fake mode", mode)
	return 3
}

// startChild starts a grandchild that outlives this process unless something
// kills it, the way a language server opencode started would, and records both
// pids.
func startChild() {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperIsOpencode$", "--")
	cmd.Env = append(os.Environ(), envMode+"=sleep")
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	line := fmt.Sprintf("%d %d\n", os.Getpid(), cmd.Process.Pid)
	if err := os.WriteFile(os.Getenv(envPids), []byte(line), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
}

func replay() int {
	if p := os.Getenv(envStream); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		os.Stdout.Write(b)
	}
	code, _ := strconv.Atoi(os.Getenv(envExit))
	return code
}
