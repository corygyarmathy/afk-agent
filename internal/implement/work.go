package implement

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/statefile"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

//go:embed prompt.md
var promptText string

//go:embed retry.md
var retryText string

var (
	prompt = template.Must(template.New("implement").Parse(promptText))
	retry  = template.Must(template.New("retry").Parse(retryText))
)

// gateTail is how much of the gate's output is kept for the session that has
// to fix it. The end is the part that says what failed. Not a parameter: it
// bounds a file the model reads, and nothing about the work depends on it.
const gateTail = 64 << 10

// handBackTail is how much of the gate's output a hand-back quotes. A comment
// is for a human skimming it, and the workspace is gone by then.
const handBackTail = 4 << 10

// gateWaitDelay is how long a cancelled gate's pipes are given to close before
// Wait stops waiting for them. Not a parameter, for the reason opencode's own
// is not: it bounds how long a cancellation takes to return.
const gateWaitDelay = 5 * time.Second

// progress is how far the work in a workspace has got. It lives beside the
// workspace in the state directory and not in the store, so the two are lost
// together: a progress file without its workspace describes nothing, and a
// workspace without its progress cannot be trusted (ADR 0001 §5, §6).
type progress struct {
	// Nonce is made with the workspace, and keys what is said about the work
	// until the job comes to rest. The branch cannot: nothing pushed means
	// its name is free again, and the next workspace takes it.
	Nonce string `json:"nonce"`

	Branch string `json:"branch"`
	Base   string `json:"base"`

	// Into is the default branch the work started from, and the one the
	// pull request asks to merge into.
	Into string `json:"into"`

	// Summary is what the session said it did, for the pull request.
	Summary string `json:"summary,omitempty"`

	// Head is the commit the push was decided for: checked against the
	// denylist, and what the remote's branch must be at once it lands.
	Head string `json:"head,omitempty"`

	// Pushed is the commit the agent last saw its own push land at on the
	// remote's branch, and the lease every later push is pinned to. Empty
	// until the first push is seen, when the branch must not exist yet.
	Pushed string `json:"pushed,omitempty"`

	// PushedAt is when that push was seen, which the CI ceiling runs from.
	PushedAt time.Time `json:"pushed_at,omitzero"`

	// Rounds is how many times CI has sent the work back to the session.
	Rounds int `json:"rounds,omitempty"`

	// Session is the opencode session that wrote the branch's commits, to
	// continue with a failure. Empty until a run succeeds.
	Session string `json:"session,omitempty"`

	// Attempts is how many times the gate has failed on this branch.
	Attempts int `json:"attempts"`

	// Failure is what the gate said the last time it failed, and Why is
	// its one-line summary. Both empty while the branch has not failed.
	Failure string `json:"failure,omitempty"`
	Why     string `json:"why,omitempty"`
}

// run is `implement-run`: one candidate model does the work in the workspace,
// or fixes what the gate said about the work already there.
func (d *Deps) run(ctx context.Context, in transition.In) (transition.Result, error) {
	// A gate failure moves the job to a new state, which clears the attempt
	// count, so the retry starts again at the first candidate - which need
	// not be the model that wrote the session. That is intended: opencode
	// continues a session under any model, and the tier's order is the
	// preference (ADR 0001 §9).
	ref, until, err := model.Choose(ctx, d.Resolve, in.Job.Attempts, d.Bound, in.Now, d.TierWait)
	if err != nil {
		return transition.Result{}, err
	}
	if !until.IsZero() {
		return transition.Result{State: Deferred, RunAt: until}, nil
	}

	n := in.Job.Subject.Number
	p, pushed, err := d.workspace(ctx, in.Job.ID, n)
	if err != nil {
		return transition.Result{}, err
	}
	if pushed {
		return d.lost(ctx, in)
	}
	ws := d.workspacePath(in.Job.ID)
	if p.Session == "" && p.Failure == "" {
		// No session has finished here, so anything in the workspace is one
		// that failed before it did, and whose id went with it. The next
		// starts from the base rather than inherit it unannounced.
		if err := reset(ctx, ws, p.Base); err != nil {
			return transition.Result{}, err
		}
	}
	if err := d.spec(ctx, ws, n); err != nil {
		return transition.Result{}, err
	}
	if p.Failure != "" {
		if err := os.WriteFile(filepath.Join(ws, ".git", "afk-gate.log"), []byte(p.Failure), 0o644); err != nil {
			return transition.Result{}, err
		}
	}

	req := opencode.Request{Model: ref, Dir: ws, Session: p.Session}
	if p.Failure == "" || p.Session == "" {
		// Only a failure is worth continuing a session for. The first run
		// is a new session, and so is a retry whose session was never
		// recorded.
		req.Session = ""
		req.Prompt, err = d.render(prompt, n, p)
	} else {
		req.Prompt, err = d.render(retry, n, p)
	}
	if err != nil {
		return transition.Result{}, err
	}

	reply, err := d.Model.Run(ctx, req)
	var gone *opencode.SessionGoneError
	if errors.As(err, &gone) {
		// The session went with opencode's data - a rebuilt host, say.
		// The retry is weaker without it, but the job carries on, in a
		// new session given the failure (ADR 0001 §6).
		p.Session = ""
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
		if req.Prompt, err = d.render(prompt, n, p); err != nil {
			return transition.Result{}, err
		}
		req.Session = ""
		reply, err = d.Model.Run(ctx, req)
	}
	var transient *opencode.TransientError
	if errors.As(err, &transient) {
		// Stay, and the attempt count moves the next run to the next
		// candidate (ADR 0001 §10).
		return transition.Result{State: Implementing, RunAt: in.Now}, nil
	}
	if err != nil {
		return transition.Result{}, err
	}

	p.Session = reply.Session
	p.Summary = strings.TrimSpace(reply.Text)
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Gating, RunAt: in.Now}, nil
}

