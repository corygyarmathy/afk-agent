package budget_test

import (
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
)

// Only monthly is limited; rolling and weekly read ok. The waiver tests change
// one thing at a time against it.
const monthlyLimited = `{"usage":{
	"rolling":{"status":"ok","percent":12,"resetsAt":"2026-09-11T18:00:00Z"},
	"weekly":{"status":"ok","percent":41,"resetsAt":"2026-09-15T00:00:00Z"},
	"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-27T00:00:00Z"}
}}`

// The acceptance criterion: a waived window at rate-limited admits work, and
// the admission carries the waiver so the operator can be told once about it.
func TestAWaivedWindowAdmits(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	wa := budget.Waiver{Window: "monthly", Until: at(t, "2026-09-27T00:00:00Z")}

	adm := decode(t, monthlyLimited, now).Admit(0, budget.Waivers{wa}, now)
	if !adm.Starts() {
		t.Fatalf("a waived, limited window stopped work: %s", adm)
	}
	if len(adm.Waived) != 1 || adm.Waived[0] != wa {
		t.Fatalf("admitted under %+v, want the one waiver", adm.Waived)
	}

	// Without the waiver the same observation defers: the waiver is the
	// difference, and a test where it changed nothing would pass either way.
	if adm := decode(t, monthlyLimited, now).Admit(0, nil, now); adm.Decision != budget.Defer {
		t.Fatalf("the unwaived window did not defer: %s", adm)
	}
}

// The acceptance criterion: the same window after its reset is not waived. Its
// next rate-limited observation defers, whatever the waiver says, because the
// waiver lapsed when the reset it named passed.
func TestAWaiverDoesNotCoverTheNextPeriod(t *testing.T) {
	wa := budget.Waiver{Window: "monthly", Until: at(t, "2026-09-27T00:00:00Z")}
	now := at(t, "2026-09-28T00:00:00Z")
	const next = `{"usage":{"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-10-27T00:00:00Z"}}}`

	adm := decode(t, next, now).Admit(0, budget.Waivers{wa}, now)
	if adm.Decision != budget.Defer {
		t.Fatalf("decision is %s, want defer: the waiver lapsed at its own timestamp", adm.Decision)
	}
	if want := at(t, "2026-10-27T00:00:00Z"); !adm.Until.Equal(want) {
		t.Fatalf("deferred until %s, want the new period's %s", adm.Until, want)
	}
}

// The acceptance criterion: a waiver whose timestamp has already passed waives
// nothing. It is not the observation's reset that decides this - the waiver's
// own timestamp does, and it is behind us.
func TestAPassedWaiverWaivesNothing(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	wa := budget.Waiver{Window: "monthly", Until: at(t, "2026-09-01T00:00:00Z")}

	adm := decode(t, monthlyLimited, now).Admit(0, budget.Waivers{wa}, now)
	if adm.Decision != budget.Defer {
		t.Fatalf("decision is %s, want defer: a passed waiver waives nothing", adm.Decision)
	}
	if len(adm.Waived) != 0 {
		t.Fatalf("a passed waiver was reported as applied: %+v", adm.Waived)
	}
}

// The acceptance criterion: a waiver on one window does not affect another. A
// spent rolling window defers while monthly is waived, and the deferral names
// the rolling window rather than the waived one.
func TestAWaiverOnOneWindowLeavesTheOthersAlone(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	const doc = `{"usage":{
		"rolling":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-11T13:00:00Z"},
		"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-27T00:00:00Z"}
	}}`
	wa := budget.Waiver{Window: "monthly", Until: at(t, "2026-09-27T00:00:00Z")}

	adm := decode(t, doc, now).Admit(0, budget.Waivers{wa}, now)
	if adm.Decision != budget.Defer {
		t.Fatalf("decision is %s, want defer: the rolling window is not waived", adm.Decision)
	}
	if adm.Window.Name != "rolling" {
		t.Fatalf("the deferral names %q, want the window that is not waived", adm.Window.Name)
	}
	if want := at(t, "2026-09-11T13:00:00Z"); !adm.Until.Equal(want) {
		t.Fatalf("deferred until %s, want rolling's %s", adm.Until, want)
	}
	if len(adm.Waived) != 0 {
		t.Fatalf("a waiver was reported on an admission that deferred: %+v", adm.Waived)
	}
}

