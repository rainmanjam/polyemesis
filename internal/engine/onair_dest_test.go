package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// DestStatus.OnAir is the one reading of "this destination is carrying media to
// viewers" that chat send and chat polls use. A destination stopped while its
// delay buffer plays the tail out is disabled in the database and still on air.
func TestDestStatusOnAir(t *testing.T) {
	running := &supervisor.Status{State: supervisor.StateRunning}
	reconnecting := &supervisor.Status{State: supervisor.StateReconnecting}
	stopped := &supervisor.Status{State: supervisor.StateStopped}
	for name, tc := range map[string]struct {
		d    DestStatus
		want bool
	}{
		"enabled and running":       {DestStatus{Enabled: true, Process: running}, true},
		"enabled and reconnecting":  {DestStatus{Enabled: true, Process: reconnecting}, true},
		"backup tier carrying":      {DestStatus{Enabled: true, Process: stopped, BackupProcess: running}, true},
		"enabled but not running":   {DestStatus{Enabled: true, Process: stopped}, false},
		"disabled and running":      {DestStatus{Enabled: false, Process: running}, false},
		"no process":                {DestStatus{Enabled: true}, false},
		"disabled, tail draining":   {DestStatus{Enabled: false, Process: running, Delay: &DelayStatus{Phase: DelayDraining}}, true},
		"disabled, delay not drain": {DestStatus{Enabled: false, Process: running, Delay: &DelayStatus{Phase: "filling"}}, false},
	} {
		if got := tc.d.OnAir(); got != tc.want {
			t.Errorf("%s: OnAir = %v, want %v", name, got, tc.want)
		}
	}
}
