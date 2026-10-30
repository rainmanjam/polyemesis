package engine

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// streamEngine is an engine with just enough for observeEdges: both watchers,
// a source, and a hub to publish into.
func streamEngine(t *testing.T) (*Engine, *eventstream.Subscription) {
	t.Helper()
	hub := eventstream.NewHub()
	e := &Engine{
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		sourceID:    4,
		sourceName:  "Studio A",
		hookWatch:   hooks.NewWatcher(hooks.SourceRef{ID: 4}, hooks.WatchConfig{DisconnectAfter: -1, DestinationDownAfter: -1}),
		streamWatch: eventstream.NewWatcher(),
		dests:       map[int64]*destination{},
	}
	e.SetStream(hub)
	return e, streamtest.Subscribe(t, hub)
}

func sweepAt(sec int, ingestLive bool, dests ...alerts.DestState) alerts.Snapshot {
	return alerts.Snapshot{
		At: time.Unix(1_700_000_000+int64(sec), 0), IngestConfigured: true, IngestLive: ingestLive,
		Destinations: dests,
	}
}

// TestTheSweepPublishesTheWebhookEdgesAndTheHoldToTheStream drives observeEdges,
// the function observeLoop calls every sweep, through the life of a destination
// whose key is rejected: the ingest starts, the destination goes live, its key
// is refused (respawns, then a hold), and Retry brings it back.
//
// Every event is the real producer's: the webhook watcher's edges, converted,
// and the stream watcher's held and restarted, from the same snapshot. The
// order is the one a script reading the stream sees -- down with the cause,
// then held, then up after Retry.
func TestTheSweepPublishesTheWebhookEdgesAndTheHoldToTheStream(t *testing.T) {
	e, sub := streamEngine(t)

	live := func(sec int, out int64, restarts int) alerts.DestState {
		return alerts.DestState{ID: 9, Name: "YouTube", Platform: "youtube", Enabled: true,
			Running: true, OutTimeMS: out, Restarts: restarts}
	}
	e.observeEdges(sweepAt(0, true, live(0, 1000, 0)))
	streamtest.Expect(t, sub, eventstream.TypeIngestPublished)
	up := streamtest.Expect(t, sub, eventstream.TypeDestinationUp)
	if up.Destination == nil || up.Destination.ID != 9 || up.Source == nil || up.Source.Name != "Studio A" {
		t.Fatalf("destination.up = %+v, want destination 9 of programme Studio A", up)
	}

	rejected := &failreason.Reason{Cause: failreason.KeyRejected, Quick: true, Rejections: 1,
		Line: "rtmp://a.rtmp.youtube.com/live2/abcd-efgh-ijkl-mnop: Input/output error"}
	failing := alerts.DestState{ID: 9, Name: "YouTube", Platform: "youtube", Enabled: true,
		Restarts: 1, Reason: rejected, Error: "publish refused"}
	e.observeEdges(sweepAt(2, true, failing))
	// The webhook's edge first, then what the stream derives beside it.
	down := streamtest.Expect(t, sub, eventstream.TypeDestinationDown)
	if down.Cause != failreason.KeyRejected || down.Failure == nil || down.Failure.Cause != failreason.KeyRejected {
		t.Fatalf("destination.down = %+v, want cause key_rejected and the whole failure", down)
	}
	restarted := streamtest.Expect(t, sub, eventstream.TypeDestinationRestarted)
	if restarted.Destination == nil || restarted.Destination.ID != 9 {
		t.Fatalf("destination.restarted = %+v", restarted)
	}

	held := *rejected
	held.Held, held.Rejections = true, 3
	failing.Reason, failing.Restarts = &held, 3
	e.observeEdges(sweepAt(4, true, failing))
	h := streamtest.Expect(t, sub, eventstream.TypeDestinationHeld)
	if !h.Held || h.Cause != failreason.KeyRejected || h.Failure == nil || h.Failure.Rejections != 3 {
		t.Fatalf("destination.held = %+v", h)
	}

	// Retry: a new process (the count resets, which is not a respawn) that
	// delivers.
	e.observeEdges(sweepAt(6, true, live(6, 500, 0)))
	streamtest.Expect(t, sub, eventstream.TypeDestinationUp)

	e.observeEdges(sweepAt(20, false))
	streamtest.Expect(t, sub, eventstream.TypeIngestDisconnected)
}

