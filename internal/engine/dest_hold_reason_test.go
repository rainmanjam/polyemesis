package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/playout"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

/* AN ABSENCE THAT CANNOT EXPLAIN ITSELF.
 *
 * reconcileOutputs holds every destination while the ingest layout is
 * unmeasured, and the hold is correct: a routing graph compiled against the
 * placeholder maps tracks the ingest may not carry. But a HELD destination and
 * a CRASHED one are byte-for-byte identical in /status -- DestStatus.Process is
 * nil in both, and omitempty drops the field entirely -- and so is one that was
 * never planned at all.
 *
 * The reason was not missing, only unreachable: it went to Engine.LastReload,
 * which is returned in a SETTINGS-SAVE response. So the only way to ask "why is
 * nothing running" was to save something, which perturbs the very thing being
 * asked about. The acceptance suite hit this from the other side: it read -1 for
 * ninety seconds and could report that no destination had a process, but not one
 * word about why.
 *
 * Asserted THROUGH Status(), not against the engine field, so this fails if the
 * publish is removed rather than merely if the string changes.
 */
func TestStatusSaysWhyEveryDestinationIsUnplanned(t *testing.T) {
	e := failoverEngine(t)
	e.settings = failoverOnSettings()
	e.play = playout.New(playout.Deps{Dir: t.TempDir()})

	dest, err := e.store.CreateDestination(&db.Destination{
		Name: "onair", Kind: db.DestRTMP, Platform: db.PlatformCustom,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(),
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if dest.SourceID == nil {
		t.Fatal("the created destination has no source")
	}
	e.sourceID = *dest.SourceID

	// A measured layout plans normally, and says nothing.
	e.mu.Lock()
	e.measured, e.probed = true, true
	e.source = routing.Source{Tracks: []routing.Track{{Index: 0, Channels: 2}}}
	e.mu.Unlock()
	if err := e.reconcileOutputs(); err != nil {
		t.Fatalf("reconcileOutputs (measured): %v", err)
	}
	if hold := e.Status().DestinationHold; hold != nil {
		t.Errorf("a normally-planned pass reported a hold %+v. A reason that is "+
			"present when nothing is held trains every reader to ignore it.", *hold)
	}

	// The ingest restarts: placeholder back, nothing measured. Every destination
	// is now held -- and the payload has to be able to say so.
	e.mu.Lock()
	e.measured, e.probed = false, false
	e.source = routing.DefaultSource()
	e.mu.Unlock()
	if err := e.reconcileOutputs(); err != nil {
		t.Fatalf("reconcileOutputs (unmeasured): %v", err)
	}

	e.mu.RLock()
	left := len(e.dests)
	e.mu.RUnlock()
	if left != 0 {
		t.Fatalf("fixture: %d destination(s) still running, so nothing is being "+
			"held and this proves nothing about the reason", left)
	}

	hold := e.Status().DestinationHold
	if hold == nil {
		t.Fatal("every destination was held and /status said nothing about it. " +
			"A reader -- the dashboard, or the acceptance suite -- sees a destination " +
			"with no process and cannot tell a deliberate hold from a crash.")
	}
	// The CODE, not the prose. Asserting on a word in the sentence is what makes
	// a reworded message a failing build, which is the whole reason the two are
	// separate fields.
	if hold.Code != "awaiting-ingest-probe" {
		t.Errorf("hold code %q, want awaiting-ingest-probe -- this is the identifier "+
			"the suite and any alerting match on, so it is not free to drift", hold.Code)
	}
	if strings.TrimSpace(hold.Reason) == "" {
		t.Error("the hold carries a code but nothing a person can read")
	}

	// AND IT CLEARS. A reason that outlives its condition is worse than none:
	// it reports a hold on a healthy install for ever.
	e.mu.Lock()
	e.measured, e.probed = true, true
	e.source = routing.Source{Tracks: []routing.Track{{Index: 0, Channels: 2}}}
	e.mu.Unlock()
	if err := e.reconcileOutputs(); err != nil {
		t.Fatalf("reconcileOutputs (re-measured): %v", err)
	}
	if hold := e.Status().DestinationHold; hold != nil {
		t.Errorf("the hold survived the layout being measured again: %+v", *hold)
	}
}
