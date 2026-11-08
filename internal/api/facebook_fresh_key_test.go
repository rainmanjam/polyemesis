package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
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
// inside one live session, a delay buffer still filling, a drain, and Start
// pressed on a process that is already up must not create a second live video,
// which would split the broadcast.
func TestOnlyANewStartRenewsAFacebookKey(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	acct := int64(1)
	fb := func() *db.Destination {
		return &db.Destination{ID: 7, Platform: db.PlatformFacebook, AccountID: &acct}
	}
	announced := func(at time.Time) *db.Destination {
		d := fb()
		d.Facebook.Announce(1, at, "777", now)
		return d
	}
	manual := fb()
	manual.AccountID = nil
	other := fb()
	other.Platform = db.PlatformYouTube
	grace := func(g time.Duration) func(int64) time.Duration {
		return func(int64) time.Duration { return g }
	}

	cases := []struct {
		name  string
		d     *db.Destination
		inUse bool
		grace time.Duration
		want  bool
	}{
		{"never started, or held, failed or stopped", fb(), false, 5 * time.Minute, true},
		{"running, respawning, filling a delay or draining", fb(), true, 5 * time.Minute, false},
		{"manual key, no account", manual, false, 5 * time.Minute, false},
		{"another platform", other, false, 5 * time.Minute, false},
		{"announced show still ahead", announced(now.Add(30 * time.Minute)), false, 5 * time.Minute, false},
		{"announced show inside its grace", announced(now.Add(-4 * time.Minute)), false, 5 * time.Minute, false},
		{"announced show past its grace", announced(now.Add(-6 * time.Minute)), false, 5 * time.Minute, true},
		{"a 24h grace is capped: aired and stopped, reused", announced(now.Add(-announcedKeepCap - time.Minute)), false, 24 * time.Hour, true},
		{"a 24h grace, inside the cap", announced(now.Add(-announcedKeepCap + time.Minute)), false, 24 * time.Hour, false},
	}
	for _, c := range cases {
		if got := needsFreshFacebookKey(c.d, c.inUse, now, grace(c.grace)); got != c.want {
			t.Errorf("%s: needsFreshFacebookKey = %v, want %v", c.name, got, c.want)
		}
	}
}

// post runs a route and returns the decoded body.
func fbPost(t *testing.T, f refreshFixture, method, path string, body any) map[string]any {
	t.Helper()
	raw := send(t, f.h, f.sign, method, path, body, http.StatusOK)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// Item 1: a destination the engine is carrying (a delay buffer still filling
// has no process, a drain reports Enabled=false) is NOT a new start, whichever
// route presses Start or Restart.
//
// Mutation: make destinationInUse return false. Red on every sub-case.
func TestNoRenewalWhileTheEngineIsCarryingTheDestination(t *testing.T) {
	for _, route := range []struct{ name, path string }{
		{"start", "/start"}, {"restart", "/restart"},
	} {
		t.Run(route.name, func(t *testing.T) {
			f := newRefreshFixture(t, route.name == "restart")
			f.s.destInUse = func(int64) bool { return true }
			fbPost(t, f, http.MethodPost, destinationPath(f.dest.ID, route.path), nil)
			if n := facebookCreates(f.stub); n != 0 {
				t.Fatalf("%d live videos created while the engine was carrying the destination, want 0", n)
			}
			if got := storedKey(t, f); got != "sk-live-old" {
				t.Fatalf("key = %q, want it untouched", got)
			}
		})
	}
}

// Item 4: Restart on a destination that is switched off must not create a live
// video nothing will use. Mutation: pass requireEnabled=false in
// handleRestartDestination.
func TestRestartOfAStoppedDestinationCreatesNothing(t *testing.T) {
	f := newRefreshFixture(t, false)
	fbPost(t, f, http.MethodPost, destinationPath(f.dest.ID, "/restart"), nil)
	if n := facebookCreates(f.stub); n != 0 {
		t.Fatalf("%d live videos created for a restart of a stopped destination, want 0", n)
	}
}

// Restart's renewed-key branch: a switched-on destination whose process is
// held gets a fresh key and the reconcile that applies it.
func TestRestartOfAHeldDestinationRenewsItsKey(t *testing.T) {
	f := newRefreshFixture(t, true)
	f.stub.fbKeys = []string{"key-after-restart"}
	fbPost(t, f, http.MethodPost, destinationPath(f.dest.ID, "/restart"), nil)
	if got := storedKey(t, f); got != "key-after-restart" {
		t.Fatalf("key = %q, want the fresh one", got)
	}
	if n := facebookCreates(f.stub); n != 1 {
		t.Fatalf("%d live videos created, want 1", n)
	}
}

// Start all maps a failed renewal to a warned result that names the problem and
// still starts the destination. Mutation: drop the KeyWarning branch in bulkOne.
func TestStartAllReportsAFailedKeyRenewalAsWarned(t *testing.T) {
	f := newRefreshFixture(t, false)
	f.stub.setCreateErr("(#4) Application request limit reached")
	out := fbPost(t, f, http.MethodPost, "/api/v1/destinations/start-all", nil)
	results, _ := out["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v", out)
	}
	r := results[0].(map[string]any)
	if r["outcome"] != "warned" || !strings.Contains(r["message"].(string), "fresh Facebook stream key") {
		t.Fatalf("result = %v, want a warned outcome naming the missing renewal", r)
	}
	if d, _ := f.store.GetDestination(f.dest.ID); !d.Enabled {
		t.Fatal("a failed renewal must not stop the destination starting")
	}
}