// TestALevelChangeIsPublishedWithHowItWasApplied: the one sink every level-
// change outcome passes through announces it, live or by restart, with the
// destination it reached.
func TestALevelChangeIsPublishedWithHowItWasApplied(t *testing.T) {
	e, sub := streamEngine(t)
	e.dests[3] = &destination{row: &db.Destination{ID: 3, Name: "Twitch", Platform: db.PlatformTwitch}}

	e.setLiveNote(3, liveNote{applied: liveApplied})
	ev := streamtest.Expect(t, sub, eventstream.TypeDestinationLevel)
	if got := ev.Data.(map[string]any)["applied"]; got != "live" {
		t.Errorf("applied = %v, want live", got)
	}
	if ev.Destination == nil || ev.Destination.Name != "Twitch" {
		t.Errorf("destination = %+v, want Twitch", ev.Destination)
	}

	e.setLiveNote(3, liveNote{applied: liveRestart, reason: "the process refused the live change"})
	ev = streamtest.Expect(t, sub, eventstream.TypeDestinationLevel)
	if got := ev.Data.(map[string]any)["applied"]; got != "restart" || ev.Reason == "" {
		t.Errorf("event = %+v, want applied restart with the reason", ev)
	}
}

// TestARolloverReachesTheStreamAsWellAsTheWebhook: noteRollover's one publish
// now goes to both surfaces.
func TestARolloverReachesTheStreamAsWellAsTheWebhook(t *testing.T) {
	e, _ := rolloverEngine(t)
	hub := eventstream.NewHub()
	e.SetStream(hub)
	sub := streamtest.Subscribe(t, hub)
	row := &db.Destination{ID: 7, Name: "archive", Kind: db.DestFile, URL: "show.mkv"}

	want, err := e.recman.Resolve(row.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("footage"), 0o600); err != nil {
		t.Fatal(err)
	}
	actual, err := e.recman.ResolveForWrite(row.URL)
	if err != nil {
		t.Fatal(err)
	}
	e.noteRollover(row, actual)
	ev := streamtest.Expect(t, sub, eventstream.TypeDestinationRolledOver)
	if ev.Destination == nil || ev.Destination.ID != 7 {
		t.Errorf("destination.rolledover = %+v", ev)
	}
}

// TestTheRecorderAnnouncesItsRealStartAndStop drives reconcileRecorder, the
// function every recording toggle and the free-space guard go through, rather
// than the settings write: recording.started is the process being started, not
// the checkbox.
func TestTheRecorderAnnouncesItsRealStartAndStop(t *testing.T) {
	e, _ := storeEngine(t)
	hub := eventstream.NewHub()
	e.SetStream(hub)
	sub := streamtest.Subscribe(t, hub)

	s := db.DefaultSettings()
	s.Recording.Enabled = true
	e.reconcileRecorder(s)
	started := streamtest.Expect(t, sub, eventstream.TypeRecordingStarted)
	if started.Source == nil || started.Source.ID != e.sourceID {
		t.Errorf("recording.started = %+v, want this programme", started)
	}

	// Reconciling the same settings again starts nothing and says nothing.
	e.reconcileRecorder(s)
	streamtest.None(t, sub, eventstream.TypeRecordingStarted, 100*time.Millisecond)

	s.Recording.Enabled = false
	e.reconcileRecorder(s)
	stopped := streamtest.Expect(t, sub, eventstream.TypeRecordingStopped)
	if stopped.Reason != "recording turned off" {
		t.Errorf("reason = %q", stopped.Reason)
	}

	// Disabled with nothing running: no stop to announce.
	e.reconcileRecorder(s)
	streamtest.None(t, sub, eventstream.TypeRecordingStopped, 100*time.Millisecond)
}

// TestAFailoverSwitchReachesTheStream: the switch an operator makes by hand goes
// through the same ensureFeed publish as an automatic one.
func TestAFailoverSwitchReachesTheStream(t *testing.T) {
	e := failoverEngine(t)
	hub := eventstream.NewHub()
	e.SetStream(hub)
	sub := streamtest.Subscribe(t, hub)
	s := failoverOnSettings()
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		e.teardownFeed(e.sel.feed)
		_ = e.sel.hub.Close()
	})
	now := time.Now()
	e.deliver(sourcePrimary, now)
	e.step(s, now)

	if err := e.SwitchSource("slate"); err != nil {
		t.Fatalf("SwitchSource: %v", err)
	}
	ev := streamtest.Expect(t, sub, eventstream.TypeFailoverSwitched)
	data := ev.Data.(map[string]any)
	if data["active"] != "slate" {
		t.Errorf("active = %v, want slate", data["active"])
	}
	for _, k := range []string{"feed", "backup"} {
		if _, ok := data[k]; ok {
			t.Errorf("the payload carries %q, a process status nobody asked for", k)
		}
	}
}

