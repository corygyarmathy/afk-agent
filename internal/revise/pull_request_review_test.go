package revise_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/revise"
)

// written is when the fixture's conversation starts; each comment and review
// in a test is written some minutes after it.
var written = time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return written.Add(time.Duration(minutes) * time.Minute) }

// sendReview is a /revise issued as a submitted review, from the operator.
func sendReview(id int64, state, body string, minutes int) github.PullRequestReview {
	return github.PullRequestReview{
		ID: id, NodeID: fmt.Sprintf("PRR_%d", id), Login: "cory", Association: "OWNER", State: state, Body: body,
		SubmittedAt: at(minutes), URL: fmt.Sprintf("https://github.com/%s/pull/12#pullrequestreview-%d", repo, id),
	}
}

// line is a line comment in a review.
func line(id int64, path string, n int, body string) github.LineComment {
	return github.LineComment{ID: id, Path: path, Line: n, Body: body, URL: fmt.Sprintf("https://github.com/%s/pull/12#discussion_r%d", repo, id)}
}

// sendAt is send, written minutes into the conversation.
func sendAt(id int64, body string, minutes int) github.Comment {
	c := send(id, body)
	c.CreatedAt = at(minutes)
	return c
}

func (tr *tracker) reviewClaimed(id int64) bool {
	return intake.Claimed(tr.reviewEyes[fmt.Sprintf("PRR_%d", id)], agent)
}

// reviewAnswers is the agent's replies to review id.
func (tr *tracker) reviewAnswers(id int64) []string {
	var out []string
	for _, c := range tr.comments[12] {
		if c.Login == agent && strings.Contains(c.Body, owed.PullRequestReviewReplyMarker(id)) {
			out = append(out, c.Body)
		}
	}
	return out
}

// A submitted review whose body starts /revise is a send-back: its body and
// each of its line comments are the points, in order, each line comment with
// where it is. The claim is a 👀 on the review, taken once. What the review
// says decides nothing, and line comments in other reviews are not points.
func TestASubmittedReviewIsASendBackWithItsLineComments(t *testing.T) {
	for _, state := range []string{"CHANGES_REQUESTED", "COMMENTED", "APPROVED"} {
		t.Run(state, func(t *testing.T) {
			f := setup(t)
			f.tr.reviews = []github.PullRequestReview{
				sendReview(4, "COMMENTED", "Looks fine so far.", 1),
				sendReview(5, state, "/revise\nKeep the test.", 2),
			}
			f.tr.lines[4] = []github.LineComment{line(41, "a.go", 1, "Not a point: another review's.")}
			f.tr.lines[5] = []github.LineComment{line(51, "a.go", 3, "Rename this."), line(52, "b.go", 0, "Split this file.")}

			made := f.pass()
			if len(made) != 1 || made[0].ID != "revise-pr-12" {
				t.Fatalf("intake made %v, want one revise job for pull request 12", made)
			}
			job := f.drive()
			if job.State != revise.Revising {
				t.Fatalf("the job is in %q, want revising", job.State)
			}
			if !f.tr.reviewClaimed(5) || f.tr.writes["react-review"] != 1 || f.tr.writes["react"] != 0 {
				t.Errorf("claims: review 5 %v, %d on reviews and %d on comments; want the one on review 5", f.tr.reviewClaimed(5), f.tr.writes["react-review"], f.tr.writes["react"])
			}
			if f.tr.labelled() {
				t.Error("the hand-off label is still on the pull request")
			}
			sb, err := f.deps.Load(job.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := []revise.Point{
				{PullRequestReview: 5, Text: "Keep the test."},
				{PullRequestReview: 5, Text: "Rename this.", Path: "a.go", Line: 3, URL: line(51, "", 0, "").URL},
				{PullRequestReview: 5, Text: "Split this file.", Path: "b.go", URL: line(52, "", 0, "").URL},
			}
			if fmt.Sprint(sb.Points) != fmt.Sprint(want) {
				t.Errorf("points = %+v, want %+v", sb.Points, want)
			}

			// Claimed, the review is answered: the next pass arms nothing, and
			// a claim run again takes nothing.
			if made := f.pass(); len(made) != 0 {
				t.Errorf("a second pass made %v, want nothing", made)
			}
			if f.tr.writes["react-review"] != 1 {
				t.Errorf("%d claims on reviews, want 1", f.tr.writes["react-review"])
			}
		})
	}
}

// A review with nothing after the word and no line comments gets the no-points
// reply, linking it, and nothing else.
func TestAReviewWithNoPointsIsRefused(t *testing.T) {
	f := setup(t)
	f.tr.reviews = []github.PullRequestReview{sendReview(5, "APPROVED", "/revise", 1)}
	f.pass()

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest in start", job.State)
	}
	if !f.tr.reviewClaimed(5) {
		t.Error("the review is not claimed")
	}
	answers := f.tr.reviewAnswers(5)
	if len(answers) != 1 || !strings.Contains(answers[0], "nothing here to revise") || !strings.Contains(answers[0], sendReview(5, "", "", 0).URL) {
		t.Errorf("answers = %q, want one no-points reply linking the review", answers)
	}
	if !f.tr.labelled() {
		t.Error("the hand-off label came off for a refusal")
	}
}

