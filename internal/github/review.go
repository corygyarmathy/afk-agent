package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Review is one review submitted on a pull request: its summary body, and not
// the line comments that came with it (ReviewComments).
type Review struct {
	ID int64

	// NodeID is the review's GraphQL id. A reaction to a review is only
	// reachable through GraphQL, so it is how the review is claimed.
	NodeID string

	Body string

	// Login and Association are the author's account and relationship to the
	// repository, as for a Comment.
	Login       string
	Association string

	// State is the API's spelling of what the review says: `APPROVED`,
	// `CHANGES_REQUESTED`, `COMMENTED`, `DISMISSED`, or `PENDING` for one not
	// yet submitted.
	State string

	// SubmittedAt is when it was submitted. Zero for a pending review.
	SubmittedAt time.Time

	// URL is the review on the pull request's page.
	URL string
}

// ReviewComment is one line comment a review carries.
type ReviewComment struct {
	ID   int64
	Body string

	// Path is the file it is on, and Line the line: in the pull request's
	// latest version, or the one it was written on when the code has since
	// moved on. Zero is a comment on the whole file.
	Path string
	Line int

	// URL is the comment on the pull request's page.
	URL string
}

// Reviews lists every review on a pull request, oldest first, to the last
// page.
func (c *Client) Reviews(ctx context.Context, number int) ([]Review, error) {
	u, err := c.repoURL("/pulls/%d/reviews?per_page=%d", number, perPage)
	if err != nil {
		return nil, err
	}
	ws, err := all[wireReview](ctx, c, u)
	if err != nil {
		return nil, err
	}
	rs := make([]Review, len(ws))
	for i, w := range ws {
		rs[i] = Review{ID: w.ID, NodeID: w.NodeID, Body: w.Body, Login: w.User.Login, Association: w.AuthorAssociation, State: w.State, SubmittedAt: w.SubmittedAt, URL: w.HTMLURL}
	}
	return rs, nil
}

// ReviewComments lists the line comments one review carries, in the order
// the API serves them, to the last page. A line comment outside that review is
// not among them.
func (c *Client) ReviewComments(ctx context.Context, number int, review int64) ([]ReviewComment, error) {
	u, err := c.repoURL("/pulls/%d/reviews/%d/comments?per_page=%d", number, review, perPage)
	if err != nil {
		return nil, err
	}
	ws, err := all[wireReviewComment](ctx, c, u)
	if err != nil {
		return nil, err
	}
	cs := make([]ReviewComment, len(ws))
	for i, w := range ws {
		line := w.Line
		if line == 0 {
			line = w.OriginalLine
		}
		cs[i] = ReviewComment{ID: w.ID, Body: w.Body, Path: w.Path, Line: line, URL: w.HTMLURL}
	}
	return cs, nil
}

// ReviewReactions lists every reaction to a review, by its GraphQL id. A
// review that is not there is a StatusError with code 404, as a comment that
// is not there is.
func (c *Client) ReviewReactions(ctx context.Context, nodeID string) ([]Reaction, error) {
	const query = `query($id: ID!, $after: String) {
  node(id: $id) {
    ... on PullRequestReview {
      reactions(first: 100, after: $after) {
        nodes { content user { login } }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`
	var (
		out   []Reaction
		after *string
	)
	for {
		var data struct {
			Node *struct {
				Reactions struct {
					Nodes []struct {
						Content string `json:"content"`
						User    *struct {
							Login string `json:"login"`
						} `json:"user"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"reactions"`
			} `json:"node"`
		}
		if err := c.graphql(ctx, query, map[string]any{"id": nodeID, "after": after}, &data); err != nil {
			return nil, err
		}
		if data.Node == nil {
			return nil, &StatusError{Method: http.MethodPost, URL: c.graphqlURL(), Code: http.StatusNotFound, Status: "review " + nodeID + " not found"}
		}
		for _, n := range data.Node.Reactions.Nodes {
			r := Reaction{Content: restContent(n.Content)}
			if n.User != nil {
				r.Login = n.User.Login
			}
			out = append(out, r)
		}
		page := data.Node.Reactions.PageInfo
		if !page.HasNextPage {
			return out, nil
		}
		after = &page.EndCursor
	}
}

// ReactToReview adds a reaction to a review, by its GraphQL id. Content is
// REST's spelling, as for React. Reacting twice with the same content is not
// an error.
func (c *Client) ReactToReview(ctx context.Context, nodeID, content string) error {
	const mutation = `mutation($id: ID!, $content: ReactionContent!) {
  addReaction(input: {subjectId: $id, content: $content}) { reaction { content } }
}`
	gql, ok := graphqlContent[content]
	if !ok {
		return fmt.Errorf("reaction %q has no GraphQL spelling", content)
	}
	var data struct{}
	return c.graphql(ctx, mutation, map[string]any{"id": nodeID, "content": gql}, &data)
}

// graphqlContent is each reaction's GraphQL spelling, by its REST one.
var graphqlContent = map[string]string{
	"+1": "THUMBS_UP", "-1": "THUMBS_DOWN", "laugh": "LAUGH", "hooray": "HOORAY",
	"confused": "CONFUSED", "heart": "HEART", "rocket": "ROCKET", "eyes": "EYES",
}

// restContent is a reaction's REST spelling, from its GraphQL one. One this
// package does not know is kept as GraphQL spells it, lower-cased, so it reads
// as nobody's claim rather than as an error.
func restContent(gql string) string {
	for rest, g := range graphqlContent {
		if g == gql {
			return rest
		}
	}
	return strings.ToLower(gql)
}

// graphql sends one GraphQL document and decodes its data into v. An answer
// carrying errors is an error, naming the endpoint and the first message; a
// NOT_FOUND one is a StatusError with code 404.
func (c *Client) graphql(ctx context.Context, query string, variables map[string]any, v any) error {
	u := c.graphqlURL()
	var out struct {
		Data   any `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	out.Data = v
	if err := c.postJSON(ctx, u, map[string]any{"query": query, "variables": variables}, &out); err != nil {
		return err
	}
	if len(out.Errors) == 0 {
		return nil
	}
	e := out.Errors[0]
	if e.Type == "NOT_FOUND" {
		return &StatusError{Method: http.MethodPost, URL: u, Code: http.StatusNotFound, Status: e.Message}
	}
	return fmt.Errorf("POST %s: %s", u, e.Message)
}

// graphqlURL is the GraphQL endpoint beside BaseURL: /graphql under
// api.github.com. A GitHub Enterprise Server root would put it elsewhere, and
// this client is not pointed at one.
func (c *Client) graphqlURL() string {
	return c.base() + "/graphql"
}

type wireReview struct {
	ID                int64     `json:"id"`
	NodeID            string    `json:"node_id"`
	Body              string    `json:"body"`
	State             string    `json:"state"`
	AuthorAssociation string    `json:"author_association"`
	SubmittedAt       time.Time `json:"submitted_at"`
	HTMLURL           string    `json:"html_url"`
	User              struct {
		Login string `json:"login"`
	} `json:"user"`
}

type wireReviewComment struct {
	ID           int64  `json:"id"`
	Body         string `json:"body"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	OriginalLine int    `json:"original_line"`
	HTMLURL      string `json:"html_url"`
}
