//go:build !windows

// Unix only: it drives the live command channel, which Windows does not have
// (liveGainsSupported); there every level change restarts instead.

package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// A live apply the running process ACKNOWLEDGED is recorded as live, even when
// the destination's entry was replaced while the command was in flight. It
// used to fall through to the fallback and record "restart" with an empty
// reason, so the dashboard said "applied by restart: " about a destination
// nothing had restarted.
//
// The child holds its acknowledgement until the test has swapped the entry, so
// the replacement lands inside the apply every time.
//
// MUTATION: restore `if err == nil && e.dests[c.id] == c.d` as the live arm's
// condition. Observed: FAIL, `applied = "restart" reason = ""`.
func TestALiveApplyAcknowledgedAfterTheEntryWasReplacedIsRecordedAsLive(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	before := planFor(t, e, row)

	gate := filepath.Join(t.TempDir(), "gate")
	script := `read l; : > "$0.got"; while [ ! -f "$0.go" ]; do sleep 0.01; done; ` +
		`echo "Command reply for stream 0: ret:0 res:" >&2; while read l; do :; done`
	p := supervisor.New(testLogger(), supervisor.Spec{
		Name: "dest-live", Kind: "destination", Bin: "/bin/sh", Args: []string{"-c", script, gate},
		LiveCommands: true,
	})
	p.Start()
	t.Cleanup(func() { _ = p.Stop(t.Context()) })
	waitRunning(t, p)
	d := liveRunningDest(e, row, before, p)

	muted := *row
	muted.Profile = mixProfile(0)
	muted.Profile.Tracks[0].Muted = true
	after := planFor(t, e, &muted)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.stopDestinations(map[int64]destPlan{row.ID: after})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(gate + ".got"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the command never reached the child")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Replaced while the command waits for its reply.
	replacement := *d
	e.mu.Lock()
	e.dests[row.ID] = &replacement
	e.mu.Unlock()
	if err := os.WriteFile(gate+".go", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	<-done

	applied, reason, ok := e.DestinationLive(row.ID)
	if !ok || applied != liveApplied {
		t.Errorf("applied = %q reason = %q, want live: the running process acknowledged the level", applied, reason)
	}
	e.mu.RLock()
	cur := e.dests[row.ID]
	e.mu.RUnlock()
	if cur != &replacement {
		t.Error("the replacement entry was overwritten or removed by a stale apply")
	}
	if p.Status().State != supervisor.StateRunning {
		t.Errorf("the process is %s; an acknowledged level must not restart it", p.Status().State)
	}
}