// gate is `implement-gate`: the agent's own reading of the work, which the
// session's word does not replace.
func (d *Deps) gate(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	ws := d.workspacePath(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !isDir(filepath.Join(ws, ".git"))) {
		// Nothing has been pushed, so nothing is lost but a model run:
		// the work starts over.
		return transition.Result{State: Implementing, RunAt: in.Now}, d.clear(in.Job.ID)
	}
	if err != nil {
		return transition.Result{}, err
	}

	if branch, err := branchOf(ctx, ws); err != nil {
		return transition.Result{}, err
	} else if branch != p.Branch {
		// The commits are not where the push would take them from, and the
		// next run's workspace would not recognise the clone: it would start
		// over with the gate's count at nothing, and the bound would never
		// be reached.
		return d.handBack(ctx, in, p, fmt.Sprintf("The session left `%s` for `%s`, and the prompt said not to change branches.", p.Branch, branch), "")
	}

	// A fix round's work is what it adds to the agent's last push. An amend
	// or a rebase of that push counts; the same head again would push nothing,
	// and CI would read the same red run.
	since, nothing := p.Base, "The session finished without committing anything, so there is nothing to push."
	if p.Pushed != "" {
		since, nothing = p.Pushed, fmt.Sprintf("The session committed nothing since `%s`, the agent's last push, so there is no fix to push.", git.Short(p.Pushed))
	}
	made, err := commits(ctx, ws, since)
	if err != nil {
		return transition.Result{}, err
	}
	if made == 0 {
		return d.handBack(ctx, in, p, nothing, "")
	}

	var failure, why string
	if dirty, err := uncommitted(ctx, ws); err != nil {
		return transition.Result{}, err
	} else if dirty != "" {
		why = fmt.Sprintf("The local gate, `%s`, was not run: the session left changes to tracked files uncommitted, and the gate reads commits.", d.Gate)
		failure = "git status --porcelain --untracked-files=no:\n" + dirty + "\n"
	} else {
		// Untracked files go before the gate runs rather than fail it
		// unread: a session runs the gate itself, and what that leaves
		// behind is not the work. A file the work needed but nobody added
		// goes too, and the gate says so.
		if _, err := git.Run(ctx, ws, "clean", "--quiet", "--force", "-d"); err != nil {
			return transition.Result{}, err
		}
		passed, output, err := runGate(ctx, ws, d.Gate)
		if err != nil {
			return transition.Result{}, err
		}
		if passed {
			p.Failure, p.Why = "", ""
			if err := d.save(in.Job.ID, p); err != nil {
				return transition.Result{}, err
			}
			return transition.Result{State: Pushing, RunAt: in.Now}, nil
		}
		why, failure = fmt.Sprintf("The local gate, `%s`, failed on the work: it exited non-zero.", d.Gate), output
	}

	p.Attempts++
	p.Failure, p.Why = failure, why
	if p.Attempts >= d.Attempts {
		return d.handBack(ctx, in, p, fmt.Sprintf("The local gate still failed after %d attempts. %s", p.Attempts, why), failure)
	}
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Implementing, RunAt: in.Now}, nil
}

