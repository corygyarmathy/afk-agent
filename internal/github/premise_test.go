package github_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/github"
)

// The default branch is read from the repository itself.
func TestDefaultBranchIsTheRepositorys(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if expect(t, w, r, "GET", "/repos/o/n", "application/vnd.github+json") {
			fmt.Fprint(w, `{"full_name":"o/n","default_branch":"trunk"}`)
		}
	})
	got, err := c.DefaultBranch(context.Background())
	if err != nil || got != "trunk" {
		t.Errorf("DefaultBranch = %q, %v; want trunk", got, err)
	}
}

// A file is read at the revision asked for, with each segment of its path
// escaped, and decoded from the base64 the API serves it in.
func TestAFileIsReadAtItsRevision(t *testing.T) {
	content := "line one\nline two\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	// The API wraps its base64 at 60 characters.
	wrapped := encoded[:10] + "\\n" + encoded[10:]
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/repos/o/n/contents/docs/a%20b.md" {
			t.Errorf("path %q, want each segment escaped", r.URL.EscapedPath())
		}
		if got := r.URL.Query().Get("ref"); got != "abc123" {
			t.Errorf("ref %q, want abc123", got)
		}
		if expect(t, w, r, "GET", "/repos/o/n/contents/docs/a b.md", "application/vnd.github+json") {
			fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":"%s"}`, wrapped)
		}
	})
	got, err := c.File(context.Background(), "docs/a b.md", "abc123")
	if err != nil || string(got) != content {
		t.Errorf("File = %q, %v; want %q", got, err, content)
	}
}

// What is not a file inline is an error, never an empty file: a directory,
// which the API serves as a listing, a submodule, and a file over a megabyte,
// whose content the API leaves out.
func TestWhatIsNotAFileIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"a directory": `[{"type":"file","name":"a"}]`,
		"a submodule": `{"type":"submodule"}`,
		"too large":   `{"type":"file","encoding":"none","content":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if got, err := c.File(context.Background(), "x", "main"); err == nil {
				t.Errorf("File = %q, want an error", got)
			} else if strings.Contains(err.Error(), token) {
				t.Errorf("the error carries the token: %v", err)
			}
		})
	}
}

// A client with no credential reads as anyone would, so a public repository
// is still read: how a premise in another repository is read (#207).
func TestAClientWithNoCredentialReadsWithNoToken(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/up/stream" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		auth = append(auth, r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"default_branch":"main"}`)
	}))
	t.Cleanup(srv.Close)

	c := &github.Client{Repo: "up/stream", BaseURL: srv.URL}
	if got, err := c.DefaultBranch(context.Background()); err != nil || got != "main" {
		t.Fatalf("DefaultBranch = %q, %v; want main", got, err)
	}
	if len(auth) != 1 || auth[0] != "" {
		t.Errorf("Authorization %q, want none", auth)
	}
}
