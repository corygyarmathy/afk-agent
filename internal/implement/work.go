package implement

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/statefile"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

//go:embed prompt.md
var promptText string

//go:embed retry.md
var retryText string

//go:embed cut.md
var cutText string

var (
	prompt = template.Must(template.New("implement").Parse(promptText))
	_      = template.Must(prompt.New("unattended").Parse(work.Unattended))
	retry  = template.Must(template.New("retry").Parse(retryText))

	// cutPrompt continues the session that wrote the work. The prompt for a
	// new session carries it too, for a cut whose session is gone.
	cutPrompt = template.Must(prompt.New("cut").Parse(cutText))
)

// progress is how far the work in a workspace has got. It lives beside the
// workspace in the state directory and not in the store, so the two are lost
// together: a progress file without its workspace describes nothing, and a
// workspace without its progress cannot be trusted (ADR 0001 §5, §6).
//
// The checkout, the push and the gate are work.Progress, which both kinds
// share; what is here is what only implement needs.
type progress struct {
	work.Progress

	// Description is the session's part of the pull request's
	// description, as its last run left the file.
	Description string `json:"description,omitempty"`

	// Lines and Tests are the size of the work at Head, measured as the push
	// was decided (package size).
	Lines int `json:"lines,omitempty"`
	Tests int `json:"tests,omitempty"`

	// Sensitive is the sensitive paths the work at Head touches, for the
	// description's line, or none.
	Sensitive []sensitive.Touched `json:"sensitive,omitempty"`

	// Cut is work that came in over the size signal and was sent back to
	// its session to be cut to a first piece: once, and never again for
	// this workspace (#127). Cutting is that cut decided and not yet run
	// to the end, which a replay of the push reads to send the work back
	// again rather than push it uncut.
	Cut     bool `json:"cut,omitempty"`
	Cutting bool `json:"cutting,omitempty"`

	// Uncut is the commit the work was at before it went back to be cut,
	// seen on the remote at wholeBranch: whatever the cut does, the work is
	// kept. Empty for work never sent back.
	Uncut string `json:"uncut,omitempty"`

	// Remainder is what the session said is left of the issue, as its last
	// run before the push left the file: the body of the issue filed for
	// the rest. Not read of work asked for whole.
	Remainder string `json:"remainder,omitempty"`

	// Rest is the issue filed for the rest, once it is seen on the tracker:
	// kept once seen, so a rest the operator closes straight away is not
	// filed again.
	Rest int `json:"rest,omitempty"`

	// Blocked is the rest seen blocked by the issue, and Unblocked its
	// rounds run out without it, which the pull request's description then
	// says. Either way the dependency is not made again.
	Blocked   bool `json:"blocked,omitempty"`
	Unblocked bool `json:"unblocked,omitempty"`
}