// TestEveryAlertReachesTheStreamThroughTheNotifiersOwnDoor: the tap is on
// Notifier.Publish, so an alert published by anything -- here, the API's audit
// path, which calls Alerts().Publish directly -- arrives, redacted.
func TestEveryAlertReachesTheStreamThroughTheNotifiersOwnDoor(t *testing.T) {
	hub := eventstream.NewHub()
	e := &Engine{sourceID: 2, alerter: alerts.New(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)}
	e.SetStream(hub)
	sub := streamtest.Subscribe(t, hub)

	e.Alerts().Publish(alerts.Event{
		Type: alerts.TypeDestinationDown, Severity: alerts.SeverityCritical, Title: "YouTube is down",
		Key:  "destination:3",
		Text: "rtmp://a.rtmp.youtube.com/live2/abcd-efgh-ijkl-mnop refused",
	})
	ev := streamtest.Expect(t, sub, eventstream.TypeAlert)
	// The subject survives: it is how a consumer pairs a fire with its
	// resolution, and a field NAMED key is not a credential.
	if got := ev.Data.(alerts.Event).Key; got != "destination:3" {
		t.Errorf("alert key = %q, want the subject destination:3", got)
	}
	if ev.Source != nil {
		t.Errorf("source = %+v: an alert names its subject in its own key and title, and "+
			"an audit event published through the first engine would be stamped with a "+
			"programme it has nothing to do with", ev.Source)
	}
	if text := ev.Data.(alerts.Event).Text; text == "" || strings.Contains(text, "abcd-efgh-ijkl-mnop") {
		t.Errorf("alert text = %q: empty, or the stream key survived", text)
	}

	// Detaching the hub detaches the tap.
	e.SetStream(nil)
	e.Alerts().Publish(alerts.Event{Type: alerts.TypeDiskLow, Title: "x"})
	streamtest.None(t, sub, eventstream.TypeAlert, 100*time.Millisecond)
}

// TestAnOpenStreamIsReasonEnoughToSweep: with no hooks, no rules, no lifecycle
// and no timeline, a connected events client is the only consumer -- and a
// stream that showed nothing on a fresh install would look broken.
func TestAnOpenStreamIsReasonEnoughToSweep(t *testing.T) {
	hub := eventstream.NewHub()
	noRules := alerts.RuleFunc(func() ([]alerts.Rule, error) { return nil, nil })
	e := &Engine{alerter: alerts.New(slog.New(slog.NewTextHandler(io.Discard, nil)), noRules)}
	if e.sweepWanted() {
		t.Fatal("an engine with no consumer at all wants a sweep; the test below proves nothing")
	}
	e.SetStream(hub)
	if e.sweepWanted() {
		t.Fatal("a hub with nobody connected opened the sweep gate")
	}
	sub, _, _ := hub.Subscribe(eventstream.Filter{}, "")
	if !e.sweepWanted() {
		t.Error("an open events connection did not open the sweep gate")
	}
	sub.Close()
	if e.sweepWanted() {
		t.Error("the gate stayed open after the last connection closed")
	}
}

// edgeLog is a LifecycleObserver that keeps every edge: the same slice of
// hooks.Event the webhook dispatcher is handed in the same loop.
type edgeLog struct{ got []hooks.Event }

func (l *edgeLog) Observe(ev hooks.Event) { l.got = append(l.got, ev) }
func (l *edgeLog) Wanted() bool           { return true }

// TestAHoldIsOneWebhookAndOneStreamEventAcrossManySweeps: destination.held is
// derived once, by the webhook watcher, and the stream converts that edge.
// Sweeping a held destination repeatedly must produce exactly one held edge for
// the dispatcher and exactly one destination.held on the stream -- not one per
// sweep, and not one from each watcher.
func TestAHoldIsOneWebhookAndOneStreamEventAcrossManySweeps(t *testing.T) {
	e, sub := streamEngine(t)
	edges := &edgeLog{}
	e.SetLifecycle(edges)

	e.observeEdges(sweepAt(0, true, alerts.DestState{ID: 9, Name: "YouTube", Enabled: true, Running: true, OutTimeMS: 1000}))
	held := alerts.DestState{ID: 9, Name: "YouTube", Enabled: true, Error: "publish refused",
		Reason: &failreason.Reason{Cause: failreason.KeyRejected, Held: true, Rejections: 3}}
	for sec := 2; sec <= 20; sec += 2 {
		e.observeEdges(sweepAt(sec, true, held))
	}

	n := 0
	for _, ev := range edges.got {
		if ev.Trigger == hooks.TriggerDestinationHeld {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d destination.held edges reached the dispatcher over ten sweeps of one hold, want 1", n)
	}
	h := streamtest.Expect(t, sub, eventstream.TypeDestinationHeld)
	if h.Failure == nil || !h.Held || h.Destination == nil || h.Destination.ID != 9 {
		t.Fatalf("destination.held = %+v", h)
	}
	streamtest.None(t, sub, eventstream.TypeDestinationHeld, 300*time.Millisecond)
}
