package intake_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
)

// reviseIntake is intake with `/revise` registered as a command a review may
// issue too.
func reviseIntake(t *testing.T, s store.Store, tr *tracker) *intake.Intake {
	t.Helper()
	in := intakeFor(t, s, tr)
	in.Commands = append(in.Commands, intake.Command{Word: "/revise", On: store.SubjectPR, Kind: store.KindRevise, Start: "start", Reviews: true})
	return in
}

func review(id int64, state, body string) github.Review {
	return github.Review{ID: id, NodeID: fmt.Sprintf("PRR_%d", id), Login: "alice", Association: "OWNER", State: state, Body: body}
}

// A submitted review whose body starts /revise is the command, whatever the
// review says: approving it or requesting changes decides nothing. A pending
// review is not submitted.
func TestASubmittedReviewStartingTheWordIsTheCommand(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  bool
	}{
		{"CHANGES_REQUESTED", true},
		{"COMMENTED", true},
		{"APPROVED", true},
		{"DISMISSED", true},
		{"PENDING", false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			s := storetest.Open(t)
			tr := &tracker{prs: []int{12}, reviews: map[int][]github.Review{12: {review(5, tc.state, "/revise\nrename it")}}}
			made := pass(t, reviseIntake(t, s, tr))
			if got := len(made) == 1 && made[0].ID == "revise-pr-12"; got != tc.want {
				t.Errorf("made due %v, want revise-pr-12: %v", ids(made), tc.want)
			}
		})
	}
}

// A review is armed once, keyed on its own id, and a claimed review is
// answered.
func TestAReviewCommandIsArmedOnceAndAClaimedOneNever(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{prs: []int{12, 13}, reviews: map[int][]github.Review{
		12: {review(5, "COMMENTED", "/revise")},
		13: {review(6, "COMMENTED", "/revise")},
	}, reviewEyes: map[string][]github.Reaction{"PRR_6": {{Login: agent, Content: intake.Claim}}}}
	in := reviseIntake(t, s, tr)

	made := pass(t, in)
	if got := ids(made); len(got) != 1 || got[0] != "revise-pr-12" {
		t.Fatalf("made due %v, want [revise-pr-12]: 13's review is claimed", got)
	}
	armed, err := s.Reserved(context.Background(), intake.ReviewKey(5))
	if err != nil || !armed {
		t.Errorf("review 5's key reserved = %v, %v; want true", armed, err)
	}
	if again := pass(t, in); len(again) != 0 {
		t.Errorf("the next pass made %v due again", ids(again))
	}
}

// Only a writer who is not the agent issues a command by review, as by
// comment, and a review that does not start with the word is none.
func TestOnlyAWritersReviewStartingTheWordIsACommand(t *testing.T) {
	for name, r := range map[string]github.Review{
		"a contributor":   {ID: 5, NodeID: "PRR_5", Login: "dave", Association: "CONTRIBUTOR", State: "COMMENTED", Body: "/revise"},
		"the agent":       {ID: 5, NodeID: "PRR_5", Login: agent, Association: "OWNER", State: "COMMENTED", Body: "/revise"},
		"another word":    {ID: 5, NodeID: "PRR_5", Login: "alice", Association: "OWNER", State: "COMMENTED", Body: "looks fine /revise"},
		"another command": {ID: 5, NodeID: "PRR_5", Login: "alice", Association: "OWNER", State: "COMMENTED", Body: "/review"},
	} {
		t.Run(name, func(t *testing.T) {
			s := storetest.Open(t)
			tr := &tracker{prs: []int{12}, reviews: map[int][]github.Review{12: {r}}}
			if made := pass(t, reviseIntake(t, s, tr)); len(made) != 0 {
				t.Errorf("made due %v, want nothing", ids(made))
			}
		})
	}
}

// Reviews are a request per pull request, so they are read only when a
// command may be issued as one, and never on an issue.
func TestReviewsAreReadOnlyWhenACommandTakesThem(t *testing.T) {
	s := storetest.Open(t)
	tr := &tracker{issues: []int{7}, prs: []int{12}}
	pass(t, intakeFor(t, s, tr))
	if len(tr.reviewed) != 0 {
		t.Errorf("read the reviews of %v with no command that takes them", tr.reviewed)
	}
	pass(t, reviseIntake(t, storetest.Open(t), tr))
	if len(tr.reviewed) != 1 || tr.reviewed[0] != 12 {
		t.Errorf("read the reviews of %v, want [12]", tr.reviewed)
	}
}

func TestOnlyAPullRequestCommandTakesReviews(t *testing.T) {
	in := intakeFor(t, storetest.Open(t), &tracker{})
	in.Commands = append(in.Commands, intake.Command{Word: "/implement", On: store.SubjectIssue, Kind: store.KindImplement, Start: "start", Reviews: true})
	if _, err := in.Pass(context.Background()); err == nil {
		t.Error("no error for an issue command that takes reviews")
	}
}