// run is `implement-run`: one candidate model does the work in the workspace,
// or fixes what the gate said about the work already there.
func (d *Deps) run(ctx context.Context, in transition.In) (transition.Result, error) {
	// A gate failure moves the job to a new state, which clears the stays,
	// so the retry starts again at the first candidate - which need
	// not be the model that wrote the session. That is intended: opencode
	// continues a session under any model, and the tier's order is the
	// preference (ADR 0001 §9).
	ref, res, ok, err := d.tier().Choose(ctx, in, Deferred)
	if err != nil || !ok {
		return res, err
	}

	n := in.Job.Subject.Number
	p, pushed, err := d.workspace(ctx, in.Job.ID, n)
	if err != nil {
		return transition.Result{}, err
	}
	if pushed {
		return d.lost(ctx, in)
	}
	ws := d.work().Dir(in.Job.ID)
	if p.Session == "" && p.Failure == "" && !p.Cut {
		// No session has finished here, so anything in the workspace is one
		// that failed before it did, and whose id went with it. The next
		// starts from the base rather than inherit it unannounced. Work
		// sent back to be cut had a session finish, whether or not its id
		// survived.
		if err := work.Reset(ctx, ws, p.Base); err != nil {
			return transition.Result{}, err
		}
		for _, name := range []string{descriptionFile, remainderFile} {
			if err := os.Remove(filepath.Join(ws, ".git", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return transition.Result{}, err
			}
		}
	}
	instructions, err := d.spec(ctx, in.Job.ID, ws, n)
	if err != nil {
		return transition.Result{}, err
	}
	whole := Whole(instructions)
	if p.Failure != "" {
		if err := os.WriteFile(filepath.Join(ws, ".git", "afk-gate.log"), []byte(p.Failure), 0o644); err != nil {
			return transition.Result{}, err
		}
	}

	// Cost, so the footer counts what the session's sub-agents spent too.
	req := opencode.Request{Model: ref, Dir: ws, Session: p.Session, Cost: true}
	switch {
	case p.Session == "" || (p.Failure == "" && !p.Cutting):
		// Only a failure or a cut is worth continuing a session for. The
		// first run is a new session, and so is a retry whose session was
		// never recorded.
		req.Session = ""
		req.Prompt, err = d.render(prompt, n, p, whole)
	case p.Cutting:
		req.Prompt, err = d.render(cutPrompt, n, p, whole)
	default:
		req.Prompt, err = d.render(retry, n, p, whole)
	}
	if err != nil {
		return transition.Result{}, err
	}

	reply, res, ok, err := d.tier().Run(ctx, in, d.Model, req, func() (string, error) {
		// The session went with opencode's data: the next run is a new
		// session, given the failure.
		p.Session = ""
		if err := d.save(in.Job.ID, p); err != nil {
			return "", err
		}
		return d.render(prompt, n, p, whole)
	}, Implementing, Deferred, d.logf)
	if err != nil {
		return res, err
	}
	p.Spent.Add(ctx, ref, d.Price, reply)
	if !ok {
		// A failed run was paid for too. Kept for the footer, and only
		// logged if it cannot be: the stay is the decision, and the spend
		// is no part of it (#22).
		if err := d.save(in.Job.ID, p); err != nil {
			d.logf("%s: what the failed run spent could not be kept, so the footer will not count it: %v", in.Job.ID, err)
		}
		return res, nil
	}

	p.Session = reply.Session
	p.Cutting = false
	// What is left is read only before the push: after it, the rest is
	// filed from what the work was pushed with, or not at all. Asked for
	// whole, it is not read: nothing is left over.
	if p.Pushed == "" {
		p.Remainder = ""
		if !whole {
			if p.Remainder, err = readRemainder(ws); err != nil {
				p.Remainder = ""
				d.logf("%s: the file of what is left could not be read, so the issue for the rest says the session did not say: %v", in.Job.ID, err)
			}
		}
	}
	// Read now, as the session left it. The file stays for a retry that
	// continues the session to amend. One that cannot be read is no
	// description: an error here would lose the session, and the work with
	// it, over what the pull request can open without.
	if p.Description, err = readDescription(ws); err != nil {
		p.Description = ""
		d.logf("%s: the description file could not be read, so the pull request opens with the agent's parts only: %v", in.Job.ID, err)
	}
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Gating, RunAt: in.Now}, nil
}

// gate is `implement-gate`: the agent's own reading of the work, which the
// session's word does not replace. The gate and its retries are shared
// (package work); the words a hand-back uses are this kind's.
func (d *Deps) gate(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !d.work().Exists(in.Job.ID)) {
		// Nothing has been pushed, so nothing is lost but a model run:
		// the work starts over.
		return transition.Result{State: Implementing, RunAt: in.Now}, d.clear(in.Job.ID)
	}
	if err != nil {
		return transition.Result{}, err
	}

	r, err := d.work().Check(ctx, in.Job.ID, &p.Progress, d.Gate, d.Attempts)
	if err != nil {
		return transition.Result{}, err
	}
	switch r.State {
	case work.GatePassed:
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
		return transition.Result{State: Pushing, RunAt: in.Now}, nil
	case work.GateSwitched:
		return d.handBack(ctx, in, p, fmt.Sprintf("The session left `%s` for `%s`, and the prompt said not to change branches.", p.Branch, r.Branch), "")
	case work.GateEmpty:
		nothing := "The session finished without committing anything, so there is nothing to push."
		if p.Pushed != "" {
			nothing = fmt.Sprintf("The session committed nothing since `%s`, the agent's last push, so there is no fix to push.", git.Short(p.Pushed))
		}
		return d.handBack(ctx, in, p, nothing, "")
	case work.GateExhausted:
		return d.handBack(ctx, in, p, fmt.Sprintf("The local gate still failed after %d attempts. %s", p.Attempts, p.Why), r.Output)
	case work.GateDirty:
		return d.handBack(ctx, in, p, p.Why, r.Output)
	default: // GateFailed
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
		return transition.Result{State: Implementing, RunAt: in.Now}, nil
	}
}

