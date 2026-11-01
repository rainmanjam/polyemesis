package db

import (
	"strings"
	"testing"
)

// The X go-live announcement setting. X posts an announcement every time a
// broadcast is published unless the PUBLISH carries should_not_tweet. polyemesis
// opens a new X broadcast after X ends one (a timeout, or a renew while the
// destination stays on), so without a setting every renew posted again. The
// destination says when the post is wanted: on the first go-live of a show only
// (the default, stored as "" or "first"), on every go-live, or never.

// TestAnXDestinationKeepsItsGoLiveTweetSettingThroughCreateAndUpdate proves both
// write paths store the setting and the read path returns it. A column missing
// from destColumns, the insert or the update list would read back empty.
//
// MUTATION: x_go_live_tweet removed from destUpdateCols. Observed: FAIL, the
// update left "every" in place.
func TestAnXDestinationKeepsItsGoLiveTweetSettingThroughCreateAndUpdate(t *testing.T) {
	d := testDB(t)
	in := validDest()
	in.Platform = PlatformX
	in.XGoLiveTweet = XTweetEveryGoLive
	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create X destination with a tweet setting: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.XGoLiveTweet != XTweetEveryGoLive {
		t.Fatalf("tweet setting after create = %q, want %q", got.XGoLiveTweet, XTweetEveryGoLive)
	}
	got.XGoLiveTweet = XTweetNever
	if _, err := d.UpdateDestination(got); err != nil {
		t.Fatalf("update the tweet setting: %v", err)
	}
	again, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back after update: %v", err)
	}
	if again.XGoLiveTweet != XTweetNever {
		t.Fatalf("tweet setting after update = %q, want %q", again.XGoLiveTweet, XTweetNever)
	}
}

// TestAnUpgradedXDestinationTweetsOnTheFirstGoLiveOnly: the empty value, which
// every row an upgrade finds carries, is the default, first go-live only.
func TestAnUpgradedXDestinationTweetsOnTheFirstGoLiveOnly(t *testing.T) {
	d := testDB(t)
	in := validDest()
	in.Platform = PlatformX
	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.XGoLiveTweet != "" || got.XGoLiveTweetMode() != XTweetFirstGoLive {
		t.Fatalf("stored %q, mode %q; want empty, read as %q", got.XGoLiveTweet, got.XGoLiveTweetMode(), XTweetFirstGoLive)
	}
}

// TestAnUnknownXGoLiveTweetSettingIsRefused: anything but the three choices is a
// typo or a client from the future, and a silent fallback would post, or not
// post, against the operator's wish.
func TestAnUnknownXGoLiveTweetSettingIsRefused(t *testing.T) {
	in := validDest()
	in.Platform = PlatformX
	in.XGoLiveTweet = "sometimes"
	if err := in.Validate(); err == nil || !strings.Contains(err.Error(), "announcement") {
		t.Fatalf("Validate with tweet setting %q = %v, want an announcement refusal", in.XGoLiveTweet, err)
	}
	for _, ok := range []string{"", XTweetFirstGoLive, XTweetEveryGoLive, XTweetNever} {
		in.XGoLiveTweet = ok
		if err := in.Validate(); err != nil {
			t.Fatalf("Validate with tweet setting %q = %v, want accepted", ok, err)
		}
	}
}

// TestAnXGoLiveTweetSettingIsRefusedWhereNothingWouldSendIt: only an X PUBLISH
// reads it. On any other platform it would be kept and never acted on.
func TestAnXGoLiveTweetSettingIsRefusedWhereNothingWouldSendIt(t *testing.T) {
	in := validDest()
	in.Platform = PlatformTwitch
	in.XGoLiveTweet = XTweetNever
	if err := in.Validate(); err == nil || !strings.Contains(err.Error(), "announcement") {
		t.Fatalf("Validate on a Twitch destination with a tweet setting = %v, want an announcement refusal", err)
	}
}
