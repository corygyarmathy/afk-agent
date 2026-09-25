// Package github is the tracker client: what a transition reaches when it reads
// or writes the tracker.
//
// GitHub owns work state (ADR 0001 §5), so everything a transition knows about a
// pull request it reads through here, fresh, rather than from the store. Only
// what a transition needs is here; a method is added when a transition
// needs it, not in anticipation of one.
//
// It speaks REST over net/http rather than driving the gh binary, so the unit
// needs no second authenticated tool and the tests serve it from httptest.
// Loopback is not the network (scripts/offline-test.sh), and BaseURL is the
// seam.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultBaseURL is the API root a Client with no BaseURL talks to.
const DefaultBaseURL = "https://api.github.com"

// Media types. The API version is pinned so that a change upstream arrives as a
// deliberate bump here rather than as a different response one morning.
const (
	mediaJSON  = "application/vnd.github+json"
	mediaDiff  = "application/vnd.github.diff"
	apiVersion = "2022-11-28"
)

// perPage is the largest page the API serves. Listings are read to the end
// either way; this only decides how many requests that takes.
const perPage = 100

// Client is one repository on the tracker, and the credential that reaches it.
type Client struct {
	// Repo is the repository, spelled `owner/name`. A parameter, from
	// configuration.
	Repo string

	// Credential supplies the bearer token each request carries. Nil sends no
	// Authorization header.
	Credential Credential

	// BaseURL is the API root. Empty means DefaultBaseURL; a test points it at
	// an httptest server.
	BaseURL string

	// HTTP is the client requests go through. Nil means http.DefaultClient.
	HTTP *http.Client
}

// Credential is where a Client's token comes from. *App is one.
//
// A token never appears in an error: a failure names the endpoint and the
// status, and a 401 says the token is wrong without saying what it is.
type Credential interface {
	// Token returns the token to present now.
	Token(ctx context.Context) (string, error)

	// Refused reports that GitHub answered token with a 401.
	Refused(token string)
}

// PullRequest is the part of a pull request a transition decides on.
type PullRequest struct {
	Number int

	// State is `open` or `closed`, as the API spells it. A merged pull
	// request is closed.
	State string

	// HeadSHA is the commit the pull request currently points at. It is what
	// "reviewed at that head" means.
	HeadSHA string

	// HeadRef is the branch the pull request is from. The agent's own pull
	// requests are recognised by it.
	HeadRef string

	// Login is the author's account.
	Login string

	// Title and Body are the pull request's description, as its author wrote
	// it. Body is empty when there is none.
	Title string
	Body  string
}

// Issue is the part of an issue a transition reads: what it asks for, and
// whether it is still open.
type Issue struct {
	Number int

	// State is `open` or `closed`, as the API spells it.
	State string

	Title string
	Body  string

	// PullRequest reports that this issue is a pull request. The API serves
	// every pull request as an issue too, and says which it is.
	PullRequest bool
}

// Comment is one comment on a pull request's conversation.
type Comment struct {
	ID   int64
	Body string

	// Login is the author's account.
	Login string

	// Association is the author's relationship to the repository as the API
	// reports it - `OWNER`, `MEMBER`, `COLLABORATOR`, `CONTRIBUTOR`, `NONE` and
	// so on. What counts as enough to issue a command is intake's to decide,
	// not this package's.
	Association string
}

// StatusError is a response that was not a success. It names the request and
// the status and nothing else, which is how it never carries the token.
type StatusError struct {
	Method string
	URL    string
	Code   int
	Status string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: %s", e.Method, e.URL, e.Status)
}

// PullRequest reads one pull request.
func (c *Client) PullRequest(ctx context.Context, number int) (PullRequest, error) {
	u, err := c.repoURL("/pulls/%d", number)
	if err != nil {
		return PullRequest{}, err
	}
	var w wirePR
	if _, err := c.getJSON(ctx, u, &w); err != nil {
		return PullRequest{}, err
	}
	return w.pullRequest(), nil
}

// Issue reads one issue.
//
// A pull request is also an issue to the API, so a number that is a pull
// request reads as one rather than failing.
func (c *Client) Issue(ctx context.Context, number int) (Issue, error) {
	u, err := c.repoURL("/issues/%d", number)
	if err != nil {
		return Issue{}, err
	}
	var w wireIssue
	if _, err := c.getJSON(ctx, u, &w); err != nil {
		return Issue{}, err
	}
	return w.issue(), nil
}

