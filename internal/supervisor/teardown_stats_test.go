package supervisor

import "testing"

/* THESE COUNTERS ARE PACKAGE-LEVEL AND WRITTEN FROM A GOROUTINE.
 *
 * supervisor.go:1108/1115 call noteTeardown inside the `go func()` that watches
 * a stopping child, so a teardown started by an EARLIER test in this package
 * lands whenever that child happens to exit -- which can be after the next test
 * has already called resetTeardownsForTest(). Nothing serialises the two: the
 * tests do not run in parallel, but the goroutine outlives the test that
 * started it.
 *
 * Every assertion of the form `len(got) != 1` therefore had a race with the
 * rest of the package, and it fired: TestCleanTeardownsAreCountedAtAll failed
 * on macos-latest with a stray second kind in the tally. A slower runner widens
 * the window; it does not create it.
 *
 * Two changes make the premise true instead of hoping for it:
 *
 *   - Each test uses a kind no production Spec has, so a real teardown cannot
 *     land in the row under assertion.
 *   - Assertions read THAT KIND's row rather than the length of the whole
 *     tally, so a foreign row is irrelevant rather than fatal.
 *
 * resetTeardownsForTest stays: it keeps one test's rows out of the next one's
 * ordering assertions. It just is no longer load-bearing for correctness.
 */

// statsFor returns the tally row for one kind, and a zero row when the kind has
// no entry -- which is a legitimate answer meaning "nothing was counted".
func statsFor(t *testing.T, kind string) TeardownStats {
	t.Helper()
	for _, s := range Teardowns() {
		if s.Kind == kind {
			return s
		}
	}
	return TeardownStats{Kind: kind}
}

// The counter exists to answer one question the logs cannot: what FRACTION of
// teardowns had to be killed. So the property that matters is not that kills
// are counted -- the log already did that, badly -- but that clean teardowns
// are counted too, in the same place, so the two cannot drift.

func TestCleanTeardownsAreCountedAtAll(t *testing.T) {
	resetTeardownsForTest()

	// The whole point. Before this, a teardown that went perfectly wrote
	// nothing anywhere: supervise() returns on context cancellation before it
	// reaches the exit log, so success was invisible and no ratio existed.
	noteTeardown("test-clean", false)

	got := statsFor(t, "test-clean")
	if got.Total != 1 {
		t.Fatalf("a clean teardown was not counted: %+v", got)
	}
	if got.Kills != 0 {
		t.Errorf("clean teardown counted as a kill: %+v", got)
	}
}

func TestKillsAreASubsetOfTotal(t *testing.T) {
	// Kills must be a subset, not a parallel tally. If a kill did not also
	// increment Total, the ratio would exceed 1 and the number would be
	// nonsense in the direction that causes a false alarm.
	resetTeardownsForTest()
	noteTeardown("test-kill", true)

	got := statsFor(t, "test-kill")
	if got.Total != 1 || got.Kills != 1 {
		t.Fatalf("kill did not increment both counters: %+v", got)
	}
}

func TestTheRatioIsComputable(t *testing.T) {
	// The scenario from production, in miniature: mostly kills, and now
	// visible as such rather than as a stream of unscaled log lines.
	resetTeardownsForTest()
	for range 3 {
		noteTeardown("test-ratio", true)
	}
	noteTeardown("test-ratio", false)

	got := statsFor(t, "test-ratio")
	if got.Total != 4 || got.Kills != 3 {
		t.Fatalf("want 3 of 4, got %+v", got)
	}
}

func TestKindsAreSeparate(t *testing.T) {
	// A recorder being killed is a different event from meters being killed --
	// one may have lost a flush, the other has nothing to lose. Merging them
	// into one number hides exactly the distinction worth alerting on.
	resetTeardownsForTest()
	noteTeardown("test-recorder", true)
	noteTeardown("test-meters", false)
	noteTeardown("test-meters", false)

	if m := statsFor(t, "test-meters"); m.Total != 2 || m.Kills != 0 {
		t.Errorf("meters: %+v", m)
	}
	if r := statsFor(t, "test-recorder"); r.Total != 1 || r.Kills != 1 {
		t.Errorf("recorder: %+v", r)
	}
}

func TestAnUnnamedKindStillCounts(t *testing.T) {
	// A Spec with no Kind is a bug, but dropping its teardowns would corrupt
	// the denominator to hide it -- the one thing this file must not do.
	resetTeardownsForTest()
	noteTeardown("", true)

	got := statsFor(t, "unknown")
	if got.Total != 1 {
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
