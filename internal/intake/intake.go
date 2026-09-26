// Package intake is how a command enters the store.
//
// A pass reads the open issues and pull requests for command comments nobody
// has answered, and makes a job due for each (ADR 0001 §14). It keeps nothing of its own about
// which commands exist: the queue is re-derived from the tracker (ADR 0001
// §5), and the store only deduplicates.
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
// and a new comment moves updated_at. It takes two passes in a row reading the
// same updated_at to settle a subject: one read can miss a comment that does
// not move updated_at past what the listing showed - posted in the same
// second, which is updated_at's resolution, or not yet in a comments read
// that lags the listing - and the next pass, a poll later, does not. What a
// pass remembers is in memory and nowhere else: a restart reads everything. A
// comment that becomes a command without moving updated_at - its author given
// write access afterwards, say - waits for the subject's next change or a
// restart.
package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/store"
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
}

// Intake is one repository's command intake.
type Intake struct {
	Tracker  Tracker
	Store    store.Store
	Commands []Command

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

	// seen is each open subject's updated_at as of the last pass that read it
	// and left nothing to come back for: no error, and every command on it
	// armed or answered. It is keyed by number, which issues and pull
	// requests share.
	seen map[int]reading
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
	return made, errors.Join(errs...)
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
		key := Key(c.ID)
		armed, err := in.Store.Reserved(ctx, key)
		if err != nil {
			return made, false, err
		}
		if armed {
			continue
		}
		answered, err := in.answered(ctx, c.ID)
		if err != nil {
			return made, false, err
		}
		if answered {
			continue
		}
		job, ok, err := in.arm(ctx, cmd, subject, key)
		if err != nil {
			return made, false, err
		}
		if ok {
			made = append(made, job)
		} else {
			done = false
		}
	}
	return made, done, nil
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
// the same commit. A command is a human asking for the work afresh, so the job
// starts over wherever it came to rest.
func (in *Intake) arm(ctx context.Context, cmd Command, subject store.Subject, key string) (store.Job, bool, error) {
	a := Armer{Store: in.Store, Holder: in.Holder, LeaseTTL: in.LeaseTTL}
	return a.Arm(ctx, cmd.Kind, subject, cmd.Start, in.now(), []string{key}, true)
}

// Armer makes jobs due under a lease of its own, for whatever asks for work
// outside the job's own transitions: a command, or another job.
type Armer struct {
	Store    store.Store
	Holder   string
	LeaseTTL time.Duration
}

// Arm makes the job of kind for subject due at now, in state start with its
// attempts cleared, and reserves keys in the same commit. It reports false,
// having changed nothing, when the job is already queued or a live process
// holds it: that run will meet whatever asked. So it does for a job at rest in
// a state other than start, unless restart says to start that over.
//
// A job that is not there yet is created at rest and then armed like any
// other, so that the keys are always reserved by the commit that made the job
// due. A crash between the two leaves a job at rest with nothing reserved, and
// the next ask arms it. With no keys to reserve, it is created due.
func (a Armer) Arm(ctx context.Context, kind store.Kind, subject store.Subject, start string, now time.Time, keys []string, restart bool) (store.Job, bool, error) {
	var runAt time.Time
	if len(keys) == 0 {
		runAt = now
	}
	job, err := a.Store.Ensure(ctx, kind, subject, start, runAt)
	if err != nil {
		return store.Job{}, false, err
	}
	armable := func(job store.Job) bool {
		return job.NextRunAt.IsZero() && (restart || job.State == start)
	}
	if !armable(job) {
		return store.Job{}, false, nil
	}

	job, ok, err := a.Store.Acquire(ctx, job.ID, a.Holder, now, a.LeaseTTL)
	if err != nil || !ok {
		return store.Job{}, false, err
	}
	if !armable(job) {
		// Scheduled between the read and the lease - by `afk run`, say.
		return store.Job{}, false, a.Store.Release(ctx, job.ID, a.Holder)
	}

	job.State = start
	job.Attempts = 0
	job.NextRunAt = now
	// Without the cancellation, for the reason the runner's commit is: a stop
	// arriving here must not leave the lease standing.
	err = a.Store.Commit(context.WithoutCancel(ctx), store.Commit{
		JobID:     job.ID,
		Holder:    a.Holder,
		State:     job.State,
		Attempts:  job.Attempts,
		NextRunAt: job.NextRunAt,
		Keys:      keys,
		Release:   true,
	})
	if err != nil {
		return store.Job{}, false, errors.Join(err, a.Store.Release(context.WithoutCancel(ctx), job.ID, a.Holder))
	}
	job.Lease = nil
	return job, true, nil
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
	// Logins are case-insensitive on GitHub, so the agent's own account is too.
	if strings.EqualFold(c.Login, login) || !writers[c.Association] {
		return false
	}
	line, _, _ := strings.Cut(strings.TrimSpace(c.Body), "\n")
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
