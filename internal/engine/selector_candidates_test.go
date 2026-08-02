package engine

import (
	"strings"
	"testing"
	"time"
)

// These tests are about the SHAPE of the selector, not its policy. The policy
// is frozen by testdata/selector_golden.txt and by the duration-boundary cases
// in failover_test.go; what is unproven by either is that the candidate list is
// a faithful rewrite rather than a plausible one, and that the ordering it
// carries is load-bearing rather than decorative.

// TestCandidatesForIsTheLadderOrder pins the preference order on its own.
//
// The golden table can only see an ordering change that reaches a decision, and
// most rows never exercise the tail of the list. This fails the moment the
// literal is reordered, which is the change Task 3 must make deliberately and
// must never make by accident.
func TestCandidatesForIsTheLadderOrder(t *testing.T) {
	got := candidatesFor(sourceChoice{now: goldenNow, grace: goldenGrace})

	// The playlist sits between the ingests and the slate, and that placement is
	// the decision Task 4 made rather than a detail of how the list is built:
	// below a live encoder because a scheduled programme is a fallback for
	// "nobody is streaming", above the slate because it is still programming.
	want := []sourceKind{sourcePrimary, sourceBackup, sourcePlaylist, sourceSlate}
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
		want: map[sourceKind]bool{sourcePrimary: false, sourceBackup: false, sourcePlaylist: false, sourceSlate: false},
	}, {
		name: "everything delivering and enabled",
		c: sourceChoice{
			now: goldenNow, grace: goldenGrace,
			primary: delivering, backup: delivering,
			backupEnabled: true, slateEnabled: true, playlistRunning: true,
		},
		want: map[sourceKind]bool{sourcePrimary: true, sourceBackup: true, sourcePlaylist: true, sourceSlate: true},
	}, {
		// The distinction the whole tier turns on: a source that stopped
		// delivering is unavailable even though its hub and its process are
		// still there.
		name: "delivery has gone stale past the grace window",
		c: sourceChoice{
			now: goldenNow, grace: goldenGrace,
			primary: stale, backup: stale, backupEnabled: true,
		},
		want: map[sourceKind]bool{sourcePrimary: false, sourceBackup: false, sourcePlaylist: false, sourceSlate: false},
	}, {
		// A backup that is delivering but switched off must not be offered, or
		// a failover would send viewers to a source the operator disabled.
		name: "backup delivering but disabled",
		c: sourceChoice{
			now: goldenNow, grace: goldenGrace,
			primary: delivering, backup: delivering, backupEnabled: false,
		},
		want: map[sourceKind]bool{sourcePrimary: true, sourceBackup: false, sourcePlaylist: false, sourceSlate: false},
	}, {
		// The slate has no liveness at all: enabled is the whole test.
		name: "slate enabled with no ingest at all",
		c:    sourceChoice{now: goldenNow, grace: goldenGrace, slateEnabled: true},
		want: map[sourceKind]bool{sourcePrimary: false, sourceBackup: false, sourcePlaylist: false, sourceSlate: true},
	}, {
		// The playlist has no liveness either, and no enabled flag separate
		// from it: a playlist feed that is running IS the availability. It is
		// the one candidate whose availability cannot go stale, which is also
		// why it never needs the grace window the ingests are judged against.
		name: "playlist running with no ingest and no slate",
		c:    sourceChoice{now: goldenNow, grace: goldenGrace, playlistRunning: true},
		want: map[sourceKind]bool{sourcePrimary: false, sourceBackup: false, sourcePlaylist: true, sourceSlate: false},
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
	// Every candidate, in an order that is nothing like the ladder's. Reversed
	// rather than a rotation missing an entry, so a list that has grown a
	// fourth kind is still shuffled in full: a "shuffle" that quietly dropped
	// the tail of the list would pass whatever chooseFrom did with it.
	shuffled := make([]candidate, 0, len(inOrder))
	for i := len(inOrder) - 1; i >= 0; i-- {
		shuffled = append(shuffled, inOrder[i])
	}

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

// TestChooseFromRefusesToWinWithoutAReason locks in a review finding from the
// candidate-list cutover: available(k) matches the FIRST candidate of a kind
// in rank order, while best (inside chooseFrom) matches the FIRST AVAILABLE
// one. Those two only agree when the list has one candidate per kind, which
// candidatesFor always builds today -- but a malformed list, built directly
// as this test does, can pull them apart: a rank-0 backup that is down and a
// rank-1 backup that is up makes available(sourceBackup) say no while best
// picks the rank-1 one and says yes.
//
// Before this fix, that meant a plain map index: the sourceBackup branch's
// fallback map has no entry for sourceBackup, on the reasoning that it
// cannot win there -- true only for a well-formed list -- so a miss quietly
// returned "" instead of a sentence, and Failover.Reason went blank on what
// looked, to an operator, like a switch that never happened. This proves the
// miss is now a panic naming the candidate, not a blank string, so the same
// gap in Task 4's fourth kind fails the build instead of shipping quietly.
func TestChooseFromRefusesToWinWithoutAReason(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("chooseFrom returned normally over a malformed candidate list; want a panic naming the candidate with no registered reason")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "backup") {
			t.Errorf("panic value %v does not name the unreasoned candidate", r)
		}
	}()

	c := sourceChoice{now: goldenNow, grace: goldenGrace, cur: sourceBackup, backupEnabled: true}
	// Two backup candidates: available(sourceBackup) reads the rank-0 one and
	// says no, so chooseFrom takes the branch that assumes backup cannot win;
	// best then walks past rank 0 and hands the rank-1 one the win instead.
	malformed := []candidate{
		{kind: sourceBackup, available: false, rank: 0},
		{kind: sourceBackup, available: true, rank: 1},
	}
	chooseFrom(malformed, c)
}

// TestChooseFromRefusesAnAvailableNoneCandidate closes the other way a
// malformed list could reach an operator, and it is the quieter of the two.
//
// sourceNone is the ABSENCE of a source, so "available: none" is a sentence
// that cannot be true. best used to return whatever kind won, verbatim, which
// meant a list saying that got sourceNone back as a decision -- and
// applySourceChoice's `if want == sourceNone` guard then dropped it. That guard
// exists for a recovered panic, where holding is right; used on a malformed
// list it turned a failover into nothing at all, with no log and no reason,
// which is strictly worse than the blank Failover.Reason the test above exists
// to prevent: a blank reason is at least visible.
//
// The panic must not depend on the reasons map. Today the empty kind happens to
// miss every branch's map and trips the missing-reason panic by accident; a map
// literal that ever gained a sourceNone entry would silently restore the
// discard. This drives the branch whose map DOES carry an empty-string reason
// for a real kind, so a reader can see the check is its own.
func TestChooseFromRefusesAnAvailableNoneCandidate(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("chooseFrom returned normally over a list offering an available sourceNone; want a panic")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "sourceNone") {
			t.Errorf("panic value %v does not name sourceNone as the malformed candidate", r)
		}
	}()

	// cur == slate is the branch whose reason map holds an entry whose VALUE is
	// "" (sourceSlate: ""), so a reader cannot confuse the panic below with a
	// blank-reason miss.
	c := sourceChoice{now: goldenNow, grace: goldenGrace, cur: sourceSlate, slateEnabled: true}
	malformed := []candidate{
		{kind: sourceNone, available: true, rank: 0},
		{kind: sourceSlate, available: true, rank: 1},
	}
	chooseFrom(malformed, c)
}