// resume is `implement-resume`: the wait is over, and the tier is tried again
// from its first model. Moving state is what clears the attempt count.
func (d *Deps) resume(_ context.Context, in transition.In) (transition.Result, error) {
	return transition.Result{State: Implementing, RunAt: in.Now}, nil
}

// handBack returns the work to a human: a comment saying what was tried, and
// the hand-back label, on the issue before anything was pushed, and on the
// pull request after (dotfiles ADR 0007 §2). The job comes to rest once both
// are read back from the tracker, and its workspace goes now.
//
// Keyed by the progress's nonce, so a replay says it once and a later
// workspace that fails again says so again.
func (d *Deps) handBack(ctx context.Context, in transition.In, p progress, reason, output string) (transition.Result, error) {
	if p.Pushed != "" {
		pr, ok, err := d.open(ctx, from(p.Branch))
		if err != nil {
			return transition.Result{}, err
		}
		if !ok {
			// Closed by a human while the fix ran, as in watch: their
			// decision, and nothing to say about it.
			return transition.Result{State: Start}, d.clear(in.Job.ID)
		}
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, reason, output)
	}
	n := in.Job.Subject.Number
	marker := handBackMarker(n, p, p.Nonce)
	body := handBackBody(marker, "I stopped without opening a pull request. "+reason, output,
		fmt.Sprintf("Nothing was pushed. Reshape the issue and `%s` again, or take it by hand.", Word))

	// Before the commit rather than after it. A commit that then fails
	// leaves the job where it was with its workspace gone, and the gate
	// starts the work over: a model run spent, and nothing said twice.
	if err := d.clear(in.Job.ID); err != nil {
		return transition.Result{}, err
	}
	return d.book().Owe(ctx, in, HandingBack, owed.Record{Next: Start, Items: []owed.Item{
		owed.Comment(fmt.Sprintf("hand-back-issue-%d-%s", n, p.Nonce), n, marker, body),
		owed.Label(fmt.Sprintf("hand-back-label-issue-%d-%s", n, p.Nonce), n, d.HandBackLabel),
	}})
}

// handBackMarker is the hidden line a hand-back carries: the issue, the
// branch, and the key it is said once under, which is what it is read back by.
func handBackMarker(n int, p progress, key string) string {
	return fmt.Sprintf("<!-- afk:hand-back issue=%d branch=%s key=%s -->", n, p.Branch, key)
}

// handBackBody is a hand-back comment: what stopped, the end of the output
// that said so, and what a human can do next.
func handBackBody(marker, stopped, output, next string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", marker)
	fmt.Fprintf(&b, "%s\n", stopped)
	if output = strings.TrimSpace(tail(output, handBackTail)); output != "" {
		fmt.Fprintf(&b, "\nThe end of the last output:\n\n````\n%s\n````\n", output)
	}
	fmt.Fprintf(&b, "\n%s\n", next)
	return b.String()
}

// workspace is the job's workspace and its progress, made afresh unless both
// are there and agree with each other - or pushed, if they were lost after the
// push. A new workspace then would be a new branch beside the pull request.
func (d *Deps) workspace(ctx context.Context, jobID string, n int) (progress, bool, error) {
	ws := d.workspacePath(jobID)
	p, err := d.load(jobID)
	if err == nil && isDir(filepath.Join(ws, ".git")) {
		if branch, err := branchOf(ctx, ws); err == nil && branch == p.Branch {
			return p, false, nil
		}
	}
	switch {
	case err == nil && p.Pushed != "":
		return progress{}, true, nil
	case errors.Is(err, os.ErrNotExist):
		// The progress may have gone with the lease: the tracker still
		// says whether the work reached a pull request.
		if _, ok, err := d.open(ctx, d.forIssue(n)); err != nil || ok {
			return progress{}, ok, err
		}
	case err != nil:
		return progress{}, false, err
	}

	// Anything else left here is from a run that did not finish, and
	// describes nothing that was pushed.
	if err := d.clear(jobID); err != nil {
		return progress{}, false, err
	}
	if err := os.MkdirAll(filepath.Dir(ws), 0o755); err != nil {
		return progress{}, false, err
	}
	branch, base, into, err := prepare(ctx, d.Remote, ws, d.BranchPrefix, n)
	if err != nil {
		return progress{}, false, err
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return progress{}, false, err
	}
	p = progress{Nonce: hex.EncodeToString(nonce), Branch: branch, Base: base, Into: into}
	return p, false, d.save(jobID, p)
}

