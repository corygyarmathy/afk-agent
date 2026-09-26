package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
)

const token = "ghs_this-must-never-appear-in-an-error"

// fixed is a credential that always presents the same token.
type fixed string

func (f fixed) Token(context.Context) (string, error) { return string(f), nil }
func (fixed) Refused(string)                          {}

// serve stands up a tracker on loopback and returns a client pointed at it.
func serve(t *testing.T, h http.HandlerFunc) (*github.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &github.Client{Repo: "o/n", Credential: fixed(token), BaseURL: srv.URL}, srv
}

// expect fails the request, and the test, when it is not the one the client
// was supposed to make.
func expect(t *testing.T, w http.ResponseWriter, r *http.Request, method, path, accept string) bool {
	t.Helper()
	ok := true
	if r.Method != method || r.URL.Path != path {
		t.Errorf("request %s %s, want %s %s", r.Method, r.URL.Path, method, path)
		ok = false
	}
	if got := r.Header.Get("Accept"); got != accept {
		t.Errorf("Accept %q, want %q", got, accept)
		ok = false
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization %q, want the bearer token", got)
		ok = false
	}
	if r.Header.Get("X-GitHub-Api-Version") == "" {
		t.Error("no API version pinned")
		ok = false
	}
	if !ok {
		http.Error(w, "unexpected request", http.StatusTeapot)
	}
	return ok
}

func TestPullRequestReadsItsHeadAndDescription(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if expect(t, w, r, "GET", "/repos/o/n/pulls/12", "application/vnd.github+json") {
			fmt.Fprint(w, `{"number":12,"state":"open","title":"Reserve a job","body":"Closes #7.","head":{"sha":"abc123","ref":"feature"},"user":{"login":"alice"},"labels":[{"name":"needs-review"}]}`)
		}
	})

	pr, err := c.PullRequest(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	want := github.PullRequest{Number: 12, State: "open", HeadSHA: "abc123", HeadRef: "feature", Login: "alice", Labels: []string{"needs-review"}, Title: "Reserve a job", Body: "Closes #7."}
	if fmt.Sprint(pr) != fmt.Sprint(want) {
		t.Errorf("got %+v, want %+v", pr, want)
	}
}

