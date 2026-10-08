// Package correction is the one pass, before hand-off, that makes the
// Correctness and Standards findings of the advisory review of the agent's own
// pull request right (#193). Distinct from a fix: a fix is caused by a red CI
// run, and a correction by findings.
//
// One review run per hand-off: a correction is checked by the local gate and
// CI, never by a second advisory review. The review stays the one posted at the
// head it reviewed, and is edited once the correction is done with: corrected,
// each corrected finding collapses to one line linking the commit that
// corrected it, and the summary names both heads; failed, the pull request is
// back at the reviewed head and the findings are advice, marked as a
// correction attempted. Its citations stay permalinks at the reviewed head, and
// its review marker keeps meaning that head.
//
// What the kinds share is here: which findings are correctable, read from the
// review as posted; the prompt and the file the session is given them in;
// where a correction sends the job next (Next), and what failing one does to
// the workspace (Fail); which commit corrected which finding, read from the
// commits' trailers; and the edit. Each kind maps a Step onto its own states,
// and keeps the session, the gate, the push and CI its own.
package correction

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/spend"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

//go:embed correct.md
var prompt string

// Prompt is the part of a session's prompt that gives it a correction to make:
// a template named "correct", given the Branch, the Gate and Reviewed, the head
// the review read. A kind parses it beside its own prompt, which carries it
// for a fresh session, and runs it alone to continue the session that wrote
// the branch.
var Prompt = `{{define "correct"}}` + strings.TrimSpace(prompt) + `{{end}}`

// File is where, in the workspace's .git, the session reads the findings it
// is to correct.
const File = "afk-findings.md"

// Finding is one correctable finding of an advisory review: its number, which
// the review numbers it by and a send-back cites it by, its axis, and its text
// as the review has it, its reproduction included.
type Finding struct {
	Number int    `json:"number"`
	Axis   string `json:"axis"`
	Text   string `json:"text"`
}

// Correction is a correction under way, or done with: kept in the progress of
// the job making it, from the review it answers until the job hands off.
type Correction struct {
	// Reviewed is the head the advisory review read, and Review the
	// comment it was posted as.
	Reviewed string `json:"reviewed"`
	Review   int64  `json:"review"`

	// Findings is the review's Correctness and Standards findings.
	Findings []Finding `json:"findings"`

	// Given is a session given the findings having finished: what follows
	// it - a gate retry, a fix - continues as any other does.
	Given bool `json:"given,omitempty"`

	// Failed is why the correction failed, once it has: the pull request
	// goes back to Reviewed, and the findings are advice. From is where the
	// rounds of that push back count from: Reviewed was pushed once
	// already, and its rounds then are not this push's (work.PushFrom).
	Failed string `json:"failed,omitempty"`
	From   int    `json:"from,omitempty"`
}

// Running reports whether c is a correction still being made: one that has
// neither failed nor been done with.
func (c *Correction) Running() bool {
	return c != nil && c.Failed == ""
}

// PushFrom is where the rounds of a push of head count from: past the first
// push's, for a failed correction's push back to the head it reviewed, and
// from the start for any other.
func (c *Correction) PushFrom(head string) int {
	if c == nil || c.Running() || head != c.Reviewed {
		return 0
	}
	return c.From
}

// Step is where a correction sends the job making it. Each kind maps it onto
// its own states.
type Step int

const (
	// Making is no correction, or one still being made: the job goes on as
	// the state it is in would.
	Making Step = iota
	// Session is a correction begun and never given to its session: the
	// move that sent it there was lost, and it goes there now.
	Session
	// PushBack is a failed correction whose push back to the head the
	// review read has not been seen yet.
	PushBack
	// Edit is a correction done with, the branch where it ended: the review
	// is edited for how it ended, and the pull request handed off.
	Edit
)

// Next is where c sends a job whose last push is pushed, in the state that
// awaits the review, where every correction begins and ends.
func Next(c *Correction, pushed string) Step {
	switch {
	case c == nil:
		return Making
	case !c.Running():
		return c.Over(pushed)
	case pushed == c.Reviewed:
		return Session
	}
	return Edit
}

// Over is where c sends a job whose last push is pushed once c has failed, in
// any state Fail is reached from: Fail saves the correction failed before the
// job moves, and a process killed between the two runs that state again. The
// state has nothing left to do for it. Making for a correction that has not
// failed, or none.
func (c *Correction) Over(pushed string) Step {
	switch {
	case c == nil || c.Running():
		return Making
	case pushed != c.Reviewed:
		return PushBack
	}
	return Edit
}

