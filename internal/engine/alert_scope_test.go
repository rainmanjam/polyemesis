package engine

import (
	"io"
	"log/slog"
	"testing"

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
