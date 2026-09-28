package revise

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
	"strconv"
	"strings"
	"text/template"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/statefile"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

//go:embed revise.md
var promptText string

//go:embed retry.md
var retryText string

var (
	prompt = template.Must(template.New("revise").Parse(promptText))
	retry  = template.Must(template.New("retry").Parse(retryText))
)

// sendBackFile is where, in the workspace's .git, the revision's session reads
// the diff, the linked issue and the points.
const sendBackFile = "afk-send-back.md"

// replyFile is where, in the workspace's .git, the session writes its part of
// the revision's reply.
const replyFile = "afk-reply.md"

// progress is how far the revision has got. It lives beside the workspace in
// the state directory and not in the store, so the two are lost together.
type progress struct {
	work.Progress

	// Points is the send-back's command ids, in order: the revision's marks
	// for the commands it answers.
	Points []int64 `json:"points,omitempty"`

	// Reply is the session's part of the reply, as its last run left the
	// file. Posting it is #149's; a hand-back shows it as the points done so
	// far.
	Reply string `json:"reply,omitempty"`
}

// run is `revise-run`: one candidate model does the send-back's points in the
// workspace, on top of the head the send-back was written against.
func (d *Deps) run(ctx context.Context, in transition.In) (transition.Result, error) {
	ref, wait, err := model.Choose(ctx, d.Resolve, in.Job.Stays, d.Bound, in.Now, d.TierWait)
	if err != nil {
		return transition.Result{}, err
	}
	if !wait.Until.IsZero() {
		return transition.Result{State: Deferred, RunAt: wait.Until, Exhausted: wait.Exhausted}, nil
	}

	sb, err := d.Load(in.Job.ID)
	if err != nil {
		// The send-back went with the state directory: there is nothing to
		// say what the points were. The pull request is still there.
		return d.handBackLost(ctx, in)
	}
	p, err := d.workspace(ctx, in.Job.ID, sb)
	if err != nil {
		return transition.Result{}, err
	}
	ws := d.work().Dir(in.Job.ID)
	if err := d.spec(ctx, ws, in.Job.Subject.Number, sb, p); err != nil {
		return transition.Result{}, err
	}
	if p.Failure != "" {
		if err := os.WriteFile(filepath.Join(ws, ".git", "afk-gate.log"), []byte(p.Failure), 0o644); err != nil {
			return transition.Result{}, err
		}
	}

	req := opencode.Request{Model: ref, Dir: ws}
	switch {
	case p.Session == "" || p.Failure == "":
		// The first run is a fresh session, as the fresh diff deserves; a
		// retry whose session was never recorded starts one too.
		req.Session = ""
		req.Prompt, err = d.render(prompt, in.Job.Subject.Number, p)
	default:
		req.Session = p.Session
		req.Prompt, err = d.render(retry, in.Job.Subject.Number, p)
	}
	if err != nil {
		return transition.Result{}, err
	}

	reply, err := d.Model.Run(ctx, req)
	var gone *opencode.SessionGoneError
	if errors.As(err, &gone) {
		// The session went with opencode's data - a rebuilt host, say. The
		// revision carries on in a new session given the failure.
		p.Session = ""
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
		req.Session = ""
		if req.Prompt, err = d.render(prompt, in.Job.Subject.Number, p); err != nil {
			return transition.Result{}, err
		}
		reply, err = d.Model.Run(ctx, req)
	}
	var transient *opencode.TransientError
	if errors.As(err, &transient) {
		d.logf("%s: %v", in.Job.ID, transient)
		if wait := model.Failed(ctx, d.Resolve, in.Job.Stays, d.Bound, in.Now, d.TierWait, transient); !wait.Until.IsZero() {
			return transition.Result{State: Deferred, RunAt: wait.Until, Exhausted: wait.Exhausted}, nil
		}
		// Stay, and the stay moves the next run to the next candidate.
		return transition.Result{State: Revising, RunAt: in.Now}, nil
	}
	if err != nil {
		return transition.Result{}, err
	}

	p.Session = reply.Session
	if p.Reply, err = work.ReadGitFile(ws, replyFile); err != nil {
		p.Reply = ""
		d.logf("%s: the reply file could not be read, so a hand-back says the session did not say: %v", in.Job.ID, err)
	}
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Gating, RunAt: in.Now}, nil
}

