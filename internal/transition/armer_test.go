package transition_test

import (
	"context"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

var now = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

// asker arms under a lease of its own, the way another job asks for work.
func asker(s store.Store) transition.Armer {
	return transition.Armer{Store: s, Holder: "an-asking-job", LeaseTTL: time.Minute}
}

var askedPR = store.Subject{Type: store.SubjectPR, Number: 12}

func TestAskCreatesAMissingJobDue(t *testing.T) {
	s := openStore(t)
	if err := asker(s).Ask(context.Background(), store.KindReview, askedPR, "start", now); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	j := storetest.Job(t, s, "review-pr-12")
	if j.State != "start" || !j.NextRunAt.Equal(now) || j.Lease != nil {
		t.Errorf("job = %s due %v leased %v, want start due %v unleased", j.State, j.NextRunAt, j.Lease != nil, now)
	}
}

func TestAskArmsAJobAtRestInStart(t *testing.T) {
	s := openStore(t)
	seeded := seed(t, s, store.KindReview, 12, "start")
	storetest.Rest(t, s, seeded.ID, "start", 2, now)

	if err := asker(s).Ask(context.Background(), store.KindReview, askedPR, "start", now); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	j := storetest.Job(t, s, seeded.ID)
	if j.State != "start" || j.Attempts != 0 || !j.NextRunAt.Equal(now) || j.Lease != nil {
		t.Errorf("job = %s attempts %d due %v leased %v, want start attempts 0 due %v unleased", j.State, j.Attempts, j.NextRunAt, j.Lease != nil, now)
	}
}

func TestAskLeavesAParkedJobAlone(t *testing.T) {
	s := openStore(t)
	seeded := seed(t, s, store.KindReview, 12, "start")
	storetest.Rest(t, s, seeded.ID, "posting", 3, now)

	if err := asker(s).Ask(context.Background(), store.KindReview, askedPR, "start", now); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	j := storetest.Job(t, s, seeded.ID)
	if j.State != "posting" || j.Attempts != 3 || !j.NextRunAt.IsZero() || j.Lease != nil {
		t.Errorf("job = %s attempts %d due %v leased %v, want it parked in posting with 3 attempts, unscheduled and unleased", j.State, j.Attempts, j.NextRunAt, j.Lease != nil)
	}
}

// Arming starts the work afresh, so it ends the job's episode of an exhausted
// tier. Left standing, the episode would count the first exhaustion of the new
// work into an old one, and tell it at once.
func TestArmingEndsAnEpisode(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seeded := seed(t, s, store.KindReview, 12, "start")
	storetest.Rest(t, s, seeded.ID, "posting", 0, now)
	if _, err := s.CountEpisode(ctx, seeded.ID, store.Episode{Running: "start", Deferred: "deferred", Since: now}); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := asker(s).Restart(ctx, store.KindReview, askedPR, "start", now, "restart-1"); err != nil || !ok {
		t.Fatalf("Restart = %v, %v; want armed", ok, err)
	}
	if _, ok, err := s.Episode(ctx, seeded.ID); err != nil || ok {
		t.Errorf("Episode after arming = ok %v, %v; want none", ok, err)
	}
}
