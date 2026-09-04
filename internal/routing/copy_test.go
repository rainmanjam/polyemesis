package routing

import (
	"strings"
	"testing"
)

// COPY IS THE MODE WHERE THE SUMMARY WOULD OTHERWISE LIE.
//
// Result.Summary ends every line with "-> stereo", which is true of the mix
// path and false here: nothing is folded, nothing is summed, and a 5.1 track
// stays 5.1. The summary is the one sentence an operator reads to check a
// destination carries what they think it does, so these pin what it says rather
// than that it says something.
//
// This file was 44 lines of new code with 44 of them uncovered -- the whole of
// it -- which is how it came to the top of the gate's list once that list was
// sorted rather than alphabetical.
func TestCopySummaryDescribesTheModeItIsActuallyIn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tracks []int
		want   string
	}{
		{"nothing selected", nil, "No audio"},
		{"empty rather than nil", []int{}, "No audio"},
		{"one track", []int{0}, "Track 1 → copied bit-exact"},
		{"two tracks", []int{0, 2}, "Tracks 1, 3 → copied bit-exact"},
		{"several", []int{1, 3, 5}, "Tracks 2, 4, 6 → copied bit-exact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CopySummary(tc.tracks)
			if got != tc.want {
				t.Fatalf("CopySummary(%v) = %q, want %q", tc.tracks, got, tc.want)
			}
			// The mix path's phrasing must never appear here. "-> stereo" on a
			// copy destination tells the operator their 5.1 has been folded
			// when it has not been touched.
			if strings.Contains(got, "stereo") {
				t.Errorf("the copy summary claims a stereo fold: %q", got)
			}
		})
	}
}

// THE TRACK NUMBERS AN OPERATOR SEES START AT 1.
//
// summarize's do, and the UI counts from 1 everywhere it is visible. A summary
// that said "Track 0" would be describing a track no screen in the product
// names.
func TestCopySummaryCountsTracksTheWayTheOperatorDoes(t *testing.T) {
	if got := CopySummary([]int{0}); !strings.Contains(got, "Track 1") {
		t.Fatalf("CopySummary([0]) = %q; the UI counts tracks from 1", got)
	}
	if got := CopySummary([]int{0}); strings.Contains(got, "Track 0") {
		t.Fatalf("CopySummary([0]) = %q; no screen in the product names track 0", got)
	}
}

// A PROFILE A COPY DESTINATION CAN HONOUR RAISES NOTHING.
//
// Returning nil rather than an empty slice matters to the caller, which appends
// unconditionally.
func TestAProfileCopyCanHonourRaisesNoProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Profile
	}{
		{"simple, unity, enabled", Profile{Mode: ModeSimple, Tracks: []TrackSel{
			{Track: 0, Enabled: true, Gain: 1}, {Track: 1, Enabled: true, Gain: 1}}}},
		// Selection is honoured by copy, so a disabled track is not a
		// contradiction -- a copy destination that could not drop the music
		// track would be useless to the archive it exists for.
		{"a disabled track at a strange gain", Profile{Mode: ModeSimple, Tracks: []TrackSel{
			{Track: 0, Enabled: true, Gain: 1}, {Track: 1, Enabled: false, Gain: 0.5}}}},
		{"an enabled track at zero gain", Profile{Mode: ModeSimple, Tracks: []TrackSel{
			{Track: 0, Enabled: true, Gain: 0}}}},
		{"matrix, unity, channel to its own output", Profile{Mode: ModeMatrix, Matrix: []Cell{
			{Track: 0, Channel: 0, Out: 0, Gain: 1}, {Track: 0, Channel: 1, Out: 1, Gain: 1}}}},
		// resolveCells drops a zero-gain cell before the graph is built, so it
		// is the absence of an instruction rather than one to contradict.
		{"matrix cell at zero gain", Profile{Mode: ModeMatrix, Matrix: []Cell{
			{Track: 0, Channel: 1, Out: 0, Gain: 0}}}},
		{"nothing at all", Profile{Mode: ModeSimple}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CopyMixProblems(tc.p); got != nil {
				t.Fatalf("CopyMixProblems raised %v; copy honours selection, and a "+
					"zero-gain instruction is not an instruction", got)
			}
		})
	}
}

// THE TWO SHAPES COPY CANNOT HONOUR, AND THEY ARE REPORTED DIFFERENTLY.
func TestCopyRefusesGainAndRoutingAndSaysWhich(t *testing.T) {
	gain := CopyMixProblems(Profile{Mode: ModeSimple, Tracks: []TrackSel{
		{Track: 2, Enabled: true, Gain: 0.5}}})
	if len(gain) != 1 {
		t.Fatalf("a non-unity gain raised %d problems, want 1: %v", len(gain), gain)
	}
	if !strings.Contains(gain[0], "track 3") {
		t.Errorf("the problem does not name the track the operator has to change: %q", gain[0])
	}

	// A matrix that only moves a channel is the subtle one: it looks like
	// nothing at all in a UI that shows numbers, and it does not survive a copy.
	route := CopyMixProblems(Profile{Mode: ModeMatrix, Matrix: []Cell{
		{Track: 0, Channel: 1, Out: 0, Gain: 1}}})
	if len(route) != 1 || !strings.Contains(route[0], "routes channels") {
		t.Fatalf("a channel re-route at unity gain raised %v, want one routing problem", route)
	}

	// Both shapes at once, and each reported ONCE rather than once per cell: a
	// 6-channel track wired across a stereo pair is twelve cells, and twelve
	// near-identical sentences bury the instruction that has to change.
	both := CopyMixProblems(Profile{Mode: ModeMatrix, Matrix: []Cell{
		{Track: 0, Channel: 0, Out: 0, Gain: 0.5},
		{Track: 0, Channel: 1, Out: 0, Gain: 0.5},
		{Track: 0, Channel: 2, Out: 1, Gain: 0.25},
		{Track: 0, Channel: 3, Out: 1, Gain: 0.25},
	}})
	if len(both) != 2 {
		t.Fatalf("four contradicting cells raised %d problems, want one per shape: %v",
			len(both), both)
	}
}
