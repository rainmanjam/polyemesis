package chat

import (
	"context"
	"fmt"
	"net/http"
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

/* THE TWO TESTS ABOVE NEVER RUN THE LOOP.
 *
 * One divides a day by ytBroadcastPollMax; the other compares ytBroadcastPollMax
 * with ytBroadcastPoll. Both are arithmetic over constants, and both stay green
 * with the backoff DELETED -- take the `if idleFor < ytBroadcastPollMax { idleFor
 * *= 2 }` out of Run and an idle install polls at the opening rate for ever,
 * which is 2,880 looks a day instead of 288, while this file still passes and
 * still prints its reassuring log line.
 *
 * That is the shape this whole sweep was about: a guard reading a proxy for the
 * behaviour it is named for. Every Sleep stub in youtube_test.go discards the
 * duration it is handed, so nothing anywhere observes the schedule.
 *
 * This runs the real Run against a broadcast that never goes live, records what
 * it actually sleeps, and derives the day's looks from the recording.
 */
func TestDiscoveryActuallyBacksOffInTheLoop(t *testing.T) {
	// No live broadcast, ever: liveChatID returns "" and Run takes the idle path.
	stub := newYTStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		fmt.Fprint(w, `{"items":[]}`)
	})

	const rounds = 12
	var slept []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		// Records rather than waits. Returning true keeps the loop going; after
		// `rounds` intervals there is nothing further to learn, so it cancels
		// and reports the loop should stop.
		c.Sleep = func(_ context.Context, d time.Duration) bool {
			slept = append(slept, d)
			if len(slept) >= rounds {
				cancel()
				return false
			}
			return true
		}
	})

	if err := a.Run(ctx, SinkFunc(func(Message) {})); err != nil {
		t.Fatalf("Run returned %v, want nil when the context ends", err)
	}

	// POSITIVE CONTROL. A Sleep that was never called records nothing, and every
	// assertion below is vacuous over an empty slice.
	if len(slept) < 3 {
		t.Fatalf("the discovery loop slept %d times; it was never exercised and "+
			"this test asserts nothing", len(slept))
	}

	// It must OPEN where the constant says, or the first look is mistimed.
	if slept[0] != ytBroadcastPoll {
		t.Errorf("first idle wait was %s, want ytBroadcastPoll (%s)", slept[0], ytBroadcastPoll)
	}

	// It must actually grow. This is the assertion that the deleted-backoff
	// mutant fails and the two constant-comparing tests above do not.
	if slept[1] <= slept[0] {
		t.Fatalf("the loop slept %s then %s: it is not backing off, so an idle "+
			"install polls at the opening rate for ever. TestDiscoveryBacksOff "+
			"compares the two constants and cannot see this.", slept[0], slept[1])
	}

	// And it must stop growing, or a long idle drifts to hours between looks and
	// a broadcast that does start is discovered arbitrarily late.
	last := slept[len(slept)-1]
	if last != ytBroadcastPollMax {
		t.Errorf("after %d idle rounds the wait is %s, want it capped at "+
			"ytBroadcastPollMax (%s)", len(slept), last, ytBroadcastPollMax)
	}

	// The day's cost, derived from the SCHEDULE THE LOOP PRODUCED rather than
	// from the ceiling constant: walk the recorded intervals, then continue at
	// the settled interval for the rest of the day.
	var elapsed time.Duration
	looks := 0
	for _, d := range slept {
		if elapsed+d > 24*time.Hour {
			break
		}
		elapsed += d
		looks++
	}
	if last > 0 {
		looks += int((24*time.Hour - elapsed) / last)
	}
	spend := looks * QuotaCostListBroadcasts
	if budget := DefaultQuotaUnits / 5; spend > budget {
		t.Errorf("the loop's own schedule costs %d units a day (%d looks at %d), "+
			"over the %d-unit ceiling TestIdleDiscoveryCannotEatTheDay asserts",
			spend, looks, QuotaCostListBroadcasts, budget)
	}
	t.Logf("measured schedule: first %s, settled %s, %d looks/day, %d units",
		slept[0], last, looks, spend)
}
