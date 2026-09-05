package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A RELAY NAME ALREADY IN USE STOPS THE CHILD AND GIVES THE PORT BACK. #711/#707.
//
// Every aux consumer takes a port and then a name, and until #711 the name was
// a bare map assignment that could not fail. Now it can, and the refusal has to
// take the same release-and-bail path the port refusal beside it already took —
// otherwise closing the collision hazard would open the leak hazard, on the
// exact code #707 was about.
//
// So the assertion here is BOTH halves at once: nothing started, and the pool
// is exactly as full as it was. The incumbent keeping its subscription is
// checked too, because that is the property the whole refusal exists for.
func TestAnAuxConsumerRefusedItsRelayNameStartsNothingAndKeepsNoPort(t *testing.T) {
	// Table over the production consumers rather than one test each: they are
	// the same hazard reached through five doors, and a table makes a sixth
	// door's omission visible as a missing row.
	for _, tc := range []struct {
		name    string
		subName string
		drive   func(t *testing.T, e *Engine)
		live    func(e *Engine) bool
	}{
		{
			name:    "meters",
			subName: "meters",
			drive: func(t *testing.T, e *Engine) {
				e.mu.Lock()
				e.source = routing.Source{Tracks: []routing.Track{{Channels: 2}}}
				e.measured, e.probed = true, true
				e.mu.Unlock()
				s := db.DefaultSettings()
				s.Meters.Enabled = true
				e.reconcileMeters(s)
			},
			live: func(e *Engine) bool { e.mu.RLock(); defer e.mu.RUnlock(); return e.meters != nil },
		},
		{
			name:    "recorder",
			subName: "recorder",
			drive: func(t *testing.T, e *Engine) {
				s := db.DefaultSettings()
				s.Recording.Enabled = true
				e.reconcileRecorder(s)
			},
			live: func(e *Engine) bool { e.mu.RLock(); defer e.mu.RUnlock(); return e.recorder != nil },
		},
		{
			name:    "preview",
			subName: "preview",
			drive: func(t *testing.T, e *Engine) {
				s := db.DefaultSettings()
				s.Preview.Enabled = true
				markPreviewFlowing(e)
				e.reconcilePreview(s)
			},
			live: func(e *Engine) bool { e.mu.RLock(); defer e.mu.RUnlock(); return e.preview != nil },
		},
		{
			name:    "clip buffer",
			subName: clipSubName,
			drive: func(t *testing.T, e *Engine) {
				e.mu.Lock()
				e.clipOn, e.clipCfg = true, clips.Config{WindowSeconds: 30, MaxRingBytes: 1 << 20}
				e.mu.Unlock()
				e.reconcileClips()
			},
			live: func(e *Engine) bool { e.mu.RLock(); defer e.mu.RUnlock(); return e.clipCap != nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := storeEngine(t)

			// THE INCUMBENT. A real consumer already reading under this name,
			// which is the only state that makes a second subscribe wrong.
			held := enginePort(t, e, "the incumbent consumer")
			hub := e.downstreamHub()
			if hub == nil {
				t.Fatal("no downstream hub on a fresh engine")
			}
			if _, err := hub.Subscribe(tc.subName, held); err != nil {
				t.Fatalf("the incumbent could not subscribe: %v", err)
			}
			// The ingest hub too, for the consumers that read it rather than
			// the downstream one; on a fresh engine they are the same object,
			// and this keeps the row honest if they stop being.
			if e.hub != hub {
				if _, err := e.hub.Subscribe(tc.subName, held); err != nil {
					t.Fatalf("the incumbent could not subscribe to the ingest hub: %v", err)
				}
			}

			before := e.heldPortCount()
			tc.drive(t, e)

			if tc.live(e) {
				t.Errorf("the %s started even though its relay name was taken. It is "+
					"subscribed to nothing, so it runs with a correct command line and "+
					"a healthy card while receiving no packets at all -- and it has "+
					"replaced the consumer that was reading under that name", tc.name)
			}
			if got := e.heldPortCount(); got != before {
				t.Errorf("the engine holds %d port(s), was %d: a refusal that keeps the "+
					"port burns one of the 500 shared across every engine, permanently "+
					"and silently", got, before)
			}
			if !hasSubscriber(hub, tc.subName) {
				t.Errorf("%q is no longer on the hub: the refusal removed the "+
					"incumbent's subscription, which is the exact outcome it exists "+
					"to prevent", tc.subName)
			}
		})
	}
}

