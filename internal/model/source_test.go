package model_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/model"
)

// Nothing in this file reaches the network: every Source under test is given a
// Fetch. That is a property of the seam rather than of the discipline here -
// Source.Fetch is a field precisely so that "the tests do not reach upstream"
// is something the compiler helps with, and scripts/offline-test.sh is what
// proves it.

func body(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/catalogue.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// serve returns a Fetch that yields b, and a counter of how often it was called.
func serve(b []byte) (func(context.Context) (io.ReadCloser, error), *int) {
	var calls int
	return func(context.Context) (io.ReadCloser, error) {
		calls++
		return io.NopCloser(strings.NewReader(string(b))), nil
	}, &calls
}

// fail returns a Fetch that is always down.
func fail(err error) (func(context.Context) (io.ReadCloser, error), *int) {
	var calls int
	return func(context.Context) (io.ReadCloser, error) {
		calls++
		return nil, err
	}, &calls
}

func TestSourceFetchesAndCaches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalogue.json")
	fetch, calls := serve(body(t))
	s := &model.Source{Path: path, Fetch: fetch}

	cat, st, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("fetched %d times", *calls)
	}
	if st.Stale() || st.Age != 0 {
		t.Fatalf("a fresh fetch is not stale, got %+v", st)
	}
	if st.Cache != nil {
		t.Fatalf("the fetch was not cached: %v", st.Cache)
	}
	if cat.Len() != 10 {
		t.Fatalf("got %d models", cat.Len())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nothing was cached: %v", err)
	}
}

// The acceptance criterion: a stale or unreachable catalogue degrades to the
// last good one rather than to an empty candidate list. The two failures look
// identical from the resolver's side and have opposite right answers - a
// day-old price list resolves every job correctly, an empty one fails them all.
func TestSourceDegradesToTheLastGoodCatalogue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalogue.json")
	if err := os.WriteFile(path, body(t), 0o644); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(path, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}

	down := errors.New("dial tcp: no route to host")
	fetch, calls := fail(down)
	s := &model.Source{Path: path, Fetch: fetch, MaxAge: time.Hour}

	cat, st, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("an unreachable upstream with a good cache must not fail: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("the cache was past MaxAge; upstream should have been tried once, was tried %d times", *calls)
	}
	if cat.Len() != 10 {
		t.Fatalf("degraded to %d models rather than to the cached catalogue", cat.Len())
	}
	if st.Age < 23*time.Hour {
		t.Fatalf("staleness was not reported: got %v", st.Age)
	}
	if !st.Stale() || !errors.Is(st.Fetch, down) {
		t.Fatalf("the caller cannot tell this came from the cache: %+v", st)
	}
}

// Upstream returning something that is not a catalogue - an HTML error page
// with a 200 is the ordinary way this goes wrong - is an outage, not a fresh
// catalogue. It must degrade like any other, and it must not overwrite the
// last good copy on the way past.
func TestSourceDoesNotCacheAnUnparseableResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalogue.json")
	good := body(t)
	if err := os.WriteFile(path, good, 0o644); err != nil {
		t.Fatal(err)
	}

	garbage, _ := serve([]byte("<!doctype html><title>502 Bad Gateway</title>"))
	s := &model.Source{Path: path, Fetch: garbage}

	cat, _, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("want the cached catalogue, got: %v", err)
	}
	if cat.Len() != 10 {
		t.Fatalf("got %d models", cat.Len())
	}
	if after, _ := os.ReadFile(path); string(after) != string(good) {
		t.Fatal("the last good catalogue was overwritten by an error page")
	}
}

// A cache inside MaxAge is used without asking upstream at all.
func TestSourceSkipsTheFetchWhileTheCacheIsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalogue.json")
	if err := os.WriteFile(path, body(t), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch, calls := serve(body(t))
	s := &model.Source{Path: path, Fetch: fetch, MaxAge: time.Hour}

	if _, _, err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Fatalf("upstream was asked %d times for a catalogue already in hand", *calls)
	}
}

// No fetch and no cache is the one case where there is nothing to degrade to.
// It fails, naming both halves, rather than resolving against an empty
// catalogue and reporting - accurately and uselessly - that nothing qualified.
func TestSourceFailsWithNeitherFetchNorCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalogue.json")
	fetch, _ := fail(errors.New("dial tcp: no route to host"))
	s := &model.Source{Path: path, Fetch: fetch}

	_, _, err := s.Load(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "no route to host") {
		t.Fatalf("the error should say why the fetch failed: %v", err)
	}
	if !strings.Contains(err.Error(), "no usable cache") {
		t.Fatalf("the error should say there was no cache either: %v", err)
	}
}

// The cache is replaced by rename, so a crash mid-write cannot leave a
// truncated file where the last good catalogue was.
func TestSourceLeavesNoPartialFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalogue.json")
	fetch, _ := serve(body(t))
	s := &model.Source{Path: path, Fetch: fetch}

	if _, _, err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "catalogue.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the state directory holds %v", names)
	}
}

// A cache that cannot be written does not fail a fetch that worked. The
// catalogue in hand resolves every job correctly; what was lost is the
// insurance against the next outage, which is reported rather than raised.
func TestSourceKeepsAFetchItCouldNotCache(t *testing.T) {
	// The cache path is made unusable by putting a *file* where its directory
	// has to be, rather than by permissions: scripts/offline-test.sh runs the
	// tests as a mapped root inside a user namespace, where a read-only
	// directory is not read-only and a permissions test quietly proves nothing.
	blocked := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch, _ := serve(body(t))
	s := &model.Source{Path: filepath.Join(blocked, "catalogue.json"), Fetch: fetch}

	cat, st, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("a good fetch was thrown away because the cache could not be written: %v", err)
	}
	if cat.Len() != 10 {
		t.Fatalf("got %d models", cat.Len())
	}
	if st.Cache == nil {
		t.Fatal("the failed cache write was not reported")
	}
	if st.Stale() {
		t.Fatalf("the catalogue came from upstream and is not stale: %+v", st)
	}
}

// A Source with no cache path is a Source without the insurance, not a Source
// that cannot load. Caching is the fallback's half of the seam; fetching is the
// primary one and does not depend on it.
func TestSourceLoadsWithNoCachePath(t *testing.T) {
	fetch, calls := serve(body(t))
	s := &model.Source{Fetch: fetch}

	cat, st, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("want the fetched catalogue: %v", err)
	}
	if cat.Len() != 10 || *calls != 1 {
		t.Fatalf("got %d models from %d fetches", cat.Len(), *calls)
	}
	if st.Cache == nil {
		t.Fatal("having nowhere to cache should be reported")
	}
}

// A cached copy that no longer parses is not a cache, even inside MaxAge: the
// fetch it was about to skip is the thing that repairs it.
func TestSourceFallsThroughAnUnparseableCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalogue.json")
	if err := os.WriteFile(path, []byte("{ truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch, calls := serve(body(t))
	s := &model.Source{Path: path, Fetch: fetch, MaxAge: time.Hour}

	cat, st, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("a fresh-but-corrupt cache must not fail the load: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("upstream should have been tried once, was tried %d times", *calls)
	}
	if cat.Len() != 10 || st.Cache != nil {
		t.Fatalf("got %d models, cache error %v", cat.Len(), st.Cache)
	}
	if after, _ := os.ReadFile(path); len(after) == len("{ truncated") {
		t.Fatal("the corrupt cache was not replaced by the fetch that succeeded")
	}
}
