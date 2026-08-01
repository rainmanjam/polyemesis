package engine

import (
	"testing"
	"time"
)

// These tests are about the SHAPE of the selector, not its policy. The policy
// is frozen by testdata/selector_golden.txt and by the duration-boundary cases
// in failover_test.go; what is unproven by either is that the candidate list is
// a faithful rewrite rather than a plausible one, and that the ordering it
// carries is load-bearing rather than decorative.

// TestChooseFromMatchesTheOldLadder is the equivalence proof, run against the
// reference implementation instead of against a file.
//
// The golden table already says "nothing moved", but it says it about
// chooseSource, which is now a wrapper — so it can only fail once the two paths
// have already been wired together. This compares them directly over the same
// 1024 inputs, which means a mismatch names the input AND has the old answer to
// hand, rather than sending the reader to a text file to work out which of the
// two implementations was right.
func TestChooseFromMatchesTheOldLadder(t *testing.T) {
	choices := allSourceChoices()
	if len(choices) == 0 {
		t.Fatal("no inputs enumerated -- this test would pass vacuously")
	}

	mismatches := 0
	for _, c := range choices {
		wantKind, wantReason := chooseSourceLadder(c)
		gotKind, gotReason := chooseFrom(candidatesFor(c), c)
		if gotKind == wantKind && gotReason == wantReason {
			continue
		}
		mismatches++
		if mismatches <= 12 {
			t.Errorf("%s\n  ladder:     %s %q\n  candidates: %s %q",
				goldenRow(c), orNone(wantKind), wantReason, orNone(gotKind), gotReason)
		}
	}
	if mismatches > 12 {
		t.Errorf("... and %d further inputs disagree", mismatches-12)
	}
}

// TestCandidatesForIsTheLadderOrder pins the preference order on its own.
//
// The golden table can only see an ordering change that reaches a decision, and
// most rows never exercise the tail of the list. This fails the moment the
// literal is reordered, which is the change Task 3 must make deliberately and
// must never make by accident.
func TestCandidatesForIsTheLadderOrder(t *testing.T) {
	got := candidatesFor(sourceChoice{now: goldenNow, grace: goldenGrace})

	want := []sourceKind{sourcePrimary, sourceBackup, sourceSlate}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].kind != w {
			t.Errorf("candidate %d is %s, want %s", i, orNone(got[i].kind), orNone(w))
		}
		// rank must agree with position, because chooseFrom sorts by rank and
		// reads nothing from the position. A list whose two orderings disagree
		// would decide by the one nobody was looking at.
		if got[i].rank != i {
			t.Errorf("candidate %d (%s) has rank %d, want %d", i, orNone(got[i].kind), got[i].rank, i)
		}
	}
}

// TestCandidatesForAvailabilityMatchesLiveness checks the one thing the shape
// change could quietly get wrong in a way the ladder's own reasons would still
// paper over: what "available" means per source.
func TestCandidatesForAvailabilityMatchesLiveness(t *testing.T) {
	delivering := liveness{rx: 1, at: goldenNow.Add(-time.Second), since: goldenNow.Add(-time.Minute)}
	stale := liveness{rx: 1, at: goldenNow.Add(-time.Hour), since: goldenNow.Add(-time.Hour)}

	cases := []struct {
		name string
		c    sourceChoice
		want map[sourceKind]bool
	}{{
		name: "nothing delivering and nothing enabled",
		c:    sourceChoice{now: goldenNow, grace: goldenGrace},
		want: map[sourceKind]bool{sourcePrimary: false, sourceBackup: false, sourceSlate: false},
	}, {
		name: "everything delivering and enabled",
		c: sourceChoice{
			now: goldenNow, grace: goldenGrace,
			primary: delivering, backup: delivering,
			backupEnabled: true, slateEnabled: true,
		},
		want: map[sourceKind]bool{sourcePrimary: true, sourceBackup: true, sourceSlate: true},
	}, {
		// The distinction the whole tier turns on: a source that stopped
		// delivering is unavailable even though its hub and its process are
		// still there.
		name: "delivery has gone stale past the grace window",
		c: sourceChoice{
			now: goldenNow, grace: goldenGrace,
			primary: stale, backup: stale, backupEnabled: true,
		},
		want: map[sourceKind]bool{sourcePrimary: false, sourceBackup: false, sourceSlate: false},
	}, {
		// A backup that is delivering but switched off must not be offered, or
		// a failover would send viewers to a source the operator disabled.
		name: "backup delivering but disabled",
		c: sourceChoice{
			now: goldenNow, grace: goldenGrace,
			primary: delivering, backup: delivering, backupEnabled: false,
		},
		want: map[sourceKind]bool{sourcePrimary: true, sourceBackup: false, sourceSlate: false},
	}, {
		// The slate has no liveness at all: enabled is the whole test.
		name: "slate enabled with no ingest at all",
		c:    sourceChoice{now: goldenNow, grace: goldenGrace, slateEnabled: true},
		want: map[sourceKind]bool{sourcePrimary: false, sourceBackup: false, sourceSlate: true},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, cand := range candidatesFor(tc.c) {
				want, ok := tc.want[cand.kind]
				if !ok {
					t.Fatalf("unexpected candidate %s", orNone(cand.kind))
				}
				if cand.available != want {
					t.Errorf("%s available=%t, want %t", orNone(cand.kind), cand.available, want)
				}
			}
		})
	}
}

// TestChooseFromDecidesByRankNotSlicePosition is what makes rank a field rather
// than a comment. If chooseFrom read the slice order instead, a caller that
// built the list in any other order would get a different broadcast out of the
// same facts.
func TestChooseFromDecidesByRankNotSlicePosition(t *testing.T) {
	// Primary is gone; a backup and a slate are both available. The ladder
	// prefers the backup.
	c := sourceChoice{
		now: goldenNow, grace: goldenGrace,
		backup:        liveness{rx: 1, at: goldenNow.Add(-time.Second), since: goldenNow.Add(-time.Minute)},
		backupEnabled: true,
		slateEnabled:  true,
	}

	inOrder := candidatesFor(c)
	shuffled := []candidate{inOrder[2], inOrder[0], inOrder[1]}

	wantKind, wantReason := chooseFrom(inOrder, c)
	if wantKind != sourceBackup {
		t.Fatalf("ladder chose %s, want backup -- this test is no longer testing what it says", orNone(wantKind))
	}
	gotKind, gotReason := chooseFrom(shuffled, c)
	if gotKind != wantKind || gotReason != wantReason {
		t.Errorf("shuffled list decided %s %q, want %s %q -- chooseFrom is reading slice position, not rank",
			orNone(gotKind), gotReason, orNone(wantKind), wantReason)
	}
}

// TestChooseFromDoesNotMutateItsInput guards the caller's list. chooseFrom
// sorts, and sorting in place would reorder a slice the caller may reuse or may
// have built from shared state.
func TestChooseFromDoesNotMutateItsInput(t *testing.T) {
	c := sourceChoice{now: goldenNow, grace: goldenGrace, slateEnabled: true}
	cands := []candidate{
		{kind: sourceSlate, available: true, rank: 2},
		{kind: sourcePrimary, available: false, rank: 0},
		{kind: sourceBackup, available: false, rank: 1},
	}
	before := append([]candidate(nil), cands...)

	chooseFrom(cands, c)

	for i := range cands {
		if cands[i] != before[i] {
			t.Fatalf("chooseFrom reordered its input: position %d was %s, now %s",
				i, orNone(before[i].kind), orNone(cands[i].kind))
		}
	}
}
