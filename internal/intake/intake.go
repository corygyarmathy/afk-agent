// Package intake is how a command enters the store.
//
// A pass reads the open issues and pull requests for command comments nobody
// has answered, and makes a job due for each (ADR 0001 §14). A command that
// may also be issued as a submitted review (`/revise`, #133) is read from the
// pull request's reviews as well, and answered on the review. It keeps nothing
// of its own about which commands exist: the queue is re-derived from the
// tracker (ADR 0001 §5), and the store only deduplicates.
//
// Answered is read from the tracker, not from the store. The transition that
// takes a command claims it with the agent's reaction (ADR 0001 §7), so a wiped
// store cannot make an old command look new. What the store adds is narrower:
// a command a pass has already armed a job for is not armed again. Without
// that, a job that came to rest before it claimed its command - a failure
// ahead of the claim, then a park - would be made due on every pass, which is
// a retry loop with no bound that nobody configured.
//
// A pass reads a subject's comments only when the subject has changed since
// passes last settled it, going by the listing's updated_at. A comments
// request per open subject per poll is a rate limit the backlog grows into,
// and a new comment moves updated_at. So does a submitted review, to when it
// was submitted, though GitHub took up to half a minute to list it so on
// 2026-10-02: a pass in that time sees the subject unchanged, and a later one
// does not. It takes two passes in a row reading the same updated_at to
// settle a subject: one read can miss a comment that does not move updated_at
// past what the listing showed - posted in the same second, which is
// updated_at's resolution, or not yet in a comments read that lags the
// listing - and the next pass, a poll later, does not. What a pass remembers
// is in memory and nowhere else: a restart reads everything. A
// comment that becomes a command without moving updated_at - its author given
// write access afterwards, say - waits for the subject's next change or a
// restart.
//
// A pass also takes work with nobody asking, when it is configured to: an
// open issue carrying the eligibility label, with no open blocker, becomes the
// same due job its command would make. The label is a queue filter, not a
// command - it says the issue may be taken - so it is read from the listing on
// every pass rather than settled, and a blocker closing need not move the
// blocked issue's updated_at. An issue is taken once: not if it has a job,
// whatever made that job, and not if it carries the agent's claim, on the
// issue or on a command for the same work, which is what stops a wiped store
// taking it again. The job claims the issue itself, whoever asked. A labelled
// issue listed with no dependency summary is not taken, and is reported once.
// Eligible issues are taken lowest number first, which on GitHub is oldest
// first.
//
// Taking is held at the review-queue limit, when one is configured: a pass
// takes no more eligible issues than the review queue has room for, counted
// afresh on every pass from the tracker and the store. A held issue is not
// taken at all - nothing is asked of it beyond whether it could be, and
// nothing is said on it - so it is still the operator's to /implement. Only
// taking is held. A command arms its job whatever the queue says, and so does
// every job for a pull request that already exists: holding a review or a
// revision would stop the queue draining. Intake logs when it starts holding
// and when it stops, from what it remembers in memory, so a process that runs
// one pass - `afk intake` - says it is holding on every pass it is.
package intake

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// Claim is the reaction the agent claims a command with. A command carrying it
// from the agent's own account is answered.
const Claim = "eyes"

// writers is who may issue a command: the author associations that carry write
// access. The repository is public and a command spends budget, so anyone else
// is ignored, silently - a reply would be the agent answering them.
var writers = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}

// Tracker is what a pass reads. *github.Client is one; a test's is a fixture.
type Tracker interface {
	// OpenIssues is every open issue and pull request, from one listing.
	OpenIssues(ctx context.Context) ([]github.Issue, error)
	Comments(ctx context.Context, number int) ([]github.Comment, error)
	Reactions(ctx context.Context, commentID int64) ([]github.Reaction, error)
	IssueReactions(ctx context.Context, number int) ([]github.Reaction, error)

	// PullRequestReviews and PullRequestReviewReactions are read only for a
	// command that may be issued as a review.
	PullRequestReviews(ctx context.Context, number int) ([]github.PullRequestReview, error)
	PullRequestReviewReactions(ctx context.Context, nodeID string) ([]github.Reaction, error)
}

