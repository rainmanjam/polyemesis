package engine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// Degraded-link failover end to end through the PRODUCTION sweep: real bytes
// on the primary's own hub, sweepSelector at chosen instants, and the switch
// observed on the real-time events stream. The pure decision and the monitor
// each have their own tests; this is the one that proves the bitrate is read
// from the right hub, the bit reaches the decision, and the measured detail
// reaches every consumer that reads Reason.

// degradedSettings is failover with a bitrate threshold and a short sustain
// window, so a test drives a trip in a few simulated seconds.
func degradedSettings() db.Settings {
	s := failoverOnSettings()
	s.Ingest.Degraded = db.DegradedSettings{Enabled: true, MinBitrateKbps: 800, SustainSeconds: 2}
	return s
}

// pace sweeps every 500 ms of simulated time for d, delivering perSweep TS
// datagrams (1316 bytes each) into the primary's hub before each sweep -- 1
// datagram per sweep is ~21 kbps, 200 is ~4.2 Mbps.
func pace(t *testing.T, e *Engine, from time.Time, d time.Duration, perSweep int, also func()) time.Time {
	t.Helper()
	now := from
	for end := from.Add(d); !now.After(end); now = now.Add(500 * time.Millisecond) {
		deliverTS(t, e.hub, perSweep)
		if also != nil {
			also()
		}
		e.sweepSelector(now)
	}
	return now
}

func startDegradedTier(t *testing.T, s db.Settings) (*Engine, *eventstream.Subscription) {
	t.Helper()
	e := failoverEngine(t)
	hub := eventstream.NewHub()
	e.SetStream(hub)
	sub := streamtest.Subscribe(t, hub)
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
	return e, sub
}

// A primary that keeps delivering, at a bitrate below the threshold for the
// sustain window, is taken off air with a reason that starts "degraded: " and
// says what was measured. Then the encoder recovers, the link reads healthy
// for the sustain window, and the slate hands back.
//
// Mutation: selector.go applySourceChoice, `primaryDegraded: s.Failover.
// Degraded.Enabled && sel.degrade.tripped` -> `primaryDegraded: false`.
// Expected to fail on the first assertion: the trickling primary stays on air.
func TestALowBitratePrimaryIsTakenOffAirAndComesBackWhenItRecovers(t *testing.T) {
	e, sub := startDegradedTier(t, degradedSettings())

	// Healthy first, so a trip later is a change and not a cold start.
	t0 := time.Now()
	now := pace(t, e, t0, 3*time.Second, 200, nil)
	if act := e.Failover().Active; act != sourcePrimary {
		t.Fatalf("a primary delivering ~4 Mbps is not on air (%q)", act)
	}

	// The uplink collapses to a trickle: still delivering, so liveness says
	// alive and a disconnect-only failover would never fire.
	now = pace(t, e, now, 4*time.Second, 1, nil)
	st := e.Failover()
	if st.Active != sourceSlate {
		t.Fatalf("a primary at ~21 kbps against an 800 kbps threshold for 4 s is still on air "+
			"(active %q, degraded %q)", st.Active, st.Degraded)
	}
	if !st.PrimaryLive {
		t.Error("the primary was reported dead; it never stopped delivering, which is the whole point")
	}
	if !strings.HasPrefix(st.Reason, "degraded: ") || !strings.Contains(st.Reason, "below 800 kbps") {
		t.Errorf("reason %q does not start with the degraded: prefix and name the breach", st.Reason)
	}
	if !strings.HasPrefix(st.Degraded, "bitrate ") {
		t.Errorf("status Degraded = %q, want the measured explanation", st.Degraded)
	}
	if st.PrimaryLink == nil || st.PrimaryLink.BitrateKbps == nil || *st.PrimaryLink.BitrateKbps >= 800 {
		t.Errorf("status PrimaryLink = %+v, want a measured bitrate below the threshold", st.PrimaryLink)
	}
	if st.PrimaryLink != nil && (st.PrimaryLink.RTTMs != nil || st.PrimaryLink.LossPercent != nil) {
		t.Error("an engine with no SRT link reported an RTT or a loss figure it cannot have measured")
	}

	// The tier's own start-up switches (slate before the first byte, primary
	// on it) are on the stream first; the degraded switch is the third.
	var ev eventstream.Envelope
	for i := 0; i < 3; i++ {
		ev = streamtest.Expect(t, sub, eventstream.TypeFailoverSwitched)
		if strings.HasPrefix(ev.Reason, "degraded: ") {
			break
		}
	}
	if !strings.HasPrefix(ev.Reason, "degraded: ") {
		t.Errorf("the events stream never carried a degraded: reason; last was %q", ev.Reason)
	}
	if data := ev.Data.(map[string]any); data["degraded"] == "" || data["degraded"] == nil {
		t.Errorf("the events stream's data carries no degraded detail: %v", data)
	}

	// One healthy second is not a recovery: hysteresis holds the slate.
	now = pace(t, e, now, time.Second, 200, nil)
	if act := e.Failover().Active; act != sourceSlate {
		t.Fatalf("returned to the primary after one healthy second (%q); the clear needs the sustain window", act)
	}
	pace(t, e, now, 3*time.Second, 200, nil)
	st = e.Failover()
	if st.Active != sourcePrimary || st.Degraded != "" {
		t.Fatalf("after the link read healthy for the sustain window: active %q degraded %q, want the primary and no degradation",
			st.Active, st.Degraded)
	}
}

