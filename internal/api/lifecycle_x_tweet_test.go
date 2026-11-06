package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// X'S GO-LIVE ANNOUNCEMENT, ONCE PER SHOW.
//
// X posts an announcement every time a broadcast goes live unless the go-live
// (POST /2/broadcasts) carries should_not_tweet, a JSON boolean (PROVISIONAL until a
// live run confirms it). polyemesis opens a new broadcast after X ends one while the
// destination stays on (renew), and that one goes live too, so before the
// destination's XGoLiveTweet setting every renew posted a fresh announcement.
// The default is the first go-live of a show only: the renewals after it go live
// with should_not_tweet. A show starts when the destination is switched on.

// xcPublishedTweetFlags is should_not_tweet as each PUBLISH carried it: true,
// or false when the field was left out.
func xcPublishedTweetFlags(t *testing.T, fake *xcFake) []bool {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var out []bool
	for _, b := range fake.publishBodies {
		v, ok := b["should_not_tweet"]
		if ok && v != true {
			t.Fatalf("should_not_tweet = %#v, want true or absent", v)
		}
		out = append(out, ok)
	}
	return out
}

// xcShowThenRenewal runs a show for a destination with the given setting: it
// opens and publishes a broadcast, X ends it, the encoder stops, polyemesis opens
// the next one, and the encoder comes back, which publishes it.
func xcShowThenRenewal(t *testing.T, setting string) (*xcFake, *db.Destination) {
	t.Helper()
	fake := &xcFake{active: true}
	row := xcRow(true, db.BroadcastControl{})
	row.XGoLiveTweet = setting
	c, store, faults := xcFixture(t, fake, true, row)

	xcSweep(t, c, store) // opens: a pending handle, nothing on X
	xcSweep(t, c, store) // bytes arriving: takes the source live as bc-1
	fake.set(func(f *xcFake) { f.state, f.active = oauth.XStateEnded, false })
	renewed := xcSweep(t, c, store) // ended, encoder stopped: renews to a pending handle
	if renewed.Lifecycle.BroadcastID == "bc-1" || renewed.Lifecycle.BroadcastID == "" {
		t.Fatalf("after X ended the show the recorded broadcast is %q, want a new one", renewed.Lifecycle.BroadcastID)
	}
	fake.set(func(f *xcFake) { f.active = true })
	last := xcSweep(t, c, store) // bytes again: takes the source live as bc-2
	if _, published, _, _ := fake.snapshot(); published != 2 {
		t.Fatalf("published %d broadcasts, want the first show and its renewal", published)
	}
	if len(*faults) != 0 {
		t.Fatalf("faults: %v", *faults)
	}
	return fake, last
}

// TestXFirstGoLiveOfAShowTweetsAndTheRenewalAfterXEndsItDoesNot is the default.
//
// MUTATION: renew resetting the block without the renewal marker. Observed:
// FAIL, the renewal was published without should_not_tweet.
func TestXFirstGoLiveOfAShowTweetsAndTheRenewalAfterXEndsItDoesNot(t *testing.T) {
	fake, last := xcShowThenRenewal(t, "")
	got := xcPublishedTweetFlags(t, fake)
	if len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("should_not_tweet per PUBLISH = %v, want [false true]: the first go-live posts, the renewal does not", got)
	}
	if !last.Lifecycle.Renewed {
		t.Fatalf("the renewed broadcast is not marked as a renewal: %+v", last.Lifecycle)
	}
}

// TestXEveryGoLiveSettingPostsOnTheRenewalToo.
//
// MUTATION: "every" read as "first". Observed: FAIL, the renewal was suppressed.
func TestXEveryGoLiveSettingPostsOnTheRenewalToo(t *testing.T) {
	fake, _ := xcShowThenRenewal(t, db.XTweetEveryGoLive)
	if got := xcPublishedTweetFlags(t, fake); len(got) != 2 || got[0] || got[1] {
		t.Fatalf("should_not_tweet per PUBLISH = %v, want [false false]", got)
	}
}

// TestXNeverSettingSuppressesTheFirstGoLiveAndTheRenewal.
//
// MUTATION: "never" read as "first". Observed: FAIL, the first go-live posted.
func TestXNeverSettingSuppressesTheFirstGoLiveAndTheRenewal(t *testing.T) {
	fake, _ := xcShowThenRenewal(t, db.XTweetNever)
	if got := xcPublishedTweetFlags(t, fake); len(got) != 2 || !got[0] || !got[1] {
		t.Fatalf("should_not_tweet per PUBLISH = %v, want [true true]", got)
	}
}

