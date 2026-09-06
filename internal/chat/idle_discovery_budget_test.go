package chat

import (
	"testing"
	"time"
)

// AN IDLE INSTALL MUST NOT SPEND THE DAY LOOKING FOR A BROADCAST. #735.
//
// youtube.go's Run polls liveBroadcasts while it waits for a broadcast to go
// live, backing off from ytBroadcastPoll to ytBroadcastPollMax. The comment on
// those constants says "a machine left connected overnight must not spend the
// morning's quota discovering that nobody is live" -- which is a claim about
// arithmetic, made in prose, over a cost constant that IS NOT VERIFIED.
//
// Google's quota calculator omits the Live Streaming endpoints, so
// QuotaCostListBroadcasts = 1 has no published source. If the real cost is 50 --
// what every WRITE in this file costs -- an idle overnight spends 144% of a
// default allowance, and the operator's first chat poll of the morning is
// refused by a quota they never knowingly used.
//
// This test does not know the true cost either. What it does is refuse to let
// the arithmetic and the claim drift apart: change the cost, lengthen the day,
// or shorten the poll interval, and the number moves and this fails with the
// consequence spelled out. That is the difference between a comment and a
// device -- the comment asserts the install is safe overnight; this checks it.
//
// Rung 2, and Control is not available: the true cost lives on Google's servers
// and is not discoverable from here. The API returns no cost with a response.
func TestIdleDiscoveryCannotEatTheDay(t *testing.T) {
	const day = 24 * time.Hour

	// The worst case is the ceiling, not the opening interval: backoff doubles
	// from ytBroadcastPoll and stops at ytBroadcastPollMax, so a long idle
	// spends nearly all of its time polling at the slower rate. Using the
	// ceiling UNDERSTATES the first few minutes and is right for the overnight
	// question this exists to answer.
	looks := int(day / ytBroadcastPollMax)
	spend := looks * QuotaCostListBroadcasts

	// A fifth of the default allowance. Not a number Google publishes -- a
	// judgement, stated so it can be argued with: an install that has done
	// nothing all night should not have spent more than a fifth of the day's
	// chat before the first viewer arrives.
	const ceilingFraction = 5
	budget := DefaultQuotaUnits / ceilingFraction

	if spend > budget {
		t.Fatalf("an idle install spends %d units a day looking for a broadcast "+
			"(%d looks at %d units), which is more than 1/%d of the default %d-unit "+
			"allowance.\n\n"+
			"That is the overnight case: nobody is live, nobody is watching, and the "+
			"morning's chat is already paid for. Either the poll ceiling has been "+
			"shortened, or QuotaCostListBroadcasts has been raised -- and if it was "+
			"raised because somebody finally MEASURED it, then this test has done its "+
			"job and the poll ceiling is what has to move.",
			spend, looks, QuotaCostListBroadcasts, ceilingFraction, DefaultQuotaUnits)
	}

	// POSITIVE CONTROL. Every assertion above is satisfied by constants that
	// make the loop do nothing at all -- a zero cost, or a ceiling so long the
	// division floors to no looks. Neither is a safe install; both are a broken
	// measurement.
	if looks <= 0 {
		t.Fatalf("the poll ceiling %s yields %d looks per day; the arithmetic this "+
			"test performs is meaningless and its pass asserts nothing",
			ytBroadcastPollMax, looks)
	}
	if QuotaCostListBroadcasts <= 0 {
		t.Fatalf("QuotaCostListBroadcasts is %d; a free call makes every budget "+
			"check below it vacuous", QuotaCostListBroadcasts)
	}

	t.Logf("idle discovery: %d looks/day at %d unit(s) = %d units, %.1f%% of the "+
		"default %d allowance (cost is UNVERIFIED -- see #735)",
		looks, QuotaCostListBroadcasts, spend,
		float64(spend)/float64(DefaultQuotaUnits)*100, DefaultQuotaUnits)
}

// The discovery loop must also back off at all. A fixed 30-second poll is 2,880
// looks a day, which clears the ceiling above only while the cost stays 1 --
// the exact coupling #735 is about.
func TestDiscoveryBacksOff(t *testing.T) {
	if ytBroadcastPollMax <= ytBroadcastPoll {
		t.Fatalf("ytBroadcastPollMax (%s) does not exceed ytBroadcastPoll (%s), so "+
			"the discovery loop never backs off and an idle install polls at the "+
			"opening rate for ever", ytBroadcastPollMax, ytBroadcastPoll)
	}
	// REPORTED, NOT SKIPPED ON. The first draft of this test skipped when the
	// opening interval was already within budget, on the grounds that the
	// backoff was not load-bearing and there was nothing to measure. The skip
	// census refused it, correctly: a test that declines to run prints ok and
	// counts as coverage, which is the free pass this repository has spent
	// whole rounds removing in other shapes.
	//
	// The fix is not a quieter skip, it is noticing the test was asserting two
	// different things. That the backoff EXISTS is always checkable and is
	// checked above. Whether it currently MATTERS is a fact about today's
	// constants, and a fact is reported.
	atOpening := int(24*time.Hour/ytBroadcastPoll) * QuotaCostListBroadcasts
	atCeiling := int(24*time.Hour/ytBroadcastPollMax) * QuotaCostListBroadcasts
	loadBearing := atOpening > DefaultQuotaUnits/5
	t.Logf("idle day at the opening interval: %d units (%.0f%% of the allowance); "+
		"at the ceiling: %d units. Backoff load-bearing at the current cost: %v",
		atOpening, float64(atOpening)/float64(DefaultQuotaUnits)*100, atCeiling, loadBearing)
	if !loadBearing {
		t.Logf("the backoff is not what keeps this within budget today -- the cost "+
			"being %d is. If QuotaCostListBroadcasts is ever measured higher, the "+
			"backoff becomes the thing holding the line and this flips.",
			QuotaCostListBroadcasts)
	}
}
