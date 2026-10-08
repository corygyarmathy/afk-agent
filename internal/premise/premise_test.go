package premise_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/premise"
)

// The links are the Premises section's, and only its: a heading at any level,
// to the next heading at its level or above. Each is read once, in the order
// the section gives them.
func TestLinksAreThePremisesSectionsOwn(t *testing.T) {
	body := strings.Join([]string{
		"## What to build",
		"",
		"See https://github.com/o/n/issues/1, which is not a premise.",
		"",
		"## Premises",
		"",
		"- The flag exists ([`flags.go`](https://github.com/up/stream/blob/abc123/cmd/flags.go#L10-L12)).",
		"- Decided in https://github.com/o/n/issues/195#issuecomment-6056615353.",
		"- Waiting on #198, confirm once it closes; and up/stream#4.",
		"- The same comment again: https://github.com/o/n/issues/195#issuecomment-6056615353",
		"",
		"### A note under the section",
		"",
		"- Pull request https://github.com/o/n/pull/203/files.",
		"- Docs at https://example.com/docs.",
		"- A tree: https://github.com/o/n/tree/main/docs",
		"- Review comments: https://github.com/o/n/pull/203#discussion_r1, https://github.com/o/n/pull/203/files#r2 and https://github.com/o/n/pull/203#pullrequestreview-3",
		"",
		"```",
		"https://github.com/o/n/issues/2 in a fence is not a link",
		"```",
		"",
		"## Open choices",
		"",
		"- #3 is not a premise.",
	}, "\n")

	got := premise.Links(body, "o/n")
	const review = "a review comment on a pull request's diff, or a review, which the agent does not fetch"
	want := []premise.Link{
		{Text: "https://github.com/up/stream/blob/abc123/cmd/flags.go#L10-L12", Repo: "up/stream", Ref: "abc123", Path: "cmd/flags.go"},
		{Text: "https://github.com/o/n/issues/195#issuecomment-6056615353", Repo: "o/n", Number: 195, Comment: 6056615353},
		{Text: "#198", Repo: "o/n", Number: 198},
		{Text: "up/stream#4", Repo: "up/stream", Number: 4},
		{Text: "https://github.com/o/n/pull/203/files", Repo: "o/n", Number: 203},
		{Text: "https://example.com/docs", Unread: "not on GitHub, so the agent does not fetch it"},
		{Text: "https://github.com/o/n/tree/main/docs", Unread: "not a file permalink, an issue, a pull request or a comment"},
		{Text: "https://github.com/o/n/pull/203#discussion_r1", Unread: review},
		{Text: "https://github.com/o/n/pull/203/files#r2", Unread: review},
		{Text: "https://github.com/o/n/pull/203#pullrequestreview-3", Unread: review},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Links =\n%+v\nwant\n%+v", got, want)
	}
}

// An issue with no Premises section has no premises, whatever it links.
func TestAnIssueWithNoPremisesSectionHasNone(t *testing.T) {
	for name, body := range map[string]string{
		"no section":          "Fix https://github.com/o/n/blob/main/a.go and #4.",
		"a premises sentence": "## Context\n\nThe premises are https://github.com/o/n/issues/1.",
		"empty":               "",
	} {
		t.Run(name, func(t *testing.T) {
			if got := premise.Links(body, "o/n"); len(got) != 0 {
				t.Errorf("Links = %+v, want none", got)
			}
		})
	}
}

// A heading's typography is forgiven.
func TestThePremisesHeadingIsReadWhateverItsTypography(t *testing.T) {
	for _, h := range []string{"## Premises", "# premises:", "### **Premises**", "## Premises ##"} {
		if got := premise.Links(h+"\n\n- #5\n", "o/n"); len(got) != 1 || got[0].Number != 5 {
			t.Errorf("under %q: Links = %+v, want #5", h, got)
		}
	}
}

// repo is a fixture repository: files by ref and path, threads by number.
type repo struct {
	branch   string
	files    map[string]string
	issues   map[int]github.Issue
	comments map[int][]github.Comment
	fail     error
	asked    int
}

func (r *repo) Issue(_ context.Context, n int) (github.Issue, error) {
	if is, ok := r.issues[n]; ok {
		return is, nil
	}
	return github.Issue{}, &github.StatusError{Method: "GET", URL: "/issues", Code: 404, Status: "404 Not Found"}
}