// The silence tier takes the same path and is worth its own case: it also opens
// a relay hub of its own before subscribing, so its refusal has three things to
// give back rather than two.
func TestASilenceTierRefusedItsRelayNameLeavesNoPortAndNoHub(t *testing.T) {
	e, _ := storeEngine(t)

	held := enginePort(t, e, "the incumbent consumer")
	if _, err := e.hub.Subscribe(silenceSubName, held); err != nil {
		t.Fatalf("the incumbent could not subscribe: %v", err)
	}
	before := e.heldPortCount()

	e.startSilence("silence|stereo|48000")

	e.mu.RLock()
	tier := e.silence
	e.mu.RUnlock()
	// startSilence records a FAILED tier rather than returning: the destinations
	// downstream have to be told why no bed is coming, and the next reconcile
	// retries. So the property is not "nothing was recorded" -- it is that
	// nothing was STARTED and the reason is carried.
	if tier == nil {
		t.Fatal("the refusal recorded nothing at all; the destinations downstream " +
			"are never told why no silent bed arrived")
	}
	if tier.proc != nil || tier.hub != nil || tier.port != 0 {
		t.Errorf("the silence tier published itself despite being refused its relay "+
			"name (%+v), so reconcile believes a bed is on air that receives nothing", tier)
	}
	if tier.err == "" {
		t.Error("the failed tier carries no reason, so the status page says the bed " +
			"is absent without saying why")
	}
	if got := e.heldPortCount(); got != before {
		t.Errorf("the engine holds %d port(s), was %d", got, before)
	}
	if !hasSubscriber(e.hub, silenceSubName) {
		t.Error("the incumbent's subscription was removed by the refusal")
	}
}

// The destination path, primary and backup, and they fail differently on
// purpose: a destination that cannot start is an error the caller reports,
// while a backup that cannot start is recorded and the primary carries on.
// #711 has to preserve both, or closing the collision would either take a live
// destination down or turn its optional half into a hard failure.
func TestADestinationRefusedItsRelayNameFailsAndReturnsThePort(t *testing.T) {
	e, _ := storeEngine(t)
	row := backupRow()

	held := enginePort(t, e, "the incumbent consumer")
	if _, err := e.hub.Subscribe(destSubName(row.ID, ""), held); err != nil {
		t.Fatalf("the incumbent could not subscribe: %v", err)
	}
	before := e.heldPortCount()

	err := e.startDest(destPlan{row: row, spec: "spec", compiled: routing.Result{}}, e.hub, 0)
	if err == nil {
		t.Error("startDest reported success while subscribed to nothing. The " +
			"destination would show a healthy card and a correct command line and " +
			"publish an empty stream, and the consumer that held the name is cut off")
	}
	if got := e.heldPortCount(); got != before {
		t.Errorf("the engine holds %d port(s), was %d: a destination that refuses to "+
			"start must give its port back", got, before)
	}
}

func TestABackupRefusedItsRelayNameIsRecordedAndLeavesThePrimaryAlone(t *testing.T) {
	e, _ := storeEngine(t)
	row := backupRow()

	held := enginePort(t, e, "the incumbent consumer")
	backupSub := destSubName(row.ID, destRoleBackup)
	if _, err := e.hub.Subscribe(backupSub, held); err != nil {
		t.Fatalf("the incumbent could not subscribe: %v", err)
	}
	before := e.heldPortCount()

	d := &destination{row: row, hub: e.hub, spec: "spec"}
	e.buildBackup(d, routing.Result{}, "spec")

	if d.backup != nil || d.backupPort != 0 || d.backupSub != "" {
		t.Errorf("the backup published itself despite being refused its relay name: "+
			"proc=%v port=%d sub=%q", d.backup != nil, d.backupPort, d.backupSub)
	}
	if d.backupErr == "" {
		t.Error("the backup failed silently. Its whole card says 'redundant feed " +
			"running' or says why it is not, and this leaves it saying neither")
	}
	if got := e.heldPortCount(); got != before {
		t.Errorf("the engine holds %d port(s), was %d", got, before)
	}
	// AND THE PRIMARY IS UNTOUCHED. A backup that cannot start is not a reason
	// to take a live destination off the air, which is why this path records a
	// reason rather than returning an error.
	if !hasSubscriber(e.hub, backupSub) {
		t.Error("the refusal removed the incumbent's subscription")
	}
}