// Fail is c failed, for why: the workspace ws put back on branch at the head
// the review read, and the rounds of its push back counted past the first
// push's (work.PushFrom). The caller saves c, and goes where c.Over says.
func (c *Correction) Fail(ctx context.Context, s store.Store, ws, branch, why string) error {
	if err := work.Restore(ctx, ws, branch, c.Reviewed); err != nil {
		return err
	}
	from, err := work.PushFrom(ctx, s, branch, c.Reviewed)
	if err != nil {
		return err
	}
	c.Failed, c.From = why, from
	return nil
}

// Begin is the correction the advisory review r of head asks for, if it has a
// correctable finding: a Correctness or Standards finding, of any severity. A
// merged duplicate is under the axis whose evidence is strongest, so it counts
// when that is one of the two. ok is false for a review with none, which is
// handed off as it is.
func Begin(r github.Comment, head string) (Correction, bool) {
	found := Correctable(r.Body)
	if len(found) == 0 {
		return Correction{}, false
	}
	return Correction{Reviewed: head, Review: r.ID, Findings: found}, true
}

// Correctable is the Correctness and Standards findings of an advisory review,
// as the reviewing-changes skill reports them: under a `## Correctness` or a
// `## Standards` heading, each a numbered item at the start of a line. Each
// runs to the next finding, heading or question, or to the end of the review.
// The skill decides the axes, so a finding is picked by the heading it is
// under: no marker of its own is needed.
func Correctable(body string) []Finding {
	lines, blocks := parse(body)
	var out []Finding
	for _, b := range blocks {
		if correctable(b.axis) {
			out = append(out, Finding{Number: b.number, Axis: b.axis, Text: strings.Join(lines[b.start:b.end], "\n")})
		}
	}
	return out
}

// correctable reports whether a finding under heading is the agent's to
// correct.
func correctable(heading string) bool {
	return strings.EqualFold(heading, "Correctness") || strings.EqualFold(heading, "Standards")
}

// block is one numbered finding of a review: its number, the heading it is
// under, and the lines it spans, end excluded and trailing blank lines left out.
type block struct {
	number     int
	axis       string
	start, end int
}

var (
	// numbered is the line a finding starts on: `3. ...`, or `**3.** ...`.
	numbered = regexp.MustCompile(`^(?:\*\*)?(\d+)\.(?:\*\*)?\s`)
	// question is the line a question starts on, after the findings.
	question = regexp.MustCompile(`^(?:\*\*)?Q\d+\b`)
)

// parse is a review's lines and its numbered findings. Nothing in a fenced
// block starts or ends one: a reproduction's source and output are fenced, and
// may hold anything. The agent's wrapper is the edges of the review: the spend
// footer, and the `</details>` that closes it, end a finding.
func parse(body string) ([]string, []block) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var (
		blocks []block
		axis   string
		fence  string
		open   bool
		depth  int
	)
	end := func(at int) {
		if !open {
			return
		}
		b := &blocks[len(blocks)-1]
		for at > b.start+1 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		b.end, open = at, false
	}
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if fence != "" {
			// Closed only by a fence of its own character, at least as
			// long, and with nothing after it: a reproduction may hold a
			// shorter one.
			if run := fenceOf(t); run != "" && run[0] == fence[0] && len(run) >= len(fence) && strings.TrimSpace(t[len(run):]) == "" {
				fence = ""
			}
			continue
		}
		if fence = fenceOf(t); fence != "" {
			continue
		}
		if h, ok := strings.CutPrefix(line, "## "); ok {
			end(i)
			axis = strings.TrimSpace(h)
			continue
		}
		if line == spend.Open || question.MatchString(line) {
			end(i)
			continue
		}
		if m := numbered.FindStringSubmatch(line); m != nil {
			end(i)
			n, _ := strconv.Atoi(m[1])
			blocks = append(blocks, block{number: n, axis: axis, start: i, end: len(lines)})
			open, depth = true, 0
			continue
		}
		if open {
			// Only a tag on a line of its own counts, as the skill and the
			// wrapper write them: one a finding's text mentions does not.
			if strings.HasPrefix(t, "<details") {
				depth++
			}
			if strings.HasSuffix(t, "</details>") && (t == "</details>" || strings.HasPrefix(t, "<details")) {
				depth--
			}
			if depth < 0 {
				// The wrapper's own `</details>`: the review ends here.
				end(i)
				axis = ""
			}
		}
	}
	end(len(lines))
	return lines, blocks
}

// fenceOf is the fence t opens or closes, if it is one: its run of backticks
// or tildes, three or more.
func fenceOf(t string) string {
	if !strings.HasPrefix(t, "```") && !strings.HasPrefix(t, "~~~") {
		return ""
	}
	n := 0
	for n < len(t) && t[n] == t[0] {
		n++
	}
	return t[:n]
}

