// Package cli is the command surface of the afk binary.
//
// It parses arguments, opens the store, and hands over to the transition
// runner. It holds no state machine of its own: `afk run` reaches exactly the
// runner the dispatcher reaches, because a transition invoked by hand and one
// invoked by the scheduler must be the same thing (ADR 0001 §4).
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/dispatch"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// Exit codes. Usage errors are distinguishable from execution failures so a
// script driving the binary can tell "you asked wrongly" from "it went wrong".
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Version is the build version, overridden at link time.
var Version = "dev"

// errUsage marks an error as the operator's mistake rather than the agent's.
type errUsage struct{ error }

func usagef(format string, a ...any) error {
	return errUsage{fmt.Errorf(format, a...)}
}

// Main runs one invocation of the afk command and returns its exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return ExitUsage
	}

	var err error
	switch cmd := args[0]; cmd {
	case "run":
		err = runCmd(args[1:], stdout)
	case "work":
		err = workCmd(args[1:], stderr)
	case "version":
		_, err = fmt.Fprintln(stdout, Version)
	case "help", "-h", "--help":
		writeUsage(stdout)
	default:
		err = usagef("unknown command %q", cmd)
	}

	if err != nil {
		fmt.Fprintln(stderr, "afk:", err)
		var u errUsage
		if errors.As(err, &u) {
			writeUsage(stderr)
			return ExitUsage
		}
		return ExitError
	}
	return ExitOK
}

// Subject is the tracker subject a transition is invoked against, or the job
// holding it. Exactly one of the three is set.
type Subject struct {
	Job   string
	Issue string
	PR    string
}

func (s Subject) count() int {
	n := 0
	for _, v := range []string{s.Job, s.Issue, s.PR} {
		if v != "" {
			n++
		}
	}
	return n
}

// String renders a subject the way it was spelled on the command line.
func (s Subject) String() string {
	switch {
	case s.Job != "":
		return "job " + s.Job
	case s.Issue != "":
		return "issue " + s.Issue
	case s.PR != "":
		return "pr " + s.PR
	}
	return "no subject"
}

// tracker is the store's spelling of this subject, or false if the subject is a
// job id rather than a tracker number. Parsing here rather than at the point of
// use keeps a mistyped number a usage error, reported before anything is
// opened.
func (s Subject) tracker() (store.Subject, bool, error) {
	if s.Job != "" {
		return store.Subject{}, false, nil
	}

	subject := store.Subject{Type: store.SubjectIssue}
	num := s.Issue
	if s.PR != "" {
		subject.Type, num = store.SubjectPR, s.PR
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 {
		return store.Subject{}, false, usagef("%q is not a %s number", num, subject.Type)
	}
	subject.Number = n
	return subject, true, nil
}

// jobID is the id of the job this subject names, creating the job in the
// transition's own starting state if the subject has never been worked.
//
// That is what makes a transition invokable against a tracker subject rather
// than only against a job id: the store's id is derived from the kind and the
// subject (store.ID), so naming the subject names the job whether or not it is
// there yet.
func (s Subject) jobID(ctx context.Context, st store.Store, t transition.Transition, now time.Time) (string, error) {
	subject, ok, err := s.tracker()
	if err != nil {
		return "", err
	}
	if !ok {
		return s.Job, nil
	}
	job, err := st.Ensure(ctx, t.Kind, subject, t.From, now)
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

// runCmd implements `afk run <transition> (--job <id> | --issue <n> | --pr <n>)`.
func runCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("afk run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		subject Subject
		p       params
	)
	fs.StringVar(&subject.Job, "job", "", "job id to run the transition against")
	fs.StringVar(&subject.Issue, "issue", "", "issue number to run the transition against")
	fs.StringVar(&subject.PR, "pr", "", "pull request number to run the transition against")
	p.bindStore(fs)
	p.bindLease(fs)
	p.bindBackoff(fs)

	// The transition name is positional and comes first, so that the flags
	// after it read as arguments to it rather than to the binary.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usagef("run needs a transition name")
	}
	name := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage{err}
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usagef("unexpected argument %q", rest[0])
	}

	switch n := subject.count(); {
	case n == 0:
		return usagef("run needs one of --job, --issue or --pr")
	case n > 1:
		return usagef("run takes only one of --job, --issue or --pr")
	}

	if _, _, err := subject.tracker(); err != nil {
		return err
	}

	reg := catalogue()
	t, ok := reg.Get(name)
	if !ok {
		return usagef("unknown transition %q; %s", name, known(reg))
	}

	path, err := p.storePath()
	if err != nil {
		return err
	}
	lease, err := p.leaseTTL()
	if err != nil {
		return err
	}
	backoff, err := p.backoff()
	if err != nil {
		return err
	}

	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	now := time.Now()
	id, err := subject.jobID(ctx, st, t, now)
	if err != nil {
		return err
	}

	runner := &transition.Runner{
		Store:    st,
		Registry: reg,
		Holder:   holder(),
		LeaseTTL: lease,
		Backoff:  backoff,
	}
	out, err := runner.Run(ctx, name, id)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, out)
	return err
}

// workCmd implements `afk work`, the worker pool. Everything a transition can
// do it can do without this command (ADR 0001 §4); what this adds is several
// of them at once, under the resource tokens that stop the heavy ones from
// overlapping.
func workCmd(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("afk work", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var p params
	p.bindStore(fs)
	p.bindLease(fs)
	p.bindBackoff(fs)
	p.bindPool(fs)

	if err := fs.Parse(args); err != nil {
		return errUsage{err}
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usagef("unexpected argument %q", rest[0])
	}

	path, err := p.storePath()
	if err != nil {
		return err
	}
	lease, err := p.leaseTTL()
	if err != nil {
		return err
	}
	backoff, err := p.backoff()
	if err != nil {
		return err
	}
	workers, poll, tokenWait, capacity, err := p.poolConfig()
	if err != nil {
		return err
	}
	pool, err := transition.NewPool(capacity)
	if err != nil {
		return err
	}

	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()

	d := &dispatch.Dispatcher{
		Store:     st,
		Registry:  catalogue(),
		Pool:      pool,
		Workers:   workers,
		Holder:    holder(),
		LeaseTTL:  lease,
		Poll:      poll,
		TokenWait: tokenWait,
		Backoff:   backoff,
		Log:       func(msg string) { fmt.Fprintln(stderr, msg) },
	}

	// A signal stops the pool between transitions rather than inside one: a
	// worker that has released its lease is doing nothing, and one that has
	// not finishes what it started. Restarting costs at most one transition
	// per worker, which is the property the whole design is for.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// holder names this process in a lease. Host and pid, because the store's
// leases are local (CONTEXT.md: lease) and the pair is unique among the live
// processes that can reach one store.
func holder() string {
	host, err := os.Hostname()
	if err != nil {
		host = "afk"
	}
	return fmt.Sprintf("%s[%d]", host, os.Getpid())
}

// known renders the registered transitions for an error message.
func known(reg *transition.Registry) string {
	names := reg.Names()
	if len(names) == 0 {
		return "no transitions are registered in this build"
	}
	return "known transitions: " + strings.Join(names, ", ")
}

const usage = `afk - an unattended agent that takes work from a tracker and leaves a pull request.

Usage:
  afk run <transition> (--job <id> | --issue <n> | --pr <n>)
  afk work
  afk version
  afk help

Every transition is invokable on its own, with no daemon present:

  afk run review --pr 12

` + paramUsage + `
`

func writeUsage(w io.Writer) {
	fmt.Fprint(w, usage)
}