// Item 2: two presses of Start at once create ONE live video. The create is
// slowed so both requests are inside the gate together.
//
// Mutation: delete the `defer s.freshKey.lock(id)()` in applyDestinationEnabled.
// Red: two live videos.
func TestTwoConcurrentStartsCreateOneLiveVideo(t *testing.T) {
	f := newRefreshFixture(t, false)
	f.stub.duringFacebookCreate = func() { time.Sleep(300 * time.Millisecond) }
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fbPost(t, f, http.MethodPost, destinationPath(f.dest.ID, "/start"), nil)
		}()
	}
	wg.Wait()
	if n := facebookCreates(f.stub); n != 1 {
		t.Fatalf("%d live videos created by two simultaneous starts, want 1", n)
	}
}

// A second start in the gap between the enable flag and the process (the
// scheduler writes the flag and reconciles after) does not mint a second video.
// Mutation: delete the renewedWithin check in renewFacebookKeyLocked.
func TestAStartInTheGapBeforeTheProcessSpawnsCreatesNothingMore(t *testing.T) {
	f := newRefreshFixture(t, false)
	fbPost(t, f, http.MethodPost, destinationPath(f.dest.ID, "/start"), nil)
	fbPost(t, f, http.MethodPost, destinationPath(f.dest.ID, "/start"), nil)
	if n := facebookCreates(f.stub); n != 1 {
		t.Fatalf("%d live videos created, want 1", n)
	}
}

