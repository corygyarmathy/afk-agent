package github_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
)

// testKey is one App key for the whole package: generating RSA keys is the
// slowest thing these tests would otherwise do.
var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

// fakeApp is GitHub as an App sees it: an installation on o/n, the endpoint
// that mints its tokens, and a pull request that takes one. Tokens expire by
// its clock, which the App under test reads too.
type fakeApp struct {
	t *testing.T

	mu           sync.Mutex
	now          time.Time
	installation int64
	minted       []string
	expiry       map[string]time.Time
	revoked      map[string]bool
	lookups      int
	jwts         []string

	// refuseApp answers every JWT with a 401 that echoes it back.
	refuseApp bool
}

func newFakeApp(t *testing.T) (*fakeApp, *github.Client, *github.App) {
	t.Helper()
	f := &fakeApp{
		t:            t,
		now:          time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC),
		installation: 42,
		expiry:       map[string]time.Time{},
		revoked:      map[string]bool{},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	app := &github.App{ID: "123", Key: testKey(), Repo: "o/n", BaseURL: srv.URL, Clock: f.clock}
	return f, &github.Client{Repo: "o/n", Credential: app, BaseURL: srv.URL}, app
}

func (f *fakeApp) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeApp) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *fakeApp) with(fn func(f *fakeApp)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	switch {
	case r.Method == "GET" && r.URL.Path == "/repos/o/n/installation":
		if f.asApp(w, bearer) {
			f.lookups++
			fmt.Fprintf(w, `{"id":%d}`, f.installation)
		}

	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/app/installations/"):
		if !f.asApp(w, bearer) {
			return
		}
		if r.URL.Path != fmt.Sprintf("/app/installations/%d/access_tokens", f.installation) {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || fmt.Sprint(in.Repositories) != "[n]" {
			f.t.Errorf("minted for %v (%v), want the one repository [n]", in.Repositories, err)
		}
		token := fmt.Sprintf("ghs_minted-%d", len(f.minted)+1)
		f.minted = append(f.minted, token)
		f.expiry[token] = f.now.Add(time.Hour)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, token, f.expiry[token].Format(time.RFC3339))

	case r.Method == "GET" && r.URL.Path == "/app":
		if f.asApp(w, bearer) {
			fmt.Fprint(w, `{"id":123,"slug":"afk-agent","name":"AFK agent"}`)
		}

	case r.Method == "GET" && r.URL.Path == "/repos/o/n/pulls/12":
		expires, ok := f.expiry[bearer]
		if !ok || f.revoked[bearer] || !f.now.Before(expires) {
			http.Error(w, "Bad credentials: "+bearer, http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"number":12,"state":"open","head":{"sha":"abc"}}`)

	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected request", http.StatusTeapot)
	}
}

// asApp checks the request carries a JWT GitHub would accept, now.
func (f *fakeApp) asApp(w http.ResponseWriter, jwt string) bool {
	f.jwts = append(f.jwts, jwt)
	if f.refuseApp {
		http.Error(w, "A JSON web token could not be decoded: "+jwt, http.StatusUnauthorized)
		return false
	}
	if err := verifyJWT(jwt, &testKey().PublicKey, f.now); err != nil {
		f.t.Errorf("JWT refused: %v", err)
		http.Error(w, "A JSON web token could not be decoded", http.StatusUnauthorized)
		return false
	}
	return true
}

// verifyJWT holds a JWT to GitHub's rules: RS256 under the App's key, issued by
// the App, valid now, and living no more than ten minutes.
func verifyJWT(jwt string, pub *rsa.PublicKey, now time.Time) error {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%d parts, want 3", len(parts))
	}
	enc := base64.RawURLEncoding
	decode := func(part string, v any) error {
		b, err := enc.DecodeString(part)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, v)
	}

	var header struct {
		Alg string `json:"alg"`
	}
	if err := decode(parts[0], &header); err != nil || header.Alg != "RS256" {
		return fmt.Errorf("header alg %q (%v), want RS256", header.Alg, err)
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("signature: %w", err)
	}

	var claims struct {
		IAT int64  `json:"iat"`
		EXP int64  `json:"exp"`
		ISS string `json:"iss"`
	}
	if err := decode(parts[1], &claims); err != nil {
		return err
	}
	switch {
	case claims.ISS != "123":
		return fmt.Errorf("issuer %q, want the App's id", claims.ISS)
	case claims.EXP-claims.IAT > 600:
		return fmt.Errorf("lives %ds, and GitHub refuses more than 600", claims.EXP-claims.IAT)
	case now.Unix() < claims.IAT || now.Unix() >= claims.EXP:
		return fmt.Errorf("valid %d-%d, not at %d", claims.IAT, claims.EXP, now.Unix())
	}
	return nil
}

func readPR(t *testing.T, c *github.Client) {
	t.Helper()
	if _, err := c.PullRequest(context.Background(), 12); err != nil {
		t.Fatal(err)
	}
}

func TestAnInstallationTokenIsMintedOnceAndReused(t *testing.T) {
	f, c, _ := newFakeApp(t)
	for range 3 {
		readPR(t, c)
	}
	f.with(func(f *fakeApp) {
		if len(f.minted) != 1 || f.lookups != 1 {
			t.Errorf("minted %d tokens after %d installation lookups, want 1 and 1", len(f.minted), f.lookups)
		}
	})
}

// A token lives an hour and afk work runs for days, so a token expiring under
// a running process is the normal case (#34). It is replaced before it expires,
// and the process never needs restarting for it.
func TestTheAgentKeepsWorkingPastATokensExpiry(t *testing.T) {
	f, c, _ := newFakeApp(t)
	mints := func() (n int) {
		f.with(func(f *fakeApp) { n = len(f.minted) })
		return n
	}

	readPR(t, c)
	f.advance(50 * time.Minute) // ten minutes left: still the first token
	readPR(t, c)
	if n := mints(); n != 1 {
		t.Fatalf("minted %d tokens with ten minutes left on the first, want 1", n)
	}

	f.advance(6 * time.Minute) // four minutes left: replaced
	readPR(t, c)
	if n := mints(); n != 2 {
		t.Fatalf("minted %d tokens with four minutes left on the first, want 2", n)
	}

	f.advance(3 * time.Hour) // long expired
	readPR(t, c)
	if n := mints(); n != 3 {
		t.Fatalf("minted %d tokens after the second expired, want 3", n)
	}

	f.with(func(f *fakeApp) {
		if f.lookups != 1 {
			t.Errorf("looked the installation up %d times, want once", f.lookups)
		}
	})
}

// A token revoked before its expiry fails the request that found out, and is
// not presented again: the next request mints another.
func TestARefusedTokenIsNotPresentedAgain(t *testing.T) {
	f, c, _ := newFakeApp(t)
	readPR(t, c)
	f.with(func(f *fakeApp) { f.revoked[f.minted[0]] = true })

	_, err := c.PullRequest(context.Background(), 12)
	var se *github.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Fatalf("err = %v, want the 401", err)
	}

	readPR(t, c)
	f.with(func(f *fakeApp) {
		if len(f.minted) != 2 {
			t.Errorf("minted %d tokens, want a second after the first was refused", len(f.minted))
		}
	})
}

// Reinstalling the App gives it a new installation id. The agent finds it
// again rather than minting against the old one until it is restarted.
func TestAReinstalledAppIsFoundAgain(t *testing.T) {
	f, c, _ := newFakeApp(t)
	readPR(t, c)
	f.with(func(f *fakeApp) {
		f.installation = 43
		for _, token := range f.minted {
			f.revoked[token] = true
		}
	})

	var ok bool
	for range 3 {
		if _, err := c.PullRequest(context.Background(), 12); err == nil {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatal("still failing after three requests; the new installation was not found")
	}
	f.with(func(f *fakeApp) {
		if f.lookups != 2 {
			t.Errorf("looked the installation up %d times, want twice", f.lookups)
		}
	})
}

// GET /user refuses an installation token (dotfiles ADR 0006), so the login is
// the App's own, read as the App. The fake fails the test on any GET /user.
func TestTheAppsLoginIsItsBotAccount(t *testing.T) {
	_, _, app := newFakeApp(t)
	got, err := app.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "afk-agent[bot]" {
		t.Errorf("login = %q, want afk-agent[bot]", got)
	}
}

// Neither the JWT nor an installation token appears in an error, whichever of
// the two GitHub refuses and however loudly it echoes it back.
func TestNoCredentialAppearsInAnError(t *testing.T) {
	t.Run("the JWT", func(t *testing.T) {
		f, c, app := newFakeApp(t)
		f.with(func(f *fakeApp) { f.refuseApp = true })

		_, prErr := c.PullRequest(context.Background(), 12)
		_, loginErr := app.Login(context.Background())
		for _, err := range []error{prErr, loginErr} {
			if err == nil || !strings.Contains(err.Error(), "401") {
				t.Fatalf("err = %v, want the 401", err)
			}
			f.with(func(f *fakeApp) {
				for _, jwt := range f.jwts {
					if strings.Contains(err.Error(), jwt) {
						t.Errorf("error %q contains the JWT", err)
					}
				}
			})
		}
	})

	t.Run("the installation token", func(t *testing.T) {
		f, c, _ := newFakeApp(t)
		readPR(t, c)
		var token string
		f.with(func(f *fakeApp) {
			token = f.minted[0]
			f.revoked[token] = true
		})

		_, err := c.PullRequest(context.Background(), 12)
		if err == nil {
			t.Fatal("no error for a 401")
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("error %q contains the token", err)
		}
	})
}

func TestParseKeyReadsWhatGitHubIssues(t *testing.T) {
	key := testKey()
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"PKCS #1, as GitHub issues it": {Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)},
		"PKCS #8":                      {Type: "PRIVATE KEY", Bytes: pkcs8},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := github.ParseKey(pem.EncodeToMemory(block))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(key) {
				t.Error("parsed a different key")
			}
		})
	}
}

// A key file that is not an App's key is refused without quoting it: a file
// at --app-key is a secret whether or not it parses.
func TestParseKeyRefusesWhatIsNotAnAppKey(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "sekrit-material"

	for name, in := range map[string][]byte{
		"not PEM":             []byte(secret),
		"a corrupt PKCS #1":   pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte(secret)}),
		"a corrupt PKCS #8":   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte(secret)}),
		"an ECDSA key":        pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}),
		"a certificate block": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte(secret)}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := github.ParseKey(in)
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error %q quotes the input", err)
			}
		})
	}
}
