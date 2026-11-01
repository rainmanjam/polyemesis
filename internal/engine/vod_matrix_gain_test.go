package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A VOD mix in simple mode takes a gain change live: the level is a volume stage
// in the same graph as the live mix's, and the restart signature masks it. A VOD
// mix in matrix mode is different. Its gain lives in the matrix cells, which sit
// in the pan and are structural, so the change restarts the destination. The
// Routing page tells the operator so; these tests pin that the restart happens
// and the live path does not claim it.

// vodMatrixRow is a custom RTMP destination (no negotiation to get in the way)
// whose VOD mix is a matrix with one cell at the given gain.
func vodMatrixRow(cellGain float64) *db.Destination {
	vod := routing.Profile{
		Mode:       routing.ModeMatrix,
		Matrix:     []routing.Cell{{Track: 1, Channel: 0, Out: routing.OutL, Gain: cellGain}},
		Normalize:  routing.NormOff,
		SampleRate: 48000,
	}
	return &db.Destination{
		ID: 52, Name: "vod-matrix", Kind: db.DestRTMP, Platform: db.PlatformCustom,
		Profile: mixProfile(0), VODProfile: &vod, Enabled: true,
	}
}

// vodSimpleRow is the same destination with the VOD mix in simple mode, at the
// given track gain.
func vodSimpleRow(trackGain float64) *db.Destination {
	vod := mixProfile(1)
	vod.Tracks[0].Gain = trackGain
	return &db.Destination{
		ID: 53, Name: "vod-simple", Kind: db.DestRTMP, Platform: db.PlatformCustom,
		Profile: mixProfile(0), VODProfile: &vod, Enabled: true,
	}
}

// planOf plans one row against the paired source and reports the plan.
func planOf(t *testing.T, e *Engine, row *db.Destination) destPlan {
	t.Helper()
	p := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", false)[row.ID]
	if p.err != "" {
		t.Fatalf("the destination will not plan: %s", p.err)
	}
	return p
}

// runningAs is a destination running with the given plan, as a reconcile would
// leave it.
func runningAs(p destPlan) *destination {
	return &destination{
		row: p.row, proc: loudTestProc(), compiled: p.compiled, spec: p.spec,
		graph: newGraphHolder(p.compiled),
	}
}

// A matrix VOD gain change is structural: the running destination is NOT kept
// on its live path, so it is restarted, and the restart is the mechanism the UI
// copy describes.
//
// MUTATION: in keepDestination, drop the spec comparison. Observed: FAIL, "a
// matrix VOD gain change kept the running destination on its live path".
func TestAMatrixVODGainChangeRestartsTheDestinationRatherThanGoingLive(t *testing.T) {
	e, _ := storeEngine(t)
	before := planOf(t, e, vodMatrixRow(1))
	after := planOf(t, e, vodMatrixRow(0.5))

	if before.spec == after.spec {
		t.Fatal("a matrix VOD cell gain did not change the destination's signature, so nothing would restart")
	}
	if keepDestination(runningAs(before), after, true, nil) {
		t.Fatal("a matrix VOD gain change kept the running destination on its live path")
	}
}

// A simple VOD mix's track gain is a level, and goes live: the destination is
// kept, and its graph differs only in that level, which is what the live path
// sends as a volume command.
//
// MUTATION: in keepDestination, return false unconditionally. Observed: FAIL,
// "a simple VOD gain change restarted the destination".
func TestASimpleVODGainChangeStaysLiveOnTheVODMix(t *testing.T) {
	e, _ := storeEngine(t)
	before := planOf(t, e, vodSimpleRow(1))
	after := planOf(t, e, vodSimpleRow(0.5))

	if !keepDestination(runningAs(before), after, true, nil) {
		t.Fatal("a simple VOD gain change restarted the destination; it should stay live")
	}
	if !levelsDiffer(before.compiled.FilterComplex, after.compiled.FilterComplex) {
		t.Fatal("the VOD mix's level did not appear as a level change, so the live path would send nothing")
	}
}
