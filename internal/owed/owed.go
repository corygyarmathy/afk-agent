// Package owed is what a transition's decision owes the tracker, and the read
// back that makes sure the tracker gets it: the claims on the requests a job
// took, the replies it gave them, and a hand-back's comment and label.
//
// The runner commits a decision and then performs its effects (ADR 0001 §5).
// A process killed between the two, or an effect that errors, loses the
// effect with its key reserved, and a replay skips it. That is the right trade
// for a duplicate only if something reads the effect back and makes it again
// under a new key. For a claim nothing else would: the command looks
// unanswered for ever, and intake does not arm it again. For a hand-back
// nothing else would either: the job has come to rest, and it did not fail, so
// the operator is not told.
//
// So a decision that owes the tracker something does not move the job
// straight to where it decided. It records what it owes, with where the job
// goes next, performs it, and moves the job to a read-back state. The
// transition from there reads each thing back from the tracker on its own,
// makes whichever are missing again under the next round's key, and moves the
// job on once all of them are there.
package owed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/statefile"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// Tracker is what an owed item is made on and read back from. *github.Client
// is one.
type Tracker interface {
	Issue(ctx context.Context, number int) (github.Issue, error)
	Comments(ctx context.Context, number int) ([]github.Comment, error)
	Reactions(ctx context.Context, commentID int64) ([]github.Reaction, error)
	IssueReactions(ctx context.Context, number int) ([]github.Reaction, error)
	Comment(ctx context.Context, number int, body string) (github.Comment, error)
	React(ctx context.Context, commentID int64, content string) error
	ReactToIssue(ctx context.Context, number int, content string) error
	Label(ctx context.Context, number int, label string) error
}

// What an item is.
type what string

const (
	claimComment what = "claim-comment" // the agent's 👀 on a comment
	claimPR      what = "claim-pr"      // the agent's 👀 on a pull request's description
	comment      what = "comment"       // a comment of the agent's, carrying a marker
	label        what = "label"         // a label
)

// Item is one thing owed to the tracker. Made by the constructors below, and
// data rather than a closure, so that it can wait in the state directory for
// the read-back.
type Item struct {
	What what `json:"what"`

	// Stem is what the item's keys are made from: <stem>-<round>.
	Stem string `json:"stem"`

	// From is the round the item's allowance counts from. Set when the item
	// is owed, and moved past the rounds it spent when they run out, so the
	// next attempt at the job - a retry, or an operator freeing it - has an
	// allowance of its own. A key is still never used twice.
	From int `json:"from,omitempty"`

	// On is the issue or pull request the item is on.
	On int `json:"on,omitempty"`

	// Comment is the comment a claim is on.
	Comment int64 `json:"comment,omitempty"`

	// Marker is the hidden line a comment is read back by, and Body the
	// whole comment, marker included.
	Marker string `json:"marker,omitempty"`
	Body   string `json:"body,omitempty"`

	Label string `json:"label,omitempty"`
}

// Claim is the agent's 👀 on a command.
func Claim(c github.Comment) Item {
	return Item{What: claimComment, Stem: fmt.Sprintf("claim-comment-%d", c.ID), Comment: c.ID}
}

// ClaimPullRequest is the agent's 👀 on pull request n's description: the
// claim on a request the pull request itself made.
func ClaimPullRequest(n int) Item {
	return Item{What: claimPR, Stem: fmt.Sprintf("claim-pr-%d", n), On: n}
}

// Reply is a comment on issue or pull request n answering command c. It is
// read back by a marker naming c, so it is said once for each command.
func Reply(stem string, n int, c github.Comment, text string) Item {
	marker := fmt.Sprintf("<!-- afk:reply comment=%d -->", c.ID)
	return Comment(stem, n, marker, marker+"\n"+text)
}

// Comment is a comment on issue or pull request n, read back by marker, which
// body must carry and which nothing else the agent says there may.
func Comment(stem string, n int, marker, body string) Item {
	return Item{What: comment, Stem: stem, On: n, Marker: marker, Body: body}
}

// Label is a label on issue or pull request n.
func Label(stem string, n int, name string) Item {
	return Item{What: label, Stem: stem, On: n, Label: name}
}

func (it Item) valid() error {
	switch {
	case it.Stem == "":
		return errors.New("owed item has no key stem")
	case it.What == claimComment && it.Comment != 0:
	case it.What == claimPR && it.On > 0:
	case it.What == comment && it.On > 0 && it.Marker != "" && strings.Contains(it.Body, it.Marker):
	case it.What == label && it.On > 0 && it.Label != "":
	default:
		return fmt.Errorf("owed item %s is malformed", it.Stem)
	}
	return nil
}

