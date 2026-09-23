package engine

import (
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// A RECORDING VOLUME FILLING UP WAS ONE disk.low PER PROGRAMME.
//
// Every engine records to cfg.RecordingsDir(), every engine measures it on its
// own sweep, and every engine has its own notifier -- so a three-programme
// install delivered three identical disk.low alerts through every rule. The
// engines now share the manager's InstallGate, and only the first engine to
// report the edge publishes it.
//
// Two engines built by hand, as observe_test.go does, with notifiers that are
// never Run: Publish only queues, and Stats().Queued is the count of what each
// engine handed on.
//
// Mutation: publish every event in publishAlerts without asking the gate.
// Observed to fail with "the install published disk.low 2 times".
func TestTwoEnginesSharingAGatePublishDiskLowOnce(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	none := alerts.RuleFunc(func() ([]alerts.Rule, error) { return nil, nil })
	gate := alerts.NewInstallGate()

	a := &Engine{alerter: alerts.New(log, none)}
	b := &Engine{alerter: alerts.New(log, none)}
	a.SetAlertGate(gate)
	b.SetAlertGate(gate)

	low := alerts.Event{Type: alerts.TypeDiskLow, Key: "disk"}
	lost := func(id string) alerts.Event {
		return alerts.Event{Type: alerts.TypeIngestLost, Key: "ingest:" + id}
	}
	a.publishAlerts([]alerts.Event{low, lost("1")})
	b.publishAlerts([]alerts.Event{low, lost("2")})

	// Two programme events plus ONE disk.low.
	if got := a.alerter.Stats().Queued + b.alerter.Stats().Queued; got != 3 {
		t.Errorf("the two engines queued %d events, want 3: the install published "+
			"disk.low %d times", got, got-2)
	}
	if b.alerter.Stats().Queued != 1 {
		t.Errorf("the second engine queued %d events, want its own ingest.lost alone",
			b.alerter.Stats().Queued)
	}
}

// THE STAMP IS ONE LINE, AND IT IS THE WHOLE FIX.
//
// A two-programme install's alerts say which programme only because every
// sweep stamps the engine's watcher with its source. Drop that line and the
// watcher is unscoped again: key "ingest" for every studio, a title naming
// none, no sourceId -- and a receiver deduplicating on key drops the second
// outage as a repeat. The watcher-level tests cannot see the wiring; this
// drives the sweep observeLoop drives, on an engine built by New.
//
// Mutation: delete the SetSource line from observeAlerts. Observed to fail
// with `key "ingest", want "ingest:<id>"`.
func TestAnEngineSweepStampsItsAlertsWithTheProgramme(t *testing.T) {
	e, _ := loggedEngine(t)
	e.mu.Lock()
	e.sourceName = "Studio B"
	e.mu.Unlock()
	want := "ingest:" + strconv.FormatInt(e.sourceID, 10)

	// Configured and not arriving, for longer than the default dwell.
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	e.observeAlerts(alerts.Snapshot{At: at, IngestConfigured: true})
	got := e.observeAlerts(alerts.Snapshot{At: at.Add(time.Hour), IngestConfigured: true})

	if len(got) != 1 || got[0].Type != alerts.TypeIngestLost {
		t.Fatalf("the sweep published %+v, want one ingest.lost", got)
	}
	ev := got[0]
	if ev.Key != want {
		t.Errorf("key %q, want %q: every programme's outage is the same subject", ev.Key, want)
	}
	if !strings.HasSuffix(ev.Title, "Studio B") {
		t.Errorf("title %q does not name the programme", ev.Title)
	}
	if alertField(ev, "sourceId") != strconv.FormatInt(e.sourceID, 10) || alertField(ev, "sourceName") != "Studio B" {
		t.Errorf("fields %+v, want sourceId and sourceName", ev.Fields)
	}
	if q := e.alerter.Stats().Queued; q != 1 {
		t.Errorf("the notifier queued %d events, want the one the sweep published", q)
	}
}

// A STOPPED ENGINE KEPT ITS CLAIM ON THE DISK.
//
// An engine whose watcher reported disk.low holds the install's disk subject
// until it reports the recovery. Deleted first, it never will; if Stop left
// the claim on the shared gate, the next disk.low from an engine alive now --
// the critical "Recording has been stopped" included -- would be dropped as a
// repeat until the process restarted.
//
// Mutation: delete the releaseAlertGate call from StopWithin. Observed to fail
// with "the live engine's disk.low was dropped".
func TestStoppingAnEngineReleasesItsDiskClaim(t *testing.T) {
	gate := alerts.NewInstallGate()
	gone, _ := loggedEngine(t)
	gone.SetAlertGate(gate)
	low := alerts.Event{Type: alerts.TypeDiskLow, Severity: alerts.SeverityCritical, Key: "disk"}
	if len(gone.publishAlerts([]alerts.Event{low})) != 1 {
		t.Fatal("the first engine's disk.low was dropped")
	}
	gone.Stop()

	live, _ := loggedEngine(t)
	live.SetAlertGate(gate)
	if len(live.publishAlerts([]alerts.Event{low})) != 1 {
		t.Error("the live engine's disk.low was dropped: the stopped engine still " +
			"holds the disk, and recording halted without a word")
	}
}

func alertField(ev alerts.Event, name string) string {
	for _, f := range ev.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}
