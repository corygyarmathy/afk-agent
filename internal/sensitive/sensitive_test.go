package sensitive

import (
	"testing"
)

var list = []Path{
	{Label: "job store schema", Globs: []string{"store/**"}},
	{Label: "CI", Globs: []string{"ci/*.yml"}},
}

// The sensitive paths are globs as the denylist's are, and refused as they
// are. A label is refused if the line could not carry it back out: one line,
// and a list of `label (files)` separated by commas.
func TestMalformedSensitivePathsAreRefused(t *testing.T) {
	for _, bad := range [][]Path{
		{{Label: "", Globs: []string{"a/**"}}},
		{{Label: "a", Globs: nil}},
		{{Label: "a", Globs: []string{"src/[a"}}},
		{{Label: "a", Globs: []string{"/abs"}}},
		{{Label: "a", Globs: []string{"x"}}, {Label: "a", Globs: []string{"y"}}},
		{{Label: "job store\nschema", Globs: []string{"a/**"}}},
		{{Label: "store\tschema", Globs: []string{"a/**"}}},
		{{Label: "store, schema", Globs: []string{"a/**"}}},
		{{Label: "store (schema)", Globs: []string{"a/**"}}},
	} {
		if err := Valid(bad); err == nil {
			t.Errorf("Valid(%q) = nil, want a refusal", bad)
		}
	}
	if err := Valid(list); err != nil {
		t.Errorf("Valid refused a good list: %v", err)
	}
	if err := Valid(nil); err != nil {
		t.Errorf("Valid refused an empty list, which is the feature off: %v", err)
	}
}

// Each label that matched is named in the operator's order, with its files
// listed, or counted for a description the files would take over the limit.
func TestTheLineNamesEachLabelTouched(t *testing.T) {
	touched := Touches(list, []string{"ci/build.yml", "src/x.go", "store/a", "store/b/c"})
	if got, want := Line(touched), "**Sensitive:** job store schema (`store/a`, `store/b/c`), CI (`ci/build.yml`)"; got != want {
		t.Errorf("Line = %q, want %q", got, want)
	}
	if got, want := Counted(touched), "**Sensitive:** job store schema (2 files), CI (1 file)"; got != want {
		t.Errorf("Counted = %q, want %q", got, want)
	}
	if none := Touches(list, []string{"src/x.go"}); Line(none) != "" || Counted(none) != "" {
		t.Errorf("a pull request touching none has a line: %q", Line(none))
	}
}

const (
	top     = "<!-- afk:implement issue=7 -->\nCloses #7\n\n"
	session = "> **Your review** (x): read #7 first.\n\n## Start here\n\n**Sensitive:** is the session's to write here.\n"
)

// The line is put in, replaced or taken out of Go's fixed part, and nothing
// from the reminder on moves.
func TestWithChangesOnlyTheLine(t *testing.T) {
	line := "**Sensitive:** CI (`ci/build.yml`)"
	withLine := top + line + "\n\n" + session
	for name, c := range map[string]struct{ body, line, want string }{
		"added":    {top + session, line, withLine},
		"replaced": {top + "**Sensitive:** old (`x`)\n\n" + session, line, withLine},
		"removed":  {withLine, "", top + session},
		"as it is": {withLine, line, withLine},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := With(c.body, c.line)
			if !ok || got != c.want {
				t.Errorf("With = %q, %v; want %q, true", got, ok, c.want)
			}
		})
	}
	if got, ok := With("A human's description.", line); ok || got != "A human's description." {
		t.Errorf("With on a description with no reminder = %q, %v; want it as it was, false", got, ok)
	}
}

// A reader that is to be unaware of the line reads the description without
// it, and everything else as it was.
func TestStripTakesOutOnlyGosLine(t *testing.T) {
	if got, want := Strip(top+"**Sensitive:** CI (`ci/build.yml`)\n\n"+session), top+session; got != want {
		t.Errorf("Strip = %q, want %q", got, want)
	}
	if got := Strip("**Sensitive:** a human wrote this."); got != "**Sensitive:** a human wrote this." {
		t.Errorf("Strip changed a description the agent did not write: %q", got)
	}
}
