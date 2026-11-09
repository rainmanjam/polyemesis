package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The broadcast is opened (inserted and bound) BEFORE the destination's process is allowed to publish, on every start
// path, so YouTube's auto-start sees the stream's inactive-to-active edge.

type obsFixture struct {
	s     *Server
	h     http.Handler
	store *db.DB
	sign  func(*http.Request)
	f     *ynFake
	dest  *db.Destination
}

func newOBSFixture(t *testing.T, mutate func(*db.Destination)) *obsFixture {
	t.Helper()
	f := newYnFake()
	f.streamState = "inactive"
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	addSource(t, h, sign, "main")
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	d := &db.Destination{Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.rtmp.youtube.com/live2", StreamKey: ynKey, AccountID: &acct}
	if mutate != nil {
		mutate(d)
	}
	created, err := store.CreateDestination(d)
	if err != nil {
		t.Fatal(err)
	}
	return &obsFixture{s: s, h: h, store: store, sign: sign, f: f, dest: created}
}

func (o *obsFixture) post(t *testing.T, path string, want int) {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+itoa(o.dest.ID)+path, nil)
	o.sign(r)
	if w := do(t, o.h, r); w.Code != want {
		t.Fatalf("POST %s = %d: %s", path, w.Code, w.Body.String())
	}
}

func (o *obsFixture) opened() bool {
	d, _ := o.store.GetDestination(o.dest.ID)
	return d.Lifecycle.BroadcastID != "" && d.Lifecycle.Opened
}

// Start, Restart, a save that enables, and Start all each open first: at the moment of the bind the destination is
// still switched OFF, so nothing could have been publishing.
func TestEveryStartPathOpensTheBroadcastBeforeTheDestinationIsSwitchedOn(t *testing.T) {
	paths := map[string]func(t *testing.T, o *obsFixture){
		"start": func(t *testing.T, o *obsFixture) { o.post(t, "/start", http.StatusOK) },
		"save that enables": func(t *testing.T, o *obsFixture) {
			r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+itoa(o.dest.ID), map[string]any{"enabled": true})
			o.sign(r)
			if w := do(t, o.h, r); w.Code != http.StatusOK {
				t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
			}
		},
		"start all": func(t *testing.T, o *obsFixture) {
			r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/start-all", nil)
			o.sign(r)
			if w := do(t, o.h, r); w.Code >= 300 {
				t.Fatalf("start-all = %d: %s", w.Code, w.Body.String())
			}
		},
	}
	for name, run := range paths {
		t.Run(name, func(t *testing.T) {
			o := newOBSFixture(t, nil)
			enabledAtBind := true
			o.f.set(func(f *ynFake) {
				f.onBind = func() {
					d, _ := o.store.GetDestination(o.dest.ID)
					enabledAtBind = d.Enabled
				}
			})
			run(t, o)
			if enabledAtBind {
				t.Error("the broadcast was bound after the destination was already switched on, so video could be flowing")
			}
			if calls, inserts, _ := o.f.snapshot(); inserts != 1 || !strings.Contains(strings.Join(calls, ","), "POST /liveBroadcasts/bind") {
				t.Errorf("calls = %v inserts = %d, want one insert and a bind", calls, inserts)
			}
			if !o.opened() {
				t.Error("the opened broadcast was not recorded")
			}
		})
	}
}

// Restart of a destination that is switched on but not publishing is a new start.
func TestARestartOfAHeldYouTubeDestinationOpensTheBroadcastFirst(t *testing.T) {
	o := newOBSFixture(t, func(d *db.Destination) { d.Enabled = true })
	o.post(t, "/restart", http.StatusOK)
	if _, inserts, _ := o.f.snapshot(); inserts != 1 || !o.opened() {
		t.Errorf("inserts = %d opened = %v, want the restart to open first", inserts, o.opened())
	}
}

// A scheduled start (the engine's pre-start hook) opens first too, and holds the destination's lock until released.
func TestAScheduledStartOpensTheYouTubeBroadcastFirst(t *testing.T) {
	o := newOBSFixture(t, nil)
	release := o.s.prepareDestinationStarts([]int64{o.dest.ID})
	defer release()
	if _, inserts, _ := o.f.snapshot(); inserts != 1 || !o.opened() {
		t.Errorf("inserts = %d opened = %v, want the scheduled start to open before the flag is written", inserts, o.opened())
	}
}

