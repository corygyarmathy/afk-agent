// Package github is the tracker client: what a transition reaches when it reads
// or writes the tracker.
//
// GitHub owns work state (ADR 0001 §5), so everything a transition knows about a
// pull request it reads through here, fresh, rather than from the store. Only
// what the /review slice needs is here; a method is added when a transition
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

	// Token is the bearer token. It never appears in an error: a failure names
	// the endpoint and the status, and a 401 says the token is wrong without
	// saying what it is. Empty sends no Authorization header.
	Token string

	// BaseURL is the API root. Empty means DefaultBaseURL; a test points it at
	// an httptest server.
	BaseURL string

	// HTTP is the client requests go through. Nil means http.DefaultClient.
	HTTP *http.Client
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

// Login returns the account the token authenticates as - the agent's own, which
// is how it recognises the comments it wrote.
func (c *Client) Login(ctx context.Context) (string, error) {
	var w struct {
		Login string `json:"login"`
	}
	if _, err := c.getJSON(ctx, c.base()+"/user", &w); err != nil {
		return "", err
	}
	if w.Login == "" {
		return "", fmt.Errorf("GET %s/user: no login in the response", c.base())
	}
	return w.Login, nil
}

type wirePR struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func (w wirePR) pullRequest() PullRequest {
	return PullRequest{Number: w.Number, State: w.State, HeadSHA: w.Head.SHA}
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
	defer drain(resp.Body)
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return "", fmt.Errorf("GET %s: decoding: %w", u, err)
	}
	return c.next(resp.Header.Get("Link"))
}

// postJSON sends payload and decodes the response into v, unless v is nil.
func (c *Client) postJSON(ctx context.Context, u string, payload, v any) error {
	resp, err := c.send(ctx, http.MethodPost, u, mediaJSON, payload)
	if err != nil {
		return err
	}
	defer drain(resp.Body)
	if v == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("POST %s: decoding: %w", u, err)
	}
	return nil
}

// send makes one request. A response outside 2xx is a StatusError, and its body
// is closed here: an error page is not a document, and the status is the part
// worth reporting.
func (c *Client) send(ctx context.Context, method, u, accept string, payload any) (*http.Response, error) {
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
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	httpc := c.HTTP
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
	owner, name, ok := strings.Cut(c.Repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("repository %q is not owner/name", c.Repo)
	}
	return c.base() + "/repos/" + c.Repo + fmt.Sprintf(format, a...), nil
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

// drain reads what is left of a body before closing it, so the connection goes
// back to the pool rather than being torn down.
func drain(rc io.ReadCloser) {
	io.Copy(io.Discard, rc)
	rc.Close()
}
