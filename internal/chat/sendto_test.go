package chat

import (
	"context"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// liveAdapter is a sending adapter that also says whether a live chat is open.
type liveAdapter struct {
	sendingAdapter
	live bool
}

func (l liveAdapter) LiveChat() bool { return l.live }

func attachAll(t *testing.T, h *Hub, as ...Adapter) {
	t.Helper()
	for _, a := range as {
		if err := h.Attach(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range as {
		a := a
		waitFor(t, "attached", func() bool { _, ok := h.Adapter(a.Platform(), a.Account()); return ok })
	}
}

// TestSendToPostsOnlyWhereThePickerSaysAndReportsTheRestAsSkipped: a chat the
// picker refuses is not posted to, and still comes back as its own result,
// skipped with the code and the words, so the operator sees where it did NOT go.
func TestSendToPostsOnlyWhereThePickerSaysAndReportsTheRestAsSkipped(t *testing.T) {
	h := testHub(t)
	yt := &fakeAdapter{platform: db.PlatformYouTube, account: "y"}
	kk := &fakeAdapter{platform: db.PlatformKick, account: "k"}
	tw := &fakeAdapter{platform: db.PlatformTwitch, account: "t"}
	attachAll(t, h, sendingAdapter{yt}, sendingAdapter{kk}, sendingAdapter{tw})

	res, err := h.SendTo(context.Background(), "hello", func(p db.Platform, _ string) SkipReason {
		if p == db.PlatformYouTube {
			return ""
		}
		return SkipNotLive
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("results = %+v, want one per connected chat", res)
	}
	for _, r := range res {
		if r.Platform == db.PlatformYouTube {
			if !r.OK {
				t.Errorf("youtube = %+v, want sent", r)
			}
			continue
		}
		if r.OK || !r.Skipped || r.Reason != string(SkipNotLive) || r.Detail != "not live" {
			t.Errorf("%s = %+v, want skipped, reason notLive, detail %q", r.Platform, r, "not live")
		}
	}
	for name, a := range map[string]*fakeAdapter{"kick": kk, "twitch": tw} {
		a.mu.Lock()
		n := a.sends
		a.mu.Unlock()
		if n != 0 {
			t.Errorf("%s was asked to send %d times, want never", name, n)
		}
	}
}

// TestSendToWithNothingConnectedIsAnError, as Send is: nothing was attempted.
func TestSendToWithNothingConnectedIsAnError(t *testing.T) {
	h := testHub(t)
	if _, err := h.SendTo(context.Background(), "x", func(db.Platform, string) SkipReason { return "" }); err == nil {
		t.Fatal("SendTo with nothing connected succeeded")
	}
}

// TestLiveChatReadsTheAdaptersOwnAnswer: an adapter that can tell whether a
// live chat is open says so; one that cannot (Twitch, Kick) is never claimed
// live by this signal, because "connected" is not "streaming".
func TestLiveChatReadsTheAdaptersOwnAnswer(t *testing.T) {
	h := testHub(t)
	on := liveAdapter{sendingAdapter{&fakeAdapter{platform: db.PlatformYouTube, account: "on"}}, true}
	off := liveAdapter{sendingAdapter{&fakeAdapter{platform: db.PlatformYouTube, account: "off"}}, false}
	tw := sendingAdapter{&fakeAdapter{platform: db.PlatformTwitch, account: "t"}}
	attachAll(t, h, on, off, tw)

	for _, c := range []struct {
		p    db.Platform
		acct string
		want bool
	}{
		{db.PlatformYouTube, "on", true},
		{db.PlatformYouTube, "off", false},
		{db.PlatformTwitch, "t", false},
		{db.PlatformKick, "nope", false},
	} {
		if got := h.LiveChat(c.p, c.acct); got != c.want {
			t.Errorf("LiveChat(%s,%s) = %v, want %v", c.p, c.acct, got, c.want)
		}
	}
}
