// Package cli is the command surface of the afk binary.
//
// It parses arguments and nothing else: no store, no scheduler, no network.
// Transitions are not implemented here and will not be - this package exists
// so that the transition runner has somewhere to be invoked from.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
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
// holding it. Exactly one of the three is set. The job store (#1) owns what a
// job id means; this is only how one is spelled on the command line.
type Subject struct {
	Job   string
	Issue string
	PR    string
}

// runCmd implements `afk run <transition> (--job <id> | --issue <n> | --pr <n>)`.
func runCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("afk run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var subject Subject
	fs.StringVar(&subject.Job, "job", "", "job id to run the transition against")
	fs.StringVar(&subject.Issue, "issue", "", "issue number to run the transition against")
	fs.StringVar(&subject.PR, "pr", "", "pull request number to run the transition against")

	// The transition name is positional and comes first, so that the flags
	// after it read as arguments to it rather than to the binary.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usagef("run needs a transition name")
	}
	transition := args[0]
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

	return runTransition(transition, subject, stdout)
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

// runTransition is where the transition runner (#2) will be handed control.
// Until then every transition is unimplemented, and says so.
func runTransition(transition string, subject Subject, stdout io.Writer) error {
	return fmt.Errorf("transition %q is not implemented (%s)", transition, subject)
}

const usage = `afk - an unattended agent that takes work from a tracker and leaves a pull request.

Usage:
  afk run <transition> (--job <id> | --issue <n> | --pr <n>)
  afk version
  afk help

Every transition is invokable on its own, with no daemon present:

  afk run review --pr 12

No transition is implemented yet.
`

func writeUsage(w io.Writer) {
	fmt.Fprint(w, usage)
}
