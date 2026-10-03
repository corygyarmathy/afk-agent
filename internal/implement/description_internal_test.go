package implement

import (
	"os"
	"strings"
	"testing"
)

// The file's first line, when it is not a heading, is the session's title for
// the piece, and never part of the body. Anything else before the first
// heading is still dropped, and so is a line too long for GitHub to take as a
// title.
func TestTheDescriptionsFirstLineIsTheTitle(t *testing.T) {
	sections := "\n## Start here\n\nok:1\n"
	for name, c := range map[string]struct{ text, title, part string }{
		"titled":          {"Reserve a job's first piece\n\n## Start here\n\nok:1\n", "Reserve a job's first piece", sections},
		"titled, crlf":    {"Reserve a job's first piece\r\n## Start here\r\nok:1\r\n", "Reserve a job's first piece", sections},
		"titled, padded":  {"  Reserve a job's first piece  \n## Start here\n\nok:1\n", "Reserve a job's first piece", sections},
		"then a preamble": {"Reserve a job's first piece\nA preamble.\n\n## Start here\n\nok:1\n", "Reserve a job's first piece", sections},
		"a heading":       {"## Start here\n\nok:1\n", "", sections},
		"another heading": {"# Reserve a job\n\n## Start here\n\nok:1\n", "", sections},
		"an issue number": {"#127's first piece\n\n## Start here\n\nok:1\n", "#127's first piece", sections},
		"a bare heading":  {"#\n\n## Start here\n\nok:1\n", "", sections},
		"a deep heading":  {"###### Reserve a job\n\n## Start here\n\nok:1\n", "", sections},
		"a bom":           {"\uFEFFReserve a job's first piece\n\n## Start here\n\nok:1\n", "Reserve a job's first piece", sections},
		"a bom, heading":  {"\uFEFF## Start here\n\nok:1\n", "", sections},
		"at the limit":    {strings.Repeat("é", titleLimit) + "\n\n## Start here\n\nok:1\n", strings.Repeat("é", titleLimit), sections},
		"over the limit":  {strings.Repeat("é", titleLimit+1) + "\n\n## Start here\n\nok:1\n", "", sections},
		"a blank line":    {"\nReserve a job's first piece\n\n## Start here\n\nok:1\n", "", sections},
		"a fence":         {"```\nx\n```\n## Start here\n\nok:1\n", "", sections},
		"no start here":   {"Reserve a job's first piece\n\n## Not verified\n\n- Needs a host run.\n", "Reserve a job's first piece", ""},
		"nothing":         {"", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			title, part := sessionPart(c.text)
			if title != c.title || part != c.part {
				t.Errorf("sessionPart(%q) = %q, %q; want %q, %q", c.text, title, part, c.title, c.part)
			}
		})
	}
}

// The implement skill names the headings of the report the prompt asks for,
// and the skill is vendored rather than written here, so a re-vendor that
// renames one would leave that section out of every description.
func TestTheSkillNamesEverySection(t *testing.T) {
	skill, err := os.ReadFile("../../.agents/skills/implement/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sections {
		if !strings.Contains(string(skill), "`## "+s+"`") {
			t.Errorf("the implement skill does not name the section %q", s)
		}
	}
}
