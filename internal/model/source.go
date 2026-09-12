package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/fetch"
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

// Staleness is what a Load had to settle for, and is not an error.
//
// A caller that wants to say something about a day-old catalogue has the
// numbers; one that does not can ignore the value entirely and still get a
// working resolution, which is the point. Every field is the zero value after a
// fetch that worked and cached cleanly.
type Staleness struct {
	// Age is how old the returned catalogue is, and zero after a fresh fetch.
	Age time.Duration

	// Fetch is why upstream was not used, when the answer came from the cache
	// instead. Non-nil means the catalogue is the last good one rather than
	// the current one.
	Fetch error

	// Cache is why a fresh catalogue was not written to the cache. The
	// resolution it came back with is unaffected - what is lost is the
	// insurance against the *next* outage, which is a different day's problem
	// and not a reason to fail this one.
	Cache error
}

// Stale reports whether the catalogue came from the cache rather than upstream.
func (s Staleness) Stale() bool { return s.Fetch != nil }

// Load returns the catalogue, refreshing the cache if it is due, and falling
// back to the cached copy if upstream cannot be reached or does not parse.
//
// It fails only when there is nothing usable at all - no fetch and no cache.
// That is the one case where continuing would mean resolving against an empty
// catalogue, and an error naming both failures is more use than a candidate
// list that is empty for reasons nobody can see. In particular a cache that
// cannot be written does not fail a fetch that worked: the catalogue in hand
// resolves every job correctly, and refusing it because the next outage will be
// worse is trading a real answer for a hypothetical one.
func (s *Source) Load(ctx context.Context) (*Catalogue, Staleness, error) {
	if age, err := s.cacheAge(); err == nil && s.MaxAge > 0 && age < s.MaxAge {
		// Decoded only now that the cache is known to be the answer. The age
		// is a stat; the decode is several megabytes, and doing it before the
		// fetch is decided spends it on every Load that goes upstream anyway.
		if cached, cachedAge, err := s.loadCache(); err == nil {
			return cached, Staleness{Age: cachedAge}, nil
		}
		// A cached copy that no longer parses is not a cache. Falling through
		// to the fetch is the same bargain as an outage, in the other
		// direction: the file is replaced once upstream answers.
	}

	fresh, body, fetchErr := s.fetch(ctx)
	if fetchErr == nil {
		// Written after it parsed, never before: a cache is only worth having
		// if what is in it is known good, and a truncated or error-page body
		// saved over the last good copy turns one bad morning into a
		// persistent one.
		return fresh, Staleness{Cache: s.writeCache(body)}, nil
	}

	cached, age, cacheErr := s.loadCache()
	if cacheErr != nil {
		return nil, Staleness{}, fmt.Errorf("no catalogue: fetch failed (%v) and no usable cache (%v)", fetchErr, cacheErr)
	}
	return cached, Staleness{Age: age, Fetch: fetchErr}, nil
}

// fetch retrieves the document, returning the catalogue and the bytes it was
// decoded from.
//
// It decodes before returning so that an unparseable response is a fetch
// failure rather than a fresh catalogue - upstream returning an HTML error page
// with a 200 is the ordinary way this goes wrong, and it must degrade to the
// cache like any other outage. The catalogue comes back rather than being
// thrown away and rebuilt by the caller, because this document is several
// megabytes and decoding it twice to learn the same thing twice is waste the
// daily refresh pays every day.
//
// The bytes come back too, and the whole body is held in memory to produce
// them: the cache is written by rename and there is nothing to rename without
// them.
func (s *Source) fetch(ctx context.Context) (*Catalogue, []byte, error) {
	get := s.Fetch
	if get == nil {
		get = httpFetch
	}
	rc, err := get(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, nil, err
	}
	c, err := DecodeCatalogue(bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	return c, body, nil
}

// httpFetch is the default Fetch: the catalogue is public, so it goes out with
// no token.
func httpFetch(ctx context.Context) (io.ReadCloser, error) {
	return fetch.Get(ctx, Endpoint, "")
}

// cacheAge is how old the cached copy is, without decoding it.
//
// Separate from loadCache because the freshness question is a stat and the
// answer is several megabytes of JSON: a Load that is going upstream anyway has
// no reason to have parsed the file it is about to replace.
func (s *Source) cacheAge() (time.Duration, error) {
	if s.Path == "" {
		return 0, errors.New("no cache path configured")
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		return 0, err
	}
	return s.age(info.ModTime()), nil
}

// age is how long ago mod was, floored at zero: a cached copy stamped in the
// future is odd but is not fresher than now.
func (s *Source) age(mod time.Time) time.Duration {
	if d := s.now().Sub(mod); d > 0 {
		return d
	}
	return 0
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
	return c, s.age(info.ModTime()), nil
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
