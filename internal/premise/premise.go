// Package premise reads an issue's premises for the session that implements
// it (#199).
//
// A premise is an outside fact an issue rests on, stated with its source of
// record (GLOSSARY.md). The implementing session checks each before its first
// edit, and stops on one that does not hold. It has no credentials for the
// tracker or the remote, so it can check only what the agent fetched for it:
// each link in the issue's Premises section, written into the workspace's
// .git beside the issue.
//
// A link that cannot be fetched fails nothing. The index says so, and the
// session lists it as not verified.
package premise

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Link is one link in an issue's Premises section, as the agent reads it.
type Link struct {
	// Text is the link as the issue writes it.
	Text string

	// Repo is the repository it is in, as owner/name. Empty for a link the
	// agent does not fetch, and Unread then says why.
	Repo   string
	Unread string

	// Number is the issue or pull request a thread link names, and Comment
	// the comment on it the link points at, or 0.
	Number  int
	Comment int64

	// Ref and Path are the revision and the file a file permalink names.
	Ref  string
	Path string
}

// File reports whether the link is a file permalink rather than a thread.
func (l Link) File() bool { return l.Path != "" }

// heading is a markdown heading: its level, and its text.
var heading = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t#]*$`)

// address is a URL in prose: up to whitespace, or the end of a markdown
// link or of an autolink.
var address = regexp.MustCompile(`https?://[^\s<>()\[\]]+`)

// short is a reference GitHub links by itself: #N, or owner/name#N. It is
// read from text with its URLs taken out, so a URL's fragment is not one.
var short = regexp.MustCompile(`(?:^|[^\w/#&])(?:([A-Za-z0-9][\w.-]*/[\w.-]+))?#([0-9]+)\b`)

// Links is each link in the issue's Premises section, once, in the order the
// section gives them: a heading named Premises, at any level, to the next
// heading at its level or above. repo is the issue's own repository, which a
// bare #N is in. An issue with no such section has none.
func Links(body, repo string) []Link {
	var links []Link
	seen := map[string]bool{}
	add := func(l Link) {
		key := l.Text
		if l.Repo != "" {
			key = fmt.Sprintf("%s|%d|%d|%s|%s", strings.ToLower(l.Repo), l.Number, l.Comment, l.Ref, l.Path)
		}
		if !seen[key] {
			seen[key] = true
			links = append(links, l)
		}
	}

	level := 0
	fenced := false
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if m := heading.FindStringSubmatch(line); m != nil && !fenced {
			switch {
			case named(m[2]):
				level = len(m[1])
			case level > 0 && len(m[1]) <= level:
				level = 0
			}
			continue
		}
		if level == 0 || fenced {
			continue
		}
		for _, u := range address.FindAllString(line, -1) {
			add(parse(strings.TrimRight(u, ".,;:!?'\"*_`")))
		}
		for _, m := range short.FindAllStringSubmatch(address.ReplaceAllString(line, " "), -1) {
			in := repo
			if m[1] != "" {
				in = m[1]
			}
			n, err := strconv.Atoi(m[2])
			if err != nil || n < 1 {
				continue
			}
			l := Link{Text: strings.TrimLeft(m[0], " \t([*_,;:"), Repo: in, Number: n}
			if in == "" {
				l.Repo, l.Number, l.Unread = "", 0, "a reference with no repository to read it in"
			}
			add(l)
		}
	}
	return links
}

// named reports whether a heading's text names the Premises section. A
// model's or a person's typography is forgiven: case, a trailing colon,
// emphasis.
func named(text string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(text), "*_: "), "Premises")
}

// parse reads a URL as a link the agent fetches: a file permalink, an issue,
// a pull request, or a comment on either. Anything else is a link, but not
// one the agent can fetch.
func parse(raw string) Link {
	l := Link{Text: raw}
	u, err := url.Parse(raw)
	if err != nil {
		l.Unread = "not a URL the agent can read"
		return l
	}
	if host := strings.ToLower(u.Hostname()); host != "github.com" && host != "www.github.com" {
		l.Unread = "not on GitHub, so the agent does not fetch it"
		return l
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 {
		l.Unread = "not a file permalink, an issue, a pull request or a comment"
		return l
	}
	repo := parts[0] + "/" + parts[1]
	switch parts[2] {
	case "blob":
		if len(parts) < 5 {
			break
		}
		path, err := url.PathUnescape(strings.Join(parts[4:], "/"))
		if err != nil {
			break
		}
		l.Repo, l.Ref, l.Path = repo, parts[3], path
		return l
	case "issues", "pull":
		n, err := strconv.Atoi(parts[3])
		if err != nil || n < 1 {
			break
		}
		l.Repo, l.Number = repo, n
		if id, ok := strings.CutPrefix(u.Fragment, "issuecomment-"); ok {
			if c, err := strconv.ParseInt(id, 10, 64); err == nil {
				l.Comment = c
			}
		}
		return l
	}
	l.Unread = "not a file permalink, an issue, a pull request or a comment"
	return l
}
