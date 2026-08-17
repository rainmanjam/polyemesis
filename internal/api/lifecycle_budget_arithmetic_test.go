package api

import (
	"testing"
	"time"
)

/* THE CADENCE CONSTANTS ARE MEASURED AGAINST THEIR OWN CLAIMS, NOT AGAINST
 * THEMSELVES.
 *
 * TestAnUpEdgeReChecksASettledLiveBroadcast writes its loop bound as
 * `i <= lifecycleLiveRecheckAfterEdge`, and its comment says that is so
 * "changing the floor does not silently change what this test proves". The
 * reasoning is inverted: a bound derived FROM the constant follows the constant
 * wherever it goes. Set the floor to 400 and that test sweeps 401 times and
 * still passes, having proved only that the mechanism is self-consistent.
 *
 * Self-consistency is not what those constants are for. They exist to buy two
 * specific things, both stated as arithmetic in lifecycle.go, and both of which
 * a plausible edit can break while every existing test stays green:
 *
 *   1. AN OPERATOR NOTICES WITHIN ABOUT A MINUTE. "Four sweeps is about a minute
 *      at the ordinary tick -- soon enough that a broadcast ended in Studio is
 *      noticed while the operator is still looking at the card."
 *
 *   2. A FLAPPING DESTINATION CANNOT EXHAUST THE PROJECT'S QUOTA. "Without a
 *      floor that same destination forced a re-read on every edge: roughly 7,200
 *      a day, 14,400 API units, against an allocation of 10,000."
 *
 * Neither claim mentions the value 4. Both stop being true if the floor moves
 * far enough, and the second stops being true if it reaches 0 -- which is
 * exactly the edit somebody makes to "fix" a report that the UI is slow to
 * notice an ended broadcast.
 *
 * THESE ARE BANDS, NOT EQUALITIES, ON PURPOSE. Asserting the constant equals 4
 * would be the same mistake pointed the other way: a test nobody can change
 * without editing the test, which teaches people to edit the test. The bounds
 * below are wide enough that any value satisfying the comment passes, and narrow
 * enough that a value which breaks it does not.
 */

// The published YouTube Data API default allocation, and the cost the sweep's
// own comments assign to one state re-read. Both are quoted from lifecycle.go
// rather than invented here.
const (
	ytDailyUnitAllocation = 10000
	unitsPerStateReRead   = 2
	// A destination flapping every twelve seconds, which is the case
	// lifecycleLiveRecheckAfterEdge's comment is written about.
	flapEverySeconds = 12
)

func TestTheLiveRecheckFloorStillNoticesWithinAboutAMinute(t *testing.T) {
	// The budget counts sweeps SKIPPED, so a floor of n means the re-read lands
	// on sweep n+1.
	notice := time.Duration(lifecycleLiveRecheckAfterEdge+1) * lifecycleTick

	if notice < 20*time.Second || notice > 2*time.Minute {
		t.Errorf("an edge now pulls the re-read forward to %s, and the constant's own "+
			"claim is \"about a minute at the ordinary tick -- soon enough that a "+
			"broadcast ended in Studio is noticed while the operator is still looking "+
			"at the card\".\n\n"+
			"floor=%d sweeps, tick=%s. If this change is intended, the comment on "+
			"lifecycleLiveRecheckAfterEdge is now wrong and should be rewritten in the "+
			"same commit — that sentence is what an operator's expectation is set by.",
			notice, lifecycleLiveRecheckAfterEdge, lifecycleTick)
	}
}

// THE QUOTA CLAIM, WHICH IS THE ONE WITH A HARD NUMBER ON THE OTHER SIDE.
//
// An install out of quota cannot END a broadcast, which lifecycle.go calls out
// separately: the failure is not "stats are stale", it is "the show stays live".
func TestAFlappingDestinationCannotExhaustTheDayOnReReads(t *testing.T) {
	edgesPerDay := int((24 * time.Hour) / (flapEverySeconds * time.Second))

	// WITH the floor: at most one re-read per (floor+1) sweeps, regardless of how
	// many edges arrive.
	sweepsPerDay := int((24 * time.Hour) / lifecycleTick)
	withFloor := (sweepsPerDay / (lifecycleLiveRecheckAfterEdge + 1)) * unitsPerStateReRead

	// WITHOUT it: an edge forces a re-read, so the rate is the flap rate. This is
	// the number the comment cites as 14,400.
	withoutFloor := edgesPerDay * unitsPerStateReRead

	if withoutFloor <= ytDailyUnitAllocation {
		t.Fatalf("the premise has moved: an unfloored flapping destination would spend "+
			"%d units against an allocation of %d, so the floor is no longer buying "+
			"what its comment says it buys. Re-derive the arithmetic before trusting "+
			"either number.", withoutFloor, ytDailyUnitAllocation)
	}

	if withFloor >= ytDailyUnitAllocation {
		t.Errorf("ONE flapping destination can now spend %d API units a day against an "+
			"allocation of %d, shared with metadata pushes and every other install "+
			"activity.\n\n"+
			"floor=%d sweeps, tick=%s. lifecycle.go is explicit about what running out "+
			"costs: an install with no quota cannot END a broadcast, so the failure is "+
			"not a stale card — it is a show that stays live.",
			withFloor, ytDailyUnitAllocation, lifecycleLiveRecheckAfterEdge, lifecycleTick)
	}

	// And the floor must actually be doing the work. A floor that permitted
	// nearly as much as no floor at all would pass the bound above while being
	// decorative.
	if withFloor*4 > withoutFloor {
		t.Errorf("the floor only reduces the worst case from %d to %d units; it is "+
			"supposed to be the difference between exhausting the allocation and not",
			withoutFloor, withFloor)
	}
}

// The ordinary cadence has the same shape of claim: lifecycleLiveRecheckEvery is
// the interval when NO edge arrives, and it is the number the failure message in
// the existing test quotes at an operator.
func TestTheOrdinaryRecheckCadenceStaysTheSlowPath(t *testing.T) {
	if lifecycleLiveRecheckEvery <= lifecycleLiveRecheckAfterEdge {
		t.Fatalf("the ordinary cadence (%d sweeps) is no longer slower than the "+
			"post-edge floor (%d), so the edge buys nothing and forgetLiveRecheck is "+
			"dead code",
			lifecycleLiveRecheckEvery, lifecycleLiveRecheckAfterEdge)
	}

	// The unedged interval is a background poll on a broadcast nobody has
	// signalled about. Ten minutes is the shape lifecycle.go describes; an hour
	// would mean an ended broadcast sat labelled live for an hour on an install
	// where no edge ever arrives.
	idle := time.Duration(lifecycleLiveRecheckEvery) * lifecycleTick
	if idle < 2*time.Minute || idle > 30*time.Minute {
		t.Errorf("with no edge at all, a settled-live broadcast is now re-read every "+
			"%s (every=%d sweeps, tick=%s). That is the window in which a broadcast "+
			"ended on the platform stays labelled live to an operator who gets no edge.",
			idle, lifecycleLiveRecheckEvery, lifecycleTick)
	}
}
