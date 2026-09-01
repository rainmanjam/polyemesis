package supervisor

import "testing"

// The counter exists to answer one question the logs cannot: what FRACTION of
// teardowns had to be killed. So the property that matters is not that kills
// are counted -- the log already did that, badly -- but that clean teardowns
// are counted too, in the same place, so the two cannot drift.

func TestCleanTeardownsAreCountedAtAll(t *testing.T) {
	resetTeardownsForTest()

	// The whole point. Before this, a teardown that went perfectly wrote
	// nothing anywhere: supervise() returns on context cancellation before it
	// reaches the exit log, so success was invisible and no ratio existed.
	noteTeardown("ingest", false)

	got := Teardowns()
	if len(got) != 1 || got[0].Total != 1 {
		t.Fatalf("a clean teardown was not counted: %+v", got)
	}
	if got[0].Kills != 0 {
		t.Errorf("clean teardown counted as a kill: %+v", got[0])
	}
}

func TestKillsAreASubsetOfTotal(t *testing.T) {
	// Kills must be a subset, not a parallel tally. If a kill did not also
	// increment Total, the ratio would exceed 1 and the number would be
	// nonsense in the direction that causes a false alarm.
	resetTeardownsForTest()
	noteTeardown("meters", true)

	got := Teardowns()[0]
	if got.Total != 1 || got.Kills != 1 {
		t.Fatalf("kill did not increment both counters: %+v", got)
	}
}

func TestTheRatioIsComputable(t *testing.T) {
	// The scenario from production, in miniature: mostly kills, and now
	// visible as such rather than as a stream of unscaled log lines.
	resetTeardownsForTest()
	for range 3 {
		noteTeardown("preview", true)
	}
	noteTeardown("preview", false)

	got := Teardowns()[0]
	if got.Total != 4 || got.Kills != 3 {
		t.Fatalf("want 3 of 4, got %+v", got)
	}
}

func TestKindsAreSeparate(t *testing.T) {
	// A recorder being killed is a different event from meters being killed --
	// one may have lost a flush, the other has nothing to lose. Merging them
	// into one number hides exactly the distinction worth alerting on.
	resetTeardownsForTest()
	noteTeardown("recorder", true)
	noteTeardown("meters", false)
	noteTeardown("meters", false)

	got := Teardowns()
	if len(got) != 2 {
		t.Fatalf("want two kinds, got %+v", got)
	}
	// Sorted by kind, so meters precedes recorder.
	if got[0].Kind != "meters" || got[0].Total != 2 || got[0].Kills != 0 {
		t.Errorf("meters: %+v", got[0])
	}
	if got[1].Kind != "recorder" || got[1].Kills != 1 {
		t.Errorf("recorder: %+v", got[1])
	}
}

func TestAnUnnamedKindStillCounts(t *testing.T) {
	// A Spec with no Kind is a bug, but dropping its teardowns would corrupt
	// the denominator to hide it -- the one thing this file must not do.
	resetTeardownsForTest()
	noteTeardown("", true)

	got := Teardowns()
	if len(got) != 1 || got[0].Kind != "unknown" || got[0].Total != 1 {
		t.Fatalf("an unnamed kind was dropped from the tally: %+v", got)
	}
}

func TestOrderIsStable(t *testing.T) {
	// Map iteration order would make this jitter between calls, which matters
	// for anything rendering it or diffing two snapshots.
	resetTeardownsForTest()
	for _, k := range []string{"preview", "ingest", "recorder", "meters"} {
		noteTeardown(k, false)
	}
	got := Teardowns()
	for i := 1; i < len(got); i++ {
		if got[i-1].Kind > got[i].Kind {
			t.Fatalf("not sorted by kind: %+v", got)
		}
	}
}