// Automatic return waits returnStable AFTER THE LINK CLEARS, not after the
// encoder connected. A degraded link never stopped delivering, so liveness's
// own stable clock was never reset; without the reset in sampleDegradeLocked
// the clear and the return would be the same sweep.
//
// Mutation: selector.go sampleDegradeLocked, delete `primary.since = now`.
// Expected to fail at "returned to the primary 1s after the link cleared".
func TestAutomaticReturnWaitsTheStableWindowAfterADegradedLinkClears(t *testing.T) {
	s := degradedSettings()
	s.Failover.Backup.Enabled = true
	s.Failover.Backup.Mode = db.IngestSRT
	s.Failover.Return = db.FailoverReturnAuto
	s.Failover.ReturnStableSeconds = 10
	e, _ := startDegradedTier(t, s)
	if e.backupHub() == nil {
		t.Fatal("the backup ingest did not start")
	}
	backup := func() { deliverTS(t, e.backupHub(), 50) }

	now := pace(t, e, time.Now(), 3*time.Second, 200, backup)
	now = pace(t, e, now, 4*time.Second, 1, backup)
	if st := e.Failover(); st.Active != sourceBackup || !strings.HasPrefix(st.Reason, "degraded: ") {
		t.Fatalf("a degraded primary with a live backup: active %q reason %q, want the backup", st.Active, st.Reason)
	}

	// Recover. The clear lands after the sustain window of healthy readings.
	now = pace(t, e, now, 3*time.Second, 200, backup)
	if e.Failover().Degraded != "" {
		t.Fatal("setup: the link did not clear")
	}
	now = pace(t, e, now, time.Second, 200, backup)
	if act := e.Failover().Active; act != sourceBackup {
		t.Fatalf("returned to the primary 1s after the link cleared (%q); auto return must wait returnStable", act)
	}
	pace(t, e, now, 11*time.Second, 200, backup)
	if act := e.Failover().Active; act != sourcePrimary {
		t.Fatalf("still on %q a full returnStable after the link cleared", act)
	}
}

// Manual return is the default and means what it says: a cleared link does not
// bring the primary back while the backup is delivering.
func TestManualReturnStaysOnTheBackupAfterADegradedLinkClears(t *testing.T) {
	s := degradedSettings()
	s.Failover.Backup.Enabled = true
	s.Failover.Backup.Mode = db.IngestSRT
	e, _ := startDegradedTier(t, s)
	backup := func() { deliverTS(t, e.backupHub(), 50) }

	now := pace(t, e, time.Now(), 3*time.Second, 200, backup)
	now = pace(t, e, now, 4*time.Second, 1, backup)
	if act := e.Failover().Active; act != sourceBackup {
		t.Fatalf("setup: active %q, want the backup", act)
	}
	pace(t, e, now, 20*time.Second, 200, backup)
	if act := e.Failover().Active; act != sourceBackup {
		t.Fatalf("manual return went back to the primary on its own (%q)", act)
	}
}