// resume is `implement-resume`: the wait is over, and the tier is tried again
// from its first model. Moving state is what clears the stays.
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
	return d.handBackIssue(ctx, in, p, reason, output)
}

// handBackIssue is the hand-back on the issue: before anything was pushed, or
// after a push whose pull request never opened.
func (d *Deps) handBackIssue(ctx context.Context, in transition.In, p progress, reason, output string) (transition.Result, error) {
	n := in.Job.Subject.Number
	marker := handBackMarker(n, p, p.Nonce)
	next := fmt.Sprintf("Nothing was pushed. Reshape the issue and `%s` again, or take it by hand.", Word)
	switch {
	case p.Pushed != "" && p.Uncut != "":
		next = fmt.Sprintf("`%s` is on the remote at `%s`, cut to a first piece, and `%s` at `%s` is the work as it was before the cut, with no pull request for either. Open one from either by hand, or `%s` again to start over on a new branch.", p.Branch, git.Short(p.Pushed), wholeBranch(p.Branch), git.Short(p.Uncut), Word)
	case p.Pushed != "":
		next = fmt.Sprintf("`%s` is on the remote at `%s`, with no pull request. Open one by hand, or `%s` again to start over on a new branch.", p.Branch, git.Short(p.Pushed), Word)
	case p.Uncut != "":
		// Handed back on the way through its cut: the cut is not pushed,
		// and the work it was cut from was, before it went back.
		next = fmt.Sprintf("The cut was not pushed. `%s` is on the remote at `%s`, the work as it was before it went back to be cut, with no pull request. Open one from it by hand, or `%s` again to start over on a new branch.", wholeBranch(p.Branch), git.Short(p.Uncut), Word)
	}
	body := work.HandBackBody(marker, "", "I stopped without opening a pull request. "+reason, "", output, next, p.Spent)

	// The workspace and the relay go before the commit rather than after
	// it. Before the push, a commit that then fails leaves the job where it
	// was with its workspace gone, and the work starts over: a model run
	// spent, and nothing said twice. The progress stays until the hand-back
	// is on the tracker: after the push, a job that never committed this
	// reads it again and hands the same branch back under the same keys,
	// rather than doing the work over on a new one.
	if err := d.discard(in.Job.ID); err != nil {
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

// workspace is the job's workspace and its progress, made afresh unless both
// are there and agree with each other - or pushed, if they were lost after the
// push. A new workspace then would be a new branch beside the pull request.
func (d *Deps) workspace(ctx context.Context, jobID string, n int) (progress, bool, error) {
	ws := d.work().Dir(jobID)
	p, err := d.load(jobID)
	if err == nil && d.work().Exists(jobID) {
		if branch, err := work.BranchOf(ctx, ws); err == nil && branch == p.Branch {
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
	if err := d.work().MakeDir(jobID); err != nil {
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
	p = progress{Progress: work.Progress{Nonce: hex.EncodeToString(nonce), Branch: branch, Base: base, Into: into}}
	return p, false, d.save(jobID, p)
}

// spec writes the issue, and the instructions of the command that asked for
// it, where the prompt says they are. It returns the instructions.
func (d *Deps) spec(ctx context.Context, jobID, ws string, n int) (string, error) {
	is, err := d.Tracker.Issue(ctx, n)
	if err != nil {
		return "", err
	}
	instructions, err := d.instructions(ctx, jobID, n)
	if err != nil {
		return "", err
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
	return instructions, os.WriteFile(filepath.Join(ws, ".git", "afk-issue.md"), []byte(b.String()), 0o644)
}

// request is the command this job's last claim took: the newest of the
// commands it claimed, whose instructions the work is done to. None for work
// nobody asked for, whatever an older command on the issue said. The claim
// writes it before there is a workspace, so clear leaves it, and the next claim
// writes over it.
type request struct {
	Command int64 `json:"command,omitempty"`
}

func (d *Deps) saveRequest(jobID string, claimed []github.Comment) error {
	var a request
	if len(claimed) > 0 {
		a.Command = claimed[len(claimed)-1].ID
	}
	return statefile.Save(d.requestPath(jobID), a)
}

// instructions is the text after the word in the command this job claimed, or
// empty. Only which command is kept: its text is read from the tracker each
// time rather than kept (ADR 0001 §5). Lost with the state directory, the work
// has no instructions.
func (d *Deps) instructions(ctx context.Context, jobID string, n int) (string, error) {
	var a request
	if err := statefile.Load(d.requestPath(jobID), &a); errors.Is(err, os.ErrNotExist) || (err == nil && a.Command == 0) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	comments, err := d.Tracker.Comments(ctx, n)
	if err != nil {
		return "", err
	}
	for _, c := range comments {
		if c.ID == a.Command {
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

// Whole reports whether a command's instructions ask for the work in one pull
// request whatever its size: "don't split", or "do not split", anywhere in
// them. It is the only override of the size signal, and only the command a job
// claimed gives it instructions, so unattended work never has it (#107).
func Whole(instructions string) bool {
	return wholeWords.MatchString(instructions)
}

var wholeWords = regexp.MustCompile(`(?i)\b(don['’]?t|do\s+not)\s+split\b`)

func (d *Deps) render(t *template.Template, n int, p progress, whole bool) (string, error) {
	var b strings.Builder
	err := t.Execute(&b, struct {
		Number int
		Branch string
		Gate   string
		Failed bool
		Why    string
		Whole  bool
		Signal int
		// Opened is work past its push, whose pull request's description
		// is written and never rewritten: the session is not asked for one.
		Opened bool
		// Cutting is work over the signal sent back to be cut, Lines and
		// Tests are what the agent counted of it, and Kept the branch it
		// was pushed to as it was.
		Cutting      bool
		Lines, Tests int
		Kept         string
	}{n, p.Branch, d.Gate, p.Failure != "", p.Why, whole, d.SizeSignal, p.Pushed != "", p.Cutting, p.Lines, p.Tests, wholeBranch(p.Branch)})
	return b.String(), err
}

// work is the shared machinery this kind works through: its state directory
// and the remote its checkouts and pushes reach.
// tier is the candidates implementing runs on.
func (d *Deps) tier() work.Tier {
	return work.Tier{Resolve: d.Resolve, Bound: d.Bound, Wait: d.TierWait}
}

func (d *Deps) work() work.Workspace {
	return work.Workspace{StateDir: d.StateDir, Remote: d.Remote}
}

func (d *Deps) requestPath(jobID string) string {
	return filepath.Join(d.StateDir, "requests", jobID+".json")
}

// notePath is where an effect's last error waits for the decision that reads it
// back (transition.Noting).
func (d *Deps) notePath(jobID string) string {
	return d.work().NotePath(jobID)
}

// clear removes a job's workspace, its relay, its progress and its note. It
// refuses with no state directory, where the paths would be relative to
// wherever the process is.
func (d *Deps) clear(jobID string) error {
	return d.work().Clear(jobID)
}

// discard removes a job's workspace and its relay, and leaves what describes
// the work.
func (d *Deps) discard(jobID string) error {
	return d.work().Discard(jobID)
}

// save writes the progress.
func (d *Deps) save(jobID string, p progress) error {
	return d.work().Save(jobID, p)
}

func (d *Deps) load(jobID string) (progress, error) {
	var p progress
	err := statefile.Load(d.work().ProgressPath(jobID), &p)
	if errors.Is(err, os.ErrNotExist) {
		return progress{}, err
	}
	if err != nil {
		return progress{}, fmt.Errorf("progress of %s: %w", jobID, err)
	}
	if !p.Complete() {
		return progress{}, fmt.Errorf("progress of %s is incomplete", jobID)
	}
	return p, nil
}