// Write puts c's findings where the prompt says they are, in the workspace ws.
func Write(ws string, c Correction) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# The findings to correct\n\nFrom the advisory review of `%s`, numbered as it numbers them.\n", c.Reviewed)
	for _, f := range c.Findings {
		fmt.Fprintf(&b, "\n## advisory %d (%s)\n\n%s\n", f.Number, f.Axis, f.Text)
	}
	return os.WriteFile(filepath.Join(ws, ".git", File), []byte(b.String()), 0o644)
}

var (
	// trailer is a commit message's line naming what it corrects.
	trailer = regexp.MustCompile(`(?mi)^corrects:[ \t]*(.+)$`)
	// advisory is one finding a trailer names.
	advisory = regexp.MustCompile(`(?i)\badvisory\s+(\d+)\b`)
)

// Commits is which commit corrected which finding: for each finding a commit
// in reviewed..head names with a `Corrects: advisory <n>` trailer, the newest
// commit that names it. Read in dir, the relay the head was pushed from, and
// never in the workspace, whose .git is the session's to write.
func Commits(ctx context.Context, dir, reviewed, head string) (map[int]string, error) {
	out, err := git.RunEnv(ctx, dir, git.Isolated, "log", "--format=%H%x00%B%x01", reviewed+".."+head)
	if err != nil {
		return nil, err
	}
	commits := map[int]string{}
	for rec := range strings.SplitSeq(out, "\x01") {
		sha, msg, ok := strings.Cut(strings.TrimSpace(rec), "\x00")
		if !ok {
			continue
		}
		for _, t := range trailer.FindAllStringSubmatch(msg, -1) {
			for _, m := range advisory.FindAllStringSubmatch(t[1], -1) {
				n, _ := strconv.Atoi(m[1])
				if _, seen := commits[n]; !seen {
					// Newest first: the last commit to touch it.
					commits[n] = sha
				}
			}
		}
	}
	return commits, nil
}

// Marker is the hidden line the advisory review carries once it is edited for
// how c ended: corrected to head, or failed. The review's own marker stays,
// and keeps meaning the head it reviewed.
func Marker(c Correction, head string) string {
	if c.Failed != "" {
		return fmt.Sprintf("<!-- afk:correction reviewed=%s failed -->", c.Reviewed)
	}
	return fmt.Sprintf("<!-- afk:correction reviewed=%s head=%s -->", c.Reviewed, head)
}

// Amended is body, the advisory review c answers, edited for how c ended, in
// repository repo: corrected to head, with commits saying which commit
// corrected which finding, or failed. Only c's findings change, each found in
// body by its number. The rest of the review is left as it is.
//
//   - A corrected finding collapses to one line linking its commit, with the
//     finding as the review wrote it folded beneath, so a send-back can still
//     read what it cites.
//   - A finding no commit corrected stays as it was, marked as advice a
//     correction was attempted on.
//   - Failed, every finding stays as it was, marked "correction attempted,
//     failed".
//
// The summary names both heads once CI passed the correction, and says it
// corrected the review only when a commit named a finding it corrected.
func Amended(body string, c Correction, head string, commits map[int]string, repo string) string {
	ours := map[int]bool{}
	for _, f := range c.Findings {
		ours[f.Number] = true
	}
	lines, blocks := parse(body)
	var out []string
	at, linked := 0, false
	for _, b := range blocks {
		if !ours[b.number] || !correctable(b.axis) {
			continue
		}
		out = append(out, lines[at:b.start]...)
		finding := lines[b.start:b.end]
		switch sha := commits[b.number]; {
		case c.Failed != "":
			out = append(out, finding...)
			out = append(out, "", "_Correction attempted, failed: this is advice._")
		case sha != "":
			linked = true
			out = append(out, fmt.Sprintf("<details><summary>%d. Corrected in %s.</summary>", b.number, commitLink(repo, sha)), "")
			out = append(out, finding...)
			out = append(out, "", "</details>")
		default:
			out = append(out, finding...)
			out = append(out, "", "_Correction attempted: no commit corrected this, so it is advice._")
		}
		at = b.end
	}
	out = append(out, lines[at:]...)
	edited := strings.Join(out, "\n")

	said := fmt.Sprintf("A correction of its Correctness and Standards findings was attempted and failed: %s The pull request is back at `%s`, as reviewed, so those findings are advice.", strings.TrimSpace(c.Failed), git.Short(c.Reviewed))
	switch {
	case c.Failed == "" && linked:
		said = fmt.Sprintf("Reviewed at `%s`; corrected to `%s` by %s of its Correctness and Standards findings, checked by their reproductions and CI, not re-reviewed. The citations are at `%s`.", git.Short(c.Reviewed), git.Short(head), compareLink(repo, c.Reviewed, head), git.Short(c.Reviewed))
		edited = review.Followed(edited, "; corrected to <code>"+git.Short(head)+"</code>, checked by reproductions and CI, not re-reviewed")
	case c.Failed == "":
		said = fmt.Sprintf("Reviewed at `%s`; pushed to `%s` by %s of its Correctness and Standards findings, checked by CI, not re-reviewed, but no commit of it named a finding it corrected, so those findings are advice. The citations are at `%s`.", git.Short(c.Reviewed), git.Short(head), compareLink(repo, c.Reviewed, head), git.Short(c.Reviewed))
		edited = review.Followed(edited, "; pushed to <code>"+git.Short(head)+"</code> by a correction that named no finding, so the findings are advice")
	}
	added := Marker(c, head) + "\n" + said + "\n"
	if mark := review.Marker(c.Reviewed); strings.Contains(edited, mark) {
		return strings.Replace(edited, mark, mark+"\n"+added, 1)
	}
	return added + "\n" + edited
}

