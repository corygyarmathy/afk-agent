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
