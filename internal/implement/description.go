package implement

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// descriptionFile is where, in the workspace's .git, the session writes its
// part of the pull request's description.
const descriptionFile = "afk-description.md"

// sections are the headings of the session's part, in the order the
// description gives them (#111). The prompt names the same ones.
var sections = []string{
	"Start here",
	"Where the ticket didn't decide",
	"Not verified",
	"Recipe",
}

// readDescription is the session's part as it left it, or nothing. Only a
// regular file is read: the workspace's .git is the model's to write, and a
// link there would put whatever it points at in the pull request.
func readDescription(ws string) (string, error) {
	root, err := os.OpenRoot(filepath.Join(ws, ".git"))
	if errors.Is(err, fs.ErrNotExist) {
		// The workspace went with the run. The gate is what says so.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer root.Close()
	fi, err := root.Lstat(descriptionFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", nil
	}
	b, err := root.ReadFile(descriptionFile)
	return string(b), err
}

// sessionPart is the session's sections, in the description's order under the
// headings it names. A heading the prompt did not name goes with what is under
// it, and so does anything before the first heading. A section with nothing to
// say is left out, and without a Start here there is no session's part: the
// pull request opens with Go's parts only, and the diff is still reviewable.
func sessionPart(text string) string {
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
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
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
		return ""
	}
	var b strings.Builder
	for _, s := range sections {
		if found[s] != "" {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", s, found[s])
		}
	}
	return b.String()
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

// description is the pull request's body: the marker, the link line and the
// reminder, which are Go's, then the session's part, already linked.
//
// It is written once, when the pull request opens. Nothing rewrites it: a
// revision changing what the operator already read would defeat the reading.
func description(n int, procedure, session string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nCloses #%d\n\n", PRMarker(n), n)
	link := "the agent has no link to the procedure"
	if procedure != "" {
		link = fmt.Sprintf("[procedure](%s)", procedure)
	}
	from := ""
	if session != "" {
		from = " from **Start here**"
	}
	fmt.Fprintf(&b, "> **Your review** (%s): read #%d first, then this, then the diff%s. Do your own reading before you open the advisory review. End by merging, sending back in your own words, or closing with one line why.\n", link, n, from)
	b.WriteString(session)
	return b.String()
}