// Command is one entry in the command registry: the word a comment starts
// with, the kind of subject it is issued on, the job kind it asks for, and the
// state a job of that kind starts in. Adding a command is an entry and a kind,
// not a change here (ADR 0001 §14).
type Command struct {
	Word string

	// On is the kind of subject the command is issued on: `/implement` on an
	// issue, `/review` on a pull request. The same word on the other kind is
	// not this command, and is ignored the way any comment that is not a
	// command is.
	On store.SubjectType

	Kind  store.Kind
	Start string

	// ByReview is whether the command may also be issued as a submitted
	// review on a pull request whose body starts with the word, as `/revise`
	// may (#133). The review is claimed and answered as a comment is.
	ByReview bool
}

// Unattended is the work an issue is taken for with nobody asking: the
// eligibility label that opts an issue in, and the job kind and start state
// its command would make.
type Unattended struct {
	// Label is the eligibility label. A parameter; empty, nothing is taken
	// unattended.
	Label string

	Kind  store.Kind
	Start string

	// Queue is the review queue's limit. Zero, taking is not limited.
	Queue ReviewQueue
}

// ReviewQueue is the limit on the review queue (GLOSSARY.md: review queue): the
// agent's open pull requests carrying the hand-off label, plus the jobs of the
// unattended kind taken and not yet handed off, whether a command or the
// eligibility label made them, plus the revisions in flight.
type ReviewQueue struct {
	// Limit is how long the queue may be before taking is held. A parameter;
	// zero, there is no limit.
	Limit int

	// Label is the hand-off label, which is how a pull request waiting on the
	// operator's review is known. Its revision's claim takes it off, and a
	// hand-back never had it.
	Label string

	// Revise is the job kind that revises a sent-back pull request. Its claim
	// takes the hand-off label off, so while one is in flight it counts in
	// the label's place. Empty, no revision counts.
	Revise store.Kind
}

// Intake is one repository's command intake.
type Intake struct {
	Tracker  Tracker
	Store    store.Store
	Commands []Command

	// Unattended is what an eligible issue is taken for. Zero, nothing is.
	Unattended Unattended

	// Login is the agent's own account. Its comments are never commands, and
	// its reaction is the claim.
	Login string

	// Holder and LeaseTTL are the lease a pass takes to make a job at rest due
	// again. The store refuses a commit from anyone not holding the job, and
	// intake is no exception.
	Holder   string
	LeaseTTL time.Duration

	// Clock is the time source. Nil means time.Now.
	Clock func() time.Time

	// Log receives a line when taking starts being held at the review-queue
	// limit, and one when it stops. Nil is silent.
	Log func(msg string)

	// seen is each open subject's updated_at as of the last pass that read it
	// and left nothing to come back for: no error, and every command on it
	// armed or answered. It is keyed by number, which issues and pull
	// requests share.
	seen map[int]reading

	// blind is the labelled issues the last pass found listed with no
	// dependency summary, so each is reported once rather than every pass.
	blind map[int]bool

	// holding is whether the last pass that counted the review queue held an
	// issue back, so that starting and stopping are each said once.
	holding bool
}

// reading is what passes last made of a subject's updated_at.
type reading struct {
	at time.Time

	// settled is whether two passes in a row read the subject at at, so that
	// neither a comment in the same second nor a lagging read can have hidden
	// a command from both.
	settled bool
}

// Key is the idempotency key a command's arming is reserved under.
func Key(commentID int64) string {
	return fmt.Sprintf("intake-comment-%d", commentID)
}

// PullRequestReviewKey is the idempotency key a command issued as a review is armed
// under. A review's id is not a comment's, so the two are kept apart.
func PullRequestReviewKey(reviewID int64) string {
	return fmt.Sprintf("intake-review-%d", reviewID)
}

