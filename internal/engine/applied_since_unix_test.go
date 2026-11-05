//go:build !windows

// The level-only case needs the command channel; see
// live_reconcile_unix_test.go. The rest of AppliedSince is pinned in
// applied_since_test.go.

package engine

import "testing"

// A level alone goes to the running process on its command channel: the
// process is kept, and the answer is "live" with no reason, which is what tells
// the routing page's toast that a level reached the output.
//
// MUTATION: drop AppliedSince's `case noted:` arm. Observed: FAIL, the reason
// reads "nothing this destination sends changed" for a level that was sent.
func TestAppliedSinceALevelAloneIsLive(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	plan := planFor(t, e, row)
	proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	liveRunningDest(e, row, plan, proc)
	before := e.DestinationRunOf(row.ID)

	quieter := *row
	quieter.Profile = mixProfile(0)
	quieter.Profile.Tracks[0].Gain = 0.25
	e.ForgetDestinationLive(row.ID)
	e.stopDestinations(map[int64]destPlan{row.ID: planFor(t, e, &quieter)})

	applied, reason := e.AppliedSince(row.ID, before)
	if applied != liveApplied || reason != "" {
		t.Fatalf("applied = %q reason = %q, want live with no reason", applied, reason)
	}
	if e.DestinationRunOf(row.ID) != before {
		t.Fatal("a live level change replaced the process")
	}
}
