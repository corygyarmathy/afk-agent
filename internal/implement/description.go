package implement

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// descriptionFile is where, in the workspace's .git, the session writes its
// part of the pull request's description.
const descriptionFile = "afk-description.md"

// bodyLimit is the most characters GitHub takes in a pull request's body.
// It is GitHub's, not the operator's to set.
const bodyLimit = 65536

// titleLimit is the most characters GitHub takes in a pull request's title.
// It is GitHub's, not the operator's to set.
const titleLimit = 256

// sections are the headings of the session's part, in the order the
// description gives them (#111). The prompt names the same ones.
var sections = []string{
	"Start here",
	"Where the ticket didn't decide",
	"Not verified",
	"Recipe",
}

// readDescription is the session's part as it left it, or nothing.
func readDescription(ws string) (string, error) {
	return work.ReadGitFile(ws, descriptionFile)
}

// readRemainder is what the session says is left, or nothing, which includes
// a file that says only "none".
func readRemainder(ws string) (string, error) {
	text, err := work.ReadGitFile(ws, remainderFile)
	if err != nil || empty([]string{text}) {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(text, "\uFEFF")), nil
}

// sessionPart is the title the session gives for the piece, and its sections,
// in the description's order under the headings it names.
//
// The title is the file's first line, when that is not a heading: the prompt
// asks for one only of work cut to a first piece, whose issue's title
// describes the whole job (#111). It is never part of the body, and only a
// Part of pull request takes it. A Closes one keeps the issue's title,
// whatever the line says. A line too long for GitHub to take is no title
// rather than a cut one, as a body too long is no session's part.
//
// A heading the prompt did not name goes with what is under it, and so does
// anything else before the first heading. A section with nothing to say is
// left out, and without a Start here there is no session's part: the pull
// request opens with Go's parts only, and the diff is still reviewable.
func sessionPart(text string) (title, part string) {
	// A byte-order mark is an editor's, not the first line's.
	text = strings.TrimPrefix(text, "\uFEFF")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if first := strings.TrimSpace(lines[0]); !opensHeading(first) && !strings.HasPrefix(first, "```") {
		if utf8.RuneCountInString(first) <= titleLimit {
			title = first
		}
		lines = lines[1:]
	}

	found := map[string]string{}
	var heading string
	var body []string
	flush := func() {
		if heading != "" && found[heading] == "" && !empty(body) {
			found[heading] = strings.TrimSpace(strings.Join(body, "\n"))
		}
		body = nil
	}
	fenced := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if name, ok := strings.CutPrefix(line, "## "); ok && !fenced {
			flush()
			heading = named(name)
			continue
		}
		body = append(body, line)
	}
	flush()

	if found[sections[0]] == "" {
		return title, ""
	}
	var b strings.Builder
	for _, s := range sections {
		if found[s] != "" {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", s, found[s])
		}
	}
	return title, b.String()
}

// opensHeading says whether line opens a heading, which is one to six #s and
// then a space or nothing: "#127's first piece" is a title that names an issue.
func opensHeading(line string) bool {
	rest := strings.TrimLeft(line, "#")
	n := len(line) - len(rest)
	return n >= 1 && n <= 6 && (rest == "" || rest[0] == ' ' || rest[0] == '\t')
}

// named is the section a heading names, or "" for one the prompt did not. A
// model's typography is forgiven: case, a trailing colon, a curly apostrophe.
func named(heading string) string {
	h := strings.TrimSuffix(strings.TrimSpace(heading), ":")
	h = strings.ReplaceAll(h, "’", "'")
	for _, s := range sections {
		if strings.EqualFold(h, s) {
			return s
		}
	}
	return ""
}

// empty says whether a section has nothing to say, which includes saying
// "none": the prompt asks for such a section to be left out.
func empty(body []string) bool {
	s := strings.TrimSuffix(strings.TrimSpace(strings.Join(body, "\n")), ".")
	return s == "" || strings.EqualFold(s, "none") || strings.EqualFold(s, "n/a")
}

// description is the pull request's body: the marker, the link line, the
// sensitive line and the reminder, which are Go's, then the session's part,
// already linked. The link line is linkLine's.
//
// It is written once, when the pull request opens. Nothing rewrites the
// session's part: a revision changing what the operator already read would
// defeat the reading. The sensitive line is Go's to recompute on every push
// (sensitive.With).
func description(n int, link, procedure, line, session string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n\n", PRMarker(n), link)
	if line != "" {
		b.WriteString(line + "\n\n")
	}
	to := "the agent has no link to the procedure"
	if procedure != "" {
		to = fmt.Sprintf("[procedure](%s)", procedure)
	}
	from := ""
	if session != "" {
		from = " from **Start here**"
	}
	fmt.Fprintf(&b, "%s (%s): read #%d first, then this, then the diff%s. Do your own reading before you open the advisory review. End by merging, sending back in your own words, or closing with one line why.\n", sensitive.Reminder, to, n, from)
	b.WriteString(session)
	return b.String()
}

// over reports whether GitHub would refuse body as a pull request's.
func over(body string) bool {
	return utf8.RuneCountInString(body) > bodyLimit
}