// Record is what a decision owes, and where the job goes once it is all on
// the tracker.
type Record struct {
	// Next is the state the job moves to once every item is there, and Due
	// whether it is due there now or at rest.
	Next string `json:"next"`
	Due  bool   `json:"due"`

	Items []Item `json:"items"`
}

// Book is one job kind's way to its read-back. Built by the kind from its own
// dependencies; it holds nothing between calls but the files in Dir.
type Book struct {
	Tracker Tracker

	// Store is read, never written: it says which round of an item is next.
	Store store.Store

	// Login is the agent's own account: whose reaction is a claim, and
	// whose comment is the agent's.
	Login string

	// Rounds is how many times one item is made before an item that never
	// appears is an error: a failed attempt, which parks the job once the
	// retries are spent. There is nothing to hand back through - what is
	// owed is the claim, the reply, or the hand-back itself. A parameter.
	//
	// Each retry has rounds of its own (effects), so the bounds multiply: an
	// item is made up to Rounds times the attempt bound before the job
	// parks.
	Rounds int

	// Dir is where a record waits for its read-back - in the state
	// directory, beside the store and never in it (ADR 0001 §5).
	Dir string
}

// Unanswered is the commands for word among comments that the agent has not
// claimed.
func (b *Book) Unanswered(ctx context.Context, comments []github.Comment, word string) ([]github.Comment, error) {
	var out []github.Comment
	for _, c := range comments {
		if !intake.IsCommand(c, b.Login, word) {
			continue
		}
		reactions, err := b.Tracker.Reactions(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		if !intake.Claimed(reactions, b.Login) {
			out = append(out, c)
		}
	}
	return out, nil
}

// Owe is the result of a decision that owes the tracker r: the job moves to
// state, the read-back state, and r's items are its effects. A decision that
// owes nothing moves straight to r.Next.
//
// The record is written before the commit, so it is there for whichever
// process runs the read-back. A commit that then fails leaves a record behind
// that the next decision overwrites.
func (b *Book) Owe(ctx context.Context, in transition.In, state string, r Record) (transition.Result, error) {
	if r.Next == "" {
		return transition.Result{}, errors.New("owed record has no next state")
	}
	if len(r.Items) == 0 {
		return next(in, r), nil
	}
	for i, it := range r.Items {
		if err := it.valid(); err != nil {
			return transition.Result{}, err
		}
		from, err := transition.Next(ctx, b.Store, it.Stem)
		if err != nil {
			return transition.Result{}, err
		}
		r.Items[i].From = from
	}
	if err := b.save(in.Job.ID, r); err != nil {
		return transition.Result{}, err
	}
	effects, err := b.effects(ctx, in.Job.ID, r, r.Items)
	if err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: state, RunAt: in.Now, Effects: effects}, nil
}

// Settle is the read-back: done, and on to the record's next state, once
// every item is on the tracker, and round again for whichever are not.
//
// Each item is read back on its own, because the runner stops at the first
// effect that errors: a hand-back whose comment failed never applied its
// label either.
//
// lost is where the job goes if the record is not there - the state
// directory was wiped - which is the kind's to say.
func (b *Book) Settle(ctx context.Context, in transition.In, lost transition.Result) (transition.Result, error) {
	r, err := b.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return lost, nil
	}
	if err != nil {
		return transition.Result{}, err
	}
	missing, err := b.missing(ctx, r.Items)
	if err != nil {
		return transition.Result{}, err
	}
	if len(missing) == 0 {
		if err := os.Remove(b.path(in.Job.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return transition.Result{}, err
		}
		return next(in, r), nil
	}
	effects, err := b.effects(ctx, in.Job.ID, r, missing)
	if err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: in.Job.State, RunAt: in.Now, Effects: effects}, nil
}

func next(in transition.In, r Record) transition.Result {
	res := transition.Result{State: r.Next}
	if r.Due {
		res.RunAt = in.Now
	}
	return res
}

