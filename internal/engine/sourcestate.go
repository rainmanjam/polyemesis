package engine

import (
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// sourceState is everything the engine believes about the ingest's layout, and
// the one place the relationship between those beliefs is written down.
//
// It exists because of a mistake this codebase made five separate times: a
// reader picking the wrong one of these fields. wantSilence and reconcileOutputs
// read `probed` where they meant `measured`; so did effectiveSourceKnown, and so
// did the stem plan. Each was the same shape -- a question about whether the
// LAYOUT IS REAL, answered with the flag for whether a STREAM IS ARRIVING -- and
// each was invisible until something downstream went quiet.
//
// A caveat, because the review that asked for this type claimed more than Go can
// deliver: embedding does not make the raw fields unreachable. Anything in this
// package can still write e.probed. What it does is put the invariant in one
// place, give the two questions names that cannot be confused for each other,
// and make the correct read the shorter one to write.
//
// THE INVARIANT, in full:
//
//	measured == true  <=>  source holds a layout measured from the live ingest
//	measured == false  =>  source is routing.DefaultSource(), the placeholder
//
// Both sides move together at both invalidation sites -- ingest start and an
// ingest-mode change -- which is what makes measured a safe proxy for "source is
// real". probed is a different question with a different lifetime: probeLoop
// clears it after three idle rounds and deliberately leaves source alone,
// because a layout that was measured stays measured.
type sourceState struct {
	// source is the probed ingest layout. Until the ingest carries a stream,
	// this is DefaultSource() so the routing editor still has something to
	// render.
	source routing.Source

	// probed is whether a stream is arriving RIGHT NOW.
	//
	// For display, and for nothing else. Anything that builds a process wants
	// measured; see the file comment for the five times that went wrong.
	probed bool

	// measured is whether source has EVER held a real layout for the current
	// ingest.
	//
	// The two come apart the moment a stream stops. probeLoop clears probed
	// after three idle rounds so the UI stops claiming tracks nobody is sending,
	// and it leaves source alone on purpose -- the last measured layout is still
	// the truth about what this encoder sends. Only starting an ingest puts the
	// PLACEHOLDER back, and that is the one state a routing graph must not be
	// compiled against.
	measured bool

	// measuredMode is the ingest mode source was measured under.
	//
	// reconcileIngest returns EARLY for SRT, for IngestUnset, and for an RTMP
	// source with no token -- all before the reset that ingest start performs.
	// So switching an RTMP source to SRT left measured true over a layout the
	// previous transport delivered, and destinations compiled against it until
	// the new probe landed seconds later.
	//
	// It cannot simply be cleared in those early returns: the SRT one runs on
	// EVERY reconcile, so clearing there would hold destinations forever on the
	// most common ingest of all. Recording the mode makes the invalidation
	// conditional on an actual change, which is the thing that matters.
	measuredMode db.IngestMode

	// sourceGen increments every time source is reset to the placeholder --
	// ingest start, and an ingest-mode change. It exists because a probe is not
	// instantaneous: ffmpeg.Probe runs with a 10s timeout, and probeOnce commits
	// its result long after it read the stream.
	//
	// Without it, the mode-change invalidation above is defeated by the very
	// race it was added for. Switch a probed RTMP source to SRT while a probe of
	// the old RTMP data is in flight: reconcileIngest clears measured and
	// restores the placeholder, then the stale probe lands, writes the RTMP
	// track list into source, and stamps measuredMode from the CURRENT settings
	// -- SRT. The guard is then permanently satisfied by a layout the dead
	// transport delivered, and destinations compile against it.
	//
	// So probeOnce captures this before it reads and discards its own result if
	// it changed underneath. Same protection for a same-mode ingest restart,
	// which measuredMode alone cannot see at all.
	sourceGen uint64

	// videoInfo is the probed video stream, or nil for a video-less ingest.
	videoInfo *ffmpeg.VideoStream
}

// layoutForProcessBuilding is the layout, plus whether it may be compiled into a
// command line.
//
// The second return is the whole point. Compiling the placeholder produces
// stream specifiers the ingest does not carry -- FFmpeg refuses to start rather
// than skipping them -- and, worse, the placeholder claims two channels on every
// track, so a real 5.1 source becomes a valid graph that silently discards the
// centre channel where dialogue lives.
//
// Callers must hold the engine's mu.
func (s *sourceState) layoutForProcessBuilding() (routing.Source, bool) {
	return s.source, s.measured
}

// layoutForDisplay is the layout to render, whether or not it is real. Never
// use it to build a process: that is layoutForProcessBuilding's job.
//
// Callers must hold the engine's mu.
func (s *sourceState) layoutForDisplay() routing.Source { return s.source }

// arrivingNow reports whether a stream is being probed right now. A UI fact, not
// a routing one.
//
// Callers must hold the engine's mu.
func (s *sourceState) arrivingNow() bool { return s.probed }

// commitProbe records a freshly measured layout. Both flags and the layout move
// together, which is the invariant this type exists to keep.
//
// Callers must hold the engine's mu.
func (s *sourceState) commitProbe(src routing.Source, video *ffmpeg.VideoStream, mode db.IngestMode) {
	s.source = src
	s.videoInfo = video
	s.probed = true
	s.measured = true
	s.measuredMode = mode
}

// invalidate puts the placeholder back and forgets everything measured about the
// old ingest. Used at ingest start and on an ingest-mode change.
//
// sourceGen moves so a probe already in flight discards its own result rather
// than resurrecting the layout of a transport that is no longer connected.
//
// Callers must hold the engine's mu.
func (s *sourceState) invalidate() {
	s.probed = false
	s.measured = false
	// Cleared with it, so "measuredMode is the mode source was measured under"
	// holds at every instant rather than only while measured is true. Leaving it
	// set is inert -- the guard is gated on measured -- but a stale value here is
	// what made the probe-resurrection race hard to see.
	s.measuredMode = db.IngestUnset
	s.source = routing.DefaultSource()
	s.sourceGen++
}

// clearProbed records that nothing is arriving, WITHOUT touching the layout.
//
// The asymmetry with invalidate is deliberate and is the distinction the whole
// type is for: a layout that was measured stays measured through an outage.
//
// Callers must hold the engine's mu.
func (s *sourceState) clearProbed() { s.probed = false }
