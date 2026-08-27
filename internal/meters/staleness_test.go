package meters

import (
	"strings"
	"testing"
	"time"
)

// passing is a report that earned a genuine pass: a real target, enough
// programme to be judged, and a level sitting on it.
func passing(at time.Time) Report {
	return Observe(1, "youtube", Target{
		LUFS: -14, TruePeakDBTP: -1, ToleranceLU: ToleranceLU, Source: TargetProfile,
	}, Frame{
		Seconds: 235.9, IntegratedLUFS: -14.0, MomentaryLUFS: -13.8,
		ShortTermLUFS: -13.9, TruePeakDBTP: -2.0, Integrated: true,
	}, at)
}

func TestAgedWithdrawsAVerdictNothingHasRemeasured(t *testing.T) {
	now := time.Now()
	r := passing(now.Add(-65 * time.Second))
	if r.Verdict != VerdictPass {
		t.Fatalf("fixture is not a pass: %q (%s)", r.Verdict, r.Reason)
	}

	got := r.Aged(now)

	if got.Verdict != VerdictUnknown {
		t.Fatalf("a reading last taken 65s ago still reads %q; #609 is that a stale pass is indistinguishable from a live one", got.Verdict)
	}
	if !strings.Contains(got.Reason, "65 seconds") {
		t.Fatalf("the reason must say how long ago the last measurement was, got %q", got.Reason)
	}
	t.Run("the numbers survive so the last reading is still legible", func(t *testing.T) {
		if got.IntegratedLUFS != r.IntegratedLUFS || got.Seconds != r.Seconds {
			t.Fatalf("aging rewrote the measurement itself: %+v", got.Frame)
		}
		// A zeroed deviation renders as +0.0 LU, which reads as dead-on target
		// -- a worse lie than the stale pass this whole change removes.
		if got.DeviationLU != r.DeviationLU {
			t.Fatalf("deviation = %v, want the last measured %v", got.DeviationLU, r.DeviationLU)
		}
	})
}

// The control. A fix that marks every report unknown passes the test above and
// destroys the feature it is guarding.
func TestAgedLeavesAFreshVerdictStanding(t *testing.T) {
	now := time.Now()
	for _, age := range []time.Duration{0, time.Second, 2 * time.Second, StaleAfter - time.Millisecond} {
		r := passing(now.Add(-age))
		got := r.Aged(now)
		if got.Verdict != VerdictPass {
			t.Fatalf("a reading %s old reads %q; the meter must keep saying pass while it is measuring", age, got.Verdict)
		}
		if got.Reason != r.Reason {
			t.Fatalf("a reading %s old had its reason rewritten to %q", age, got.Reason)
		}
	}
}

func TestAgedLeavesAnAlreadyUnknownReportSayingWhy(t *testing.T) {
	now := time.Now()
	old := now.Add(-10 * time.Minute)
	cases := map[string]Report{
		"starting": Starting(1, "youtube", Target{Source: TargetNone}, old),
		"failed":   Failed(1, "youtube", Target{Source: TargetNone}, "exec: no ffmpeg", old),
	}
	for name, r := range cases {
		got := r.Aged(now)
		if got.Reason != r.Reason {
			t.Fatalf("%s: reason became %q, losing the specific answer %q", name, got.Reason, r.Reason)
		}
		if got.Error != r.Error {
			t.Fatalf("%s: error became %q, want %q", name, got.Error, r.Error)
		}
	}
}

func TestAgedTreatsAReportWithNoTimestampAsUndated(t *testing.T) {
	// A report assembled without an At is not a report from 1 January year 1.
	// Aging it against the zero time would announce "no measurement for
	// 63000000000 seconds" to an operator who has done nothing wrong.
	r := passing(time.Time{})
	got := r.Aged(time.Now())
	if got.Verdict != VerdictPass || got.Reason != r.Reason {
		t.Fatalf("an undated report was aged out: %q / %q", got.Verdict, got.Reason)
	}
}

func TestStoreAgesEveryReportItHandsOut(t *testing.T) {
	s := NewStore()
	now := time.Now()
	// Two destinations whose analysers went quiet, one that kept measuring --
	// exactly the shape of #609, where all three rendered identically.
	s.Put(passingFor(1, "youtube", now.Add(-65*time.Second)))
	s.Put(passingFor(2, "twitch", now.Add(-65*time.Second)))
	s.Put(passingFor(3, "kick", now))

	all := s.All()
	if len(all) != 3 {
		t.Fatalf("want 3 reports, got %d", len(all))
	}
	if all[0].Verdict != VerdictUnknown || all[1].Verdict != VerdictUnknown {
		t.Fatalf("Store.All handed out stale verdicts %q and %q", all[0].Verdict, all[1].Verdict)
	}
	if all[2].Verdict != VerdictPass {
		t.Fatalf("the destination that kept measuring reads %q, want pass", all[2].Verdict)
	}

	t.Run("Get ages too, so no read path escapes the clock", func(t *testing.T) {
		got, ok := s.Get(1)
		if !ok {
			t.Fatal("destination 1 is missing from the store")
		}
		if got.Verdict != VerdictUnknown {
			t.Fatalf("Store.Get handed out a stale %q", got.Verdict)
		}
	})
}

func passingFor(id int64, name string, at time.Time) Report {
	r := passing(at)
	r.DestinationID, r.Destination = id, name
	return r
}
