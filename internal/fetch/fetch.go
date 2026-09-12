// Package fetch is the one authenticated GET this agent makes.
//
// Two packages read a document from upstream - the model catalogue and the
// usage endpoint - and the request they make is the same request. It lives here
// so that "what a fetch failure looks like" is decided once: the error names the
// endpoint and the status, and never the token.
//
// It is not a seam and nothing here is injectable. Each caller keeps its own
// Fetch field and this is only what that field defaults to, which is what keeps
// the tests offline (AGENTS.md).
package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Get issues a GET and returns the body, which the caller closes.
//
// A non-200 is an error rather than a body, and it closes the body itself: an
// HTML error page decoded as the document is the ordinary way this goes wrong,
// and the status is the only part of the response worth reporting.
//
// An empty token sends no Authorization header, which is the unauthenticated
// case rather than a mistake - the model catalogue is public.
func Get(ctx context.Context, url, token string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp.Body, nil
}