func commitLink(repo, sha string) string {
	if repo == "" {
		return "<code>" + git.Short(sha) + "</code>"
	}
	return fmt.Sprintf(`<a href="https://github.com/%s/commit/%s"><code>%s</code></a>`, repo, sha, git.Short(sha))
}

func compareLink(repo, from, to string) string {
	if repo == "" {
		return fmt.Sprintf("`%s..%s`", git.Short(from), git.Short(to))
	}
	return fmt.Sprintf("[the correction](https://github.com/%s/compare/%s...%s)", repo, from, to)
}

// Tracker is what the edit reads and writes. *github.Client is one.
type Tracker interface {
	Comments(ctx context.Context, number int) ([]github.Comment, error)
	EditComment(ctx context.Context, commentID int64, body string) error
}

// Editor is the edit of the advisory review, and its bounds.
type Editor struct {
	Tracker Tracker

	// Store is read, never written: which round of the edit is next.
	Store store.Store

	// Login is the agent's own account, whose comment the review is, and
	// Repo the repository, as owner/name, the links are built on.
	Login string
	Repo  string

	// Rounds is how many times the edit is made before one that never shows
	// is given up on. A parameter.
	Rounds int
}

// Edit is the edit of pull request pr's advisory review for how c ended: at
// head, with the commits that corrected it read from relay, as Amended says. It is false when there is nothing to
// edit, and the caller hands off: the review already says so, it is gone, or
// the edit was made Rounds times and never showed. Either of the last two is
// logged and costs the hand-off nothing: the review is advice, and the pull
// request at head is what CI checked. The caller reads the review back after
// the effect, and calls this again.
//
// The effect reads the review again and edits that, so a replay after a slow
// success edits nothing.
func (e Editor) Edit(ctx context.Context, pr int, c Correction, head, relay string, logf func(format string, a ...any)) (transition.Effect, bool, error) {
	var commits map[int]string
	if c.Running() {
		var err error
		if commits, err = Commits(ctx, relay, c.Reviewed, head); err != nil {
			return transition.Effect{}, false, err
		}
	}
	comments, err := e.Tracker.Comments(ctx, pr)
	if err != nil {
		return transition.Effect{}, false, err
	}
	r, ok := e.review(comments, c)
	if !ok {
		logf("the advisory review of `%s` on #%d is gone, so it is not edited for its correction", git.Short(c.Reviewed), pr)
		return transition.Effect{}, false, nil
	}
	mark := Marker(c, head)
	if strings.Contains(r.Body, mark) {
		return transition.Effect{}, false, nil
	}
	stem := fmt.Sprintf("correction-pr-%d-%s-%s", pr, c.Reviewed, head)
	key, err := transition.Round(ctx, e.Store, stem, 0, e.Rounds)
	if ran, ok := transition.Spent(err); ok {
		logf("the advisory review of `%s` on #%d was edited %d times for its correction and never showed it, so it is left as it is", git.Short(c.Reviewed), pr, ran.Rounds)
		return transition.Effect{}, false, nil
	}
	if err != nil {
		return transition.Effect{}, false, err
	}
	return transition.Effect{Key: key, Do: func(ctx context.Context) error {
		comments, err := e.Tracker.Comments(ctx, pr)
		if err != nil {
			return err
		}
		r, ok := e.review(comments, c)
		if !ok || strings.Contains(r.Body, mark) {
			return nil
		}
		return e.Tracker.EditComment(ctx, r.ID, Amended(r.Body, c, head, commits, e.Repo))
	}}, true, nil
}

// review is c's advisory review among comments: the agent's comment it was
// posted as, still carrying the review's marker.
func (e Editor) review(comments []github.Comment, c Correction) (github.Comment, bool) {
	for _, cm := range comments {
		if cm.ID == c.Review && strings.EqualFold(cm.Login, e.Login) && strings.Contains(cm.Body, review.Marker(c.Reviewed)) {
			return cm, true
		}
	}
	return github.Comment{}, false
}
