package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// I4's live mute and gain are for the tracks a destination mixes, and a
// destination with a VOD track mixes two: the live mix and the VOD mix. A
// control that moved only the live half would leave the archive carrying a
// track the operator had just silenced on air, so the same track's mute and
// gain reach the VOD mix as well.
//
// This is a stored change, and the running FFmpeg takes it through the same
// level path the live mix uses: the VOD half is a volume@vod_trkN stage in the
// same graph, which applyLiveLevels already addresses. What was missing was the
// write, which touched only the live profile.

// vodMixOn gives a destination a second mix that selects the given track.
func vodMixOn(track int) *routing.Profile {
	return &routing.Profile{
		Mode:       routing.ModeSimple,
		Tracks:     []routing.TrackSel{{Track: track, Enabled: true, Gain: 1}},
		Normalize:  routing.NormOff,
		SampleRate: 48000,
	}
}

// Muting track 0 live silences it on the VOD mix too.
//
// MUTATION: in handleSetTrackGain, delete the block that mirrors Muted onto
// row.VODProfile. Observed: FAIL, "the VOD mix still carries track 0".
func TestALiveMuteOnATrackSilencesItOnTheVODMixToo(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "vod-mute")
	row, err := store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	row.VODProfile = vodMixOn(0)
	if _, err := store.UpdateDestination(row); err != nil {
		t.Fatalf("storing the VOD mix: %v", err)
	}

	send(t, h, sign, http.MethodPost, trackGainPath(id, 0),
		map[string]any{"muted": true}, http.StatusOK)

	row, err = store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.VODProfile == nil || len(row.VODProfile.Tracks) != 1 {
		t.Fatalf("the VOD mix was lost or changed shape: %+v", row.VODProfile)
	}
	if !row.VODProfile.Tracks[0].Muted {
		t.Error("the VOD mix still carries track 0 after it was muted live")
	}
}

// A gain reaches the VOD mix's same track, so the two mixes keep one fader per
// source rather than drifting apart.
//
// MUTATION: in handleSetTrackGain, delete the gain half of the mirror. Observed:
// FAIL, "the VOD mix kept gain 1".
func TestALiveGainOnATrackReachesTheVODMixToo(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "vod-gain")
	row, err := store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	row.VODProfile = vodMixOn(0)
	if _, err := store.UpdateDestination(row); err != nil {
		t.Fatalf("storing the VOD mix: %v", err)
	}

	send(t, h, sign, http.MethodPost, trackGainPath(id, 0),
		map[string]any{"gain": 0.5}, http.StatusOK)

	row, err = store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := row.VODProfile.Tracks[0].Gain; got != 0.5 {
		t.Errorf("the VOD mix kept gain %v for track 0; want the live change 0.5", got)
	}
}

// A track the VOD mix does not select stays out of it. A live change to a
// track the archive excludes, such as the music bed, must not add that track to
// the VOD mix.
//
// MUTATION: in the mirror, drop the "selected by the VOD mix" check and append a
// row for any track. Observed: FAIL, "the VOD mix gained track 1".
func TestALiveChangeDoesNotAddATrackTheVODMixDoesNotSelect(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "vod-unselected")
	row, err := store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	// The live mix carries track 1 already (the default selection), so a live
	// change to it is addressable while the VOD mix selects only track 0.
	row.VODProfile = vodMixOn(0)
	if _, err := store.UpdateDestination(row); err != nil {
		t.Fatalf("storing the mixes: %v", err)
	}

	send(t, h, sign, http.MethodPost, trackGainPath(id, 1),
		map[string]any{"muted": true}, http.StatusOK)

	row, err = store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(row.VODProfile.Tracks) != 1 || row.VODProfile.Tracks[0].Track != 0 {
		t.Errorf("the VOD mix changed shape to %+v; a track it does not select must stay out", row.VODProfile.Tracks)
	}
}
