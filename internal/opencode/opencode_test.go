//go:build linux

package opencode_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

	// envExports is a directory of `opencode export` output, one <session>.json
	// each. A session with no file is one opencode does not have.
	envExports = "AFK_OPENCODE_EXPORTS"

	// envExportHang is a file an export writes when it starts, and then hangs
	// rather than exporting anything.
	envExportHang = "AFK_OPENCODE_EXPORT_HANG"
)

var ref = model.Ref{Provider: "opencode-go", Model: "muse-spark-1.3-contributor"}

// bound is a run's bound in every test that is not about it: long enough that
// no run here reaches it.
const bound = time.Minute

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
	return opencode.Command{Path: path, Timeout: bound}
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

// costed is a request that asks for its sub-agents' cost.
func costed(t *testing.T) opencode.Request {
	req := request(t)
	req.Cost = true
	return req
}

// A run that succeeded, recorded from the real binary on 2026-09-13.
func TestAReplyIsReadFromARecordedRun(t *testing.T) {
	c := fake(t, "replay", map[string]string{envStream: fixture(t, "ok.jsonl"), envExit: "0"})

	got, err := c.Run(context.Background(), request(t))
	if err != nil {
		t.Fatal(err)
	}
	want := opencode.Reply{
		Text:    "ok",
		Cost:    0.000644486,
		Session: "ses_f65d00bbeffecf7REGDTBMqK4o",
		Tokens:  opencode.Tokens{Input: 6367, Output: 11, Reasoning: 14, CacheRead: 1393},
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// A run that handed work to a sub-agent, recorded from the real binary on
// 2026-09-28 with the child's export beside it. The sub-agent ran in a session
// of its own, and nothing of it is in the run's stream but the task tool call
// naming that session: its cost is read from its export and counted, or a
// review that fans out reports a fraction of what it spent (#99).
func TestASubAgentsCostIsCounted(t *testing.T) {
	dir := exports(t, map[string]string{"ses_f18153054ffe81GX1wynCfZBXI": readFixture(t, "task-child.json")})
	c := fake(t, "replay", map[string]string{envStream: fixture(t, "task.jsonl"), envExit: "0", envExports: dir})

	got, err := c.Run(context.Background(), costed(t))
	if err != nil {
		t.Fatal(err)
	}
	// The run's own steps, then the sub-agent's session.
	const want = 0.001925676 + 0.00007284 + 0.001617276
	if math.Abs(got.Cost-want) > 1e-12 {
		t.Errorf("cost %v, want %v: the run's and its sub-agent's", got.Cost, want)
	}
	wantTokens := opencode.Tokens{Input: 12306 + 188 + 10730, Output: 124 + 4 + 4, CacheRead: 1792 + 14080 + 1792}
	if got.Tokens != wantTokens {
		t.Errorf("tokens %+v, want %+v", got.Tokens, wantTokens)
	}
	if got.SubAgents != 1 || got.Unread != 0 {
		t.Errorf("%d sub-agents with %d unread, want 1 with none", got.SubAgents, got.Unread)
	}
	if got.Text != "pong" || got.Session != "ses_f1815410dffe1YAxJ0v1eQ7qFL" {
		t.Errorf("text %q in %s, want the run's own reply in its own session", got.Text, got.Session)
	}
}

// A sub-agent whose cost cannot be read does not fail the run: the cost
// informs and decides nothing. The reply says how many went uncounted, so
// that what it reports reads as the floor it is.
func TestASubAgentWhoseCostCannotBeReadIsUnread(t *testing.T) {
	c := fake(t, "replay", map[string]string{envStream: fixture(t, "task.jsonl"), envExit: "0", envExports: exports(t, nil)})

	got, err := c.Run(context.Background(), costed(t))
	if err != nil {
		t.Fatalf("got %v, want the reply: an uncounted sub-agent is not a failed run", err)
	}
	if got.Unread != 1 {
		t.Errorf("%d sub-agents unread, want 1", got.Unread)
	}
	const want = 0.001925676 + 0.00007284
	if math.Abs(got.Cost-want) > 1e-12 {
		t.Errorf("cost %v, want the run's own %v", got.Cost, want)
	}
}

// A sub-agent's own sub-agents are counted too, each session once however
// many times it is named. opencode 1.18.31 denies the general sub-agent the
// task tool, so this is a shape the recording could not show: the export's
// task part is the one the parent's session holds for the same call.
func TestASubAgentsSubAgentsAreCounted(t *testing.T) {
	c := fake(t, "replay", map[string]string{
		envExit: "0",
		envStream: stream(t,
			`{"type":"step_start","sessionID":"ses_p","part":{"type":"step-start"}}`,
			taskEvent("ses_p", "ses_c"),
			taskEvent("ses_p", "ses_c"),
			`{"type":"text","sessionID":"ses_p","part":{"type":"text","text":"done"}}`,
			`{"type":"step_finish","sessionID":"ses_p","part":{"type":"step-finish","cost":1}}`,
		),
		envExports: exports(t, map[string]string{
			"ses_c": exported("ses_c", 2, "ses_g"),
			"ses_g": exported("ses_g", 4),
		}),
	})

	got, err := c.Run(context.Background(), costed(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Cost != 7 || got.SubAgents != 2 || got.Unread != 0 {
		t.Errorf("cost %v across %d sub-agents with %d unread, want 7 across 2 with none: the run, its sub-agent and that one's, once each", got.Cost, got.SubAgents, got.Unread)
	}
}

// A request that does not ask for its sub-agents' cost does not read their
// sessions: its reply's cost is the run's own, and a caller that has no use
// for the figure does not wait on the reads.
func TestSubAgentsAreNotReadUnlessAskedFor(t *testing.T) {
	dir := exports(t, map[string]string{"ses_f18153054ffe81GX1wynCfZBXI": readFixture(t, "task-child.json")})
	c := fake(t, "replay", map[string]string{envStream: fixture(t, "task.jsonl"), envExit: "0", envExports: dir})

	got, err := c.Run(context.Background(), request(t))
	if err != nil {
		t.Fatal(err)
	}
	const want = 0.001925676 + 0.00007284
	if math.Abs(got.Cost-want) > 1e-12 || got.SubAgents != 0 || got.Unread != 0 {
		t.Errorf("cost %v across %d sub-agents with %d unread, want the run's own %v and none read", got.Cost, got.SubAgents, got.Unread, want)
	}
}

// The reads of the sub-agents' sessions have what is left of the run's bound,
// not a bound of their own, so a run and its reads together stay inside the
// one the lease is sized against. Here the run exits as its bound runs out,
// which leaves nothing for the read: the sub-agent is unread, and the run
// still succeeded.
func TestSubAgentsAreReadWithinTheRunsBound(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	dir := exports(t, map[string]string{"ses_f18153054ffe81GX1wynCfZBXI": readFixture(t, "task-child.json")})
	c := fake(t, "linger", map[string]string{envPids: pids, envStream: fixture(t, "task.jsonl"), envExit: "0", envExports: dir})
	c.Timeout = 500 * time.Millisecond

	got, err := c.Run(context.Background(), costed(t))
	if err != nil {
		t.Fatalf("got %v, want the reply: an unread sub-agent is not a failed run", err)
	}
	if got.SubAgents != 1 || got.Unread != 1 {
		t.Errorf("%d sub-agents with %d unread, want the one unread: the bound had run out", got.SubAgents, got.Unread)
	}
	for _, pid := range waitForPids(t, pids) {
		gone(t, pid)
	}
}

// A caller that stops while a finished run's sub-agents' sessions are being
// read gets the reply, with those unread. The run succeeded; throwing it away
// over a figure that decides nothing would lose its session to a retry.
func TestStoppingWhileSubAgentsAreReadKeepsTheReply(t *testing.T) {
	started := filepath.Join(t.TempDir(), "export")
	c := fake(t, "replay", map[string]string{envStream: fixture(t, "task.jsonl"), envExit: "0", envExportHang: started})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		reply opencode.Reply
		err   error
	}
	done := make(chan result, 1)
	go func() {
		r, err := c.Run(ctx, costed(t))
		done <- result{r, err}
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the export never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("got %v, want the reply the run wrote", r.err)
		}
		if r.reply.Text != "pong" || r.reply.Session != "ses_f1815410dffe1YAxJ0v1eQ7qFL" {
			t.Errorf("text %q in %s, want the run's reply in its session", r.reply.Text, r.reply.Session)
		}
		if r.reply.Unread != 1 {
			t.Errorf("%d sub-agents unread, want the one whose read was stopped", r.reply.Unread)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// taskEvent is a finished task tool call in a run's stream, as opencode 1.18.31
// writes it, trimmed to what names the sub-agent's session.
func taskEvent(parent, child string) string {
	return fmt.Sprintf(`{"type":"tool_use","sessionID":%q,"part":{"type":"tool","tool":"task","state":{"status":"completed","metadata":{"parentSessionId":%[1]q,"sessionId":%q}}}}`, parent, child)
}

// exported is a session's `opencode export`, trimmed to its cost and to a
// task tool call for each of children.
func exported(session string, cost float64, children ...string) string {
	var parts []string
	for _, c := range children {
		parts = append(parts, fmt.Sprintf(`{"type":"tool","tool":"task","state":{"status":"completed","metadata":{"sessionId":%q}}}`, c))
	}
	return fmt.Sprintf(`{"info":{"id":%q,"cost":%v,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}},"messages":[{"info":{"role":"assistant"},"parts":[%s]}]}`,
		session, cost, strings.Join(parts, ","))
}

// exports writes the sessions opencode has, by id, for the fake's export.
func exports(t *testing.T, sessions map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for id, body := range sessions {
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
			if te.Bound != 0 {
				t.Errorf("a run that failed on its own says it was killed at a bound of %s", te.Bound)
			}
		})
	}
}

// A run that failed was still paid for as far as it got (#22): the failure
// comes with what its steps and its sub-agents spent, and with no text, which
// is not a reply.
func TestAFailedRunSaysWhatItSpent(t *testing.T) {
	task := strings.Split(strings.TrimSpace(readFixture(t, "task.jsonl")), "\n")
	var lines []string
	for _, l := range task {
		// The run's first step: some text, the task call, and its cost.
		lines = append(lines, l)
		if strings.Contains(l, `"step_finish"`) {
			break
		}
	}
	lines = append(lines, `{"type":"error","error":{"name":"APIError","data":{"message":"overloaded"}}}`)
	dir := exports(t, map[string]string{"ses_f18153054ffe81GX1wynCfZBXI": readFixture(t, "task-child.json")})
	c := fake(t, "replay", map[string]string{envStream: stream(t, lines...), envExit: "1", envExports: dir})

	got, err := c.Run(context.Background(), costed(t))
	var te *opencode.TransientError
	if !errors.As(err, &te) {
		t.Fatalf("got %v, want a TransientError", err)
	}
	const want = 0.001925676 + 0.001617276
	if math.Abs(got.Cost-want) > 1e-12 {
		t.Errorf("cost %v, want %v: the step that finished, and the sub-agent's", got.Cost, want)
	}
	if got.Tokens.Input != 12306+10730 || got.SubAgents != 1 || got.Unread != 0 {
		t.Errorf("tokens %+v over %d sub-agents with %d unread, want the step's and the sub-agent's", got.Tokens, got.SubAgents, got.Unread)
	}
	if got.Text != "" || got.Session != "" {
		t.Errorf("text %q in session %q came with a failure, want only what it spent", got.Text, got.Session)
	}
}

// A failure that is not the model's comes with nothing: no run was paid for.
func TestAFatalFailureSpentNothing(t *testing.T) {
	c := fake(t, "replay", map[string]string{envStream: stream(t, `{"type":"step_finish","part":{"cost":0.5}}`, "not json"), envExit: "0"})

	got, err := c.Run(context.Background(), request(t))
	var fe *opencode.FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("got %v, want a FatalError", err)
	}
	if got != (opencode.Reply{}) {
		t.Errorf("a fatal failure came with %+v, want nothing", got)
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
				return opencode.Command{Path: filepath.Join(t.TempDir(), "no-opencode-here"), Timeout: bound}
			},
		},
		{
			name: "no binary configured",
			cmd:  func(t *testing.T) opencode.Command { return opencode.Command{Timeout: bound} },
		},
		{
			name: "no bound configured",
			cmd: func(t *testing.T) opencode.Command {
				c := fake(t, "replay", map[string]string{envStream: fixture(t, "ok.jsonl"), envExit: "0"})
				c.Timeout = 0
				return c
			},
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
			c := opencode.Command{Path: "/nonexistent", Timeout: bound}
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

// A run that continues a session names it, before the prompt.
func TestASessionIsContinued(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record.json")
	c := fake(t, "record", map[string]string{envRecord: record, envStream: fixture(t, "ok.jsonl")})
	req := request(t)
	req.Session = "ses_earlier"

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
	wantArgs := []string{"run", "--model", ref.String(), "--dir", req.Dir, "--format", "json", "--session", "ses_earlier", req.Prompt}
	if fmt.Sprint(got.Args) != fmt.Sprint(wantArgs) {
		t.Errorf("args %q\nwant %q", got.Args, wantArgs)
	}
}

// A session opencode does not have is not a model failing: another model
// would not find it either, and the caller starts a new session instead.
// What opencode 1.18.31 did for an unknown session: exit 1, nothing on
// stdout, and this on stderr.
func TestASessionThatIsGoneSaysSo(t *testing.T) {
	env := map[string]string{envExit: "1", envStderr: "\x1b[91m\x1b[1mError: \x1b[0mSession not found\n", envStream: stream(t)}
	c := fake(t, "replay", env)
	req := request(t)
	req.Session = "ses_gone"

	_, err := c.Run(context.Background(), req)
	var gone *opencode.SessionGoneError
	if !errors.As(err, &gone) || gone.Session != "ses_gone" {
		t.Fatalf("got %v, want a SessionGoneError naming ses_gone", err)
	}

	// Without a session asked for, the same failure is a model's.
	_, err = c.Run(context.Background(), request(t))
	var te *opencode.TransientError
	if !errors.As(err, &te) {
		t.Errorf("got %v, want a TransientError for a run that asked for no session", err)
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

// A run still going when its bound runs out is killed with what it started,
// and is transient: it is the model, or its provider, that did not finish, and
// the next candidate may. It is not the caller's context running out, which
// would read as a stop.
func TestARunPastItsBoundIsKilledAndTransient(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	c := fake(t, "hang", map[string]string{envPids: pids})
	c.Timeout = 500 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := c.Run(context.Background(), request(t))
		done <- err
	}()
	started := waitForPids(t, pids)

	select {
	case err := <-done:
		var te *opencode.TransientError
		if !errors.As(err, &te) {
			t.Fatalf("got %v, want a TransientError", err)
		}
		if te.Model != ref {
			t.Errorf("the error names %s, want %s", te.Model, ref)
		}
		// The bound is what the operator is told about, when this runs a
		// tier out (#98).
		if te.Bound != c.Timeout {
			t.Errorf("the error says the bound was %s, want %s", te.Bound, c.Timeout)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Errorf("%v reads as the caller's context ending, which is a stop rather than a failure", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its bound ran out")
	}
	for _, pid := range started {
		gone(t, pid)
	}
}

// A run that exited cleanly as its bound ran out finished, and keeps its
// reply. Here what it left behind holds its stdout open, so the stream does not
// end until the bound kills the group, and the kill lands after the run itself
// had exited 0.
func TestARunThatFinishedAsItsBoundRanOutKeepsItsReply(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	c := fake(t, "linger", map[string]string{envPids: pids, envStream: fixture(t, "ok.jsonl"), envExit: "0"})
	c.Timeout = 500 * time.Millisecond

	reply, err := c.Run(context.Background(), request(t))
	if err != nil {
		t.Fatalf("got %v, want the reply the run wrote before it exited", err)
	}
	if strings.TrimSpace(reply.Text) == "" {
		t.Error("the reply is empty")
	}
	for _, pid := range waitForPids(t, pids) {
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
	if len(args) == 2 && args[0] == "export" {
		return export(args[1])
	}
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
		startChild(nil)
		time.Sleep(time.Hour)
		return 0
	case "orphan":
		startChild(nil)
		return replay()
	case "linger":
		startChild(os.Stdout)
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
// pids. A non-nil stdout is handed to it, as opencode's own is to a language
// server that inherits it.
func startChild(stdout *os.File) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperIsOpencode$", "--")
	cmd.Env = append(os.Environ(), envMode+"=sleep")
	if stdout != nil {
		cmd.Stdout = stdout
	}
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

// export is `opencode export <session>`, which writes its progress to stderr
// and the session to stdout.
func export(session string) int {
	fmt.Fprintf(os.Stderr, "Exporting session: %s\n", session)
	if p := os.Getenv(envExportHang); p != "" {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		time.Sleep(time.Hour)
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv(envExports), session+".json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Session not found")
		return 1
	}
	os.Stdout.Write(b)
	return 0
}
