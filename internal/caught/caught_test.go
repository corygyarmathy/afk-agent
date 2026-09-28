package caught_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/caught"
	"github.com/corygyarmathy/afk-agent/internal/github"
)

const login = "afk[bot]"

// tracker serves runs by conclusion and jobs by run, and remembers who was
// asked about.
type tracker struct {
	runs    map[string][]github.WorkflowRun
	totals  map[string]int
	jobs    map[int64][]github.WorkflowJob
	started int
	actors  []string
	read    []int64
	err     error
}

func (t *tracker) WorkflowRuns(_ context.Context, actor, conclusion string) ([]github.WorkflowRun, int, error) {
	t.actors = append(t.actors, actor)
	total, ok := t.totals[conclusion]
	if !ok {
		total = len(t.runs[conclusion])
	}
	return t.runs[conclusion], total, t.err
}

func (t *tracker) WorkflowRunCount(_ context.Context, actor string) (int, error) {
	t.actors = append(t.actors, actor)
	return t.started, t.err
}

func (t *tracker) WorkflowJobs(_ context.Context, run int64) ([]github.WorkflowJob, error) {
	t.read = append(t.read, run)
	return t.jobs[run], nil
}

func at(day, hour int) time.Time { return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC) }

// A failed or timed-out job on a head the agent pushed is a catch; a job that
// passed, was skipped or was cancelled beside it is not.
func TestAFailedOrTimedOutJobIsACatch(t *testing.T) {
	tr := &tracker{
		runs: map[string][]github.WorkflowRun{
			"failure":   {{ID: 1, Event: "pull_request", HeadSHA: "aaa", HeadBranch: "afk/issue-7", Created: at(20, 10)}},
			"timed_out": {{ID: 2, Event: "pull_request", HeadSHA: "bbb", HeadBranch: "afk/issue-8", Created: at(21, 10)}},
		},
		jobs: map[int64][]github.WorkflowJob{
			1: {{Name: "test", Conclusion: "failure", URL: "u1"}, {Name: "vet", Conclusion: "success"}, {Name: "lint", Conclusion: "cancelled"}, {Name: "deploy", Conclusion: "skipped"}},
			2: {{Name: "test", Conclusion: "timed_out", URL: "u2"}},
		},
	}

	r, err := caught.Read(context.Background(), tr, login)
	if err != nil {
		t.Fatal(err)
	}
	want := []caught.Catch{
		{Head: "aaa", Branch: "afk/issue-7", Check: "test", URL: "u1", At: at(20, 10)},
		{Head: "bbb", Branch: "afk/issue-8", Check: "test", URL: "u2", At: at(21, 10)},
	}
	if !reflect.DeepEqual(r.Catches, want) {
		t.Errorf("got %+v\nwant %+v", r.Catches, want)
	}
	for _, a := range tr.actors {
		if a != login {
			t.Errorf("asked for %q's runs, want %q's", a, login)
		}
	}
}