// gate is `revise-gate`: the agent's own reading of the revision's commits.
// The gate and its retries are shared (package work); the words a hand-back
// uses are this kind's.
func (d *Deps) gate(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !d.work().Exists(in.Job.ID)) {
		return d.handBackLost(ctx, in)
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
		return d.handBack(ctx, in, p, fmt.Sprintf("The session committed nothing on top of `%s`, the head the send-back was written against, so there is nothing to push.", git.Short(p.Pushed)), "")
	case work.GateExhausted:
		return d.handBack(ctx, in, p, fmt.Sprintf("The local gate still failed after %d attempts. %s", p.Attempts, p.Why), r.Output)
	case work.GateDirty:
		return d.handBack(ctx, in, p, p.Why, r.Output)
	default: // GateFailed
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
		return transition.Result{State: Revising, RunAt: in.Now}, nil
	}
}

// resume is `revise-resume`: the wait is over, and the tier is tried again
// from its first model. Moving state is what clears the stays.
func (d *Deps) resume(_ context.Context, in transition.In) (transition.Result, error) {
	return transition.Result{State: Revising, RunAt: in.Now}, nil
}

// handedBack is `revise-handed-back`: at rest once the hand-back's comment and
// label are both on the tracker. A record lost with the state directory rests
// all the same: claiming again from there would start a revision nobody asked
// for.
func (d *Deps) handedBack(ctx context.Context, in transition.In) (transition.Result, error) {
	if err := d.clear(in.Job.ID); err != nil {
		return transition.Result{}, err
	}
	return d.book().Settle(ctx, in, transition.Result{State: Start})
}

// handBack returns the revision to a human on its pull request: the branch may
// have moved under it, the gate may have run out, or the session may have
// rewritten what was read. The reply so far goes with it, which is the points
// done so far.
func (d *Deps) handBack(ctx context.Context, in transition.In, p progress, reason, output string) (transition.Result, error) {
	return d.handBackOn(ctx, in, p, p.Nonce, reason, output)
}

// handBackLost is the hand-back when the revision's record went with the state
// directory. The pull request is still there, and it is still the operator's
// to finish.
func (d *Deps) handBackLost(ctx context.Context, in transition.In) (transition.Result, error) {
	n := in.Job.Subject.Number
	pr, err := d.Tracker.PullRequest(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	return d.handBackOn(ctx, in, progress{}, "lost-"+pr.HeadSHA,
		"The agent lost its record of this revision - its state directory was wiped - so it cannot finish it.", "")
}

// handBackOn owes the tracker one hand-back comment and label, keyed by key so
// a replay says it once.
func (d *Deps) handBackOn(ctx context.Context, in transition.In, p progress, key, reason, output string) (transition.Result, error) {
	n := in.Job.Subject.Number
	return d.work().HandBackPR(ctx, in, in.Job.ID, work.HandBack{
		Book:        d.book(),
		Label:       d.HandBackLabel,
		Number:      n,
		Key:         key,
		Marker:      revisionHandBackMarker(n, key),
		Also:        revisionMarkers(p.Points),
		Stopped:     "I stopped the revision. " + reason,
		Detail:      p.Reply,
		Output:      output,
		Next:        fmt.Sprintf("The pull request stays open: finish the branch by hand, or send it back again with `%s`.", Word),
		HandingBack: HandingBack,
		Rest:        Start,
	})
}

// revisionHandBackMarker is the hidden line a revision's hand-back carries, so
// it is read back once. The revision's markers for the send-back's commands go
// beside it (revisionMarkers), so a later command can tell it was written
// while a revision was in flight.
func revisionHandBackMarker(n int, key string) string {
	return fmt.Sprintf("<!-- afk:revision-hand-back pr=%d key=%s -->", n, key)
}

// revisionMarkers is the hidden line for each command of the send-back, which
// a revision's hand-back and its reply both carry.
func revisionMarkers(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%s\n", owed.RevisionMarker(id))
	}
	return b.String()
}

