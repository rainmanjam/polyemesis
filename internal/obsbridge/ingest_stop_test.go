package obsbridge_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/obsbridge"
)

// A world with one destination that is either running or stopped, shared by
// the OBS actions and the ingest-loss stop, the way the real destination row is.
type world struct {
	mu      sync.Mutex
	running bool
	stops   int
}

// stop is the real stop path's behaviour: "stopped" when there was something to
// stop, "skipped" when the destination was already off (obsActions.Stop and the
// API's ingest stop both report it so).
func (w *world) stop() []obsbridge.Outcome {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		return []obsbridge.Outcome{{ID: 7, Outcome: "skipped", Message: "already stopped"}}
	}
	w.running = false
	w.stops++
	return []obsbridge.Outcome{{ID: 7, Outcome: "stopped"}}
}

func (w *world) stopCount() int { w.mu.Lock(); defer w.mu.Unlock(); return w.stops }

type worldActions struct {
	*fakeActions
	w *world
}

func (a worldActions) Stop(ctx context.Context, cfg obsbridge.Config) []obsbridge.Outcome {
	out := a.w.stop()
	a.fakeActions.stopCalled <- struct{}{}
	return out
}

func ingestRig(t *testing.T, cfg obsbridge.Config) (*rig, *world) {
	t.Helper()
	r := newRig(t, cfg, "")
	w := &world{running: true}
	r.b.SetActions(worldActions{r.acts, w})
	return r, w
}

func driving(id int64) obsbridge.Config {
	cfg := baseConfig("")
	cfg.SourceID = &id
	return cfg
}

func records(r *rig) []obsbridge.ActionRecord { return r.b.Status().LastActions }

// THE FALLBACK'S STOP RUNS ON THE BRIDGE'S WORKER AND IS RECORDED AS ITS OWN.
//
// Mutation: record it as ViaScript / not at all. Observed to fail with
// `via "script", want "ingest"`.
func TestAnIngestLossStopIsRecordedViaIngest(t *testing.T) {
	r, w := ingestRig(t, driving(3))
	if !r.b.RunIngestStop(3, func(context.Context) []obsbridge.Outcome { return w.stop() }) {
		t.Fatal("the bridge did not take a stop for the programme it drives")
	}
	waitFor(t, "the record", func() bool { return len(records(r)) == 1 })
	rec := records(r)[0]
	if rec.Via != obsbridge.ViaIngest || rec.Action != "stop" || len(rec.Results) != 1 || rec.Results[0].Outcome != "stopped" {
		t.Fatalf("recorded %+v", rec)
	}
}

// The bridge is not involved for a programme it does not drive, or when off.
func TestAnIngestLossStopIsNotTakenForAnotherProgrammeOrWhenOff(t *testing.T) {
	r, _ := ingestRig(t, driving(3))
	if r.b.RunIngestStop(4, func(context.Context) []obsbridge.Outcome { return nil }) {
		t.Error("the bridge took a stop for a programme it does not drive")
	}
	off := driving(3)
	off.Enabled = false
	r2, _ := ingestRig(t, off)
	if r2.b.RunIngestStop(3, func(context.Context) []obsbridge.Outcome { return nil }) {
		t.Error("a switched-off bridge took a stop")
	}
}

// The fallback works with the bridge's own stopping unarmed and OBS still
// "streaming": it is the programme's setting, not the bridge's.
func TestAnIngestLossStopNeedsNeitherStopOnStreamNorOBSStopped(t *testing.T) {
	cfg := driving(3)
	cfg.StopOnStream, cfg.StopAcknowledged = false, false
	r, w := ingestRig(t, cfg)
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectCall(t, r.acts.startCalled, "Start")
	r.b.RunIngestStop(3, func(context.Context) []obsbridge.Outcome { return w.stop() })
	waitFor(t, "the record", func() bool { return w.stopCount() == 1 && len(records(r)) == 2 })
}