// A review with only line comments has them as its points.
func TestAReviewsLineCommentsAloneArePoints(t *testing.T) {
	f := setup(t)
	f.tr.reviews = []github.PullRequestReview{sendReview(5, "COMMENTED", "/revise", 1)}
	f.tr.lines[5] = []github.LineComment{line(51, "a.go", 3, "Rename this.")}
	f.pass()

	job := f.drive()
	sb, err := f.deps.Load(job.ID)
	if err != nil {
		t.Fatalf("the job is in %q: %v", job.State, err)
	}
	if len(sb.Points) != 1 || sb.Points[0].Text != "Rename this." {
		t.Errorf("points = %+v, want the line comment", sb.Points)
	}
}

// Comments and reviews waiting together are one send-back, in the order they
// were written.
func TestCommentsAndReviewsAreOneSendBackInTheOrderWritten(t *testing.T) {
	f := setup(t)
	f.tr.say(12, sendAt(1, "/revise First.", 1))
	f.tr.say(12, sendAt(3, "/revise Third.", 3))
	f.tr.reviews = []github.PullRequestReview{sendReview(5, "COMMENTED", "/revise Second.", 2)}
	f.pass()

	job := f.drive()
	sb, err := f.deps.Load(job.ID)
	if err != nil {
		t.Fatalf("the job is in %q: %v", job.State, err)
	}
	want := []revise.Point{{Comment: 1, Text: "First."}, {PullRequestReview: 5, Text: "Second."}, {Comment: 3, Text: "Third."}}
	if fmt.Sprint(sb.Points) != fmt.Sprint(want) {
		t.Errorf("points = %+v, want %+v", sb.Points, want)
	}
	if !f.tr.claimed(1) || !f.tr.claimed(3) || !f.tr.reviewClaimed(5) {
		t.Error("not every command is claimed")
	}
}

// A review submitted while a revision was in flight is refused, as a comment
// is: the revision's answer came after it. So is a comment written while a
// revision of a review was in flight.
func TestAReviewWrittenWhileARevisionWasInFlightIsRefused(t *testing.T) {
	t.Run("a review during a comment's revision", func(t *testing.T) {
		f := setup(t)
		f.tr.say(12, sendAt(1, "/revise Earlier.", 1))
		f.tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
		f.tr.reviews = []github.PullRequestReview{sendReview(5, "COMMENTED", "/revise During.", 2)}
		reply := answer(2, 1)
		reply.CreatedAt = at(3)
		f.tr.say(12, reply)
		f.pass()

		if job := f.drive(); job.State != revise.Start {
			t.Fatalf("the job is in %q, want at rest", job.State)
		}
		if answers := f.tr.reviewAnswers(5); len(answers) != 1 || !strings.Contains(answers[0], "in flight") {
			t.Errorf("answers = %q, want one in-flight refusal", answers)
		}
	})
	t.Run("a comment during a review's revision", func(t *testing.T) {
		f := setup(t)
		f.tr.reviews = []github.PullRequestReview{sendReview(5, "COMMENTED", "/revise Earlier.", 1)}
		f.tr.reviewEyes["PRR_5"] = []github.Reaction{{Login: agent, Content: intake.Claim}}
		f.tr.say(12, sendAt(1, "/revise During.", 2))
		f.tr.say(12, github.Comment{ID: 2, Login: agent, Body: owed.PullRequestReviewRevisionMarker(5) + "\nDone.", CreatedAt: at(3)})
		f.pass()

		if job := f.drive(); job.State != revise.Start {
			t.Fatalf("the job is in %q, want at rest", job.State)
		}
		if answers := f.tr.answers(1); len(answers) != 1 || !strings.Contains(answers[0], "in flight") {
			t.Errorf("answers = %q, want one in-flight refusal", answers)
		}
	})
	t.Run("a review after the revision's answer", func(t *testing.T) {
		f := setup(t)
		f.tr.say(12, sendAt(1, "/revise Earlier.", 1))
		f.tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
		reply := answer(2, 1)
		reply.CreatedAt = at(2)
		f.tr.say(12, reply)
		f.tr.reviews = []github.PullRequestReview{sendReview(5, "COMMENTED", "/revise After.", 3)}
		f.pass()

		if job := f.drive(); job.State != revise.Revising {
			t.Fatalf("the job is in %q, want revising", job.State)
		}
	})
}