// OpenIssues lists every open issue and every open pull request, to the last
// page: the API serves both from one listing, and Issue.PullRequest says which
// each is. It is how intake reads both kinds of subject a command can be on
// in one read.
func (c *Client) OpenIssues(ctx context.Context) ([]Issue, error) {
	u, err := c.repoURL("/issues?state=open&per_page=%d", perPage)
	if err != nil {
		return nil, err
	}
	ws, err := all[wireIssue](ctx, c, u)
	if err != nil {
		return nil, err
	}
	out := make([]Issue, len(ws))
	for i, w := range ws {
		out[i] = w.issue()
	}
	return out, nil
}

// OpenPullRequests lists every open pull request, to the last page.
func (c *Client) OpenPullRequests(ctx context.Context) ([]PullRequest, error) {
	u, err := c.repoURL("/pulls?state=open&per_page=%d", perPage)
	if err != nil {
		return nil, err
	}
	ws, err := all[wirePR](ctx, c, u)
	if err != nil {
		return nil, err
	}
	prs := make([]PullRequest, len(ws))
	for i, w := range ws {
		prs[i] = w.pullRequest()
	}
	return prs, nil
}

// Diff reads a pull request's diff against its base, as unified diff text.
func (c *Client) Diff(ctx context.Context, number int) (string, error) {
	u, err := c.repoURL("/pulls/%d", number)
	if err != nil {
		return "", err
	}
	resp, err := c.send(ctx, http.MethodGet, u, mediaDiff, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("GET %s: reading: %w", u, err)
	}
	return string(b), nil
}

// Comments lists every comment on a pull request's conversation, oldest first,
// to the last page.
//
// These are the conversation's comments - the ones a command is written in -
// and not review comments attached to lines of the diff, which the API keeps
// under a different resource.
func (c *Client) Comments(ctx context.Context, number int) ([]Comment, error) {
	u, err := c.repoURL("/issues/%d/comments?per_page=%d", number, perPage)
	if err != nil {
		return nil, err
	}
	ws, err := all[wireComment](ctx, c, u)
	if err != nil {
		return nil, err
	}
	cs := make([]Comment, len(ws))
	for i, w := range ws {
		cs[i] = w.comment()
	}
	return cs, nil
}

// Comment posts a comment on a pull request's conversation and returns it as
// created.
//
// Not idempotent, and it cannot be: the API has no key to deduplicate on. That
// is the runner's job, which is why a transition returns this as an effect
// rather than calling it.
func (c *Client) Comment(ctx context.Context, number int, body string) (Comment, error) {
	u, err := c.repoURL("/issues/%d/comments", number)
	if err != nil {
		return Comment{}, err
	}
	var w wireComment
	if err := c.postJSON(ctx, u, map[string]string{"body": body}, &w); err != nil {
		return Comment{}, err
	}
	return w.comment(), nil
}

// React adds a reaction to a conversation comment. Content is the API's
// spelling of the reaction: `eyes`, `+1`, and so on.
//
// Reacting twice with the same content is not an error; the API answers the
// second with the reaction that already exists.
func (c *Client) React(ctx context.Context, commentID int64, content string) error {
	u, err := c.repoURL("/issues/comments/%d/reactions", commentID)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, u, map[string]string{"content": content}, nil)
}

// Label adds a label to an issue or a pull request. Adding a label it already
// has is not an error, and a label the repository does not have yet is
// created.
func (c *Client) Label(ctx context.Context, number int, label string) error {
	u, err := c.repoURL("/issues/%d/labels", number)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, u, map[string][]string{"labels": {label}}, nil)
}

type wirePR struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Head   struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (w wirePR) pullRequest() PullRequest {
	return PullRequest{
		Number: w.Number, State: w.State, HeadSHA: w.Head.SHA, HeadRef: w.Head.Ref,
		Login: w.User.Login, Title: w.Title, Body: w.Body,
	}
}

type wireIssue struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Body   string `json:"body"`

	// PullRequest is present, and its contents are links, only on an issue
	// that is a pull request.
	PullRequest *struct{} `json:"pull_request"`
}

func (w wireIssue) issue() Issue {
	return Issue{Number: w.Number, State: w.State, Title: w.Title, Body: w.Body, PullRequest: w.PullRequest != nil}
}