// GUARD 5, OBS FIRST: OBS stops and the ingest is lost at the same moment. The
// OBS stop runs; the ingest stop finds the destination already off. One stop,
// one lastActions entry.
//
// Mutation: record every stop job, not only the ones that acted. Observed to
// fail with "2 records, want 1".
func TestOBSStopThenIngestLossIsOneStopAndOneRecord(t *testing.T) {
	cfg := driving(3)
	cfg.StopGraceSeconds = 0
	r, w := ingestRig(t, cfg)
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectCall(t, r.acts.startCalled, "Start")
	waitFor(t, "the start record", func() bool { return len(records(r)) == 1 })

	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStopped, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectCall(t, r.acts.stopCalled, "Stop")
	r.b.RunIngestStop(3, func(context.Context) []obsbridge.Outcome { return w.stop() })
	// Both jobs are on the one worker, in order; a barrier job proves the
	// second has run before the count is read.
	r.b.RunIngestStop(3, func(context.Context) []obsbridge.Outcome { return nil })
	waitFor(t, "the worker to drain", func() bool { return len(r.b.Status().LastActions) >= 2 })
	time.Sleep(50 * time.Millisecond)

	if n := w.stopCount(); n != 1 {
		t.Fatalf("the destination was stopped %d times, want 1", n)
	}
	var stops int
	for _, rec := range records(r) {
		if rec.Action == "stop" {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("%d stop records, want 1: %+v", stops, records(r))
	}
}

// GUARD 5, INGEST FIRST: the fallback stops first, then OBS's own stop arrives.
// The OBS stop finds nothing running and records nothing.
func TestIngestLossThenOBSStopIsOneStopAndOneRecord(t *testing.T) {
	cfg := driving(3)
	cfg.StopGraceSeconds = 0
	r, w := ingestRig(t, cfg)
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectCall(t, r.acts.startCalled, "Start")
	waitFor(t, "the start record", func() bool { return len(records(r)) == 1 })

	r.b.RunIngestStop(3, func(context.Context) []obsbridge.Outcome { return w.stop() })
	waitFor(t, "the ingest stop record", func() bool { return len(records(r)) == 2 })
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStopped, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectCall(t, r.acts.stopCalled, "the OBS stop job")
	time.Sleep(50 * time.Millisecond)

	if n := w.stopCount(); n != 1 {
		t.Fatalf("the destination was stopped %d times, want 1", n)
	}
	var stops []obsbridge.Via
	for _, rec := range records(r) {
		if rec.Action == "stop" {
			stops = append(stops, rec.Via)
		}
	}
	if len(stops) != 1 || stops[0] != obsbridge.ViaIngest {
		t.Fatalf("stop records %v, want exactly the ingest one", stops)
	}
}

// An OBS stop still waiting out its grace is cancelled by the fallback's stop,
// which covers the same destinations: nothing is left pending.
func TestAnIngestLossStopCancelsAPendingOBSStop(t *testing.T) {
	cfg := driving(3)
	cfg.StopGraceSeconds = 3600
	r, w := ingestRig(t, cfg)
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectCall(t, r.acts.startCalled, "Start")
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStopped, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	if r.b.Status().StopPendingUntil == nil {
		t.Fatal("OBS's stop is not pending")
	}
	r.b.RunIngestStop(3, func(context.Context) []obsbridge.Outcome { return w.stop() })
	if r.b.Status().StopPendingUntil != nil {
		t.Fatal("the OBS stop is still pending after the fallback stopped everything")
	}
}

// The script and obs-websocket cannot claim to be the fallback.
func TestAnEventCannotClaimViaIngest(t *testing.T) {
	r, _ := ingestRig(t, driving(3))
	err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStopped, Via: obsbridge.ViaIngest})
	if !errors.Is(err, obsbridge.ErrBadEvent) {
		t.Fatalf("Handle accepted via=ingest: %v", err)
	}
}