// linked is a line comment's link in a send-back file.
var linked = regexp.MustCompile(`https://github\.com/\S+#discussion_r\d+`)

// A revision of a review hands its session each line comment with its link,
// and the reply answers the review: it carries the revision's marker for it,
// and identifies each line-comment point by its link.
func TestTheReplyToAReviewLinksEachLineCommentPoint(t *testing.T) {
	f := revisionFixture(t)
	f.tr.live = f.remote
	f.deps.SizeSignal = 1000
	f.tr.reviews = []github.PullRequestReview{sendReview(5, "CHANGES_REQUESTED", "/revise\nKeep the test.", 1)}
	f.tr.lines[5] = []github.LineComment{line(51, "a.go", 3, "Rename this."), line(52, "b.go", 7, "Split this.")}
	var spec string
	f.model.then(func(dir string) error {
		b, err := os.ReadFile(filepath.Join(dir, ".git", "afk-send-back.md"))
		if err != nil {
			return err
		}
		spec = string(b)
		var reply strings.Builder
		reply.WriteString("## Points\n\n- \"Keep the test.\" done.\n")
		for _, link := range linked.FindAllString(spec, -1) {
			fmt.Fprintf(&reply, "- %s done.\n", link)
		}
		return reviseOn("bar.txt", reply.String())(dir)
	})
	f.claim()

	if job := f.finish(); job.State != revise.Start {
		t.Fatalf("the revision is in %q, want at rest\n%s", job.State, f.handBack())
	}
	for _, want := range []string{"`a.go` line 3", "`b.go` line 7", line(51, "", 0, "").URL, line(52, "", 0, "").URL} {
		if !strings.Contains(spec, want) {
			t.Errorf("the send-back file does not give %q:\n%s", want, spec)
		}
	}
	reply, n := f.reply()
	if n != 1 {
		t.Fatalf("%d replies, want one", n)
	}
	for _, want := range []string{owed.PullRequestReviewRevisionMarker(5), line(51, "", 0, "").URL, line(52, "", 0, "").URL} {
		if !strings.Contains(reply.Body, want) {
			t.Errorf("the reply does not carry %q:\n%s", want, reply.Body)
		}
	}
	if strings.Contains(reply.Body, owed.RevisionMarker(5)) {
		t.Errorf("the reply marks comment 5, which is not a command:\n%s", reply.Body)
	}
}

// A review names the commit it was written on. One written on a head the pull
// request has since left is refused, even when it was submitted after the
// revision that moved the head had answered: begun on the old head, its line
// comments' lines are lines there, not in the head the revision would start
// from. So is a review on the head whose line comment was written on another.
func TestAReviewWrittenOnAnotherHeadIsRefused(t *testing.T) {
	t.Run("a review begun before a revision and submitted after its answer", func(t *testing.T) {
		f := setup(t)
		f.tr.say(12, sendAt(1, "/revise Earlier.", 1))
		f.tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
		reply := answer(2, 1)
		reply.CreatedAt = at(2)
		f.tr.say(12, reply)
		r := sendReview(5, "COMMENTED", "/revise", 3)
		r.CommitID = "0ld0000"
		f.tr.reviews = []github.PullRequestReview{r}
		f.tr.lines[5] = []github.LineComment{{ID: 51, Path: "a.go", Line: 3, Body: "Rename this.", CommitID: "0ld0000"}}
		f.pass()

		if job := f.drive(); job.State != revise.Start || !job.NextRunAt.IsZero() {
			t.Fatalf("the job is in %q, want at rest in start", job.State)
		}
		if !f.tr.reviewClaimed(5) {
			t.Error("the review is not claimed")
		}
		answers := f.tr.reviewAnswers(5)
		if len(answers) != 1 || !strings.Contains(answers[0], "written against `0ld0000`") || !strings.Contains(answers[0], r.URL) {
			t.Errorf("answers = %q, want one refusal naming the commit and linking the review", answers)
		}
		if !f.tr.labelled() {
			t.Error("the hand-off label came off for a refusal")
		}
	})
	t.Run("a line comment written on another commit", func(t *testing.T) {
		f := setup(t)
		f.tr.reviews = []github.PullRequestReview{sendReview(5, "COMMENTED", "/revise Keep the test.", 1)}
		f.tr.lines[5] = []github.LineComment{line(51, "a.go", 3, "Rename this."), {ID: 52, Path: "b.go", Line: 9, Body: "Split this.", CommitID: "0ld0000"}}
		f.pass()

		if job := f.drive(); job.State != revise.Start {
			t.Fatalf("the job is in %q, want at rest", job.State)
		}
		if answers := f.tr.reviewAnswers(5); len(answers) != 1 || !strings.Contains(answers[0], "written against `0ld0000`") {
			t.Errorf("answers = %q, want one refusal naming the commit", answers)
		}
	})
}