// Item 5: saving a destination with enabled=true is a start.
// Mutation: delete the `!wasEnabled && updated.Enabled` block in the PUT handler.
func TestSavingADestinationOnRenewsItsKey(t *testing.T) {
	f := newRefreshFixture(t, false)
	f.stub.fbKeys = []string{"key-from-the-save"}
	fbPost(t, f, http.MethodPut, destinationPath(f.dest.ID, ""), map[string]any{"enabled": true})
	if got := storedKey(t, f); got != "key-from-the-save" {
		t.Fatalf("key = %q, want the fresh one", got)
	}
	if d, _ := f.store.GetDestination(f.dest.ID); !d.Enabled {
		t.Fatal("the save did not enable the destination")
	}
	// A save that leaves it on is not a start.
	fbPost(t, f, http.MethodPut, destinationPath(f.dest.ID, ""), map[string]any{"name": "renamed"})
	if n := facebookCreates(f.stub); n != 1 {
		t.Fatalf("%d live videos created, want 1", n)
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
// a scheduled start of that show keeps it. Paired with the stale case below, so
// the two together fail if the hook is removed (the stale one never renews) AND
// if the announcement rule is removed (this one renews).
//
// Mutation: drop the Broadcasts() loop in needsFreshFacebookKey. Red here.
func TestAScheduledShowKeepsTheKeyOfItsAnnouncedEvent(t *testing.T) {
	f := newRefreshFixture(t, false)
	f.stub.fbKeys = []string{"a-key-nobody-should-be-given"}
	now := time.Now()
	at := now.Add(-30 * time.Second)
	schedID := seedStartSchedule(t, f.store, scheduler.KindOnce, at, f.dest.ID)
	seedAnnouncement(t, f, schedID, at, now)

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

func seedAnnouncement(t *testing.T, f refreshFixture, schedID int64, at, now time.Time) {
	t.Helper()
	if _, err := f.store.UpdateAnnouncement(f.dest.ID, func(d *db.Destination) bool {
		d.StreamKey = "key-of-the-announced-event"
		d.Facebook.Announce(schedID, at, "777", now)
		return true
	}); err != nil {
		t.Fatalf("seed announcement: %v", err)
	}
}

// An announcement for a show that has long since aired does not hold the key: a
// scheduled start for a LATER show renews.
func TestAScheduledShowRenewsPastAStaleAnnouncement(t *testing.T) {
	f := newRefreshFixture(t, false)
	f.stub.fbKeys = []string{"key-for-the-next-show"}
	now := time.Now()
	seedAnnouncement(t, f, 0, now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	seedStartSchedule(t, f.store, scheduler.KindOnce, now.Add(-30*time.Second), f.dest.ID)

	f.s.mgr.Scheduler().Tick(now)

	if got := storedKey(t, f); got != "key-for-the-next-show" {
		t.Fatalf("stored key = %q, want the fresh one", got)
	}
}

// Item 3: a sweep with several Facebook targets renews them concurrently, and a
// slow Graph is cut off at the shared deadline.
//
// Mutation (concurrency): run the goroutine body inline in
// prepareDestinationStarts. Red: the sweep takes the sum of the creates.
func TestScheduledRenewalsRunConcurrentlyUnderOneDeadline(t *testing.T) {
	f := newRefreshFixture(t, false)
	second, err := f.store.CreateDestination(&db.Destination{
		Name: "Facebook two", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://ingest.example/app", StreamKey: "sk-live-old-2", AccountID: &f.acctID,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.stub.duringFacebookCreate = func() { time.Sleep(500 * time.Millisecond) }

	t0 := time.Now()
	release := f.s.prepareDestinationStarts([]int64{f.dest.ID, second.ID})
	elapsed := time.Since(t0)
	release()
	if n := facebookCreates(f.stub); n != 2 {
		t.Fatalf("%d live videos created, want 2", n)
	}
	if elapsed > 900*time.Millisecond {
		t.Fatalf("two renewals took %v: they ran one after the other", elapsed)
	}
}

func TestAScheduledRenewalPastTheDeadlineIsAbandonedAndTheStartProceeds(t *testing.T) {
	old := scheduledRenewTimeout
	scheduledRenewTimeout = 150 * time.Millisecond
	t.Cleanup(func() { scheduledRenewTimeout = old })
	f := newRefreshFixture(t, false)
	f.stub.duringFacebookCreate = func() { time.Sleep(1500 * time.Millisecond) }

	t0 := time.Now()
	release := f.s.prepareDestinationStarts([]int64{f.dest.ID})
	elapsed := time.Since(t0)
	release()
	if elapsed > 1300*time.Millisecond {
		t.Fatalf("the sweep waited %v for a renewal with a %v deadline", elapsed, scheduledRenewTimeout)
	}
	if got := storedKey(t, f); got != "sk-live-old" {
		t.Fatalf("key = %q: an abandoned renewal must leave the stored key", got)
	}
}

// A start that renewed the key and a PUT with enabled:true racing it must end on
// the START's key, with one live video. The PUT is released only once the
// start's create is in flight, so it reads the stored (dead) key early and would
// write it back late.
//
// Mutation: in handleUpdateDestination, move `defer s.freshKey.lock(id)()` to
// after the GetDestination read. Red: the stored key is the pre-start one.
func TestAConcurrentStartAndSaveNeverWriteTheDeadKeyBack(t *testing.T) {
	f := newRefreshFixture(t, false)
	f.stub.fbKeys = []string{"key-from-the-start", "key-nobody-should-get"}
	inFlight := make(chan struct{}, 1)
	f.stub.setOnCall(func(c stubCall) {
		if isFacebookCreate(c) {
			select {
			case inFlight <- struct{}{}:
			default:
			}
		}
	})
	f.stub.duringFacebookCreate = func() { time.Sleep(400 * time.Millisecond) }

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		fbPost(t, f, http.MethodPost, destinationPath(f.dest.ID, "/start"), nil)
	}()
	go func() {
		defer wg.Done()
		select {
		case <-inFlight:
		case <-time.After(5 * time.Second):
			t.Error("the start's live video was never requested")
		}
		fbPost(t, f, http.MethodPut, destinationPath(f.dest.ID, ""), map[string]any{"enabled": true})
	}()
	wg.Wait()

	if got := storedKey(t, f); got != "key-from-the-start" {
		t.Fatalf("stored key = %q, want the key the start fetched (sk-live-old is the dead one)", got)
	}
	if n := facebookCreates(f.stub); n != 1 {
		t.Fatalf("%d live videos created, want 1", n)
	}
}

// While a save's renewal waits on a slow Graph, install-wide routes that share
// trackGainMu (track gain, OBS levels, routing presets) still complete.
//
// Mutation: take s.trackGainMu.Lock() before the renewal in
// handleUpdateDestination. Red: the gain request waits out the Graph call.
func TestASavesKeyRenewalDoesNotHoldTheTrackGainLock(t *testing.T) {
	f := newRefreshFixture(t, false)
	inFlight := make(chan struct{}, 1)
	f.stub.setOnCall(func(c stubCall) {
		if isFacebookCreate(c) {
			select {
			case inFlight <- struct{}{}:
			default:
			}
		}
	})
	f.stub.duringFacebookCreate = func() { time.Sleep(1500 * time.Millisecond) }

	done := make(chan struct{})
	go func() {
		defer close(done)
		fbPost(t, f, http.MethodPut, destinationPath(f.dest.ID, ""), map[string]any{"enabled": true})
	}()
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("the save never asked Facebook for a live video")
	}

	t0 := time.Now()
	r := jsonRequest(t, http.MethodPost, destinationPath(f.dest.ID, "/tracks/1/gain"), map[string]any{"muted": true})
	f.sign(r)
	do(t, f.h, r) // any status: only that it is not held behind the Graph call
	if elapsed := time.Since(t0); elapsed > 700*time.Millisecond {
		t.Fatalf("the track gain route took %v: it waited behind the save's Graph call", elapsed)
	}
	<-done
}

// A save that will be refused must not leave an orphan live video on the
// operator's Facebook page: the renewal waits for the same checks the write
// runs.
//
// Mutation: delete the planDestinationUpdate pre-check in
// handleUpdateDestination. Red: one live video created, then a 400.
func TestARefusedSaveCreatesNoLiveVideo(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"unknown field":  {"enabled": true, "noSuchField": 1},
		"unknown preset": {"enabled": true, "presetId": "no-such-preset"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRefreshFixture(t, false)
			send(t, f.h, f.sign, http.MethodPut, destinationPath(f.dest.ID, ""), body, http.StatusBadRequest)
			if n := facebookCreates(f.stub); n != 0 {
				t.Fatalf("%d live videos created for a save that was refused, want 0", n)
			}
			d, _ := f.store.GetDestination(f.dest.ID)
			if d.Enabled || d.StreamKey != "sk-live-old" {
				t.Fatalf("a refused save changed the row: enabled=%v key=%q", d.Enabled, d.StreamKey)
			}
		})
	}
}

// A save refused for a go-live title on a platform that never sends one (Facebook) is refused BEFORE the key renewal
// a save that switches the destination on makes: no live video, and the row is untouched.
//
// Mutation: in refuseUnsendableGoLiveTitle make the condition `true ||` first so it never refuses. Red: the save
// succeeds, the title is silently dropped and a live video is created.
func TestAGoLiveTitleRefusalCreatesNoFacebookLiveVideo(t *testing.T) {
	f := newRefreshFixture(t, false)
	send(t, f.h, f.sign, http.MethodPut, destinationPath(f.dest.ID, ""),
		map[string]any{"enabled": true, "goLiveTitle": "not for Facebook"}, http.StatusBadRequest)
	if n := facebookCreates(f.stub); n != 0 {
		t.Fatalf("%d live videos created for a save refused over its go-live title, want 0", n)
	}
	d, _ := f.store.GetDestination(f.dest.ID)
	if d.Enabled || d.StreamKey != "sk-live-old" || d.GoLiveTitle != "" {
		t.Fatalf("a refused save changed the row: %+v", d)
	}
}
