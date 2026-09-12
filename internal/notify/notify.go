// Package notify is the operator's interrupt channel (ADR 0001 §13).
//
// Two conditions reach a person who is not watching: a job that has come to
// rest and needs them, and a budget that is spent. Nothing else. Not a pull
// request ready for review, not a job handed back, not a red CI run - those are
// states queried when the operator chooses to look, and notifying on them
// spends the one resource this agent exists to protect.
//
// That is why the two conditions are methods here rather than a general Send
// this package's callers compose messages for. The set of things that may
// interrupt an operator is the decision; a package with one Send has no set,
// and the next condition worth a log line arrives as a third notification
// nobody decided on.
//
// Everything here is best effort. A notification that cannot be published is a
// log line, and nothing downstream of it changes: by the time either condition
// fires, the park is in the store and the budget is readable with `afk budget`.
package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
	"github.com/corygyarmathy/afk-agent/internal/fetch"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// bodyLimit is the largest body published, in bytes. ntfy refuses a message
// over 4 KB, and a park's cause is the one part of either message with no
// bound on its length - a transition may return whatever error it likes. A
// notification truncated just under the cap is still a notification; one the
// server refused is not.
const bodyLimit = 3800

// Tags on the two conditions, as ntfy's emoji short names.
//
// Not parameters. A tag is how a phone tells the two apart at a glance without
// the message being read, which makes it part of what each notification is
// rather than a value someone would tune - and there are exactly two of them
// for as long as ADR 0001 §13 holds.
const (
	tagParked    = "octagonal_sign"
	tagExhausted = "money_with_wings"
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

	// Post publishes one message. Nil means HTTP to URL with Token. A field
	// rather than a package-level function because "the tests do not reach the
	// network" is enforced by scripts/offline-test.sh (AGENTS.md), and a seam
	// that has to be honoured is better than one that has to be remembered.
	Post func(ctx context.Context, title, tag, body string) error

	// Now is the clock, for tests. Nil means time.Now.
	Now func() time.Time

	mu sync.Mutex

	// sent is the conditions already published, by the key that identifies one
	// occurrence of one. It is how the same condition, re-observed on every
	// pass of every worker, is reported once.
	//
	// In memory, and nowhere else. The store holds run state (ADR 0001 §5,
	// §6), and its idempotency history is job-scoped by its schema - a budget
	// that is spent belongs to no job, and attaching it to whichever job a
	// worker happened to claim would be a record that says something untrue. A
	// restart therefore re-notifies a condition that is still true, which is
	// the right way round: the operator may not have seen the first one.
	sent map[string]time.Time
}

// Parked reports a job that has come to rest and will not move without an
// operator (CONTEXT.md: park). cause is what failed, or nil for a job parked
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

	body := fmt.Sprintf("%s is parked in state %q after %d attempt(s), on %s #%d.",
		job.ID, job.State, job.Attempts, job.Subject.Type, job.Subject.Number)
	if cause != nil {
		body += "\n\n" + cause.Error()
	}
	// No tracker link: this agent does not yet know which repository it is
	// working (#3), and a link built from a guess is worse than the subject
	// spelled out. Add it here when the repository becomes configuration.
	body += "\n\nIt stays there until an operator moves it; nothing will pick it up."

	return n.send(ctx, key, "afk-agent parked "+job.ID, tagParked, body)
}

// Exhausted reports a budget window the provider says is spent (ADR 0001 §11).
//
// Only an actual limit. Approaching one also stops new jobs starting, and is
// deliberately silent: the queue standing down for an hour as a rolling window
// fills is admission control working, and a notification for it would train the
// operator to ignore the channel that carries the other two.
func (n *Notifier) Exhausted(ctx context.Context, w budget.Window) error {
	// The reset timestamp is in the key, so the next time the same window is
	// spent is a new occurrence. Without it, one notification would cover every
	// future exhaustion of that window for the life of the process.
	key := fmt.Sprintf("exhausted:%s:%s", w.Name, w.ResetsAt.Format(time.RFC3339))

	body := w.String() + "\n\n"
	if w.ResetsAt.IsZero() {
		body += "New jobs are not starting. The provider reported no reset time, so the queue is looked at again each poll."
	} else {
		body += "New jobs are deferred until " + w.ResetsAt.Format(time.RFC3339) + "."
	}
	body += "\nWork already in flight is untouched, and `afk run` still works."

	return n.send(ctx, key, "afk-agent: the "+w.Name+" budget is spent", tagExhausted, body)
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
func (n *Notifier) send(ctx context.Context, key, title, tag, body string) error {
	if !n.reserve(key) {
		return nil
	}

	post := n.Post
	if post == nil {
		post = n.publish
	}
	if err := post(ctx, clean(title), tag, truncate(body)); err != nil {
		n.give(key)
		return fmt.Errorf("publishing %q: %w", title, err)
	}
	return nil
}

// reserve claims this occurrence, reporting false if it is already claimed.
func (n *Notifier) reserve(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, already := n.sent[key]; already {
		return false
	}
	if n.sent == nil {
		n.sent = map[string]time.Time{}
	}
	n.sent[key] = n.now()
	return true
}

// give releases a claim whose publish did not happen.
func (n *Notifier) give(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.sent, key)
}

// publish is the default Post: an ntfy publish, with the bearer token.
func (n *Notifier) publish(ctx context.Context, title, tag, body string) error {
	return fetch.Post(ctx, n.URL, n.Token, http.Header{
		"Title": {title},
		"Tags":  {tag},
	}, []byte(body))
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
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
