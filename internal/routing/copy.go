package routing

import "fmt"

// CopyMixProblems reports every level and channel-routing instruction in p that
// a destination copying its audio bit-for-bit cannot honour.
//
// It lives in this package rather than in the destination validator that calls
// it because the knowledge it needs is the coefficient knowledge: which cells
// resolveCells will actually keep, that a cell at gain <= 0 contributes nothing
// and is therefore not a level change, and that OutL/OutR are channel indices
// and not just the numbers 0 and 1. All of that has moved before and would have
// had to move twice.
//
// WHAT COUNTS AS UNHONOURABLE. Copy forwards each selected ingest track exactly
// as it arrived: same codec, same channel count, same level. So anything that
// would have changed the samples is a contradiction, and there are two shapes of
// it here. A gain other than unity is the obvious one. The subtler one is a
// matrix cell that moves a channel -- `channel 1 of track 0 into the left
// output` is a re-route, and a track wider than stereo folded into two outputs
// is a downmix; neither survives a copy, and both look like nothing at all in a
// UI that only shows numbers.
//
// WHAT DOES NOT COUNT. Which TRACKS are selected. Copy still selects, so an
// enabled/disabled checkbox and a matrix that simply omits a track are both
// honoured exactly. That is deliberate and it is the point: a copy destination
// that could not drop the music track would be useless to the archive it exists
// for.
//
// A profile with no problems returns nil, so a caller can append unconditionally.
func CopyMixProblems(p Profile) []string {
	var probs []string
	add := func(f string, v ...any) { probs = append(probs, fmt.Sprintf(f, v...)) }

	switch p.Mode {
	case ModeMatrix:
		// Reported once per shape rather than once per cell. A 6-channel track
		// wired across a stereo pair is twelve cells, and twelve near-identical
		// sentences would bury the one instruction the operator has to change.
		var gain, route bool
		for _, c := range p.Matrix {
			// A cell at zero gain contributes nothing and resolveCells drops it
			// before the graph is built, so it is not an instruction to
			// contradict -- it is the absence of one.
			if c.Gain <= 0 {
				continue
			}
			if c.Gain != 1 {
				gain = true
			}
			if c.Channel != c.Out {
				route = true
			}
		}
		if gain {
			add("the mix matrix sets a gain other than unity, which a destination " +
				"that copies its audio cannot apply: every selected track is " +
				"forwarded at the level it arrived")
		}
		if route {
			add("the mix matrix routes channels between outputs, which a " +
				"destination that copies its audio cannot do: each selected " +
				"track is forwarded with the channel layout it arrived with")
		}

	default: // ModeSimple
		for _, sel := range p.Tracks {
			// Only enabled rows. ApplyDefaults leaves a gain on a disabled row
			// and the UI keeps whatever the operator last dragged there, so a
			// disabled track at 50% is a setting nothing will ever read.
			if !sel.Enabled || sel.Gain <= 0 {
				continue
			}
			if sel.Gain != 1 {
				add("track %d is set to a gain other than unity, which a "+
					"destination that copies its audio cannot apply: every "+
					"selected track is forwarded at the level it arrived",
					sel.Track+1)
			}
		}
	}
	return probs
}
