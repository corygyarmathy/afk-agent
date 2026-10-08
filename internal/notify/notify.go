// Package notify is the operator's interrupt channel (ADR 0001 §13).
//
// Four conditions reach a person who is not watching: a job that has come to
// rest and needs them, a budget that is spent, a job whose model tier stays
// exhausted (ADR 0001 §10), and the first job admitted under a budget waiver.
// Nothing else. Not a pull request ready for
// review, not a job handed back, not a red CI run - those are states queried
// when the operator chooses to look, and notifying on them spends the one
// resource this agent exists to protect.
//
// That is why the conditions are methods here rather than a general Send
// this package's callers compose messages for. The set of things that may
// interrupt an operator is the decision; a package with one Send has no set,
// and the next condition worth a log line arrives as a notification nobody
// decided on.
//
// Everything here is best effort. A notification that cannot be published is a
// log line, and nothing downstream of it changes: by the time any condition
// fires, the park or the deferral is in the store and the budget is readable
// with `afk budget`.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
	"github.com/corygyarmathy/afk-agent/internal/fetch"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// bodyLimit is the largest body published, in bytes. ntfy refuses a message
// over 4 KB, and a park's cause is the one part of any message with no
// bound on its length - a transition may return whatever error it likes. A
// notification truncated just under the cap is still a notification; one the
// server refused is not.
const bodyLimit = 3800

// Tags on the conditions, as ntfy's emoji short names.
//
// Not parameters. A tag is how a phone tells the conditions apart at a glance
// without the message being read, which makes it part of what each
// notification is rather than a value someone would tune - and there is one
// per condition, for as long as the set of conditions is the one above.
const (
	tagParked    = "octagonal_sign"
	tagExhausted = "money_with_wings"
	tagTier      = "hourglass"
	tagWaived    = "moneybag"
)

// Notifier publishes to ntfy.
//
// The server, the topic and the token are configuration and arrive as the URL
// and Token below; this agent has no opinion about which ntfy instance it
// reaches, and holds no default for one (AGENTS.md).
type Notifier struct {
	// URL is the topic to publish to, topic included:
	// https://ntfy.example/afk-agent. One parameter rather than a server and a
	// topic, because ntfy's publish endpoint is that URL and splitting it here
	// would mean rejoining it before every request.
	URL string

	// Token is the bearer token. It never appears in a log line or an error:
	// Post reports the URL and the status, and a 401 says the token is wrong
	// without saying what it is.
	Token string

	// Repo is the tracker's repository, owner/name, which a notification about
	// a job links its subject in. Empty means no link: the subject is still
	// spelled out, and `afk work` with no repository has no tracker for one to
	// point at.
	Repo string

	// Post publishes one message. Nil means HTTP to URL with Token. A field
	// rather than a package-level function because "the tests do not reach the
	// network" is enforced by scripts/offline-test.sh (AGENTS.md), and a seam
	// that has to be honoured is better than one that has to be remembered.
	Post func(ctx context.Context, m Message) error

	// TierAfter is how many times one episode of an exhausted tier defers a
	// job before the operator is told: see TierExhausted. A parameter, and at
	// least one; the dispatcher refuses a notifier without it.
	TierAfter int

	mu sync.Mutex

	// sent is the conditions already published, by the key that identifies one
	// occurrence of one. It is how the same condition, re-observed on every
	// pass of every worker, is reported once.
	//
	// A set, and nothing is ever taken out of it except a publish that failed.
	// There is deliberately no expiry: a key that aged out would re-publish a
	// condition nobody has fixed, which is the poll-that-suppresses shape this
	// package exists to avoid, and an occurrence that is genuinely new already
	// has a new key. It grows with the number of distinct occurrences one
	// process lives through, which is bounded by the work the queue actually
	// did.
	//
	// In memory, and nowhere else. The store holds run state (ADR 0001 §5,
	// §6), and its idempotency history is job-scoped by its schema - a budget
	// that is spent belongs to no job, and attaching it to whichever job a
	// worker happened to claim would be a record that says something untrue. A
	// restart therefore re-notifies a condition that is still true, which is
	// the right way round: the operator may not have seen the first one.
	sent map[string]struct{}
}

