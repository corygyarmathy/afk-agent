// Command afk runs the agent's transitions.
//
// Every transition is invokable standalone, with no daemon present
// (ADR 0001 §4), so this command is the whole operator-facing surface.
package main

import (
	"os"

	"github.com/corygyarmathy/afk-agent/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
