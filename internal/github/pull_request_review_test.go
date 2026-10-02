package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
)

func TestPullRequestReviewsReadEachReviewsBodyAuthorStateAndCommit(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if expect(t, w, r, "GET", "/repos/o/n/pulls/12/reviews", "application/vnd.github+json") {
			fmt.Fprint(w, `[{"id":5,"node_id":"PRR_5","body":"/revise","state":"APPROVED","author_association":"OWNER","submitted_at":"2026-09-30T10:00:00Z","commit_id":"abc123","html_url":"https://github.com/o/n/pull/12#pullrequestreview-5","user":{"login":"alice"}}]`)
		}
	})
	got, err := c.PullRequestReviews(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	want := []github.PullRequestReview{{ID: 5, NodeID: "PRR_5", Body: "/revise", Login: "alice", Association: "OWNER", State: "APPROVED", SubmittedAt: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC), CommitID: "abc123", URL: "https://github.com/o/n/pull/12#pullrequestreview-5"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A line comment's line is the line in the commit it was written on, even
// when the pull request has since moved on, and one on a whole file has none.
func TestLineCommentsAreOneReviewsLineCommentsOnTheCommitWrittenOn(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if expect(t, w, r, "GET", "/repos/o/n/pulls/12/reviews/5/comments", "application/vnd.github+json") {
			fmt.Fprint(w, `[
				{"id":1,"body":"rename","path":"a.go","line":3,"original_line":2,"commit_id":"def456","original_commit_id":"abc123","html_url":"https://github.com/o/n/pull/12#discussion_r1"},
				{"id":2,"body":"moved","path":"b.go","line":null,"original_line":9,"commit_id":"def456","original_commit_id":"abc123","html_url":"u2"},
				{"id":3,"body":"whole file","path":"c.go","line":null,"original_line":null,"commit_id":"abc123","original_commit_id":"abc123","html_url":"u3"}]`)
		}
	})
	got, err := c.LineComments(context.Background(), 12, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []github.LineComment{
		{ID: 1, Body: "rename", Path: "a.go", Line: 2, CommitID: "abc123", URL: "https://github.com/o/n/pull/12#discussion_r1"},
		{ID: 2, Body: "moved", Path: "b.go", Line: 9, CommitID: "abc123", URL: "u2"},
		{ID: 3, Body: "whole file", Path: "c.go", CommitID: "abc123", URL: "u3"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// graphqlBody is a GraphQL request as the client sends it.
type graphqlBody struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// Reactions to a review are GraphQL's, read to the last page and spelled as
// REST spells them. An App's reaction carries its REST login under user, as
// GitHub served it for the agent's 👀 on a review on 2026-10-02.
func TestReviewReactionsAreReadToTheLastPage(t *testing.T) {
	pages := 0
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if !expect(t, w, r, "POST", "/graphql", "application/vnd.github+json") {
			return
		}
		var in graphqlBody
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Variables["id"] != "PRR_5" {
			t.Errorf("sent %+v (%v), want the review's node id", in, err)
		}
		pages++
		if in.Variables["after"] == nil {
			fmt.Fprint(w, `{"data":{"node":{"reactions":{"nodes":[{"content":"THUMBS_UP","user":{"login":"alice"}}],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}}}`)
			return
		}
		if in.Variables["after"] != "c1" {
			t.Errorf("second page after %v, want c1", in.Variables["after"])
		}
		fmt.Fprint(w, `{"data":{"node":{"reactions":{"nodes":[{"content":"EYES","user":{"login":"afk-agent[bot]"}}],"pageInfo":{"hasNextPage":false,"endCursor":"c2"}}}}}`)
	})
	got, err := c.PullRequestReviewReactions(context.Background(), "PRR_5")
	if err != nil {
		t.Fatal(err)
	}
	want := []github.Reaction{{Login: "alice", Content: "+1"}, {Login: "afk-agent[bot]", Content: "eyes"}}
	if fmt.Sprint(got) != fmt.Sprint(want) || pages != 2 {
		t.Errorf("got %v over %d pages, want %v over 2", got, pages, want)
	}
}

// A review that is not there reads as a 404, as a comment that is not there
// does.
func TestAReviewThatIsNotThereIsNotFound(t *testing.T) {
	for name, body := range map[string]string{
		"null node": `{"data":{"node":null}}`,
		"NOT_FOUND": `{"data":null,"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a node with the global id of 'PRR_5'"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			_, err := c.PullRequestReviewReactions(context.Background(), "PRR_5")
			var se *github.StatusError
			if !errors.As(err, &se) || se.Code != http.StatusNotFound {
				t.Errorf("error %v, want a StatusError with code 404", err)
			}
		})
	}
}

func TestAGraphQLErrorIsAnError(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":null,"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`)
	})
	if err := c.ReactToPullRequestReview(context.Background(), "PRR_5", "eyes"); err == nil {
		t.Error("no error for a GraphQL error")
	}
}

func TestReactToReviewSendsGraphQLsSpelling(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if !expect(t, w, r, "POST", "/graphql", "application/vnd.github+json") {
			return
		}
		var in graphqlBody
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Variables["id"] != "PRR_5" || in.Variables["content"] != "EYES" {
			t.Errorf("sent %+v (%v), want the review's node id and EYES", in, err)
		}
		fmt.Fprint(w, `{"data":{"addReaction":{"reaction":{"content":"EYES"}}}}`)
	})
	if err := c.ReactToPullRequestReview(context.Background(), "PRR_5", "eyes"); err != nil {
		t.Fatal(err)
	}
}
