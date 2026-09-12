// Package fetch is the authenticated HTTP this agent makes.
//
// Three packages reach upstream - the model catalogue, the usage endpoint and
// the notification channel - and the request they make is the same request. It
// lives here so that "what a request failure looks like" is decided once: the
// error names the endpoint and the status, and never the token.
//
// It is not a seam and nothing here is injectable. Each caller keeps its own
// Fetch or Post field and this is only what that field defaults to, which is
// what keeps the tests offline (AGENTS.md).
package fetch

import (
	"bytes"
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
	authorise(req, token)
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

// Post issues a POST with the given headers and body, and discards the
// response.
//
// The only caller is the notification channel, and what it needs back is
// whether the message was published. A response body that says something more
// than the status does is a body nobody would act on: a notification that
// failed is logged and the event it reports is on the tracker or in the journal
// already.
//
// The body is drained before it is closed even though it is thrown away, so the
// connection returns to the pool rather than being torn down after every
// notification.
func Post(ctx context.Context, url, token string, header http.Header, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	authorise(req, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return nil
}

// authorise adds the bearer token, if there is one. An empty token sends no
// header at all.
func authorise(req *http.Request, token string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
