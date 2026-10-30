package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// PUT /destinations/{id} decodes over the stored row, so that a client sending
// only what it changed does not blank the rest. Arrays inside it must REPLACE:
// a track or matrix cell carries muted / invert as omitempty, so a client sends
// them as absent when false, and an index-wise decode left the stored true on
// whichever element now sat at that index. See resetSentCollections.
//
// MUTATION (all four): remove the resetSentCollections call from
// decodeJSONInto. Observed: FAIL in each, naming the stale flag.

func putProfile(t *testing.T, h http.Handler, sign func(*http.Request), id int64, profile map[string]any) {
	t.Helper()
	send(t, h, sign, http.MethodPut, destPath(id), map[string]any{"profile": profile}, http.StatusOK)
}

func tracksOf(rows []routing.TrackSel) []map[string]any {
	out := []map[string]any{}
	for _, s := range rows {
		out = append(out, map[string]any{"track": s.Track, "enabled": s.Enabled, "gain": s.Gain})
	}
	return out
}

// The review's proof, as a test: a PUT whose tracks omit muted (= false) must
// leave the track unmuted.
func TestPutWithoutMutedUnmutesTheTrack(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "unmute")
	send(t, h, sign, http.MethodPost, trackGainPath(id, 0), map[string]any{"muted": true}, http.StatusOK)
	row, _ := store.GetDestination(id)

	putProfile(t, h, sign, id, map[string]any{"mode": "simple", "tracks": tracksOf(row.Profile.Tracks)})

	row, _ = store.GetDestination(id)
	for _, s := range row.Profile.Tracks {
		if s.Muted {
			t.Errorf("track %d is still muted after a PUT that sent it unmuted: %+v", s.Track, row.Profile.Tracks)
		}
	}
}

// Re-sorting the cells must not move an inversion onto another cell, and
// un-inverting must clear it.
func TestPutReplacesMatrixCellsRatherThanMergingByIndex(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "matrix")
	row, _ := store.GetDestination(id)
	tracks := tracksOf(row.Profile.Tracks)

	putProfile(t, h, sign, id, map[string]any{"mode": "matrix", "tracks": tracks, "matrix": []map[string]any{
		{"track": 0, "channel": 0, "out": 0, "gain": 1, "invert": true},
		{"track": 0, "channel": 1, "out": 1, "gain": 1},
	}})
	// Re-sorted, the inverted cell now second and still inverted.
	putProfile(t, h, sign, id, map[string]any{"mode": "matrix", "tracks": tracks, "matrix": []map[string]any{
		{"track": 0, "channel": 1, "out": 1, "gain": 1},
		{"track": 0, "channel": 0, "out": 0, "gain": 0.5, "invert": true},
	}})
	row, _ = store.GetDestination(id)
	for _, c := range row.Profile.Matrix {
		want := c.Channel == 0 && c.Out == 0
		if c.Invert != want {
			t.Errorf("after a re-sort, cell ch%d->out%d invert=%v, want %v (matrix %+v)",
				c.Channel, c.Out, c.Invert, want, row.Profile.Matrix)
		}
	}

	// Un-inverted: the same cells, invert omitted everywhere.
	putProfile(t, h, sign, id, map[string]any{"mode": "matrix", "tracks": tracks, "matrix": []map[string]any{
		{"track": 0, "channel": 1, "out": 1, "gain": 1},
		{"track": 0, "channel": 0, "out": 0, "gain": 0.5},
	}})
	row, _ = store.GetDestination(id)
	for _, c := range row.Profile.Matrix {
		if c.Invert {
			t.Errorf("cell ch%d->out%d is still inverted though the PUT inverted nothing", c.Channel, c.Out)
		}
	}
}

// Re-sorting the tracks must not move a mute onto another track.
func TestPutReplacesTracksRatherThanMergingByIndex(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "resort")
	putProfile(t, h, sign, id, map[string]any{"mode": "simple", "tracks": []map[string]any{
		{"track": 0, "enabled": true, "gain": 1, "muted": true},
		{"track": 1, "enabled": true, "gain": 1},
	}})
	putProfile(t, h, sign, id, map[string]any{"mode": "simple", "tracks": []map[string]any{
		{"track": 1, "enabled": true, "gain": 1},
		{"track": 0, "enabled": true, "gain": 1, "muted": true},
	}})
	row, _ := store.GetDestination(id)
	for _, s := range row.Profile.Tracks {
		if s.Muted != (s.Track == 0) {
			t.Errorf("after a re-sort, track %d muted=%v, want %v (tracks %+v)",
				s.Track, s.Muted, s.Track == 0, row.Profile.Tracks)
		}
	}
}

// Absent means unchanged: a PUT that does not mention the profile keeps the
// stored tracks, mute and all. The replacement is for SENT arrays only.
func TestPutWithoutAProfileKeepsTheStoredTracks(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "keep")
	send(t, h, sign, http.MethodPost, trackGainPath(id, 0), map[string]any{"muted": true}, http.StatusOK)

	send(t, h, sign, http.MethodPut, destPath(id), map[string]any{"name": "keep-renamed"}, http.StatusOK)

	row, _ := store.GetDestination(id)
	if row.Name != "keep-renamed" {
		t.Fatalf("name = %q; the PUT did not apply", row.Name)
	}
	if len(row.Profile.Tracks) == 0 || !row.Profile.Tracks[0].Muted {
		t.Errorf("a PUT that never mentioned the profile changed its tracks: %+v", row.Profile.Tracks)
	}
}

// The reset runs ahead of the ONE strict decode, so an unknown key inside the
// profile is still refused. A per-type UnmarshalJSON would have lost this.
func TestPutStillRefusesAnUnknownKeyInsideTheProfile(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "strict")
	send(t, h, sign, http.MethodPut, destPath(id), map[string]any{"profile": map[string]any{
		"mode": "simple", "tracks": []map[string]any{{"track": 0, "enabled": true, "gain": 1, "mutd": true}},
	}}, http.StatusBadRequest)
}