// A description nobody wrote is null on the wire, and empty here.
func TestAPullRequestWithNoDescriptionHasAnEmptyBody(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"number":12,"state":"open","title":"t","body":null,"head":{"sha":"abc123"}}`)
	})

	pr, err := c.PullRequest(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Body != "" {
		t.Errorf("body %q, want empty", pr.Body)
	}
}

func TestIssueReadsItsTitleBodyStateAndLabels(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if expect(t, w, r, "GET", "/repos/o/n/issues/7", "application/vnd.github+json") {
			fmt.Fprint(w, `{"number":7,"title":"Jobs are reserved","body":"A job is reserved before it runs.","state":"open","labels":[{"name":"needs-decision"}]}`)
		}
	})

	is, err := c.Issue(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if want := (github.Issue{Number: 7, State: "open", Title: "Jobs are reserved", Body: "A job is reserved before it runs.", Labels: []string{"needs-decision"}}); !reflect.DeepEqual(is, want) {
		t.Errorf("got %+v, want %+v", is, want)
	}
}

func TestDiffAsksForTheDiff(t *testing.T) {
	const diff = "diff --git a/x b/x\n+added\n"
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if expect(t, w, r, "GET", "/repos/o/n/pulls/12", "application/vnd.github.diff") {
			fmt.Fprint(w, diff)
		}
	})

	got, err := c.Diff(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if got != diff {
		t.Errorf("got %q, want %q", got, diff)
	}
}

// A listing is read to the end. A command written on the hundred-and-first
// comment of a long conversation is a command, and a client that stopped at
// the first page would never see it.
func TestListingsAreReadPastTheFirstPage(t *testing.T) {
	var srvURL string
	pages := func(path string, first, second string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !expect(t, w, r, "GET", path, "application/vnd.github+json") {
				return
			}
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, second)
				return
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="next", <%s%s?page=2>; rel="last"`, srvURL, path, srvURL, path))
			fmt.Fprint(w, first)
		}
	}

	t.Run("comments", func(t *testing.T) {
		c, srv := serve(t, pages("/repos/o/n/issues/12/comments",
			`[{"id":1,"body":"first","user":{"login":"alice"},"author_association":"OWNER"},
			  {"id":2,"body":"second","user":{"login":"bob"},"author_association":"NONE"}]`,
			`[{"id":3,"body":"/review","user":{"login":"alice"},"author_association":"OWNER"}]`))
		srvURL = srv.URL

		got, err := c.Comments(context.Background(), 12)
		if err != nil {
			t.Fatal(err)
		}
		want := []github.Comment{
			{ID: 1, Body: "first", Login: "alice", Association: "OWNER"},
			{ID: 2, Body: "second", Login: "bob", Association: "NONE"},
			{ID: 3, Body: "/review", Login: "alice", Association: "OWNER"},
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("got %+v\nwant %+v", got, want)
		}
	})

	t.Run("open issues", func(t *testing.T) {
		c, srv := serve(t, pages("/repos/o/n/issues",
			`[{"number":7,"state":"open","title":"An issue","updated_at":"2026-09-13T12:00:00Z"}]`,
			`[{"number":12,"state":"open","title":"A pull request","pull_request":{"url":"https://api.github.com/repos/o/n/pulls/12"}}]`))
		srvURL = srv.URL

		got, err := c.OpenIssues(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := []github.Issue{
			{Number: 7, State: "open", Title: "An issue", UpdatedAt: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)},
			{Number: 12, State: "open", Title: "A pull request", PullRequest: true},
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("open pull requests", func(t *testing.T) {
		c, srv := serve(t, pages("/repos/o/n/pulls",
			`[{"number":1,"state":"open","head":{"sha":"a"}}]`,
			`[{"number":2,"state":"open","head":{"sha":"b"}}]`))
		srvURL = srv.URL

		got, err := c.OpenPullRequests(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := []github.PullRequest{{Number: 1, State: "open", HeadSHA: "a"}, {Number: 2, State: "open", HeadSHA: "b"}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

// The token goes with every request, so a next page is only followed when it
// is under the API root the client was configured with.
func TestANextPageElsewhereIsNotFollowed(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://elsewhere.example/steal?page=2>; rel="next"`)
		fmt.Fprint(w, `[]`)
	})

	_, err := c.Comments(context.Background(), 12)
	if err == nil || !strings.Contains(err.Error(), "elsewhere.example") {
		t.Fatalf("got %v, want a refusal naming the foreign page", err)
	}
}

func TestCommentPostsTheBody(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if !expect(t, w, r, "POST", "/repos/o/n/issues/12/comments", "application/vnd.github+json") {
			return
		}
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in["body"] != "looks fine" {
			t.Errorf("posted %v (%v), want body \"looks fine\"", in, err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":77,"body":"looks fine","user":{"login":"afk-bot"},"author_association":"COLLABORATOR"}`)
	})

	got, err := c.Comment(context.Background(), 12, "looks fine")
	if err != nil {
		t.Fatal(err)
	}
	if want := (github.Comment{ID: 77, Body: "looks fine", Login: "afk-bot", Association: "COLLABORATOR"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A reaction that already exists comes back 200 rather than 201, and is not an
// error: reacting is how a command is claimed, and a replayed claim must not
// fail the transition.
func TestReactAcceptsAReactionThatAlreadyExists(t *testing.T) {
	for _, code := range []int{http.StatusCreated, http.StatusOK} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if !expect(t, w, r, "POST", "/repos/o/n/issues/comments/99/reactions", "application/vnd.github+json") {
					return
				}
				var in map[string]string
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in["content"] != "eyes" {
					t.Errorf("posted %v (%v), want content eyes", in, err)
				}
				w.WriteHeader(code)
				fmt.Fprint(w, `{"id":1,"content":"eyes"}`)
			})
			if err := c.React(context.Background(), 99, "eyes"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The Checks API wraps its listing in an object, and pages it like any other.
func TestCheckRunsAreReadToTheLastPage(t *testing.T) {
	var srvURL string
	c, srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if !expect(t, w, r, "GET", "/repos/o/n/commits/abc/check-runs", "application/vnd.github+json") {
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"total_count":2,"check_runs":[{"name":"test","status":"in_progress","conclusion":null}]}`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/n/commits/abc/check-runs?page=2>; rel="next"`, srvURL))
		fmt.Fprint(w, `{"total_count":2,"check_runs":[{"name":"build","status":"completed","conclusion":"failure","html_url":"https://github.com/o/n/runs/1","output":{"title":"1 error","summary":"compile failed","text":"x.go:1: undefined"}}]}`)
	})
	srvURL = srv.URL

	got, err := c.CheckRuns(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	want := []github.CheckRun{
		{Name: "build", Status: "completed", Conclusion: "failure", URL: "https://github.com/o/n/runs/1", Title: "1 error", Summary: "compile failed", Text: "x.go:1: undefined"},
		{Name: "test", Status: "in_progress"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestCreatePullRequestSendsItsFields(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if !expect(t, w, r, "POST", "/repos/o/n/pulls", "application/vnd.github+json") {
			return
		}
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"title": "Reserve a job", "head": "afk/7-1", "base": "main", "body": "Closes #7."}
		if fmt.Sprint(in) != fmt.Sprint(want) {
			t.Errorf("posted %v, want %v", in, want)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"number":40,"state":"open","head":{"sha":"abc","ref":"afk/7-1"},"user":{"login":"afk-agent[bot]"}}`)
	})
	pr, err := c.CreatePullRequest(context.Background(), github.NewPullRequest{Title: "Reserve a job", Head: "afk/7-1", Base: "main", Body: "Closes #7."})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 40 || pr.HeadRef != "afk/7-1" {
		t.Errorf("got %+v, want #40 from afk/7-1", pr)
	}
}

func TestReactionsOnAPullRequestItself(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			if expect(t, w, r, "GET", "/repos/o/n/issues/12/reactions", "application/vnd.github+json") {
				fmt.Fprint(w, `[{"content":"eyes","user":{"login":"afk-agent[bot]"}}]`)
			}
		default:
			if !expect(t, w, r, "POST", "/repos/o/n/issues/12/reactions", "application/vnd.github+json") {
				return
			}
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in["content"] != "eyes" {
				t.Errorf("posted %v (%v), want content eyes", in, err)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":1,"content":"eyes"}`)
		}
	})
	got, err := c.IssueReactions(context.Background(), 12)
	if err != nil || fmt.Sprint(got) != fmt.Sprint([]github.Reaction{{Login: "afk-agent[bot]", Content: "eyes"}}) {
		t.Errorf("IssueReactions = %v, %v", got, err)
	}
	if err := c.ReactToIssue(context.Background(), 12, "eyes"); err != nil {
		t.Fatal(err)
	}
}

func TestLabelAddsTheLabel(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if !expect(t, w, r, "POST", "/repos/o/n/issues/7/labels", "application/vnd.github+json") {
			return
		}
		var in map[string][]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || fmt.Sprint(in["labels"]) != "[needs-decision]" {
			t.Errorf("posted %v (%v), want labels [needs-decision]", in, err)
		}
		fmt.Fprint(w, `[{"name":"needs-decision"}]`)
	})
	if err := c.Label(context.Background(), 7, "needs-decision"); err != nil {
		t.Fatal(err)
	}
}

// A failure names the endpoint and the status, and never the token: errors end
// up in the journal, and a 401 must say the token is wrong without saying what
// it is.
func TestAFailureNamesTheEndpointAndStatusButNotTheToken(t *testing.T) {
	c, srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		// Echo the credential back in the body, the way a careless proxy
		// might, so a client that reported the body would be caught.
		http.Error(w, "bad credentials: "+r.Header.Get("Authorization"), http.StatusUnauthorized)
	})

	_, err := c.PullRequest(context.Background(), 12)
	if err == nil {
		t.Fatal("no error for a 401")
	}
	msg := err.Error()
	if !strings.Contains(msg, srv.URL+"/repos/o/n/pulls/12") || !strings.Contains(msg, "401") {
		t.Errorf("error %q does not name the endpoint and the status", msg)
	}
	if strings.Contains(msg, token) {
		t.Errorf("error %q contains the token", msg)
	}
	var se *github.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Errorf("error %v is not a StatusError with code 401", err)
	}
}

// A repository that is not owner/name is refused before anything is sent.
func TestARepositoryThatIsNotOwnerSlashNameIsRefused(t *testing.T) {
	for _, repo := range []string{"", "o", "/n", "o/", "o/n/x"} {
		t.Run(repo, func(t *testing.T) {
			c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("sent %s %s for repository %q", r.Method, r.URL.Path, repo)
			})
			c.Repo = repo
			if _, err := c.PullRequest(context.Background(), 1); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestReactionsNameWhoReacted(t *testing.T) {
	var srvURL string
	c, srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if !expect(t, w, r, "GET", "/repos/o/n/issues/comments/99/reactions", "application/vnd.github+json") {
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"id":2,"content":"eyes","user":{"login":"afk-bot"}}]`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/n/issues/comments/99/reactions?page=2>; rel="next"`, srvURL))
		fmt.Fprint(w, `[{"id":1,"content":"+1","user":{"login":"alice"}}]`)
	})
	srvURL = srv.URL

	got, err := c.Reactions(context.Background(), 99)
	if err != nil {
		t.Fatal(err)
	}
	want := []github.Reaction{{Login: "alice", Content: "+1"}, {Login: "afk-bot", Content: "eyes"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