// effects is each item's effect, under the next round's key.
//
// An item whose rounds ran out is an error. Its allowance moves on in r first,
// so the attempt after this one makes it again rather than failing straight
// away: freeing a parked job is then enough to try once more.
func (b *Book) effects(ctx context.Context, jobID string, r Record, items []Item) ([]transition.Effect, error) {
	out := make([]transition.Effect, 0, len(items))
	var errs []error
	for _, it := range items {
		key, err := transition.Round(ctx, b.Store, it.Stem, it.From, b.Rounds)
		if spent, ok := transition.Spent(err); ok {
			for i := range r.Items {
				if r.Items[i].Stem == it.Stem {
					r.Items[i].From = spent.Next
				}
			}
			errs = append(errs, err)
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, transition.Effect{Key: key, Do: b.do(it)})
	}
	if len(errs) > 0 {
		return nil, errors.Join(append(errs, b.save(jobID, r))...)
	}
	return out, nil
}

func (b *Book) do(it Item) func(context.Context) error {
	switch it.What {
	case claimComment:
		return func(ctx context.Context) error { return b.Tracker.React(ctx, it.Comment, intake.Claim) }
	case claimPR:
		return func(ctx context.Context) error { return b.Tracker.ReactToIssue(ctx, it.On, intake.Claim) }
	case label:
		return func(ctx context.Context) error { return b.Tracker.Label(ctx, it.On, it.Label) }
	}
	return func(ctx context.Context) error {
		// The key stops this run posting twice. The tracker is what stops a
		// round that follows a slow success from posting again.
		comments, err := b.Tracker.Comments(ctx, it.On)
		if err != nil {
			return err
		}
		if b.said(comments, it.Marker) {
			return nil
		}
		_, err = b.Tracker.Comment(ctx, it.On, it.Body)
		return err
	}
}

// missing is the items the tracker does not show.
func (b *Book) missing(ctx context.Context, items []Item) ([]Item, error) {
	comments := map[int][]github.Comment{}
	labels := map[int][]string{}
	var out []Item
	for _, it := range items {
		there, err := b.there(ctx, it, comments, labels)
		if err != nil {
			return nil, err
		}
		if !there {
			out = append(out, it)
		}
	}
	return out, nil
}

// there reports whether an item is on the tracker. What each subject shows is
// read once per read-back, into comments and labels.
func (b *Book) there(ctx context.Context, it Item, comments map[int][]github.Comment, labels map[int][]string) (bool, error) {
	switch it.What {
	case claimComment:
		reactions, err := b.Tracker.Reactions(ctx, it.Comment)
		if gone(err) {
			// The command was deleted: there is nothing left to claim, and
			// nothing left to arm.
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return intake.Claimed(reactions, b.Login), nil
	case claimPR:
		reactions, err := b.Tracker.IssueReactions(ctx, it.On)
		if err != nil {
			return false, err
		}
		return intake.Claimed(reactions, b.Login), nil
	case comment:
		cs, ok := comments[it.On]
		if !ok {
			var err error
			if cs, err = b.Tracker.Comments(ctx, it.On); err != nil {
				return false, err
			}
			comments[it.On] = cs
		}
		return b.said(cs, it.Marker), nil
	case label:
		ls, ok := labels[it.On]
		if !ok {
			is, err := b.Tracker.Issue(ctx, it.On)
			if err != nil {
				return false, err
			}
			ls = is.Labels
			labels[it.On] = ls
		}
		for _, l := range ls {
			if strings.EqualFold(l, it.Label) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("owed item %s is malformed", it.Stem)
}

// said reports whether the agent has written a comment carrying marker.
func (b *Book) said(comments []github.Comment, marker string) bool {
	for _, c := range comments {
		if strings.EqualFold(c.Login, b.Login) && strings.Contains(c.Body, marker) {
			return true
		}
	}
	return false
}

func gone(err error) bool {
	var se *github.StatusError
	return errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusGone)
}

func (b *Book) path(jobID string) string {
	return filepath.Join(b.Dir, jobID+".json")
}

// save writes the record.
func (b *Book) save(jobID string, r Record) error {
	if b.Dir == "" {
		// The path would be relative to wherever the process is.
		return errors.New("owed has no directory to keep records in")
	}
	return statefile.Save(b.path(jobID), r)
}

func (b *Book) load(jobID string) (Record, error) {
	if b.Dir == "" {
		return Record{}, errors.New("owed has no directory to keep records in")
	}
	var r Record
	err := statefile.Load(b.path(jobID), &r)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, err
	}
	if err != nil {
		return Record{}, fmt.Errorf("what %s owes: %w", jobID, err)
	}
	if r.Next == "" || len(r.Items) == 0 {
		return Record{}, fmt.Errorf("what %s owes is incomplete", jobID)
	}
	for _, it := range r.Items {
		if err := it.valid(); err != nil {
			return Record{}, fmt.Errorf("what %s owes: %w", jobID, err)
		}
	}
	return r, nil
}