func (r *repo) Comments(_ context.Context, n int) ([]github.Comment, error) {
	return r.comments[n], nil
}

func (r *repo) DefaultBranch(context.Context) (string, error) {
	if r.fail != nil {
		return "", r.fail
	}
	return r.branch, nil
}

func (r *repo) File(_ context.Context, path, ref string) ([]byte, error) {
	if r.fail != nil {
		return nil, r.fail
	}
	if s, ok := r.files[ref+":"+path]; ok {
		return []byte(s), nil
	}
	return nil, &github.StatusError{Method: "GET", URL: "/contents/" + path, Code: 404, Status: "404 Not Found"}
}

// Each link is written beside the index that lists it: a permalink at its
// revision and at the head, with whether the two differ, and a thread as it is
// now. A link that cannot be fetched is listed as not fetched, and fails
// nothing.
func TestFetchWritesEachPremiseAndAnIndexOfThem(t *testing.T) {
	at := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	own := &repo{
		branch: "master",
		files:  map[string]string{"abc:a.go": "old\n", "master:a.go": "old\n"},
		issues: map[int]github.Issue{195: {Number: 195, State: "open", Title: "Which levers?", Body: "The question."}},
		comments: map[int][]github.Comment{195: {
			{ID: 1, Login: "someone", Body: "First.", CreatedAt: at},
			{ID: 2, Login: "owner", Body: "**Resolution.** One lever.", CreatedAt: at.Add(time.Hour)},
		}},
	}
	up := &repo{branch: "main", files: map[string]string{"v1:docs/x.md": "before\n", "main:docs/x.md": "after\n"}}
	private := &repo{fail: &github.StatusError{Method: "GET", URL: "/repos/p/q", Code: 404, Status: "404 Not Found"}}
	readers := map[string]*repo{"o/n": own, "up/stream": up, "p/q": private}

	links := premise.Links(strings.Join([]string{
		"## Premises",
		"- https://github.com/o/n/blob/abc/a.go",
		"- https://github.com/up/stream/blob/v1/docs/x.md#L3",
		"- https://github.com/o/n/issues/195#issuecomment-2",
		"- https://github.com/p/q/blob/main/secret.go",
		"- #404",
		"- https://example.com",
	}, "\n"), "o/n")

	dir := filepath.Join(t.TempDir(), premise.Dir)
	failed, err := premise.Fetch(context.Background(), dir, links, func(name string) premise.Reader {
		r := readers[name]
		r.asked++
		return r
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed != 3 {
		t.Errorf("failed = %d, want 3: the private repository, the missing issue, and the link off GitHub", failed)
	}
	if own.asked != 1 {
		t.Errorf("the issue's own repository was asked for %d times, want once for all its links", own.asked)
	}

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return string(b)
	}
	if got := read("1-linked-a.go") + read("1-head-a.go"); got != "old\nold\n" {
		t.Errorf("the first permalink's files = %q", got)
	}
	if got := read("2-linked-x.md") + read("2-head-x.md"); got != "before\nafter\n" {
		t.Errorf("the second permalink's files = %q", got)
	}
	thread := read("3-thread.md")
	for _, want := range []string{
		"# o/n#195: Which levers?",
		"an issue, open.",
		"The question.",
		"**someone, 2026-10-08T09:00:00Z**\n\nFirst.",
		"**owner, 2026-10-08T10:00:00Z** (the comment the premise links to)\n\n**Resolution.** One lever.",
	} {
		if !strings.Contains(thread, want) {
			t.Errorf("the thread has no %q:\n%s", want, thread)
		}
	}

	index := read(premise.Index)
	for _, want := range []string{
		"1. https://github.com/o/n/blob/abc/a.go\n   - `a.go` at the linked revision, `abc`: `1-linked-a.go`\n   - `a.go` at the head of `master`, the default branch: `1-head-a.go`, the same as at the linked revision\n",
		"   - `docs/x.md` at the head of `main`, the default branch: `2-head-x.md`, which differs from the linked revision\n",
		"3. https://github.com/o/n/issues/195#issuecomment-2\n   - o/n#195, an issue, open, as its thread is now: `3-thread.md`, with the comment the link points at marked\n",
		"4. https://github.com/p/q/blob/main/secret.go\n   - not fetched at the linked revision, `main`: GET /repos/p/q: 404 Not Found\n   - not fetched at the head of the default branch, which could not be read: GET /repos/p/q: 404 Not Found\n",
		"5. #404\n   - not fetched: GET /issues: 404 Not Found\n",
		"6. https://example.com\n   - not fetched: not on GitHub, so the agent does not fetch it\n",
	} {
		if !strings.Contains(index, want) {
			t.Errorf("the index has no %q:\n%s", want, index)
		}
	}
}

// A fetch starts afresh: what an earlier fetch left is gone, so a fetch that
// stops part way leaves no index, and the next one does the whole of it again.
func TestFetchStartsAfresh(t *testing.T) {
	dir := filepath.Join(t.TempDir(), premise.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1-thread.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := premise.Fetch(context.Background(), dir, nil, func(string) premise.Reader { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1-thread.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an earlier fetch's file is still there: %v", err)
	}
}

// flaky is a repository that fails its first read of each thread and of each
// file, as GitHub might for a moment, and counts every read.
type flaky struct {
	repo
	failed map[string]bool
	reads  int
}

func (r *flaky) once(key string) error {
	r.reads++
	if r.failed == nil {
		r.failed = map[string]bool{}
	}
	if !r.failed[key] {
		r.failed[key] = true
		return &github.StatusError{Method: "GET", URL: "/" + key, Code: 502, Status: "502 Bad Gateway"}
	}
	return nil
}

func (r *flaky) Issue(ctx context.Context, n int) (github.Issue, error) {
	if err := r.once(fmt.Sprint("issue ", n)); err != nil {
		return github.Issue{}, err
	}
	return r.repo.Issue(ctx, n)
}

func (r *flaky) File(ctx context.Context, path, ref string) ([]byte, error) {
	if err := r.once(ref + ":" + path); err != nil {
		return nil, err
	}
	return r.repo.File(ctx, path, ref)
}

// A link the earlier fetch read all of is kept as it was read, and one it
// could not read, or read only part of, is fetched again, so a failure that
// was GitHub's for a moment is not the rest of the job's.
func TestFetchAgainReadsOnlyWhatWasNotRead(t *testing.T) {
	r := &flaky{repo: repo{
		branch: "main",
		files:  map[string]string{"abc:a.go": "linked\n", "main:a.go": "head\n"},
		issues: map[int]github.Issue{5: {Number: 5, State: "open", Title: "Five"}},
	}}
	links := premise.Links("## Premises\n\n- #5\n- https://github.com/o/n/blob/abc/a.go\n", "o/n")
	dir := filepath.Join(t.TempDir(), premise.Dir)
	fetch := func() (int, string) {
		t.Helper()
		failed, err := premise.Fetch(context.Background(), dir, links, func(string) premise.Reader { return r })
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, premise.Index))
		if err != nil {
			t.Fatal(err)
		}
		return failed, string(b)
	}

	if failed, index := fetch(); failed != 2 || !strings.Contains(index, "502 Bad Gateway") {
		t.Fatalf("first fetch: failed = %d, index:\n%s\nwant both links failed for a moment", failed, index)
	}
	failed, index := fetch()
	if failed != 0 || strings.Contains(index, "502") || !strings.Contains(index, "`1-thread.md`") || !strings.Contains(index, "which differs from the linked revision") {
		t.Fatalf("second fetch: failed = %d, index:\n%s\nwant both read in full", failed, index)
	}
	reads := r.reads
	if failed, again := fetch(); failed != 0 || again != index || r.reads != reads {
		t.Errorf("third fetch: failed = %d, %d more reads, index:\n%s\nwant what the second read, read again from nothing", failed, r.reads-reads, again)
	}
}

// A fetch whose context ends is an error, rather than an index that says the
// premises it did not get to could not be read.
func TestACancelledFetchIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &repo{issues: map[int]github.Issue{5: {Number: 5}}}
	dir := filepath.Join(t.TempDir(), premise.Dir)
	if _, err := premise.Fetch(ctx, dir, premise.Links("## Premises\n\n- #5\n", "o/n"), func(string) premise.Reader { return r }); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch = %v, want the context's error", err)
	}
	if _, err := os.Stat(filepath.Join(dir, premise.Index)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a cancelled fetch left an index: %v", err)
	}
}