// spec writes the issue, and the instructions of the command that asked for
// it, where the prompt says they are.
func (d *Deps) spec(ctx context.Context, ws string, n int) error {
	is, err := d.Tracker.Issue(ctx, n)
	if err != nil {
		return err
	}
	instructions, err := d.instructions(ctx, n)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Issue #%d: %s\n\n", is.Number, is.Title)
	if strings.TrimSpace(is.Body) == "" {
		b.WriteString("(no description)\n")
	} else {
		fmt.Fprintf(&b, "%s\n", is.Body)
	}
	if instructions != "" {
		fmt.Fprintf(&b, "\n# Instructions from the person who asked\n\n%s\n", instructions)
	}
	return os.WriteFile(filepath.Join(ws, ".git", "afk-issue.md"), []byte(b.String()), 0o644)
}

// instructions is the text after the word in the most recent `/implement` the
// agent claimed on the issue, or empty. Read from the tracker each time rather
// than kept in the store (ADR 0001 §5).
func (d *Deps) instructions(ctx context.Context, n int) (string, error) {
	comments, err := d.Tracker.Comments(ctx, n)
	if err != nil {
		return "", err
	}
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		if !intake.IsCommand(c, d.Login, Word) {
			continue
		}
		reactions, err := d.Tracker.Reactions(ctx, c.ID)
		if err != nil {
			return "", err
		}
		if intake.Claimed(reactions, d.Login) {
			return Instructions(c), nil
		}
	}
	return "", nil
}

// Instructions is what a command says after its word.
func Instructions(c github.Comment) string {
	body := strings.TrimSpace(c.Body)
	_, rest, _ := strings.Cut(body, Word)
	return strings.TrimSpace(rest)
}

func (d *Deps) render(t *template.Template, n int, p progress) (string, error) {
	var b strings.Builder
	err := t.Execute(&b, struct {
		Number int
		Branch string
		Gate   string
		Failed bool
		Why    string
	}{n, p.Branch, d.Gate, p.Failure != "", p.Why})
	return b.String(), err
}

// runGate runs the gate command in the workspace. It reports whether the gate
// passed, and the end of what it wrote. An error is a gate that could not be
// run at all, which is not the work's fault.
func runGate(ctx context.Context, dir, gate string) (bool, string, error) {
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
		return false, tail(out.String(), gateTail), nil
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

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func (d *Deps) workspacePath(jobID string) string {
	return filepath.Join(d.StateDir, "workspaces", jobID)
}

func (d *Deps) progressPath(jobID string) string {
	return filepath.Join(d.StateDir, "progress", jobID+".json")
}

func (d *Deps) relayPath(jobID string) string {
	return filepath.Join(d.StateDir, "relays", jobID+".git")
}

// clear removes a job's workspace, its relay and its progress. It refuses with no state
// directory, where the paths would be relative to wherever the process is.
func (d *Deps) clear(jobID string) error {
	if d.StateDir == "" {
		return errors.New("implement has no state directory")
	}
	if err := os.RemoveAll(d.workspacePath(jobID)); err != nil {
		return err
	}
	if err := os.RemoveAll(d.relayPath(jobID)); err != nil {
		return err
	}
	if err := os.Remove(d.progressPath(jobID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// save writes the progress.
func (d *Deps) save(jobID string, p progress) error {
	return statefile.Save(d.progressPath(jobID), p)
}

func (d *Deps) load(jobID string) (progress, error) {
	var p progress
	err := statefile.Load(d.progressPath(jobID), &p)
	if errors.Is(err, os.ErrNotExist) {
		return progress{}, err
	}
	if err != nil {
		return progress{}, fmt.Errorf("progress of %s: %w", jobID, err)
	}
	if p.Nonce == "" || p.Branch == "" || p.Base == "" || p.Into == "" {
		return progress{}, fmt.Errorf("progress of %s is incomplete", jobID)
	}
	return p, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
