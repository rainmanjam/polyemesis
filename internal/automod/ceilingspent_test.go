package automod

import (
	"context"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A spent hourly ceiling is surfaced on the stats the console reads, and the
// reading goes back to false once the hour has rolled over.
func TestModelStatsSaysWhenTheCeilingIsSpent(t *testing.T) {
	b := NewBudget()
	clk := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return clk }
	m := NewModel(ModelConfig{Enabled: true, MaxCallsPerHour: 1}, b)
	e := New(DefaultMatrix(), PlatformCaps{}, nil, nil, NewChain(m, nil))
	if e.ModelStats().CeilingSpent {
		t.Fatal("reported spent before any call")
	}
	_, _ = m.Check(context.Background(), "x") // takes the one call allowed
	if !e.ModelStats().CeilingSpent {
		t.Fatal("a spent ceiling is not reported")
	}
	clk = clk.Add(61 * time.Minute)
	if e.ModelStats().CeilingSpent {
		t.Fatal("still reported spent after the hour rolled over")
	}
}

// A decline by the gate's limits is told apart from a line with nothing to ask
// about: only the first carries a signal.
func TestModelOutcomeSeparatesDeclinedFromNothingToAsk(t *testing.T) {
	g := NewModelGate(GateConfig{})
	e := hardEngine(t, &fakeChecker{}, g)
	p := db.PlatformTwitch
	if o := e.ModelOutcome(p, Subject{AuthorID: "a", Text: hardLine}, Verdict{}); !o.Ask || !o.Signal {
		t.Fatalf("first hard line = %+v", o)
	}
	if o := e.ModelOutcome(p, Subject{AuthorID: "a", Text: hardLine + " now"}, Verdict{}); o.Ask || !o.Declined() {
		t.Fatalf("capped hard line = %+v, want declined with a signal", o)
	}
	if o := e.ModelOutcome(p, Subject{AuthorID: "b", Text: "gg wp"}, Verdict{}); o.Ask || o.Signal || o.Declined() {
		t.Fatalf("a line with nothing to ask about = %+v", o)
	}
}