// However many runs saw one check fail on one head - a re-run, a second
// workflow with a job of the same name, a run served twice - it is one catch.
// The same check on the next head is the next catch.
func TestACheckIsCaughtOnceForEachHead(t *testing.T) {
	tr := &tracker{
		runs: map[string][]github.WorkflowRun{
			"failure": {
				{ID: 1, Event: "pull_request", HeadSHA: "aaa", Created: at(20, 10)},
				{ID: 2, Event: "pull_request", HeadSHA: "aaa", Created: at(20, 11)},
				{ID: 2, Event: "pull_request", HeadSHA: "aaa", Created: at(20, 11)},
				{ID: 3, Event: "pull_request", HeadSHA: "bbb", Created: at(20, 12)},
			},
		},
		jobs: map[int64][]github.WorkflowJob{
			1: {{Name: "test", Conclusion: "failure"}},
			2: {{Name: "test", Conclusion: "failure"}, {Name: "vet", Conclusion: "failure"}},
			3: {{Name: "test", Conclusion: "failure"}},
		},
	}

	r, err := caught.Read(context.Background(), tr, login)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range r.Catches {
		got = append(got, c.Head+" "+c.Check)
	}
	if want := []string{"aaa test", "aaa vet", "bbb test"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if counts := r.Counts(); !reflect.DeepEqual(counts, []caught.Count{{Check: "test", N: 2}, {Check: "vet", N: 1}}) {
		t.Errorf("counts %+v", counts)
	}
}

func TestReadingStopsAtTheTrackersError(t *testing.T) {
	boom := errors.New("boom")
	_, err := caught.Read(context.Background(), &tracker{err: boom}, login)
	if !errors.Is(err, boom) {
		t.Errorf("got %v, want %v", err, boom)
	}
}

func TestTheReportCountsByCheckAndSaysWhatItCovers(t *testing.T) {
	r := caught.Report{
		Catches: []caught.Catch{
			{Head: "aaaaaaaaaaaaaaaa", Branch: "afk/issue-7", Check: "vet", URL: "u1", At: at(20, 10)},
			{Head: "aaaaaaaaaaaaaaaa", Branch: "afk/issue-7", Check: "test", URL: "u2", At: at(20, 10)},
			{Head: "bbbbbbbbbbbbbbbb", Branch: "afk/issue-8", Check: "test", URL: "u3", At: at(27, 9)},
		},
		Login: login, Started: 40,
		Runs: 2, Listed: 2,
	}

	var b strings.Builder
	if err := r.Write(&b, false); err != nil {
		t.Fatal(err)
	}
	want := "2\ttest\n1\tvet\n3 catches on 2 heads, from 2026-09-20 to 2026-09-27, of 40 runs afk[bot] started.\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}

	b.Reset()
	if err := r.Write(&b, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "2026-09-20T10:00:00Z\taaaaaaaaaaaa\tafk/issue-7\tvet\tu1\n") || !strings.HasSuffix(b.String(), want) {
		t.Errorf("list got\n%s", b.String())
	}
}

func TestAReportOfNothingSaysSo(t *testing.T) {
	var b strings.Builder
	if err := (caught.Report{Login: login, Started: 1}).Write(&b, true); err != nil {
		t.Fatal(err)
	}
	if want := "CI has caught nothing the local gate passed, of 1 run afk[bot] started.\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// A login that matches no run reads exactly like an agent CI never caught, so
// a report of no runs at all says that it may be the login.
func TestAReportOfNoRunsAtAllSaysToCheckTheLogin(t *testing.T) {
	r, err := caught.Read(context.Background(), &tracker{}, login)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := r.Write(&b, false); err != nil {
		t.Fatal(err)
	}
	want := "CI has caught nothing the local gate passed, of 0 runs afk[bot] started.\n" +
		"GitHub serves no run afk[bot] started: check that it is the agent's login.\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}

// A run the agent's push did not start on its head - a workflow on an issue
// it claimed, or a comment it wrote - ran on the default branch, where the
// gate never ran, so its jobs are not read.
func TestARunOnAnotherEventIsNotACatch(t *testing.T) {
	tr := &tracker{
		runs: map[string][]github.WorkflowRun{
			"failure": {
				{ID: 1, Event: "issue_comment", HeadSHA: "main", HeadBranch: "main", Created: at(20, 10)},
				{ID: 2, Event: "issues", HeadSHA: "main", HeadBranch: "main", Created: at(20, 11)},
				{ID: 3, Event: "dynamic", HeadSHA: "aaa", HeadBranch: "refs/pull/7/head", Created: at(20, 12)},
				{ID: 4, Event: "push", HeadSHA: "bbb", HeadBranch: "afk/issue-8", Created: at(20, 13)},
			},
		},
		jobs: map[int64][]github.WorkflowJob{
			1: {{Name: "triage", Conclusion: "failure"}},
			2: {{Name: "triage", Conclusion: "failure"}},
			3: {{Name: "Analyze (go)", Conclusion: "failure"}},
			4: {{Name: "test", Conclusion: "failure"}},
		},
	}
	r, err := caught.Read(context.Background(), tr, login)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range r.Catches {
		got = append(got, c.Head+" "+c.Check)
	}
	if want := []string{"aaa Analyze (go)", "bbb test"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if want := []int64{3, 4}; !reflect.DeepEqual(tr.read, want) {
		t.Errorf("read the jobs of runs %v, want only %v", tr.read, want)
	}
}

// A human's push cancels the run on the agent's head, after one of its jobs
// has already failed there. The failed job is a catch; the jobs cancelled
// around it are not.
func TestAFailedJobInACancelledRunIsACatch(t *testing.T) {
	tr := &tracker{
		runs: map[string][]github.WorkflowRun{
			"cancelled": {{ID: 1, Event: "pull_request", HeadSHA: "aaa", HeadBranch: "afk/issue-7", Created: at(20, 10)}},
		},
		jobs: map[int64][]github.WorkflowJob{
			1: {{Name: "test", Conclusion: "failure", URL: "u1"}, {Name: "vet", Conclusion: "cancelled"}},
		},
	}
	r, err := caught.Read(context.Background(), tr, login)
	if err != nil {
		t.Fatal(err)
	}
	want := []caught.Catch{{Head: "aaa", Branch: "afk/issue-7", Check: "test", URL: "u1", At: at(20, 10)}}
	if !reflect.DeepEqual(r.Catches, want) {
		t.Errorf("got %+v\nwant %+v", r.Catches, want)
	}
}

// What the watch logs and what this counts are one definition.
func TestACatchIsAFailedOrTimedOutCheck(t *testing.T) {
	for conclusion, want := range map[string]bool{
		"failure": true, "timed_out": true,
		"cancelled": false, "action_required": false, "startup_failure": false, "stale": false,
		"success": false, "neutral": false, "skipped": false,
	} {
		if got := caught.IsCatch(conclusion); got != want {
			t.Errorf("IsCatch(%q) = %v, want %v", conclusion, got, want)
		}
	}
}

// GitHub serves a filtered listing only so far. A report that did not see
// every failed run says so, rather than passing a partial count off as the
// whole.
func TestAReportGitHubCutOffSaysSo(t *testing.T) {
	tr := &tracker{
		runs:   map[string][]github.WorkflowRun{"failure": {{ID: 1, Event: "pull_request", HeadSHA: "aaa", Created: at(20, 10)}}},
		totals: map[string]int{"failure": 1200},
		jobs:   map[int64][]github.WorkflowJob{1: {{Name: "test", Conclusion: "failure"}}},
	}
	r, err := caught.Read(context.Background(), tr, login)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := r.Write(&b, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "GitHub served 1 of 1200 runs that did not pass; the oldest are not counted.") {
		t.Errorf("got\n%s", b.String())
	}
}