// RTT and loss reach the monitor through the Manager's probe. An SRT primary
// with a healthy bitrate but a collapsing round trip is degraded too.
func TestAHighRTTFromTheSRTProbeTripsAPrimaryWithAHealthyBitrate(t *testing.T) {
	s := degradedSettings()
	s.Ingest.Degraded.MaxRTTMs = 500
	e := failoverEngine(t)
	e.linkProbe = func(id int64, backup bool) (linkHealth, bool) {
		if id != e.sourceID || backup {
			t.Errorf("probe asked about source %d backup=%v, want this programme's primary", id, backup)
		}
		return linkHealth{rttMs: 2400, lossPct: 0}, true
	}
	hub := eventstream.NewHub()
	e.SetStream(hub)
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		e.teardownFeed(e.sel.feed)
		_ = e.sel.hub.Close()
	})

	pace(t, e, time.Now(), 5*time.Second, 200, nil)
	st := e.Failover()
	if st.Active != sourceSlate || !strings.Contains(st.Reason, "RTT 2400 ms above 500 ms") {
		t.Fatalf("active %q reason %q, want the slate for RTT", st.Active, st.Reason)
	}
	if st.PrimaryLink == nil || st.PrimaryLink.RTTMs == nil || *st.PrimaryLink.RTTMs != 2400 {
		t.Errorf("status PrimaryLink = %+v, want the probed RTT", st.PrimaryLink)
	}
}

// Off means off: with the thresholds disabled a trickling primary is merely a
// slow one, exactly as before #6.
func TestDegradedLinkFailoverOffLeavesASlowPrimaryOnAir(t *testing.T) {
	s := degradedSettings()
	s.Ingest.Degraded.Enabled = false
	e, _ := startDegradedTier(t, s)
	pace(t, e, time.Now(), 8*time.Second, 1, nil)
	st := e.Failover()
	if st.Active != sourcePrimary || st.Degraded != "" || st.PrimaryLink != nil {
		t.Fatalf("with degraded-link failover off: active %q degraded %q link %+v", st.Active, st.Degraded, st.PrimaryLink)
	}
}

// hookCapture is an http Doer that keeps every webhook body it is handed.
type hookCapture struct {
	mu     sync.Mutex
	bodies []string
}

func (h *hookCapture) Do(req *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(req.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, string(b))
	h.mu.Unlock()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
}

func (h *hookCapture) find(sub ...string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
next:
	for _, b := range h.bodies {
		for _, s := range sub {
			if !strings.Contains(b, s) {
				continue next
			}
		}
		return b, true
	}
	return "", false
}