// Message is one notification, as Post publishes it.
type Message struct {
	Title string
	Tag   string
	Body  string

	// Click is where tapping the notification goes, or empty for nowhere in
	// particular. A notification about a job carries its subject's link here
	// and in Body both: here because a tap is what an operator does with a
	// notification on a phone, and in Body because not every client honours a
	// click action, and the message read anywhere else - ntfy's web view, an
	// email forward - should still say where the work is.
	Click string
}

// Parked reports a job that has come to rest and will not move without an
// operator (GLOSSARY.md: park). cause is what failed, or nil for a job parked
// with nothing wrong - a state no transition leads out of.
//
// This is the failure ADR 0001 §13 reserves the channel for. A deferral is not
// one and does not arrive here: a deferred job comes back on its own.
func (n *Notifier) Parked(ctx context.Context, job store.Job, cause error) error {
	// Attempts are in the key, so a job an operator freed and which parked
	// again is a new occurrence rather than one this process has already
	// reported. A transition that moves resets the count (Runner.apply), so
	// the key changes as soon as the job has got anywhere.
	key := fmt.Sprintf("parked:%s:%s:%d", job.ID, job.State, job.Attempts)

	link := n.link(job.Subject)
	body := fmt.Sprintf("%s is parked in state %q after %d attempt(s), on %s #%d.",
		job.ID, job.State, job.Attempts, job.Subject.Type, job.Subject.Number)
	body += paragraph(link)
	if cause != nil {
		body += "\n\n" + cause.Error()
	}
	body += "\n\nIt stays there until an operator moves it; nothing will pick it up."

	return n.send(ctx, key, Message{Title: "afk-agent parked " + job.ID, Tag: tagParked, Body: body, Click: link})
}

// Exhausted reports a budget window the provider says is spent (ADR 0001 §11).
//
// Only an actual limit. Approaching one also stops new jobs starting, and is
// deliberately silent: the queue standing down for an hour as a rolling window
// fills is admission control working, and a notification for it would train the
// operator to ignore the channel that carries the others.
func (n *Notifier) Exhausted(ctx context.Context, w budget.Window) error {
	// The reset timestamp is in the key, so the next time the same window is
	// spent is a new occurrence. Without it, one notification would cover every
	// future exhaustion of that window for the life of the process.
	//
	// Two edges this does not handle, both narrow and both left alone:
	//
	//   - A limit the provider reports with no resetsAt has the zero time in
	//     its key, which is stable, so it is the case the timestamp was meant
	//     to fix and does not. The pool is waiting rather than deferring there
	//     and re-asks every poll, so the condition is not lost - only the
	//     second notification is.
	//   - Admission names the window that reopens last while it can defer, and
	//     the worst one once the timestamp has passed with the account still
	//     limited. Those can be different windows, and a second name is a
	//     second key. One spent window can therefore publish twice.
	key := fmt.Sprintf("exhausted:%s:%s", w.Name, w.ResetsAt.Format(time.RFC3339))

	body := w.String() + "\n\n"
	if w.ResetsAt.IsZero() {
		body += "New jobs are not starting. The provider reported no reset time, so the queue is looked at again each poll."
	} else {
		body += "New jobs are deferred until " + w.ResetsAt.Format(time.RFC3339) + "."
	}
	body += "\nWork already in flight is untouched, and `afk run` still works."

	return n.send(ctx, key, Message{Title: "afk-agent: the " + w.Name + " budget is spent", Tag: tagExhausted, Body: body})
}

