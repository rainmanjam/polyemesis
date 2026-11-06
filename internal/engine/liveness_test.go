package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// THE SCRAPE'S INGEST STATE FOR A PROGRAMME WITH NO INGEST PROCESS.
//
// An SRT source is received by the manager's shared SRT listener, which hands
// datagrams to the programme's relay hub with no FFmpeg child in between. The
// scrape read the state off that child alone, so an SRT programme read
// polyemesis_ingest_up 0 and ingest_state{state="stopped"} 1 for its whole
// life -- including while its encoder was pushing -- and the "failed" half of
// PolyemesisIngestDown could never fire for it, whatever happened to the port.
//
// The listener IS that programme's ingest, so it carries the state: running
// while it is bound (waiting for or receiving the encoder, exactly as an RTMP
// source's child reads running while it waits), failed when a port is
// configured and nothing holds it, and stopped only when the operator turned
// the port off. Other modes have nothing here: their ingest is the child.
func TestAnSRTProgrammesIngestStateIsItsListeners(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    db.IngestMode
		enabled bool
		port    int
		bound   bool
		want    supervisor.State
	}{
		{"srt, listener up", db.IngestSRT, true, 6000, true, supervisor.StateRunning},
		{"srt, port configured and not bound", db.IngestSRT, true, 6000, false, supervisor.StateFailed},
		{"srt, port turned off", db.IngestSRT, true, 0, false, supervisor.StateStopped},
		// The listener refuses a disabled source's encoder: switched off, not up.
		{"srt, source disabled", db.IngestSRT, false, 6000, true, supervisor.StateStopped},
		{"srt, source disabled, port not bound", db.IngestSRT, false, 6000, false, supervisor.StateStopped},
		{"rtmp with no child", db.IngestRTMP, true, 6000, true, supervisor.StateStopped},
		{"pull with no child", db.IngestPull, true, 6000, true, supervisor.StateStopped},
		{"unset", db.IngestUnset, true, 6000, true, supervisor.StateStopped},
	} {
		if got := childlessIngestState(tc.mode, tc.enabled, tc.port, tc.bound); got != tc.want {
			t.Errorf("%s: childlessIngestState = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDestinationUpIsOneJudgement(t *testing.T) {
	run := &supervisor.Status{State: supervisor.StateRunning}
	recon := &supervisor.Status{State: supervisor.StateReconnecting}
	for _, tc := range []struct {
		name                     string
		d                        DestStatus
		up, carrying, delivering bool
	}{
		{"no process", DestStatus{}, false, false, false},
		{"running, no uptime yet", DestStatus{Process: run}, true, true, false},
		{"running with uptime", DestStatus{Process: &supervisor.Status{State: supervisor.StateRunning, UptimeSec: 5}}, true, true, true},
		{"running but errored", DestStatus{Error: "boom", Process: run}, false, true, false},
		{"reconnecting", DestStatus{Process: recon}, false, true, false},
	} {
		if got := tc.d.IsUp(); got != tc.up {
			t.Errorf("%s: IsUp = %v, want %v", tc.name, got, tc.up)
		}
		if got := processCarrying(tc.d.Process); got != tc.carrying {
			t.Errorf("%s: processCarrying = %v, want %v", tc.name, got, tc.carrying)
		}
		if got := processDelivering(tc.d.Process); got != tc.delivering {
			t.Errorf("%s: processDelivering = %v, want %v", tc.name, got, tc.delivering)
		}
	}
}

func TestAlertStateAppliesTheDelayRule(t *testing.T) {
	buf := DestStatus{ID: 1, Enabled: true, Delay: &DelayStatus{Phase: DelayBuffering}}
	if a := buf.AlertState(); !a.Buffering || !a.Enabled || a.Running {
		t.Errorf("buffering: %+v", a)
	}
	drain := DestStatus{ID: 2, Enabled: false, Delay: &DelayStatus{Phase: DelayDraining}}
	if a := drain.AlertState(); a.Buffering || !a.Enabled {
		t.Errorf("draining: %+v", a)
	}
	p := DestStatus{ID: 3, Enabled: true, Process: &supervisor.Status{State: supervisor.StateRunning, Restarts: 2}}
	if a := p.AlertState(); !a.Running || a.Restarts != 2 {
		t.Errorf("running: %+v", a)
	}
}