// The lifecycle webhook carries the degraded reason too: a script subscribed to
// failover.switched learns that the link broke up, not merely that the source
// moved. Through the real dispatcher, with only the HTTP transport replaced.
//
// Mutation: selector.go ensureFeed, delete the e.Hooks().Publish block.
// Expected to fail: no failover.switched body is ever delivered.
func TestADegradedSwitchIsDeliveredAsAFailoverWebhookWithItsReason(t *testing.T) {
	cap := &hookCapture{}
	d := hooks.NewDispatcher(slog.New(slog.NewTextHandler(io.Discard, nil)),
		hooks.SourceFunc(func() ([]hooks.Hook, error) {
			return []hooks.Hook{hooks.Hook{ID: 1, Name: "ops", Enabled: true,
				URL: "https://example.com/hook", Secret: "not-a-real-secret"}.Normalized()}, nil
		}),
		hooks.WithDoer(cap), hooks.WithReloadInterval(10*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)
	waitUntil(t, d.HasHooks, "the dispatcher to load its hook")

	e, _ := startDegradedTier(t, degradedSettings())
	e.SetHooks(d)
	now := pace(t, e, time.Now(), 3*time.Second, 200, nil)
	pace(t, e, now, 4*time.Second, 1, nil)

	waitUntil(t, func() bool {
		_, ok := cap.find(`"failover.switched"`, `degraded: `)
		return ok
	}, "a failover.switched webhook carrying the degraded: reason")
	body, _ := cap.find(`"failover.switched"`, `degraded: `)
	if !strings.Contains(body, "below 800 kbps") {
		t.Errorf("the webhook's reason does not say what was measured: %s", body)
	}
}

// Chat's !fix: the pin is cleared and the on-air feed is a NEW process, not the
// one that was running -- otherwise "refresh" refreshed nothing. Measured where
// it is hardest: the same source stays on air, so nothing but the refresh
// itself can produce a new feed.
//
// Mutation: selector.go FixSource, delete `sel.feedAt = time.Time{}`.
// Expected to fail: ensureFeed holds the restart back as a start inside its
// backoff, and nothing is on air until a later sweep.
func TestFixClearsThePinAndReplacesTheFeedOnAir(t *testing.T) {
	e, _ := startDegradedTier(t, failoverOnSettings())
	pace(t, e, time.Now(), time.Second, 50, nil)
	if err := e.SwitchSource("primary"); err != nil {
		t.Fatalf("SwitchSource: %v", err)
	}
	e.mu.RLock()
	before := e.sel.feed
	e.mu.RUnlock()
	if before == nil {
		t.Fatal("setup: no feed on air")
	}

	deliverTS(t, e.hub, 50)
	if err := e.FixSource(); err != nil {
		t.Fatalf("FixSource: %v", err)
	}
	st := e.Failover()
	if st.Pinned != sourceNone {
		t.Errorf("!fix left the pin on %q", st.Pinned)
	}
	if st.Active != sourcePrimary {
		t.Errorf("after !fix with the primary delivering, %q is on air", st.Active)
	}
	e.mu.RLock()
	after := e.sel.feed
	e.mu.RUnlock()
	if after == nil || after == before {
		t.Fatalf("!fix did not replace the feed (before %p after %p)", before, after)
	}
}

func TestFixWithFailoverOffSaysThereIsNothingToRefresh(t *testing.T) {
	e := failoverEngine(t)
	setSettings(e, db.DefaultSettings())
	if err := e.FixSource(); err == nil {
		t.Fatal("!fix with failover off reported success")
	}
}

// PER PROGRAMME, which is why the thresholds live on the ingest. Failover's
// own settings are install-wide; each programme's ingest block is its own row,
// and effectiveSettings lays it over the install's. Two programmes on one
// install -- a bonded cellular rig and a studio line -- get their own floors.
//
// Mutation: effectiveSettings, delete `settings.Ingest = src.Ingest`. Expected
// to fail: both engines read the install-wide default.
func TestEachProgrammeReadsItsOwnDegradedThresholds(t *testing.T) {
	e := failoverEngine(t)
	irl := &db.Source{Name: "IRL", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	irl.Ingest.Mode = db.IngestSRT
	irl.Ingest.Degraded = db.DegradedSettings{Enabled: true, MinBitrateKbps: 2500, SustainSeconds: 3}
	if err := e.store.CreateSource(irl); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	studio := &db.Source{Name: "Studio", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	studio.Ingest.Mode = db.IngestSRT
	studio.Ingest.Degraded = db.DegradedSettings{Enabled: false, MinBitrateKbps: 600}
	if err := e.store.CreateSource(studio); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	read := func(id int64) db.DegradedSettings {
		eng := &Engine{log: e.log, store: e.store, sourceID: id}
		s, err := eng.effectiveSettings()
		if err != nil {
			t.Fatalf("effectiveSettings(%d): %v", id, err)
		}
		return s.Ingest.Degraded
	}
	if got := read(irl.ID); !got.Enabled || got.MinBitrateKbps != 2500 {
		t.Errorf("the IRL programme reads %+v, want its own 2500 kbps, on", got)
	}
	if got := read(studio.ID); got.Enabled || got.MinBitrateKbps != 600 {
		t.Errorf("the studio programme reads %+v, want its own 600 kbps, off", got)
	}
}
