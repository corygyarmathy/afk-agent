package github

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Timings an App's credentials are bound by. Constants rather than parameters:
// the first two are GitHub's rules for a JWT it will accept, and the third has
// one right answer - long enough that no request outlives the token it was sent
// with - which no operator's choice would improve on.
const (
	// jwtLifetime is how far ahead of now a JWT expires. GitHub refuses one
	// more than ten minutes out, by its own clock.
	jwtLifetime = 9 * time.Minute

	// jwtBackdate is how far before now a JWT says it was issued, for a host
	// clock running ahead of GitHub's. GitHub's recommendation.
	jwtBackdate = time.Minute

	// refreshAhead is how long before GitHub's expiry an installation token is
	// replaced: a request in flight, and a host clock running behind.
	refreshAhead = 5 * time.Minute
)

// App is the agent's identity on the tracker: a GitHub App installed on one
// repository, as a Credential (ADR 0005 §1, §2).
//
// What the host holds is the App's private key; what the API accepts is an
// installation token minted from it, which lives an hour. So a token is minted
// when it is first needed, reused while it has more than refreshAhead left, and
// minted again after that. That is checked on every request rather than on a
// timer, which is how a process outlives the token it started with: a request
// is the only moment the answer matters.
type App struct {
	// ID is the App's client ID or app ID. GitHub accepts either as a JWT's
	// issuer. A parameter, from configuration.
	ID string

	// Key is the App's private key. It signs JWTs, and is never sent.
	Key *rsa.PrivateKey

	// Repo is the repository, spelled `owner/name`. The installation is found
	// from it, and a token minted for it reaches it and nothing else.
	Repo string

	// BaseURL is the API root. Empty means DefaultBaseURL.
	BaseURL string

	// HTTP is the client requests go through. Nil means http.DefaultClient.
	HTTP *http.Client

	// Clock is the time. Nil means time.Now; a test moves it past an expiry.
	Clock func() time.Time

	mu           sync.Mutex
	installation int64
	token        string
	expires      time.Time
}

// Token returns an installation token with more than refreshAhead left,
// minting one if the one held has less.
//
// The lock is held across the mint, so workers that find the token stale at
// the same moment mint it once between them.
func (a *App) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.token != "" && a.now().Before(a.expires.Add(-refreshAhead)) {
		return a.token, nil
	}
	a.token = ""

	if a.installation == 0 {
		id, err := a.findInstallation(ctx)
		if err != nil {
			return "", err
		}
		a.installation = id
	}
	token, expires, err := a.mint(ctx)
	if se := (*StatusError)(nil); errors.As(err, &se) && se.Code == http.StatusNotFound {
		// The installation found earlier is gone. Reinstalling an App gives
		// it a new id, so the next mint looks it up again rather than
		// failing until a restart.
		a.installation = 0
	}
	if err != nil {
		return "", err
	}
	a.token, a.expires = token, expires
	return token, nil
}

// Refused forgets token if it is the one held, so the next request mints
// another. A token refused before its expiry has been revoked, and presenting
// it for the rest of its hour would fail every request until then.
func (a *App) Refused(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if token != "" && token == a.token {
		a.token = ""
	}
}

// Login returns the account the App's comments and reactions carry: its slug,
// and `[bot]`.
//
// Read from GET /app rather than configured, and not from GET /user, which
// refuses an installation token (ADR 0005 §3).
func (a *App) Login(ctx context.Context) (string, error) {
	u := baseURL(a.BaseURL) + "/app"
	var w struct {
		Slug string `json:"slug"`
	}
	if err := a.getJSON(ctx, u, &w); err != nil {
		return "", err
	}
	if w.Slug == "" {
		return "", fmt.Errorf("GET %s: no slug in the response", u)
	}
	return w.Slug + "[bot]", nil
}

// findInstallation reads the id of the App's installation on Repo.
func (a *App) findInstallation(ctx context.Context) (int64, error) {
	if _, _, ok := splitRepo(a.Repo); !ok {
		return 0, fmt.Errorf("repository %q is not owner/name", a.Repo)
	}
	u := baseURL(a.BaseURL) + "/repos/" + a.Repo + "/installation"
	var w struct {
		ID int64 `json:"id"`
	}
	if err := a.getJSON(ctx, u, &w); err != nil {
		return 0, err
	}
	if w.ID == 0 {
		return 0, fmt.Errorf("GET %s: no installation id in the response", u)
	}
	return w.ID, nil
}

// mint asks for an installation token that reaches Repo only (ADR 0005 §4).
func (a *App) mint(ctx context.Context) (string, time.Time, error) {
	_, name, _ := splitRepo(a.Repo)
	u := fmt.Sprintf("%s/app/installations/%d/access_tokens", baseURL(a.BaseURL), a.installation)
	jwt, err := a.jwt()
	if err != nil {
		return "", time.Time{}, err
	}
	resp, err := request(ctx, a.HTTP, http.MethodPost, u, mediaJSON, jwt, map[string][]string{"repositories": {name}})
	if err != nil {
		return "", time.Time{}, err
	}
	var w struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := readJSON(resp, http.MethodPost, u, &w); err != nil {
		return "", time.Time{}, err
	}
	if w.Token == "" || w.ExpiresAt.IsZero() {
		return "", time.Time{}, fmt.Errorf("POST %s: no token and expiry in the response", u)
	}
	return w.Token, w.ExpiresAt, nil
}

// getJSON reads one document as the App itself rather than as its
// installation.
func (a *App) getJSON(ctx context.Context, u string, v any) error {
	jwt, err := a.jwt()
	if err != nil {
		return err
	}
	resp, err := request(ctx, a.HTTP, http.MethodGet, u, mediaJSON, jwt, nil)
	if err != nil {
		return err
	}
	return readJSON(resp, http.MethodGet, u, v)
}

// jwt is the App's own credential, RS256-signed with Key. It can ask only about
// the App - who it is, where it is installed, and for a token - and a fresh one
// is signed for every such request, since signing costs nothing next to the
// request.
func (a *App) jwt() (string, error) {
	if a.Key == nil {
		return "", errors.New("the GitHub App has no private key")
	}
	now := a.now()
	claims, err := json.Marshal(struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}{now.Add(-jwtBackdate).Unix(), now.Add(jwtLifetime).Unix(), a.ID})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signed := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(nil, a.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("signing the GitHub App's JWT: %w", err)
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

func (a *App) now() time.Time {
	if a.Clock == nil {
		return time.Now()
	}
	return a.Clock()
}

// ParseKey reads a GitHub App's private key from PEM: PKCS #1, which is what
// GitHub issues, or PKCS #8, which is what converting one with openssl gives.
//
// An error never quotes the input, which is a secret however malformed.
func ParseKey(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("not a PKCS #1 RSA private key")
		}
		return k, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("not a PKCS #8 private key")
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("a %T, and a GitHub App's key is RSA", k)
		}
		return rk, nil
	}
	return nil, fmt.Errorf("a PEM block of type %q, and a GitHub App's key is an RSA private key", block.Type)
}