// Waived reports work starting through a spent window the operator waived (ADR
// 0001 §11, §13).
//
// The fourth condition, and the one that needs no action: the operator set the
// waiver, and this is their confirmation that work is now spending the
// pay-as-you-go balance. It is worth the interruption because the endpoint
// cannot show whether the provider's fallback to the balance is on, and a
// waiver set with it off fails every run as a transient failure - so hearing
// sooner is the whole point.
func (n *Notifier) Waived(ctx context.Context, wa budget.Waiver) error {
	// The waiver's timestamp is in the key, so a waiver renewed for the next
	// period is a new occurrence. One occurrence is once per waiver, per
	// process: the pool's workers re-observe the spent window on every pass,
	// and the first job admitted under it is the thing worth saying.
	key := fmt.Sprintf("waived:%s:%s", wa.Window, wa.Until.Format(time.RFC3339))

	body := fmt.Sprintf("The operator waived the %s window until %s, so a spent %s window is not stopping work: new jobs are starting and spending the account's balance.",
		wa.Window, wa.Until.Format(time.RFC3339), wa.Window)
	body += "\n\nThis needs no action. A spent " + wa.Window + " window defers work again once the waiver lapses."
	body += "\n\nWork already in flight is untouched, and `afk run` still works."

	return n.send(ctx, key, Message{Title: "afk-agent: the " + wa.Window + " budget is waived", Tag: tagWaived, Body: body})
}

// TierExhausted reports a job whose model tier has run out and keeps running
// out (ADR 0001 §10: exhausting a tier is a human-facing event). cause is what
// the tier said the last time - with what the last candidate's run failed
// with, when the run that ran the tier out saw it - and job is as committed,
// deferred.
//
// Once per episode, and only once the episode reaches TierAfter. A tier that
// ran out once and came back on the next try is a bad few minutes at a
// provider, which §10 says must not generate work for the operator; one that
// is still out after TierAfter tries is enrolled models the provider does not
// know, or an outage longer than the agent can wait out, and neither ends
// without somebody looking. Each resume tries the whole tier again, so without
// this the job defers and resumes indefinitely and the only sign is a job in
// the store that is always deferred.
//
// It reports whether the episode has been told, by this call or an earlier
// one, for the pool to record in the store: an episode told is not told again
// by the next process to count it (#91).
func (n *Notifier) TierExhausted(ctx context.Context, job store.Job, ep store.Episode, cause error) (bool, error) {
	if ep.Told {
		return true, nil
	}
	if ep.Times < n.TierAfter {
		return false, nil
	}
	// The episode's start is in the key, so a job that got past the model and
	// later ran out again is a new occurrence, and one whose tier stays out is
	// told once rather than once every tier wait. As an instant rather than a
	// formatted time: the same start read back from the store is in another
	// location, and must not be another occurrence.
	key := fmt.Sprintf("tier:%s:%d", job.ID, ep.Since.UnixNano())

	link := n.link(job.Subject)
	body := fmt.Sprintf("%s, on %s #%d, has run out of models %d times since %s.",
		job.ID, job.Subject.Type, job.Subject.Number, ep.Times, ep.Since.UTC().Format(time.RFC3339))
	body += paragraph(link)
	body += "\n\nIt is deferred"
	if !job.NextRunAt.IsZero() {
		body += " until " + job.NextRunAt.Format(time.RFC3339)
	}
	body += " and will try the tier again, and keeps doing so until a model answers. "
	// The last run's failure is the only one carried here (#98). A run killed
	// at its bound is said outright, because the bound is the one cause the
	// operator set, and a bound too short for the work fails every candidate
	// the same way.
	var transient *opencode.TransientError
	if errors.As(cause, &transient) && transient.Bound > 0 {
		body += fmt.Sprintf("The last candidate's run was killed at its bound, --model-timeout (%s). "+
			"If every run is, the bound is too short for the work, and the tier stays exhausted until --model-timeout is raised. ", transient.Bound)
	} else {
		body += "Every enrolled candidate failed transiently: the enrolment may name models the provider does not know, the provider is down, " +
			"or every run is outlasting --model-timeout. "
	}
	body += "This is not repeated while the tier stays exhausted."
	// The cause last: it carries the run's stderr, and a body over the limit is
	// cut at the end.
	if cause != nil {
		body += "\n\n" + cause.Error()
	}

	if err := n.send(ctx, key, Message{Title: "afk-agent: " + job.ID + " cannot reach a model", Tag: tagTier, Body: body, Click: link}); err != nil {
		return false, err
	}
	return true, nil
}