// A respawn inside one session is not a new start: nothing is opened for a destination the engine already carries.
func TestNothingIsOpenedForADestinationTheEngineIsAlreadyCarrying(t *testing.T) {
	o := newOBSFixture(t, nil)
	o.s.destInUse = func(int64) bool { return true }
	o.post(t, "/start", http.StatusOK)
	if calls, _, _ := o.f.snapshot(); len(calls) != 0 {
		t.Errorf("calls = %v, want none for a destination in use", calls)
	}
}

// Destinations polyemesis must not touch, and one that already has a broadcast.
func TestNothingIsOpenedForManualKeyStudioStreamNowOrAnAlreadyOpenedDestination(t *testing.T) {
	for name, mutate := range map[string]func(*db.Destination){
		"manual key":        func(d *db.Destination) { d.AccountID = nil },
		"studio stream now": func(d *db.Destination) { d.Compliance.YouTube = &db.YouTubeBroadcastControls{StudioStreamNow: true} },
	} {
		t.Run(name, func(t *testing.T) {
			o := newOBSFixture(t, mutate)
			o.post(t, "/start", http.StatusOK)
			if calls, _, _ := o.f.snapshot(); len(calls) != 0 {
				t.Errorf("calls = %v, want none", calls)
			}
		})
	}
	o := newOBSFixture(t, nil)
	if _, err := o.store.UpdateLifecycle(o.dest.ID, func(d *db.Destination) bool {
		d.Lifecycle = db.BroadcastControl{BroadcastID: "bcast-old", Phase: "created", Opened: true, Created: true}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	o.post(t, "/start", http.StatusOK)
	if _, inserts, _ := o.f.snapshot(); inserts != 0 {
		t.Errorf("inserts = %d, want the destination's existing broadcast kept", inserts)
	}
}

// A failed open does not block the start, and the coordinator opens the broadcast on its next sweep.
func TestAFailedOpenDoesNotBlockTheStartAndTheCoordinatorOpensItLater(t *testing.T) {
	o := newOBSFixture(t, nil)
	o.f.set(func(f *ynFake) { f.listedKey = "" }) // the key is not listed: the open fails
	o.post(t, "/start", http.StatusOK)
	d, _ := o.store.GetDestination(o.dest.ID)
	if !d.Enabled {
		t.Fatal("the destination did not start because the open failed")
	}
	if d.Lifecycle.BroadcastID != "" {
		t.Errorf("lifecycle = %+v, want nothing opened", d.Lifecycle)
	}
	o.f.set(func(f *ynFake) { f.listedKey = ynKey })
	o.s.lifecycle.sweepOnce(context.Background(), sweepEverything)
	if !o.opened() {
		t.Error("the coordinator did not open the broadcast on its sweep after a failed pre-open")
	}
}

// A hung YouTube holds the start for the deadline and no longer.
func TestAHungYouTubeHoldsTheStartOnlyForTheOpenDeadline(t *testing.T) {
	old := youtubeOpenBeforeStartTimeout
	youtubeOpenBeforeStartTimeout = 300 * time.Millisecond
	t.Cleanup(func() { youtubeOpenBeforeStartTimeout = old })
	o := newOBSFixture(t, nil)
	gate := make(chan struct{})
	o.f.set(func(f *ynFake) { f.hang = gate })
	t.Cleanup(func() { close(gate) })
	begin := time.Now()
	o.post(t, "/start", http.StatusOK)
	if took := time.Since(begin); took > 5*time.Second {
		t.Errorf("the start waited %v on a hung YouTube, want about the deadline", took)
	}
	if d, _ := o.store.GetDestination(o.dest.ID); !d.Enabled {
		t.Error("the destination did not start")
	}
}

// Two starts at once open one broadcast.
func TestTwoSimultaneousStartsOpenOneYouTubeBroadcast(t *testing.T) {
	o := newOBSFixture(t, nil)
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+itoa(o.dest.ID)+"/start", nil)
			o.sign(r)
			do(t, o.h, r)
			done <- struct{}{}
		}()
	}
	<-done
	<-done
	if _, inserts, _ := o.f.snapshot(); inserts != 1 {
		t.Errorf("inserts = %d, want one", inserts)
	}
}
