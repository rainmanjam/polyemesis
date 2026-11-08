package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// facebookCreates counts the live videos the stub was asked to create.
func facebookCreates(stub *platformStub) int {
	return len(stub.matching(http.MethodPost, "/me/live_videos"))
}

func storedKey(t *testing.T, f refreshFixture) string {
	t.Helper()
	d, err := f.store.GetDestination(f.dest.ID)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	return d.StreamKey
}

// A Facebook key is valid for one live video. Stop then start of an
// account-linked destination must publish with a key that has not been used,
// not with the one the ended video left behind.
//
// Mutation: make renewFacebookKeyForStart return (false, "") at the top. Red:
// the first start publishes with the old stored key and the stub saw no create.
func TestStartingAnAccountLinkedFacebookDestinationAfterAStopUsesAFreshKey(t *testing.T) {
	f := newRefreshFixture(t, false)
	f.stub.fbKeys = []string{"key-first-video", "key-second-video", "key-third-video"}

	// burned is every key a live video has already used. Publishing with one is
	// what Facebook refuses with "Input/output error".
	burned := map[string]bool{"sk-live-old": true}
	start := func(label string, wantCreates int) string {
		t.Helper()
		send(t, f.h, f.sign, http.MethodPost, destinationPath(f.dest.ID, "/start"), nil, http.StatusOK)
		key := storedKey(t, f)
		if burned[key] {
			t.Fatalf("%s: the destination started with a key that already belonged to an ended live video", label)
		}
		if got := facebookCreates(f.stub); got != wantCreates {
			t.Fatalf("%s: %d live videos created, want %d", label, got, wantCreates)
		}
		burned[key] = true
		return key
	}

	if k := start("first start", 1); k != "key-first-video" {
		t.Fatalf("first start key = %q, want the key of the new live video", k)
	}
	send(t, f.h, f.sign, http.MethodPost, destinationPath(f.dest.ID, "/stop"), nil, http.StatusOK)
	if k := start("start after stop", 2); k != "key-second-video" {
		t.Fatalf("restart key = %q, want the key of a second new live video", k)
	}
}

