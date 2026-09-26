package git_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/git"
)

// The token reaches git through the environment, for the remote's URL only,
// minted afresh for every process.
func TestTheTokenIsInTheEnvironmentForTheRemoteOnly(t *testing.T) {
	mints := 0
	r := git.Remote{URL: "https://github.com/o/n.git", Token: func(context.Context) (string, error) {
		mints++
		return "ghs_secret", nil
	}}
	for range 2 {
		env, err := r.Env(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(env, "\n")
		for _, want := range []string{
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=http.https://github.com/o/n.git.extraheader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic eC1hY2Nlc3MtdG9rZW46Z2hzX3NlY3JldA==",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("environment does not contain %q:\n%s", want, joined)
			}
		}
	}
	if mints != 2 {
		t.Errorf("the token was minted %d times for two processes, want 2", mints)
	}
}

// No token, none sent: still isolated. A token that cannot be minted is an
// error, not a read without one.
func TestARemoteWithoutATokenSendsNone(t *testing.T) {
	env, err := git.Remote{URL: "/srv/remote.git"}.Env(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(env, "\n"); strings.Contains(joined, "extraheader") || !strings.Contains(joined, "GIT_CONFIG_NOSYSTEM=1") {
		t.Errorf("environment without a token:\n%s", joined)
	}

	failing := git.Remote{URL: "https://github.com/o/n.git", Token: func(context.Context) (string, error) {
		return "", errors.New("502 Bad Gateway")
	}}
	if _, err := failing.Run(context.Background(), "", "ls-remote", failing.URL); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want the mint's", err)
	}
}

// A read given no directory does not take one from the agent's own: a
// repository around the agent's working directory has a configuration that
// neither the global nor the system one shuts out, and it could send the read,
// and its token, elsewhere.
func TestAReadWithNoDirectoryTakesNoRepositoryItIsIn(t *testing.T) {
	root := t.TempDir()
	src, elsewhere, around := filepath.Join(root, "src.git"), filepath.Join(root, "elsewhere.git"), filepath.Join(root, "around")
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", src},
		{"init", "--quiet", "--bare", elsewhere},
		{"init", "--quiet", around},
		{"-C", around, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "--allow-empty", "-m", "elsewhere"},
		{"-C", around, "push", "--quiet", elsewhere, "HEAD:refs/heads/elsewhere"},
		{"-C", around, "config", "url." + elsewhere + ".insteadOf", src},
	} {
		if _, err := git.RunEnv(context.Background(), "", git.Isolated, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(around)

	r := git.Remote{URL: src}
	if heads, err := r.Run(context.Background(), "", "ls-remote", r.URL); err != nil || heads != "" {
		t.Errorf("ls-remote = %q, %v: want the empty remote's nothing", heads, err)
	}
}

// Over HTTP, as GitHub is reached: every request to the remote carries the
// token, and a request to any other URL, from the same environment, does not.
func TestTheTokenIsSentToTheRemoteAndNowhereElse(t *testing.T) {
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", filepath.Join(root, "n.git")},
		{"init", "--quiet", "--bare", filepath.Join(root, "other.git")},
	} {
		if _, err := git.RunEnv(context.Background(), "", git.Isolated, args...); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	sent := map[string][]string{}
	backend := &cgi.Handler{Path: bin, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		repo, _, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/")
		mu.Lock()
		sent[repo] = append(sent[repo], req.Header.Get("Authorization"))
		mu.Unlock()
		backend.ServeHTTP(w, req)
	}))
	defer srv.Close()

	r := git.Remote{URL: srv.URL + "/n.git", Token: func(context.Context) (string, error) { return "ghs_secret", nil }}
	if _, err := r.Run(context.Background(), "", "ls-remote", r.URL); err != nil {
		t.Fatal(err)
	}
	env, err := r.Env(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := git.RunEnv(context.Background(), "/", env, "ls-remote", srv.URL+"/other.git"); err != nil {
		t.Fatal(err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_secret"))
	if len(sent["n.git"]) == 0 {
		t.Fatal("no request reached the remote")
	}
	for _, got := range sent["n.git"] {
		if got != want {
			t.Errorf("a request to the remote carried %q, want %q", got, want)
		}
	}
	if len(sent["other.git"]) == 0 {
		t.Fatal("no request reached the other URL")
	}
	for _, got := range sent["other.git"] {
		if got != "" {
			t.Errorf("a request to another URL carried %q", got)
		}
	}
}

// held is a credential as the App is one: a token held until it is refused,
// and a new one minted after.
type held struct {
	mu      sync.Mutex
	token   string
	mints   []string
	refused []string
}

func (h *held) Token(context.Context) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.token == "" {
		h.token = h.mints[0]
		h.mints = h.mints[1:]
	}
	return h.token, nil
}

func (h *held) Refused(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refused = append(h.refused, token)
	if token == h.token {
		h.token = ""
	}
}

// remoteRefusing is a remote over HTTP, as GitHub is reached, that answers a
// request bearing the token revoked with status, and serves any other.
func remoteRefusing(t *testing.T, revoked string, status int) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	root := t.TempDir()
	if _, err := git.RunEnv(context.Background(), "", git.Isolated, "init", "--quiet", "--bare", filepath.Join(root, "n.git")); err != nil {
		t.Fatal(err)
	}
	refuse := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+revoked))
	backend := &cgi.Handler{Path: bin, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") == refuse {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			w.WriteHeader(status)
			return
		}
		backend.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/n.git"
}

// A process the remote refuses with a 401 tells the credential which token it
// carried, so the next process mints another rather than presenting a revoked
// one until it would have expired (ADR 0005 §2).
func TestATokenTheRemoteRefusesIsDiscarded(t *testing.T) {
	url := remoteRefusing(t, "ghs_revoked", http.StatusUnauthorized)
	cred := &held{mints: []string{"ghs_revoked", "ghs_fresh"}}
	r := git.Remote{URL: url, Token: cred.Token, Refused: cred.Refused}

	_, err := r.Run(context.Background(), "", "ls-remote", r.URL)
	if err == nil {
		t.Fatal("ls-remote with the revoked token succeeded")
	}
	if len(cred.refused) != 1 || cred.refused[0] != "ghs_revoked" {
		t.Fatalf("refused %q, want the revoked token once; the process said: %v", cred.refused, err)
	}
	if strings.Contains(err.Error(), "ghs_") {
		t.Errorf("the error names a token: %v", err)
	}
	if _, err := r.Run(context.Background(), "", "ls-remote", r.URL); err != nil {
		t.Errorf("the next process, with a new token, failed: %v", err)
	}
}

// A 403 is a refusal of what the token may do, not of the token: another
// minted for the same installation may do no more. It is not discarded, as the
// API client does not discard one either.
func TestATokenForbiddenAnActionIsKept(t *testing.T) {
	url := remoteRefusing(t, "ghs_limited", http.StatusForbidden)
	cred := &held{mints: []string{"ghs_limited"}}
	r := git.Remote{URL: url, Token: cred.Token, Refused: cred.Refused}

	if _, err := r.Run(context.Background(), "", "ls-remote", r.URL); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the 403", err)
	}
	if len(cred.refused) != 0 {
		t.Errorf("refused %q after a 403, want nothing", cred.refused)
	}
}
