package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// measuredEngine is an engine holding a real measured layout, with no ingest,
// no relay and no processes — enough to ask what it thinks it knows.
func measuredEngine(t *testing.T, tracks int) *Engine {
	t.Helper()
	e := &Engine{}
	e.source = routing.Source{}
	for i := 0; i < tracks; i++ {
		e.source.Tracks = append(e.source.Tracks,
			routing.Track{Index: i, Channels: 2, Codec: "aac", Layout: "stereo"})
	}
	e.measured = true
	e.measuredMode = db.IngestRTMP
	e.probed = true
	return e
}

// A layout that was measured STAYS measured when the encoder goes quiet.
//
// probeLoop clears probed after three idle rounds -- roughly nine seconds --
// and deliberately leaves e.source alone, because the last measured layout is
// still the truth about what this encoder sends. effectiveSourceKnown asked
// probed anyway, so nine seconds into any outage it began reporting a real
// measured layout as a placeholder: the meters were torn down, the captioner
// was rebuilt against an unknown source, and the API's routing preview stopped
// describing the graph the destinations were actually running.
func TestAMeasuredLayoutSurvivesTheEncoderGoingQuiet(t *testing.T) {
	e := measuredEngine(t, 3)

	src, known := e.effectiveSourceKnown()
	if !known || len(src.Tracks) != 3 {
		t.Fatalf("precondition: known=%v tracks=%d", known, len(src.Tracks))
	}

	// The idle branch: probed drops, the layout and measured are untouched.
	e.probed = false

	src, known = e.effectiveSourceKnown()
	if !known {
		t.Error("a measured layout was reported as unknown once the stream went idle; " +
			"the meters and the captioner are torn down on this, and the routing " +
			"preview starts calling a real layout placeholder-derived")
	}
	if len(src.Tracks) != 3 {
		t.Errorf("tracks = %d, want the 3 that were measured", len(src.Tracks))
	}
}

// The other direction has to keep holding: an engine that has never been probed
// is reporting DefaultSource(), and compiling that into a command line asks
// FFmpeg to map streams that do not exist.
func TestAnUnmeasuredLayoutIsStillUnknown(t *testing.T) {
	e := &Engine{source: routing.DefaultSource()}
	if _, known := e.effectiveSourceKnown(); known {
		t.Error("the placeholder layout was reported as known; every process built " +
			"from it maps streams the ingest does not carry, and FFmpeg refuses to start")
	}
}

// Invalidation moves both together, which is what makes measured a safe proxy
// for "e.source is a measurement".
func TestInvalidationClearsMeasuredAndTheLayoutTogether(t *testing.T) {
	e := measuredEngine(t, 3)
	// What reconcileIngest does when it spawns a new ingest child.
	e.probed = false
	e.measured = false
	e.measuredMode = db.IngestUnset
	e.source = routing.DefaultSource()

	if _, known := e.effectiveSourceKnown(); known {
		t.Error("known survived the layout being reset to the placeholder")
	}
}

// The stem plan is the ingest's own track set, so it must not empty out for a
// blip. It used to be derived from probed: an encoder going quiet emptied the
// plan, changed the recorder signature, and restarted the recorder without
// stems mid-outage -- then restarted it again when the probe returned.
func TestTheStemPlanSurvivesAnIdleGap(t *testing.T) {
	rec := db.RecordingSettings{Enabled: true, Stems: true, StemCodec: ffmpeg.DefaultStemCodec}
	src := routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"},
		{Index: 1, Channels: 2, Codec: "aac", Layout: "stereo"},
	}}

	live := stemPlanFor(rec, src, true)
	if len(live) == 0 {
		t.Fatal("precondition: a measured two-track source plans no stems")
	}
	// Same call after the encoder goes quiet: measured is still true, so the
	// plan -- and therefore the recorder signature -- must not move.
	if idle := stemPlanFor(rec, src, true); stemPlanSig(idle) != stemPlanSig(live) {
		t.Errorf("the stem plan changed across an idle gap: %q -> %q",
			stemPlanSig(live), stemPlanSig(idle))
	}
	// And an unmeasured layout still plans nothing.
	if none := stemPlanFor(rec, routing.DefaultSource(), false); len(none) != 0 {
		t.Errorf("stems were planned from an unmeasured layout: %d", len(none))
	}
}