// Pass reads the tracker once and returns the jobs it made due.
//
// A subject that cannot be read does not stop the rest: its error is returned
// alongside whatever the pass did manage, and the next pass tries it again.
// A subject that has not changed since passes settled it is not read at all.
//
// Making a job due is all a pass does. Whether that job starts is admission's
// decision (ADR 0001 §11), and a pass never runs anything.
//
// Passes are not safe to run concurrently on the same Intake.
func (in *Intake) Pass(ctx context.Context) ([]store.Job, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	open, err := in.Tracker.OpenIssues(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing open issues: %w", err)
	}

	var (
		made []store.Job
		errs []error
		next = make(map[int]reading, len(open))
	)
	for _, is := range open {
		last, ok := in.seen[is.Number]
		// A zero updated_at says nothing about whether the subject changed.
		unchanged := ok && !is.UpdatedAt.IsZero() && last.at.Equal(is.UpdatedAt)
		if unchanged && last.settled {
			next[is.Number] = last
			continue
		}
		subject := store.Subject{Type: store.SubjectIssue, Number: is.Number}
		name := "issue"
		if is.PullRequest {
			subject.Type, name = store.SubjectPR, "pull request"
		}
		jobs, done, err := in.subject(ctx, subject)
		made = append(made, jobs...)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %d: %w", name, is.Number, err))
		}
		// The listing's updated_at, not a later one: a comment posted after
		// the listing's second moves it past this, and the next pass reads it.
		if done && err == nil {
			next[is.Number] = reading{at: is.UpdatedAt, settled: unchanged}
		}
	}
	// Built afresh from the open listing, so a closed subject is forgotten.
	in.seen = next

	taken, err := in.take(ctx, open)
	made = append(made, taken...)
	errs = append(errs, err)
	return made, errors.Join(errs...)
}

// take makes the unattended work on each eligible issue due, lowest number
// first. It runs after the commands, so an issue commanded in the same pass
// already has its job.
func (in *Intake) take(ctx context.Context, open []github.Issue) ([]store.Job, error) {
	if in.Unattended.Label == "" {
		return nil, nil
	}
	var (
		eligible []github.Issue
		errs     []error
		blind    = make(map[int]bool)
	)
	for _, is := range open {
		if is.PullRequest || !in.labelled(is) {
			continue
		}
		switch {
		case !is.DependenciesRead:
			// Said once, not every pass: the listing stays as blind until
			// something changes it.
			if !in.blind[is.Number] {
				errs = append(errs, fmt.Errorf("issue %d: carries the eligibility label, and the listing gave no dependency summary; not taken", is.Number))
			}
			blind[is.Number] = true
		case is.BlockedBy == 0:
			eligible = append(eligible, is)
		}
	}
	// Built afresh, so an issue closed, unlabelled or read again is forgotten.
	in.blind = blind
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].Number < eligible[j].Number })

	// counted is whether this pass counted the review queue, and full whether
	// it found no room in it.
	room, queue, counted, full := len(eligible), 0, false, false
	if limit := in.Unattended.Queue.Limit; limit > 0 && len(eligible) > 0 {
		var err error
		if queue, err = in.queue(ctx); err != nil {
			// Not knowing how full the queue is, take nothing.
			return nil, errors.Join(append(errs, fmt.Errorf("counting the review queue: %w", err))...)
		}
		room, counted = max(limit-queue, 0), true
		full = room == 0
	}

	var (
		made []store.Job
		held bool
	)
	for _, is := range eligible {
		ok, err := in.takeable(ctx, is.Number)
		if err != nil {
			errs = append(errs, fmt.Errorf("issue %d: %w", is.Number, err))
		}
		if !ok {
			continue
		}
		if room == 0 {
			// One issue held back is enough to say so, and the rest need not
			// be read.
			held = true
			break
		}
		job, err := in.ensure(ctx, is.Number)
		if err != nil {
			errs = append(errs, fmt.Errorf("issue %d: %w", is.Number, err))
			continue
		}
		made = append(made, job)
		room--
	}
	switch {
	case !counted:
	case held:
		// What this pass took is in the queue now too.
		in.hold(true, queue+len(made))
	case !full:
		in.hold(false, queue+len(made))
	}
	return made, errors.Join(errs...)
}

