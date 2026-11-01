package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// Follow-ups to the planned rollover (#19) from the wave 3 re-judge. Each test names the
// finding it pins.

// dueYouTubeRig is a YouTube destination well inside its margin window, so one sweep rolls it.
func dueYouTubeRig(t *testing.T) *rolloverRig {
	t.Helper()
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	return r
}

// #5: the old broadcast was ended and the replacement would not start. The record must
// move to the replacement in a phase the lifecycle coordinator takes live, or every later
// sweep reads an ended broadcast and the show stays dark.
func TestARolloverWhoseReplacementWouldNotStartAfterTheOldEndedPointsAtTheReplacement(t *testing.T) {
	r := dueYouTubeRig(t)
	r.yt.err = &oauth.RolloverError{Stage: "start", OldStillLive: false, NewBroadcastID: "new-2"}

	r.c.sweepOnce(context.Background())

	lc := r.store.dests[1].Lifecycle
	if lc.BroadcastID != "new-2" {
		t.Fatalf("the destination points at %q; the old broadcast is ended, so it must point at new-2", lc.BroadcastID)
	}
	if lc.Phase == phaseLive || isTerminalPhase(lc.Phase) {
		t.Errorf("phase = %q; want a non-live, non-terminal phase so the lifecycle takes it live", lc.Phase)
	}
	if lc.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 so the lifecycle is not already holding", lc.Attempts)
	}
	if len(r.store.results) != 1 || !strings.HasPrefix(r.store.results[0], "failed: ") {
		t.Errorf("results = %v; the rollover still failed and must say so", r.store.results)
	}
}

// #5, the other side: while the old broadcast is still live the record must stay on it.
func TestARolloverThatFailsWithTheOldStillLiveKeepsTheOldRecord(t *testing.T) {
	r := dueYouTubeRig(t)
	r.yt.err = &oauth.RolloverError{Stage: "bind", OldStillLive: true, NewBroadcastID: "new-2"}
	r.c.sweepOnce(context.Background())
	if got := r.store.dests[1].Lifecycle.BroadcastID; got != "live-1" {
		t.Errorf("the destination moved to %q while the old broadcast was still live", got)
	}
}

// #14: a successful rollover's warnings are platform text. They reach the fault, the
// result, the webhook and the timeline, and must be redacted on the way, as failures are.
func TestASuccessfulRolloversWarningsAreRedactedEverywhereTheyGo(t *testing.T) {
	r := dueYouTubeRig(t)
	secret := "rtmp://a.rtmp.youtube.com/live2/" + runTokenFixture()
	r.yt.outcome = &oauth.RolloverOutcome{NewBroadcastID: "new-1", Warnings: []string{"could not end old: " + secret}}

	r.c.sweepOnce(context.Background())

	if f := r.store.dests[1].Lifecycle.Fault; f == "" || strings.Contains(f, runTokenFixture()) {
		t.Errorf("fault = %q; want the warning, redacted", f)
	}
	for _, ev := range r.announced {
		if strings.Contains(ev.Reason, runTokenFixture()) {
			t.Errorf("event %s carries the credential: %q", ev.Trigger, ev.Reason)
		}
	}
	for _, row := range r.store.timelineRows() {
		if strings.Contains(row.Detail, runTokenFixture()) {
			t.Errorf("timeline row carries the credential: %q", row.Detail)
		}
	}
}

// #9: a countdown-only platform (Facebook, 8 h) still has a countdown. The session is
// recorded on the first sighting, and kept on later sweeps rather than moved to now.
func TestACountdownOnlyDestinationRecordsItsSessionAndKeepsIt(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(2, db.PlatformFacebook, "fb-live")
	r.enable(2, "")
	first := r.now

	r.c.sweepOnce(context.Background())
	if got := r.store.settings[2]; !got.SessionStartedAt.Equal(first) || got.SessionBroadcastID != "fb-live" {
		t.Fatalf("session = %s/%q, want %s/fb-live", got.SessionStartedAt, got.SessionBroadcastID, first)
	}
	r.now = r.now.Add(time.Hour)
	r.c.sweepOnce(context.Background())
	if got := r.store.settings[2].SessionStartedAt; !got.Equal(first) {
		t.Errorf("the second sweep moved the session start to %s; the countdown would never advance", got)
	}
}

// #10: the notice goes to the chat of the destination's OWN account, not to every chat
// of the same platform.
func TestTheRolloverChatNoticeNamesTheDestinationsOwnAccount(t *testing.T) {
	r := aDueTwitchRig(t, true)
	r.c.host.tokenFor = func(context.Context, int64) (*db.PlatformAccount, error) {
		return &db.PlatformAccount{AccessToken: "tok", AccountRef: "owner-ref"}, nil
	}
	var got []string
	r.c.host.chatNotice = func(_ context.Context, p db.Platform, account, _ string) error {
		got = append(got, string(p)+"/"+account)
		return nil
	}
	r.c.sweepOnce(context.Background())
	if len(got) != 1 || got[0] != "twitch/owner-ref" {
		t.Errorf("notices = %v, want one to twitch/owner-ref", got)
	}
}

