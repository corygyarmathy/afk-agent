// Package work is the machinery both job kinds share: a job's checkout and the
// progress that describes it, the relay the agent pushes from, the local gate
// and its retries, the denylist, the leased push, the CI watch and its fixes
// (#147), and the hand-back on a pull request (#146).
//
// It exists so that `/implement` and `/revise` run one copy of each rather than
// two that drift. What is kind-specific stays with the kind: the words a
// hand-back says, the branch a workspace starts on, and the states the job
// moves between.
package work

import "time"

// Progress is the state of one job's workspace that outlives a transition:
// where the checkout is, what it has pushed, and how the gate has fared. A kind
// embeds it in its own progress, which adds what that kind alone needs.
type Progress struct {
	// Nonce is made with the workspace, and keys what is said about the work
	// until the job comes to rest. The branch cannot: nothing pushed means
	// its name is free again, and the next workspace takes it.
	Nonce string `json:"nonce"`

	// Branch is the branch the workspace is on. Base is the commit the work
	// started from, and Into the branch it started from, which a pull
	// request asks to merge into: the default branch for new work, and the
	// pull request's base branch for a revision.
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Into   string `json:"into"`

	// Head is the commit the push was decided for: checked against the
	// denylist, and what the remote's branch must be at once it lands.
	Head string `json:"head,omitempty"`

	// Pushed is the commit the agent last saw its own push land at on the
	// remote's branch, and the lease every later push is pinned to. Empty
	// until the first push is seen, when the branch must not exist yet.
	Pushed string `json:"pushed,omitempty"`

	// PushedAt is when that push was seen, which the CI ceiling runs from.
	PushedAt time.Time `json:"pushed_at,omitzero"`

	// Session is the opencode session that wrote the branch's commits, to
	// continue with a failure. Empty until a run succeeds.
	Session string `json:"session,omitempty"`

	// Attempts is how many times the gate has failed on this branch.
	Attempts int `json:"attempts"`

	// Failure is what the gate said the last time it failed, and Why is
	// its one-line summary. Both empty while the branch has not failed.
	Failure string `json:"failure,omitempty"`
	Why     string `json:"why,omitempty"`

	// Fixes is how many times CI has sent the work back to the session,
	// and FixedHead the head the last of them was counted for.
	Fixes     int    `json:"fixes,omitempty"`
	FixedHead string `json:"fixed_head,omitempty"`
}

// Complete reports whether p describes a workspace: the fields a checkout is
// named and made with. A progress missing one of them is not one anything can
// be done from.
func (p Progress) Complete() bool {
	return p.Nonce != "" && p.Branch != "" && p.Base != "" && p.Into != ""
}