// hold says when taking starts being held, and when it stops. Only a pass that
// counted the review queue says either: it stops being held when the queue has
// room, not when there is nothing left to hold back - the operator took the
// held issue by hand, or its label came off - or when deciding whether an
// issue could be taken failed.
func (in *Intake) hold(held bool, queue int) {
	if held == in.holding {
		return
	}
	in.holding = held
	if in.Log == nil {
		return
	}
	if held {
		in.Log(fmt.Sprintf("intake: holding eligible issues, with %d in the review queue and a limit of %d", queue, in.Unattended.Queue.Limit))
	} else {
		in.Log("intake: stopped holding eligible issues")
	}
}

// queue counts the review queue: the jobs of the unattended kind and of the
// revision kind taken and not yet handed off, and the agent's open pull
// requests carrying the hand-off label. A job is taken and not handed off
// while it is scheduled or leased; one that handed off, handed back or parked
// is at rest. A job made due that no pool runs is in the queue until something
// runs it.
//
// The jobs are read first and the pull requests listed afresh after, rather
// than from the pass's own listing: a job that hands off in between is then
// counted twice, once as its pull request, rather than not at all. A job
// applying the hand-off label is counted twice the same way until it reads the
// label back. Both err towards holding.
func (in *Intake) queue(ctx context.Context) (int, error) {
	jobs, err := in.Store.Jobs(ctx)
	if err != nil {
		return 0, err
	}
	q, n := in.Unattended.Queue, 0
	now := in.now()
	for _, j := range jobs {
		if j.Kind != in.Unattended.Kind && (q.Revise == "" || j.Kind != q.Revise) {
			continue
		}
		if !j.NextRunAt.IsZero() || (j.Lease != nil && !j.Lease.Expired(now)) {
			n++
		}
	}
	open, err := in.Tracker.OpenIssues(ctx)
	if err != nil {
		return 0, err
	}
	for _, is := range open {
		if is.PullRequest && strings.EqualFold(is.Author, in.Login) && github.HasLabel(is.Labels, q.Label) {
			n++
		}
	}
	return n, nil
}

// takeable reports whether issue n's unattended work may be taken: it has no
// job, and was not taken before one - it carries no claim of the agent's, and
// nor does any of its commands for the same work.
func (in *Intake) takeable(ctx context.Context, n int) (bool, error) {
	subject := store.Subject{Type: store.SubjectIssue, Number: n}
	_, err := in.Store.Job(ctx, store.ID(in.Unattended.Kind, subject))
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, store.ErrNoJob) {
		return false, err
	}
	taken, err := in.taken(ctx, n)
	return !taken && err == nil, err
}

// ensure makes issue n's unattended work due.
func (in *Intake) ensure(ctx context.Context, n int) (store.Job, error) {
	u := in.Unattended
	// Created due rather than armed: there is no job to lease, and the job
	// being there is what stops the next pass.
	return in.Store.Ensure(ctx, u.Kind, store.Subject{Type: store.SubjectIssue, Number: n}, u.Start, in.now())
}

// taken reports whether issue n carries the agent's claim, on the issue itself
// or on a command for the unattended work. It is read on every pass the issue
// has no job, because an operator takes a claim back to queue the issue again.
func (in *Intake) taken(ctx context.Context, n int) (bool, error) {
	reactions, err := in.Tracker.IssueReactions(ctx, n)
	if err != nil {
		return false, err
	}
	if Claimed(reactions, in.Login) {
		return true, nil
	}
	comments, err := in.Tracker.Comments(ctx, n)
	if err != nil {
		return false, err
	}
	for _, c := range comments {
		if cmd, ok := in.command(c, store.SubjectIssue); !ok || cmd.Kind != in.Unattended.Kind {
			continue
		}
		answered, err := in.answered(ctx, c.ID)
		if err != nil || answered {
			return answered, err
		}
	}
	return false, nil
}