// send publishes a message unless this occurrence has already been published.
//
// The key is taken before the publish and given back if it failed, rather than
// recorded after a publish that worked. Both properties this needs follow from
// that ordering, and neither follows from the other one:
//
//   - Several workers observing the same condition at the same moment publish
//     once between them. The pool is the caller, and "the budget is spent" is
//     something every worker sees on the same pass; checking and then recording
//     would let all of them through the gap.
//   - A publish that failed reported nothing, so it does not suppress the next
//     attempt. A channel that cannot be reached keeps trying and keeps logging,
//     which is the visible symptom a broken notifier should have - suppressing
//     on the attempt would turn an unreachable ntfy into a silent one, and
//     silence is what this channel says when nothing is wrong.
//
// The lock is not held across the publish. A notification is best effort and
// the server is on the other side of a network; a worker that has nothing to
// report should not wait behind one that is talking to it.
func (n *Notifier) send(ctx context.Context, key string, m Message) error {
	if !n.reserve(key) {
		return nil
	}

	post := n.Post
	if post == nil {
		post = n.publish
	}
	title := m.Title
	m.Title, m.Click, m.Body = clean(m.Title), clean(m.Click), truncate(m.Body)
	if err := post(ctx, m); err != nil {
		n.give(key)
		return fmt.Errorf("publishing %q: %w", title, err)
	}
	return nil
}

// link is a subject's page on the tracker, or empty with no repository.
//
// Built rather than read back from GitHub: the repository is configuration,
// the subject's type and number are the job's, and the URL is GitHub's, which
// is the one tracker this agent works. A notification is the last place to
// spend a request that can fail.
func (n *Notifier) link(s store.Subject) string {
	if n.Repo == "" {
		return ""
	}
	path := "issues"
	if s.Type == store.SubjectPR {
		path = "pull"
	}
	return fmt.Sprintf("https://github.com/%s/%s/%d", n.Repo, path, s.Number)
}

// paragraph is s as a paragraph of its own, or nothing for an empty s. A link
// is on a line by itself so a client that linkifies the body does not take the
// sentence's full stop with it, and before the cause so the length limit,
// which cuts the end, never reaches it.
func paragraph(s string) string {
	if s == "" {
		return ""
	}
	return "\n\n" + s
}

// reserve claims this occurrence, reporting false if it is already claimed.
func (n *Notifier) reserve(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, already := n.sent[key]; already {
		return false
	}
	if n.sent == nil {
		n.sent = map[string]struct{}{}
	}
	n.sent[key] = struct{}{}
	return true
}

// give releases a claim whose publish did not happen.
func (n *Notifier) give(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.sent, key)
}

// publish is the default Post: an ntfy publish, with the bearer token.
func (n *Notifier) publish(ctx context.Context, m Message) error {
	h := http.Header{
		"Title": {m.Title},
		"Tags":  {m.Tag},
	}
	if m.Click != "" {
		h.Set("Click", m.Click)
	}
	return fetch.Post(ctx, n.URL, n.Token, h, []byte(m.Body))
}

// clean makes a string safe to carry in a header. A newline in a title is a
// request ntfy rejects, and a job id or a state name is not this agent's to
// trust: both are strings a transition or an operator chose.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

// truncate bounds a body at what the server will accept, cutting on a rune
// boundary so the message stays valid UTF-8.
func truncate(s string) string {
	if len(s) <= bodyLimit {
		return s
	}
	cut := bodyLimit
	for cut > 0 && !runeStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n(truncated)"
}

// runeStart reports whether b begins a rune rather than continuing one.
func runeStart(b byte) bool { return b&0xC0 != 0x80 }
