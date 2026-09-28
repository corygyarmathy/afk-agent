// Package caught reads what CI caught that the local gate did not, across
// every job, from GitHub (dotfiles ADR 0007 §8, ADR 0006).
//
// Nothing here is recorded by the agent. The gate runs before every push the
// agent makes, so a failed workflow job on a head the agent pushed is a catch,
// and GitHub already keeps both halves: who pushed the head, on the run it
// started, and how each of its jobs finished. A catch is counted once for each
// head and check, however many times the watch read it.
package caught

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
)

// failed is the conclusions of a job that ran and did not pass, and of the
// run that holds one. A cancelled job is not one: CI cancels a pull request's
// run when a newer push makes it obsolete.
var failed = []string{"failure", "timed_out"}

// Tracker is what reading the catches needs of the tracker. *github.Client is
// one.
type Tracker interface {
	WorkflowRuns(ctx context.Context, actor, conclusion string) ([]github.WorkflowRun, int, error)
	WorkflowJobs(ctx context.Context, run int64) ([]github.WorkflowJob, error)
}

// Catch is one check that failed on one head the agent pushed.
type Catch struct {
	Head   string
	Branch string
	Check  string

	// URL is the failed job's page, for a human.
	URL string

	// At is when the run that caught it was created.
	At time.Time
}

// Report is every catch GitHub served, oldest first.
type Report struct {
	Catches []Catch

	// Runs is how many failed runs were read, and Failed how many GitHub
	// says there are. Fewer read than there are means GitHub cut the
	// listing off, and the oldest were not counted.
	Runs, Failed int
}

// Read is every catch on a head login pushed.
func Read(ctx context.Context, t Tracker, login string) (Report, error) {
	var r Report
	seen := map[int64]bool{}
	caught := map[[2]string]bool{}
	for _, conclusion := range failed {
		runs, total, err := t.WorkflowRuns(ctx, login, conclusion)
		if err != nil {
			return Report{}, err
		}
		r.Failed += total
		r.Runs += len(runs)
		for _, run := range runs {
			// A listing paged while new runs land can serve one twice.
			if seen[run.ID] {
				continue
			}
			seen[run.ID] = true
			jobs, err := t.WorkflowJobs(ctx, run.ID)
			if err != nil {
				return Report{}, err
			}
			for _, j := range jobs {
				if !isFailed(j.Conclusion) {
					continue
				}
				// A second run on the same head - a re-run, or another
				// workflow with a job of the same name - is the same catch.
				k := [2]string{run.HeadSHA, j.Name}
				if caught[k] {
					continue
				}
				caught[k] = true
				r.Catches = append(r.Catches, Catch{Head: run.HeadSHA, Branch: run.HeadBranch, Check: j.Name, URL: j.URL, At: run.Created})
			}
		}
	}
	sort.SliceStable(r.Catches, func(i, j int) bool { return r.Catches[i].At.Before(r.Catches[j].At) })
	return r, nil
}

func isFailed(conclusion string) bool {
	for _, c := range failed {
		if conclusion == c {
			return true
		}
	}
	return false
}

// Count is how many times one check was caught.
type Count struct {
	Check string
	N     int
}

// Counts is the catches by check, the most caught first, and by name among
// equals.
func (r Report) Counts() []Count {
	n := map[string]int{}
	for _, c := range r.Catches {
		n[c.Check]++
	}
	out := make([]Count, 0, len(n))
	for check, k := range n {
		out = append(out, Count{Check: check, N: k})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Check < out[j].Check
	})
	return out
}

// Write prints the counts, a line saying what they cover, and, with list,
// every catch.
func (r Report) Write(w io.Writer, list bool) error {
	if list {
		for _, c := range r.Catches {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.At.UTC().Format(time.RFC3339), git.Short(c.Head), c.Branch, c.Check, c.URL); err != nil {
				return err
			}
		}
	}
	for _, c := range r.Counts() {
		if _, err := fmt.Fprintf(w, "%d\t%s\n", c.N, c.Check); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, r.summary()); err != nil {
		return err
	}
	if r.Runs < r.Failed {
		_, err := fmt.Fprintf(w, "GitHub served %d of %d failed runs; the oldest are not counted.\n", r.Runs, r.Failed)
		return err
	}
	return nil
}

func (r Report) summary() string {
	if len(r.Catches) == 0 {
		return "CI has caught nothing the local gate passed."
	}
	heads := map[string]bool{}
	for _, c := range r.Catches {
		heads[c.Head] = true
	}
	first, last := r.Catches[0].At.UTC().Format(time.DateOnly), r.Catches[len(r.Catches)-1].At.UTC().Format(time.DateOnly)
	return fmt.Sprintf("%s on %s, from %s to %s.", plural(len(r.Catches), "catch", "catches"), plural(len(heads), "head", "heads"), first, last)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
