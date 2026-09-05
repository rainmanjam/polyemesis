package childcensus

import (
	"testing"
	"time"
)

func TestTheCensusRefusesAPIDThatIsNotOne(t *testing.T) {
	// cmd.Process.Pid is only meaningful after a successful Start. A zero here
	// would be a permanent entry for a child that never existed, and a census
	// with a phantom in it is one nobody trusts the rest of.
	before := LiveCount()
	Enrol(0, "ghost", "ghost")
	Enrol(-1, "ghost", "ghost")
	if LiveCount() != before {
		t.Fatalf("a non-pid was enrolled: count went %d -> %d", before, LiveCount())
	}
	Discharge(0)
	Discharge(-1)
	if LiveCount() != before {
		t.Fatalf("discharging a non-pid disturbed the census: %d -> %d", before, LiveCount())
	}
}

func TestTheOldestSurvivorIsReportedFirst(t *testing.T) {
	// A report leads with the child that has been wrong for longest, because
	// that is the one whose cause is furthest back and least likely to be the
	// thing the operator is currently looking at.
	before := len(Live())
	now := time.Now()
	census.mu.Lock()
	if census.live == nil {
		census.live = map[int]Child{}
	}
	census.live[900001] = Child{PID: 900001, Name: "newer", Since: now}
	census.live[900002] = Child{PID: 900002, Name: "older", Since: now.Add(-time.Hour)}
	census.mu.Unlock()
	t.Cleanup(func() { Discharge(900001); Discharge(900002) })

	got := Live()
	if len(got) != before+2 {
		t.Fatalf("expected %d entries, got %d", before+2, len(got))
	}
	var names []string
	for _, c := range got {
		if c.PID == 900001 || c.PID == 900002 {
			names = append(names, c.Name)
		}
	}
	if len(names) != 2 || names[0] != "older" {
		t.Fatalf("Live() ordered the survivors %v; oldest must come first", names)
	}
}