// workspace is the revision's workspace and its progress, made afresh from the
// head the send-back was written against unless both are there and agree.
//
// The head is fetched through the relay - a repository the agent owns and has
// isolated - and brought into the workspace locally with no token, because the
// workspace's .git/config is the model's to write (from #41's comment on #84).
func (d *Deps) workspace(ctx context.Context, jobID string, sb SendBack) (progress, error) {
	w := d.work()
	ws := w.Dir(jobID)
	p, err := d.load(jobID)
	if err == nil && w.Exists(jobID) {
		if branch, err := work.BranchOf(ctx, ws); err == nil && branch == p.Branch {
			return p, nil
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return progress{}, err
	}
	if err := w.Clear(jobID); err != nil {
		return progress{}, err
	}
	if err := w.MakeDir(jobID); err != nil {
		return progress{}, err
	}
	into, err := work.Clone(ctx, d.Remote, ws)
	if err != nil {
		return progress{}, err
	}
	ref := "refs/heads/" + sb.Ref
	if _, err := work.FetchInto(ctx, w.RelayDir(jobID), d.Remote, ref); err != nil {
		return progress{}, fmt.Errorf("the pull request's branch `%s` could not be fetched: %w", sb.Ref, err)
	}
	if err := work.Import(ctx, w.RelayDir(jobID), ws, sb.Ref, ref, sb.Head); err != nil {
		return progress{}, fmt.Errorf("the head `%s` the send-back was written against is gone: %w", git.Short(sb.Head), err)
	}
	base, err := work.MergeBase(ctx, ws, "origin/"+into, sb.Head)
	if err != nil {
		return progress{}, err
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return progress{}, err
	}
	p = progress{
		Progress: work.Progress{
			Nonce: hex.EncodeToString(nonce), Branch: sb.Ref, Base: base, Into: into,
			// The revision pushes on top of the head the send-back was
			// written against, and is leased on it.
			Pushed: sb.Head,
		},
		Points: pointIDs(sb.Points),
	}
	return p, w.Save(jobID, p)
}

// pointIDs is the send-back's command ids, in order.
func pointIDs(points []Point) []int64 {
	ids := make([]int64, len(points))
	for i, p := range points {
		ids[i] = p.Comment
	}
	return ids
}

// spec writes the diff, the linked issue and the points where the prompt says
// they are.
func (d *Deps) spec(ctx context.Context, ws string, n int, sb SendBack, p progress) error {
	diff, err := work.Diff(ctx, ws, p.Base, sb.Head)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Revision of pull request #%d\n\n", n)
	fmt.Fprintf(&b, "The head this send-back was written against is `%s`.\n\n", sb.Head)
	fmt.Fprintf(&b, "## The points\n\n")
	for i, pt := range sb.Points {
		fmt.Fprintf(&b, "%d. %s\n   (from the command with id %d)\n\n", i+1, pt.Text, pt.Comment)
	}
	fmt.Fprintf(&b, "## The diff at that head\n\n```diff\n%s\n```\n", diff)
	if issue, ok, err := d.linkedIssue(ctx, n); err != nil {
		return err
	} else if ok {
		fmt.Fprintf(&b, "\n## The issue this pull request is for\n\n# Issue #%d: %s\n\n", issue.Number, issue.Title)
		if strings.TrimSpace(issue.Body) == "" {
			b.WriteString("(no description)\n")
		} else {
			fmt.Fprintf(&b, "%s\n", issue.Body)
		}
	}
	return os.WriteFile(filepath.Join(ws, ".git", sendBackFile), []byte(b.String()), 0o644)
}

// linkedIssue is the issue the pull request is for, if the agent opened it:
// the hidden line an implement pull request's description carries, and nothing
// else. A human's pull request has none.
func (d *Deps) linkedIssue(ctx context.Context, n int) (githubIssue, bool, error) {
	pr, err := d.Tracker.PullRequest(ctx, n)
	if err != nil {
		return githubIssue{}, false, err
	}
	m := implementMarker.FindStringSubmatch(pr.Body)
	if m == nil {
		return githubIssue{}, false, nil
	}
	number, err := strconv.Atoi(m[1])
	if err != nil {
		return githubIssue{}, false, nil
	}
	is, err := d.Tracker.Issue(ctx, number)
	if err != nil {
		return githubIssue{}, false, err
	}
	return githubIssue{Number: is.Number, Title: is.Title, Body: is.Body}, true, nil
}

// implementMarker is the hidden line an implement pull request's description
// carries (implement.PRMarker). Spelled here rather than imported, because
// revise does not depend on implement.
var implementMarker = regexp.MustCompile(`<!-- afk:implement issue=(\d+) -->`)

// githubIssue is the little of an issue the revision's spec needs.
type githubIssue struct {
	Number int
	Title  string
	Body   string
}

// render builds the prompt a session runs with.
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

// work is the shared machinery this kind works through.
func (d *Deps) work() work.Workspace {
	return work.Workspace{StateDir: d.StateDir, Remote: d.Remote}
}

// notePath is where an effect's last error waits for the decision that reads
// it back (transition.Noting).
func (d *Deps) notePath(jobID string) string {
	return d.work().NotePath(jobID)
}

// save writes the revision's progress.
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
