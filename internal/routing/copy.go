package routing

import (
	"fmt"
	"strconv"
	"strings"
)

// CopySummary is what a destination card says about a destination that copies
// its audio.
//
// It exists because Result.Summary would otherwise LIE to that card. summarize
// ends every line with "-> stereo", which is true of the mix path and false
// here: nothing is folded, nothing is summed, and a 5.1 track stays 5.1. The
// summary is the one sentence an operator reads to check a destination is
// carrying what they think it is, so it has to describe the mode it is in.
//
// The track numbers are 1-based for the same reason summarize's are: the UI
// counts tracks from 1 everywhere an operator can see them.
func CopySummary(tracks []int) string {
	if len(tracks) == 0 {
		return "No audio"
	}
	parts := make([]string, len(tracks))
	for i, t := range tracks {
		parts[i] = strconv.Itoa(t + 1)
	}
	if len(tracks) == 1 {
		return "Track " + parts[0] + " → copied bit-exact"
	}
	return "Tracks " + strings.Join(parts, ", ") + " → copied bit-exact"
}

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
	switch p.Mode {
	case ModeMatrix:
		return matrixCopyProblems(p.Matrix)
	default: // ModeSimple
		return simpleCopyProblems(p.Tracks)
	}
}

// matrixCopyProblems is CopyMixProblems for a matrix profile.
func matrixCopyProblems(cells []Cell) []string {
	var probs []string
	// Reported once per shape rather than once per cell. A 6-channel track
	// wired across a stereo pair is twelve cells, and twelve near-identical
	// sentences would bury the one instruction the operator has to change.
	gain, route, invert := matrixCopyShapes(cells)
	if gain {
		probs = append(probs, "the mix matrix sets a gain other than unity, which a destination "+
			"that copies its audio cannot apply: every selected track is "+
			"forwarded at the level it arrived")
	}
	if invert {
		probs = append(probs, "the mix matrix inverts a channel's polarity, which a destination "+
			"that copies its audio cannot do: every selected track is "+
			"forwarded with the samples it arrived with")
	}
	if route {
		probs = append(probs, "the mix matrix routes channels between outputs, which a "+
			"destination that copies its audio cannot do: each selected "+
			"track is forwarded with the channel layout it arrived with")
	}
	return probs
}

// matrixCopyShapes reports which shapes of change a copy cannot honour the
// matrix's cells ask for: a gain, a re-route, a polarity inversion.
func matrixCopyShapes(cells []Cell) (gain, route, invert bool) {
	for _, c := range cells {
		// A cell at zero gain contributes nothing and resolveCells drops it
		// before the graph is built, so it is not an instruction to
		// contradict -- it is the absence of one.
		if c.Gain <= 0 {
			continue
		}
		gain = gain || c.Gain != 1
		route = route || c.Channel != c.Out
		invert = invert || c.Invert
	}
	return gain, route, invert
}

// simpleCopyProblems is CopyMixProblems for a simple profile.
func simpleCopyProblems(tracks []TrackSel) []string {
	var probs []string
	for _, sel := range tracks {
		// Only enabled rows. ApplyDefaults leaves a gain on a disabled row
		// and the UI keeps whatever the operator last dragged there, so a
		// disabled track at 50% is a setting nothing will ever read.
		if !sel.Enabled || sel.Gain <= 0 {
			continue
		}
		if sel.Gain != 1 {
			probs = append(probs, fmt.Sprintf("track %d is set to a gain other than unity, which a "+
				"destination that copies its audio cannot apply: every "+
				"selected track is forwarded at the level it arrived",
				sel.Track+1))
		}
	}
	return probs
}