// #11: with a template, the replacement gets the template's category and thumbnail too.
func TestTheReplacementTakesTheTemplatesThumbnailAndCategory(t *testing.T) {
	r := dueYouTubeRig(t)
	ed := &fakeRolloverEditor{fakeYouTube: r.yt}
	r.c.host.youtube = func() (youtubeRollover, bool) { return ed, true }
	r.store.templates["1/live-1"] = &scheduler.BroadcastTemplate{
		ScheduleID: 5, TitlePattern: "Show {date}", Category: "Gaming", HasThumbnail: true,
	}
	r.store.thumbs = map[int64][]byte{5: []byte("\x89PNG\r\n\x1a\nfake")}

	r.c.sweepOnce(context.Background())

	if ed.category != "Gaming" || ed.editedID != "new-1" {
		t.Errorf("category edit = %q on %q, want Gaming on new-1", ed.category, ed.editedID)
	}
	if ed.thumbID != "new-1" {
		t.Errorf("thumbnail set on %q, want new-1", ed.thumbID)
	}
}

// #13: a pass waits for another only as long as its own context allows.
func TestARolloverPassGivesUpWaitingWhenItsContextEnds(t *testing.T) {
	r := dueYouTubeRig(t)
	r.c.rollSem <- struct{}{} // another pass holds the coordinator
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.c.considerOne(ctx, db.RolloverSettings{DestinationID: 1}, true)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want the context's deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the pass is still blocked on the coordinator lock after its context ended")
	}
}

// #38: after a Twitch reconnect the session is cleared, so the next sweep re-reads Helix's
// started_at. If Twitch kept the old start, the reconnect moment is used, so the margin is
// not due again the moment the debounce ends.
func TestATwitchReconnectRereadsTheStartAndDoesNotLoopOnAnUnchangedOne(t *testing.T) {
	r := aTwitchOnAirRig(t)
	old := r.now.Add(-47*time.Hour - 50*time.Minute)
	r.c.host.twitchStart = func(context.Context, string) (time.Time, error) { return old, nil }
	r.store.settings[1] = db.RolloverSettings{DestinationID: 1, Enabled: true, MarginMinutes: 30, SessionStartedAt: old}

	r.c.sweepOnce(context.Background())
	if len(r.restarts) != 1 {
		t.Fatalf("restarts = %d, want 1", len(r.restarts))
	}
	if got := r.store.settings[1].SessionStartedAt; !got.IsZero() {
		t.Errorf("session start after a reconnect = %s, want zero so it is re-read", got)
	}
	r.now = r.now.Add(11 * time.Minute)
	r.c.sweepOnce(context.Background())
	if len(r.restarts) != 1 {
		t.Errorf("restarts = %d after the debounce; an unchanged Helix start must not restart it again", len(r.restarts))
	}
}

// #46: the OBS subscription closes when it overflows. The loop resubscribes and the sweep
// keeps running, instead of the loop returning for good.
func TestTheRolloverLoopResubscribesAfterTheOBSStreamClosesAndKeepsSweeping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	subs, sweeps := 0, 0
	tick := make(chan time.Time)
	go runRolloverLoop(ctx, func() (<-chan eventstream.Envelope, func()) {
		mu.Lock()
		subs++
		mu.Unlock()
		ch := make(chan eventstream.Envelope)
		close(ch) // overflowed at once
		return ch, func() {}
	}, tick, func(eventstream.Envelope) {}, func(context.Context) {
		mu.Lock()
		sweeps++
		mu.Unlock()
	})
	for i := 0; i < 3; i++ {
		select {
		case tick <- time.Now():
		case <-time.After(2 * time.Second):
			t.Fatal("the loop stopped taking ticks after its subscription closed")
		}
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if sweeps < 3 || subs < 2 {
		t.Errorf("sweeps=%d subscriptions=%d; want the sweep to keep running and the loop to resubscribe", sweeps, subs)
	}
}

// fakeRolloverEditor is the YouTube fake with the template writes the rollover uses.
type fakeRolloverEditor struct {
	*fakeYouTube
	editedID, category, thumbID string
}

func (e *fakeRolloverEditor) EditScheduledBroadcast(_ context.Context, _, id string, ed oauth.ScheduledEdit) ([]string, error) {
	e.editedID, e.category = id, ed.Category
	return nil, nil
}

func (e *fakeRolloverEditor) SetScheduledThumbnail(_ context.Context, _, id string, _ []byte, _ string) error {
	e.thumbID = id
	return nil
}