// A destination with no connected account has a pasted key and nothing to renew
// it with: starting it must not touch Graph and must leave the key alone.
func TestStartingAManualKeyFacebookDestinationCallsNothing(t *testing.T) {
	f := newRefreshFixture(t, false)
	manual, err := f.store.CreateDestination(&db.Destination{
		Name: "Facebook manual", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp/", StreamKey: "pasted-key",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	send(t, f.h, f.sign, http.MethodPost, destinationPath(manual.ID, "/start"), nil, http.StatusOK)
	if n := len(f.stub.calls()); n != 0 {
		t.Fatalf("%d platform calls for a manual-key start, want 0", n)
	}
	got, _ := f.store.GetDestination(manual.ID)
	if got.StreamKey != "pasted-key" {
		t.Fatalf("manual key changed to %q", got.StreamKey)
	}
}

// A failed renewal never blocks the start, and says so.
func TestAFailedKeyRenewalStillStartsAndWarns(t *testing.T) {
	f := newRefreshFixture(t, false)
	f.stub.setCreateErr("(#4) Application request limit reached")
	body := send(t, f.h, f.sign, http.MethodPost, destinationPath(f.dest.ID, "/start"), nil, http.StatusOK)
	if !strings.Contains(string(body), "fresh Facebook stream key") {
		t.Fatalf("no warning about the missing renewal: %s", body)
	}
	if got, _ := f.store.GetDestination(f.dest.ID); !got.Enabled || got.StreamKey != "sk-live-old" {
		t.Fatalf("enabled=%v key=%q: the start must go ahead on the stored key", got.Enabled, got.StreamKey)
	}
	if strings.Contains(string(body), "sk-live-old") {
		t.Fatalf("the stream key leaked into the response: %s", body)
	}
}

// The gate that decides whether a start is a NEW start. A supervisor respawn
// inside one live session, and Start pressed on a process that is already up,
// must not create a second live video, which would split the broadcast.
func TestOnlyANewStartRenewsAFacebookKey(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	acct := int64(1)
	fb := func() *db.Destination {
		return &db.Destination{ID: 7, Platform: db.PlatformFacebook, AccountID: &acct}
	}
	withProc := func(st supervisor.State) []engine.DestStatus {
		return []engine.DestStatus{{ID: 7, Enabled: true, Process: &supervisor.Status{State: st}}}
	}
	announced := func(at time.Time) *db.Destination {
		d := fb()
		d.Facebook.Announce(1, at, "777", now)
		return d
	}
	grace := func(int64) time.Duration { return 5 * time.Minute }
	manual := fb()
	manual.AccountID = nil
	other := fb()
	other.Platform = db.PlatformYouTube

	cases := []struct {
		name  string
		d     *db.Destination
		stats []engine.DestStatus
		want  bool
	}{
		{"never started", fb(), nil, true},
		{"stopped process", fb(), withProc(supervisor.StateStopped), true},
		{"held after quick refusals", fb(), withProc(supervisor.StateFailed), true},
		{"running: same live video", fb(), withProc(supervisor.StateRunning), false},
		{"supervisor respawning mid-session", fb(), withProc(supervisor.StateReconnecting), false},
		{"starting", fb(), withProc(supervisor.StateStarting), false},
		{"manual key, no account", manual, nil, false},
		{"another platform", other, nil, false},
		{"announced show still ahead", announced(now.Add(30 * time.Minute)), nil, false},
		{"announced show inside its grace", announced(now.Add(-4 * time.Minute)), nil, false},
		{"announced show past its grace", announced(now.Add(-6 * time.Minute)), nil, true},
	}
	for _, c := range cases {
		if got := needsFreshFacebookKey(c.d, c.stats, now, grace); got != c.want {
			t.Errorf("%s: needsFreshFacebookKey = %v, want %v", c.name, got, c.want)
		}
	}
}

// A scheduled show is the common start. The scheduler writes the enable flag
// itself rather than through the start route, so it must renew the key too.
//
// Mutation: delete the `a.m.startPrep` call in scheduleActuator
// .SetDestinationEnabled (internal/engine/engine.go). Red: the show starts on
// the used key and no live video is created.
func TestAScheduledShowStartsAnAccountLinkedFacebookDestinationOnAFreshKey(t *testing.T) {
	f := newRefreshFixture(t, false)
	f.stub.fbKeys = []string{"key-for-tonight"}
	now := time.Now()
	seedStartSchedule(t, f.store, scheduler.KindOnce, now.Add(-30*time.Second), f.dest.ID)

	f.s.mgr.Scheduler().Tick(now)

	if got := storedKey(t, f); got != "key-for-tonight" {
		t.Fatalf("the scheduled start published with key %q, want the fresh one (the stored sk-live-old belonged to an ended video)", got)
	}
	if d, _ := f.store.GetDestination(f.dest.ID); !d.Enabled {
		t.Fatal("the schedule did not enable the destination")
	}
	if n := facebookCreates(f.stub); n != 1 {
		t.Fatalf("%d live videos created, want 1", n)
	}
}

// The event page the pre-announce sweep made for THIS show owns the stored key;
// a scheduled start of that show must keep it.
func TestAScheduledShowKeepsTheKeyOfItsAnnouncedEvent(t *testing.T) {
	f := newRefreshFixture(t, false)
	now := time.Now()
	at := now.Add(-30 * time.Second)
	schedID := seedStartSchedule(t, f.store, scheduler.KindOnce, at, f.dest.ID)
	if _, err := f.store.UpdateAnnouncement(f.dest.ID, func(d *db.Destination) bool {
		d.StreamKey = "key-of-the-announced-event"
		d.Facebook.Announce(schedID, at, "777", now)
		return true
	}); err != nil {
		t.Fatalf("seed announcement: %v", err)
	}

	f.s.mgr.Scheduler().Tick(now)

	if got := storedKey(t, f); got != "key-of-the-announced-event" {
		t.Fatalf("stored key = %q: the announced event's key was replaced", got)
	}
	if d, _ := f.store.GetDestination(f.dest.ID); !d.Enabled {
		t.Fatal("the schedule did not enable the destination")
	}
	if n := facebookCreates(f.stub); n != 0 {
		t.Fatalf("%d live videos created for an announced show, want 0", n)
	}
}