type wireComment struct {
	ID                int64  `json:"id"`
	Body              string `json:"body"`
	AuthorAssociation string `json:"author_association"`
	User              struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (w wireComment) comment() Comment {
	return Comment{ID: w.ID, Body: w.Body, Login: w.User.Login, Association: w.AuthorAssociation}
}

// all reads a listing page by page, following the Link header to the end.
func all[T any](ctx context.Context, c *Client, u string) ([]T, error) {
	var out []T
	for u != "" {
		var page []T
		next, err := c.getJSON(ctx, u, &page)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		u = next
	}
	return out, nil
}

// getJSON reads one document, and returns the next page's URL if the response
// has one.
func (c *Client) getJSON(ctx context.Context, u string, v any) (string, error) {
	resp, err := c.send(ctx, http.MethodGet, u, mediaJSON, nil)
	if err != nil {
		return "", err
	}
	if err := readJSON(resp, http.MethodGet, u, v); err != nil {
		return "", err
	}
	return c.next(resp.Header.Get("Link"))
}

// postJSON sends payload and decodes the response into v, unless v is nil.
func (c *Client) postJSON(ctx context.Context, u string, payload, v any) error {
	resp, err := c.send(ctx, http.MethodPost, u, mediaJSON, payload)
	if err != nil {
		return err
	}
	if v == nil {
		drain(resp.Body)
		return nil
	}
	return readJSON(resp, http.MethodPost, u, v)
}

// readJSON decodes a response's document into v, and closes the body.
func readJSON(resp *http.Response, method, u string, v any) error {
	defer drain(resp.Body)
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("%s %s: decoding: %w", method, u, err)
	}
	return nil
}

// send makes one request with the credential's token.
//
// A 401 is reported to the credential, so a revoked token is not presented
// again. The request is not retried here: a 401 means it was not performed, and
// whether to try again is the transition's to decide.
func (c *Client) send(ctx context.Context, method, u, accept string, payload any) (*http.Response, error) {
	var token string
	if c.Credential != nil {
		var err error
		if token, err = c.Credential.Token(ctx); err != nil {
			return nil, fmt.Errorf("%s %s: no token: %w", method, u, err)
		}
	}
	resp, err := request(ctx, c.HTTP, method, u, accept, token, payload)
	if se := (*StatusError)(nil); errors.As(err, &se) && se.Code == http.StatusUnauthorized && c.Credential != nil {
		c.Credential.Refused(token)
	}
	return resp, err
}

// request makes one request, bearing token unless it is empty. A response
// outside 2xx is a StatusError, and its body is closed here: an error page is
// not a document, and the status is the part worth reporting.
func request(ctx context.Context, httpc *http.Client, method, u, accept, token string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		drain(resp.Body)
		return nil, &StatusError{Method: method, URL: u, Code: resp.StatusCode, Status: resp.Status}
	}
	return resp, nil
}

// next returns the rel="next" target of a Link header, or "" on the last page.
//
// A next page that is not under the API root is refused rather than followed.
// The token goes with every request, and where it goes should be decided by
// configuration and not by a response header.
func (c *Client) next(link string) (string, error) {
	for _, part := range strings.Split(link, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		u := strings.Trim(strings.TrimSpace(target), "<>")
		if !strings.HasPrefix(u, c.base()+"/") {
			return "", fmt.Errorf("next page %s is not under %s; refusing to send the token there", u, c.base())
		}
		return u, nil
	}
	return "", nil
}

// repoURL is an endpoint under the repository, or an error if Repo is not
// `owner/name` - reported before anything is sent.
func (c *Client) repoURL(format string, a ...any) (string, error) {
	if _, _, ok := splitRepo(c.Repo); !ok {
		return "", fmt.Errorf("repository %q is not owner/name", c.Repo)
	}
	return c.base() + "/repos/" + c.Repo + fmt.Sprintf(format, a...), nil
}

func (c *Client) base() string {
	return baseURL(c.BaseURL)
}

// splitRepo splits `owner/name`, and reports whether repo is spelled that way.
func splitRepo(repo string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(repo, "/")
	return owner, name, ok && owner != "" && name != "" && !strings.Contains(name, "/")
}

// baseURL is the API root u names, or DefaultBaseURL if it is empty.
func baseURL(u string) string {
	if u == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(u, "/")
}

// drain reads what is left of a body before closing it, so the connection goes
// back to the pool rather than being torn down.
func drain(rc io.ReadCloser) {
	io.Copy(io.Discard, rc)
	rc.Close()
}

// Reaction is one account's reaction to a comment.
type Reaction struct {
	Login string

	// Content is the API's spelling of the reaction: `eyes`, `+1`, and so on.
	Content string
}

// Reactions lists every reaction to a conversation comment, to the last page.
//
// It is how a command reads as answered: the agent's own reaction is the
// claim, and it lives on the tracker rather than in the store, so a wiped
// store does not make an old command look new.
func (c *Client) Reactions(ctx context.Context, commentID int64) ([]Reaction, error) {
	u, err := c.repoURL("/issues/comments/%d/reactions?per_page=%d", commentID, perPage)
	if err != nil {
		return nil, err
	}
	ws, err := all[wireReaction](ctx, c, u)
	if err != nil {
		return nil, err
	}
	rs := make([]Reaction, len(ws))
	for i, w := range ws {
		rs[i] = Reaction{Login: w.User.Login, Content: w.Content}
	}
	return rs, nil
}

type wireReaction struct {
	Content string `json:"content"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
}