// The acceptance criterion: a waived window is left out of the threshold check
// as well. A waived window at 100% is no reason to stand down, which is the
// point of waiving one before the provider calls it limited.
func TestAWaivedWindowIsLeftOutOfTheThreshold(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	const near = `{"usage":{
		"rolling":{"status":"ok","percent":10,"resetsAt":"2026-09-11T18:00:00Z"},
		"monthly":{"status":"ok","percent":100,"resetsAt":"2026-09-27T00:00:00Z"}
	}}`
	s := decode(t, near, now)

	if adm := s.Admit(80, nil, now); adm.Decision != budget.Wait {
		t.Fatalf("the unwaived monthly window did not stop work at the threshold: %s", adm)
	}
	wa := budget.Waiver{Window: "monthly", Until: at(t, "2026-09-27T00:00:00Z")}
	if adm := s.Admit(80, budget.Waivers{wa}, now); !adm.Starts() {
		t.Fatalf("a waived window stopped work at the threshold: %s", adm)
	}
}

// The acceptance criterion: when every limited window is waived, the resolver
// returns the candidates rather than a LimitedError. model.Budget is the
// conversion the resolver reads, so the property is that it reads as unlimited.
func TestTheResolverAppliesTheWaiver(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	s := decode(t, monthlyLimited, now)
	wa := budget.Waiver{Window: "monthly", Until: at(t, "2026-09-27T00:00:00Z")}

	if b := s.Resolver(budget.Waivers{wa}, now); b.Limited {
		t.Fatalf("the only limited window is waived and the resolver's budget is %+v", b)
	}

	// A second limited window that is not waived still limits the resolution,
	// and the waiver does not hide its reset.
	const both = `{"usage":{
		"weekly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-15T00:00:00Z"},
		"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-27T00:00:00Z"}
	}}`
	b := decode(t, both, now).Resolver(budget.Waivers{wa}, now)
	if !b.Limited {
		t.Fatal("the unwaived weekly window no longer limits the resolution")
	}
	if want := at(t, "2026-09-15T00:00:00Z"); !b.ResetsAt.Equal(want) {
		t.Fatalf("resets at %s, want the unwaived window's %s", b.ResetsAt, want)
	}
}

// A window name is not hard-coded: a waiver for a window this build has never
// heard of is applied exactly as one for rolling is, which is what lets the
// module restrict the names rather than this package.
func TestNoWindowNameIsHardCoded(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	const doc = `{"usage":{"fortnightly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-27T00:00:00Z"}}}`
	wa := budget.Waiver{Window: "fortnightly", Until: at(t, "2026-09-27T00:00:00Z")}

	if adm := decode(t, doc, now).Admit(0, budget.Waivers{wa}, now); !adm.Starts() {
		t.Fatalf("a waiver for a window the build has not heard of did nothing: %s", adm)
	}
}

// Live is what `afk budget` shows by hand: the waivers in force, whether or not
// their windows read anything yet.
func TestWaiversLiveExcludesLapsedOnes(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	ws := budget.Waivers{
		{Window: "monthly", Until: at(t, "2026-09-27T00:00:00Z")},
		{Window: "weekly", Until: at(t, "2026-09-01T00:00:00Z")},
	}

	live := ws.Live(now)
	if len(live) != 1 || live[0].Window != "monthly" {
		t.Fatalf("live waivers are %+v, want only the unexpired monthly one", live)
	}
	if _, ok := ws.Waived(budget.Window{Name: "weekly"}, now); ok {
		t.Fatal("a lapsed waiver reports itself as covering its window")
	}
	if wa, ok := ws.Waived(budget.Window{Name: "monthly"}, now); !ok || wa.Until != at(t, "2026-09-27T00:00:00Z") {
		t.Fatalf("Waived = %+v, %v; want the monthly waiver", wa, ok)
	}
	if _, ok := ws.Waived(budget.Window{Name: "rolling"}, now); ok {
		t.Fatal("a waiver covered a window it does not name")
	}
}

// The zero State admits with or without waivers: an unobserved budget is not a
// spent one (ADR 0001 §12), and a waiver cannot make that more true.
func TestAnUnobservedBudgetAdmitsUnderAWaiver(t *testing.T) {
	wa := budget.Waiver{Window: "monthly", Until: time.Now().Add(time.Hour)}
	if adm := (budget.State{}).Admit(0, budget.Waivers{wa}, time.Now()); !adm.Starts() {
		t.Fatalf("an unobserved budget stopped work: %s", adm)
	}
}
