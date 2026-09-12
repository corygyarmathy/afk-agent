package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Endpoint is where the capability catalogue comes from.
//
// A constant rather than a parameter: it is not a value expected to change, it
// is the identity of the document this package knows how to read. Pointing it
// elsewhere would mean a different schema, which is a code change and not a
// configuration one. Tests never reach it - Source takes its fetch as a field.
const Endpoint = "https://models.dev/api.json"

// Source is the fetch-and-cache seam: the one thing in this package that
// reaches the network, kept apart from the resolver so the resolver stays pure.
//
// Its job is narrower than it looks. It is not a cache for speed - the document
// is a few megabytes once a day and nobody would notice. It exists so that a
// stale or unreachable catalogue degrades to the last good one rather than to
// an empty candidate list, because those two failures look identical from the
// resolver's side and have opposite right answers: a day-old price list still
// resolves every job correctly, and an empty one fails every job while
// reporting, accurately and uselessly, that nothing qualified.
type Source struct {
	// Path is the cached copy on disk. It belongs in the state directory
	// beside the job store and not in it: the store holds run state only
	// (ADR 0001 §5), and a catalogue is re-derivable from models.dev by
	// definition - putting it in a table is the exact decay
	// TestNoRederivableColumns exists to catch.
	Path string

	// Fetch retrieves the document. Nil means HTTP to Endpoint. A field rather
	// than a package-level function because "the tests do not reach the
	// network" is enforced by scripts/offline-test.sh, and a seam that has to
	// be honoured is better than one that has to be remembered.
	Fetch func(ctx context.Context) (io.ReadCloser, error)

	// MaxAge is how old the cached copy may be before a Load tries upstream.
	// Zero means always try. It is a parameter and comes from configuration.
	MaxAge time.Duration

	// Now is the clock, for tests. Nil means time.Now.
	Now func() time.Time
}

// Load returns the catalogue, refreshing the cache if it is due, and falling
// back to the cached copy if upstream cannot be reached or does not parse.
//
// The second return value is the staleness of what came back: zero after a
// successful fetch, and the age of the cached copy otherwise. A caller that
// wants to say something about a day-old catalogue has the number; one that
// does not can ignore it and still get a working resolution, which is the point.
//
// It fails only when there is nothing usable at all - no fetch and no cache.
// That is the one case where continuing would mean resolving against an empty
// catalogue, and an error naming both failures is more use than a candidate
// list that is empty for reasons nobody can see.
func (s *Source) Load(ctx context.Context) (*Catalogue, time.Duration, error) {
	cached, age, cacheErr := s.loadCache()
	if cacheErr == nil && s.MaxAge > 0 && age < s.MaxAge {
		return cached, age, nil
	}

	fresh, fetchErr := s.fetch(ctx)
	if fetchErr == nil {
		// Written after it parsed, never before: a cache is only worth having
		// if what is in it is known good, and a truncated or error-page body
		// saved over the last good copy turns one bad morning into a
		// persistent one.
		if err := s.writeCache(fresh); err != nil {
			return nil, 0, fmt.Errorf("cache catalogue: %w", err)
		}
		c, err := DecodeCatalogue(bytes.NewReader(fresh))
		if err != nil {
			return nil, 0, err
		}
		return c, 0, nil
	}

	if cacheErr != nil {
		return nil, 0, fmt.Errorf("no catalogue: fetch failed (%v) and no usable cache (%v)", fetchErr, cacheErr)
	}
	return cached, age, nil
}

// fetch retrieves and validates the document, returning its bytes.
//
// It decodes before returning so that an unparseable response is a fetch
// failure rather than a fresh catalogue - upstream returning an HTML error page
// with a 200 is the ordinary way this goes wrong, and it must degrade to the
// cache like any other outage.
func (s *Source) fetch(ctx context.Context) ([]byte, error) {
	get := s.Fetch
	if get == nil {
		get = httpFetch
	}
	rc, err := get(ctx)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	if _, err := DecodeCatalogue(bytes.NewReader(body)); err != nil {
		return nil, err
	}
	return body, nil
}

func httpFetch(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", Endpoint, resp.Status)
	}
	return resp.Body, nil
}

// loadCache reads the cached copy and its age.
func (s *Source) loadCache() (*Catalogue, time.Duration, error) {
	if s.Path == "" {
		return nil, 0, errors.New("no cache path configured")
	}
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	c, err := DecodeCatalogue(f)
	if err != nil {
		return nil, 0, err
	}
	age := s.now().Sub(info.ModTime())
	if age < 0 {
		age = 0
	}
	return c, age, nil
}

// writeCache replaces the cached copy atomically.
//
// Write-then-rename, because the alternative is a crash mid-write leaving a
// truncated file where the last good catalogue was - which is the one outcome
// this whole type exists to avoid.
func (s *Source) writeCache(body []byte) error {
	if s.Path == "" {
		return errors.New("no cache path configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), filepath.Base(s.Path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