// labelled reports whether an issue carries the eligibility label.
func (in *Intake) labelled(is github.Issue) bool {
	return github.HasLabel(is.Labels, in.Unattended.Label)
}

// subject reads one subject's comments and arms what they ask for. It reports
// done when there is nothing to come back for until the subject changes:
// every command on it is armed or answered. A command whose job was already
// queued or held is neither - that run may park before it claims the command
// - and the claim is a reaction, which need not move updated_at.
func (in *Intake) subject(ctx context.Context, subject store.Subject) ([]store.Job, bool, error) {
	comments, err := in.Tracker.Comments(ctx, subject.Number)
	if err != nil {
		return nil, false, err
	}

	var made []store.Job
	done := true
	for _, c := range comments {
		cmd, ok := in.command(c, subject.Type)
		if !ok {
			continue
		}
		job, settled, err := in.armUnanswered(ctx, cmd, subject, Key(c.ID), func(ctx context.Context) (bool, error) {
			return in.answered(ctx, c.ID)
		})
		if err != nil {
			return made, false, err
		}
		made = append(made, job...)
		done = done && settled
	}
	if subject.Type != store.SubjectPR || !in.byReview() {
		return made, done, nil
	}

	reviews, err := in.Tracker.PullRequestReviews(ctx, subject.Number)
	if err != nil {
		return made, false, err
	}
	for _, r := range reviews {
		cmd, ok := in.commandByReview(r)
		if !ok {
			continue
		}
		job, settled, err := in.armUnanswered(ctx, cmd, subject, PullRequestReviewKey(r.ID), func(ctx context.Context) (bool, error) {
			reactions, err := in.Tracker.PullRequestReviewReactions(ctx, r.NodeID)
			return Claimed(reactions, in.Login), err
		})
		if err != nil {
			return made, false, err
		}
		made = append(made, job...)
		done = done && settled
	}
	return made, done, nil
}

// armUnanswered arms command cmd under key, unless a pass has armed it
// already or answered reports it answered: the job it made, if it made one,
// and settled unless it found the command neither armed nor answered and could
// not arm it either, because its job was queued or held.
func (in *Intake) armUnanswered(ctx context.Context, cmd Command, subject store.Subject, key string, answered func(context.Context) (bool, error)) ([]store.Job, bool, error) {
	armed, err := in.Store.Reserved(ctx, key)
	if err != nil || armed {
		return nil, err == nil, err
	}
	yes, err := answered(ctx)
	if err != nil || yes {
		return nil, err == nil, err
	}
	job, ok, err := in.arm(ctx, cmd, subject, key)
	if err != nil || !ok {
		return nil, err == nil && ok, err
	}
	return []store.Job{job}, true, nil
}

// byReview reports whether any command this intake answers may be issued as a
// review, which is whether a pull request's byReview are read at all.
func (in *Intake) byReview() bool {
	for _, cmd := range in.Commands {
		if cmd.ByReview {
			return true
		}
	}
	return false
}

// commandByReview reports whether a review is a command this intake answers, as
// command does for a comment.
func (in *Intake) commandByReview(r github.PullRequestReview) (Command, bool) {
	for _, cmd := range in.Commands {
		if cmd.ByReview && cmd.On == store.SubjectPR && IsCommandByReview(r, in.Login, cmd.Word) {
			return cmd, true
		}
	}
	return Command{}, false
}

// command reports whether a comment is a command this intake answers: written
// by an account with write access that is not the agent's, on the kind of
// subject a registered command is issued on, and starting with its word.
func (in *Intake) command(c github.Comment, on store.SubjectType) (Command, bool) {
	for _, cmd := range in.Commands {
		if cmd.On == on && IsCommand(c, in.Login, cmd.Word) {
			return cmd, true
		}
	}
	return Command{}, false
}

func (in *Intake) answered(ctx context.Context, commentID int64) (bool, error) {
	reactions, err := in.Tracker.Reactions(ctx, commentID)
	if err != nil {
		return false, err
	}
	return Claimed(reactions, in.Login), nil
}

