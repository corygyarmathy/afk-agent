package work

import (
	_ "embed"
	"strings"
)

//go:embed unattended.md
var unattended string

// Unattended is the part of a session's prompt that says how its workspace
// differs from an interactive one, the same for every kind that commits: a
// template named "unattended", given the Branch and the Gate. Parsing it also
// defines "nobody", the line on there being no one to ask, which a review's
// prompt carries alone because a review commits nothing.
var Unattended = strings.TrimSpace(unattended)
