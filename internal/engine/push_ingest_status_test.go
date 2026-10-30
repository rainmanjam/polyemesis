package engine

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/stats"
)

// AN SRT SOURCE HAS AN INGEST, IT JUST HAS NO INGEST PROCESS. The shared SRT
// listener hands datagrams straight to the relay hub, so reconcileIngest leaves
// e.ingest nil for it on purpose -- and "is an ingest configured" used to be
// read off that nil. Every destination's frozen output after the encoder
// disconnected was then blamed on the destination: stalled, up=0 and a warning
// per card, a false destination.down "stalled" on the webhooks and the events
// stream, and the built-in alerts could never say "ingest lost" for SRT at all.
//
// Driven through Status and alertSnapshot, the two places that decide it,
// because the verdict table on destinationStalled is handed the flag and so
// cannot see how it was derived.
func TestAnSRTSourceIsAConfiguredIngestWithNoIngestProcess(t *testing.T) {
	e := failoverEngine(t)
	row, err := e.store.CreateDestination(&db.Destination{
		Name: "frozen", Kind: db.DestFile, URL: "frozen.ts", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	frozen := progressChild(t, "dest:frozen", false)
	live := liveMonitor(t)

	e.mu.Lock()
	e.settings.Ingest.Mode = db.IngestSRT
	e.ingest = nil // what reconcileIngest leaves for SRT
	e.mon = live
	e.dests[row.ID] = &destination{row: row, proc: frozen}
	e.mu.Unlock()

	find := func(st Status) DestStatus {
		t.Helper()
		for _, d := range st.Destinations {
			if d.ID == row.ID {
				return d
			}
		}
		t.Fatalf("destination %d missing from Status", row.ID)
		return DestStatus{}
	}

	// The encoder is pushing and the destination is not moving: its own stall,
	// which an SRT source must still report.
	deadline := time.Now().Add(20 * time.Second)
	for !find(e.Status()).Stalled {
		if time.Now().After(deadline) {
			t.Fatalf("frozen destination on a receiving SRT source never read stalled: %+v", find(e.Status()).Process)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The encoder disconnects. Every output freezes because the source has;
	// that is the ingest's news, not each platform's.
	e.mu.Lock()
	e.mon = stats.NewMonitor(nil)
	e.mu.Unlock()

	st := e.Status()
	d := find(st)
	if d.Process == nil || !d.Process.Stalled {
		t.Fatalf("the process itself must be stalled for this case to mean anything: %+v", d.Process)
	}
	if d.Stalled {
		t.Error("SRT source, encoder gone, and a healthy destination read stalled: " +
			"up=0 and a warning on every card between shows")
	}
	if w := stallWarning(d); w != "" {
		t.Errorf("SRT source, encoder gone, and the card carries a stall warning: %q", w)
	}

	snap := e.alertSnapshot(time.Now(), false)
	if !snap.IngestConfigured {
		t.Error("alertSnapshot says an SRT source has no ingest configured: the hooks and events " +
			"watchers then read every frozen destination as down/stalled, and ingest.lost can never fire")
	}

	// Disabled, the listener refuses its encoder: nothing to lose, no
	// ingest.lost 20 seconds after boot for a programme switched off.
	e.mu.Lock()
	e.sourceDisabled = true
	e.mu.Unlock()
	if e.alertSnapshot(time.Now(), false).IngestConfigured {
		t.Error("a disabled SRT source reads as a configured ingest")
	}
}

// The counterpart: a programme whose ingest mode was never chosen, with no
// child, really has nothing to lose -- a frozen destination there is its own.
func TestAnUnsetIngestIsStillNotConfigured(t *testing.T) {
	for _, tc := range []struct {
		mode    db.IngestMode
		child   bool
		enabled bool
		want    bool
	}{
		{db.IngestSRT, false, true, true},
		{db.IngestRTMP, true, true, true},
		{db.IngestPull, true, true, true},
		{db.IngestUnset, false, true, false},
		// An RTMP or pull source whose child was not spawned (no token, a
		// refused upload) has nothing feeding its hub: the old reading stands.
		{db.IngestRTMP, false, true, false},
		{db.IngestPull, false, true, false},
		// A DISABLED SRT source: the shared listener refuses its encoder, so
		// there is nothing to lose and nothing to page about.
		{db.IngestSRT, false, false, false},
	} {
		if got := ingestConfigured(tc.child, tc.mode, tc.enabled); got != tc.want {
			t.Errorf("ingestConfigured(child=%v, %q, enabled=%v) = %v, want %v",
				tc.child, tc.mode, tc.enabled, got, tc.want)
		}
	}
}