// arm makes the job a command asks for due, and reserves the command's key in
// the same commit.
func (in *Intake) arm(ctx context.Context, cmd Command, subject store.Subject, key string) (store.Job, bool, error) {
	a := transition.Armer{Store: in.Store, Holder: in.Holder, LeaseTTL: in.LeaseTTL}
	return a.Restart(ctx, cmd.Kind, subject, cmd.Start, in.now(), key)
}

func (in *Intake) validate() error {
	switch {
	case in.Tracker == nil:
		return errors.New("intake has no tracker")
	case in.Store == nil:
		return errors.New("intake has no store")
	case in.Login == "":
		// Without it the agent's own comments would be commands, and its
		// claims would not read as answers.
		return errors.New("intake does not know the agent's own login")
	case in.Holder == "":
		return errors.New("intake has no holder name")
	case in.LeaseTTL <= 0:
		return errors.New("intake has no lease duration")
	}
	for _, cmd := range in.Commands {
		switch {
		case !strings.HasPrefix(cmd.Word, "/") || strings.ContainsAny(cmd.Word, " \t\n"):
			return fmt.Errorf("command %q is not a single word starting with /", cmd.Word)
		case !cmd.On.Valid():
			return fmt.Errorf("command %s: unknown subject type %q", cmd.Word, cmd.On)
		case !cmd.Kind.Valid():
			return fmt.Errorf("command %s: unknown job kind %q", cmd.Word, cmd.Kind)
		case cmd.Start == "":
			return fmt.Errorf("command %s: no start state", cmd.Word)
		case cmd.ByReview && cmd.On != store.SubjectPR:
			return fmt.Errorf("command %s: only a pull request has reviews", cmd.Word)
		}
	}
	if u := in.Unattended; u.Label != "" {
		switch {
		case !u.Kind.Valid():
			return fmt.Errorf("unattended work: unknown job kind %q", u.Kind)
		case u.Start == "":
			return errors.New("unattended work: no start state")
		case u.Queue.Limit < 0:
			return fmt.Errorf("unattended work: review-queue limit %d is negative", u.Queue.Limit)
		case u.Queue.Revise != "" && !u.Queue.Revise.Valid():
			return fmt.Errorf("unattended work: unknown revision kind %q", u.Queue.Revise)
		case u.Queue.Limit > 0 && u.Queue.Label == "":
			// Without it no pull request would count, and the limit would
			// hold only in-flight work.
			return errors.New("unattended work: a review-queue limit and no hand-off label")
		}
	}
	return nil
}

func (in *Intake) now() time.Time {
	if in.Clock != nil {
		return in.Clock()
	}
	return time.Now()
}

// IsCommand reports whether a comment issues the command word: written by an
// account with write access that is not login, with word as its first word.
//
// Exported because intake is not the only thing that has to agree on what a
// command is. The transition that answers one reads the same comments, and two
// definitions would let intake arm a job for a comment the transition then
// fails to claim.
func IsCommand(c github.Comment, login, word string) bool {
	return issues(c.Login, c.Association, c.Body, login, word)
}

// IsCommandByReview reports whether a review issues the command word, as
// IsCommand does for a comment: submitted, by an account with write access
// that is not login, with word as its body's first word. What the review
// says - approve, request changes, comment - decides nothing.
func IsCommandByReview(r github.PullRequestReview, login, word string) bool {
	return r.State != "PENDING" && issues(r.Login, r.Association, r.Body, login, word)
}

// issues is IsCommand and IsCommandByReview's shared rule.
func issues(author, association, body, login, word string) bool {
	// Logins are case-insensitive on GitHub, so the agent's own account is too.
	if strings.EqualFold(author, login) || !writers[association] {
		return false
	}
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	fields := strings.Fields(line)
	return len(fields) > 0 && fields[0] == word
}

// Claimed reports whether reactions carry login's claim.
func Claimed(reactions []github.Reaction, login string) bool {
	for _, r := range reactions {
		if r.Content == Claim && strings.EqualFold(r.Login, login) {
			return true
		}
	}
	return false
}