// TestSwitchingAnXDestinationOffEndsTheShowSoTheNextGoLivePosts: the renewal
// marker must not outlive the show. Two rows keep a lifecycle block across a
// switch-off: a renewal whose open failed (no broadcast, a fault and attempts),
// and a renewal that never aired (created, read once, then left quiet). Both
// lose the marker on the first sweep that sees the destination off, so the next
// show's first go-live posts.
//
// MUTATION: the disabled-row clear in considerRow removed. Observed: FAIL, the
// marker survived the switch-off.
func TestSwitchingAnXDestinationOffEndsTheShowSoTheNextGoLivePosts(t *testing.T) {
	cases := map[string]struct {
		fake *xcFake
		lc   db.BroadcastControl
	}{
		"a renewal whose open failed": {
			fake: &xcFake{},
			lc:   db.BroadcastControl{Renewed: true, Attempts: 2, Fault: "X would not open a broadcast"},
		},
		"a renewal that never aired": {
			fake: &xcFake{},
			lc:   db.BroadcastControl{Renewed: true, BroadcastID: xcPending, Phase: phaseCreated},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, store, _ := xcFixture(t, tc.fake, true, xcRow(false, tc.lc))
			row := xcSweep(t, c, store)
			if row.Lifecycle.Renewed {
				t.Fatalf("a switched-off destination still carries the renewal marker: %+v", row.Lifecycle)
			}
			// Quiet sweeps after it leave it cleared.
			row = xcSweep(t, c, store)
			if row.Lifecycle.Renewed {
				t.Fatalf("the renewal marker came back: %+v", row.Lifecycle)
			}
		})
	}
}

// TestTheNextShowAfterASwitchOffPostsItsFirstGoLive is the same, end to end: a
// renewal that never aired, the destination switched off and on, and the next
// go-live posts.
func TestTheNextShowAfterASwitchOffPostsItsFirstGoLive(t *testing.T) {
	fake := &xcFake{}
	c, store, _ := xcFixture(t, fake, true,
		xcRow(false, db.BroadcastControl{Renewed: true, BroadcastID: xcPending, Phase: phaseCreated}))
	xcSweep(t, c, store) // off
	store.mu.Lock()
	store.rows[5].Enabled = true
	store.mu.Unlock()
	fake.set(func(f *xcFake) { f.active = true })
	xcSweep(t, c, store)
	xcSweep(t, c, store)
	got := xcPublishedTweetFlags(t, fake)
	if len(got) != 1 || got[0] {
		t.Fatalf("should_not_tweet per PUBLISH = %v, want one PUBLISH that posts", got)
	}
}

// TestABroadcastXEndedBeforeItWasEverPublishedDoesNotUseUpTheShowsPost: polyemesis
// opens the X broadcast as soon as the destination is switched on, which can be
// long before the encoder starts. If X ends that unpublished broadcast while the
// encoder is idle, it is renewed, but nothing has been posted yet, so the show's
// first real go-live must still post.
//
// MUTATION: renew carrying Renewed unconditionally. Observed: FAIL, the first
// real go-live was published with should_not_tweet.
func TestABroadcastXEndedBeforeItWasEverPublishedDoesNotUseUpTheShowsPost(t *testing.T) {
	fake := &xcFake{state: oauth.XStateEnded, id: "bc-1", seq: 1}
	c, store, _ := xcFixture(t, fake, true, xcRow(true, db.BroadcastControl{BroadcastID: "bc-1", Phase: phaseCreated}))
	row := xcSweep(t, c, store) // ended unpublished, encoder idle: renewed to bc-2
	if row.Lifecycle.BroadcastID == "bc-1" || row.Lifecycle.BroadcastID == "" {
		t.Fatalf("recorded broadcast = %q, want a renewal", row.Lifecycle.BroadcastID)
	}
	if row.Lifecycle.Renewed {
		t.Fatalf("a renewal of a never-published broadcast is marked as a renewal of the show: %+v", row.Lifecycle)
	}
	fake.set(func(f *xcFake) { f.active = true })
	xcSweep(t, c, store)
	if got := xcPublishedTweetFlags(t, fake); len(got) != 1 || got[0] {
		t.Fatalf("should_not_tweet per PUBLISH = %v, want one PUBLISH that posts", got)
	}
}
