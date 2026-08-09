package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// The destination card's one-line summary is what an operator reads to check a
// destination is carrying what they think it is. For a destination that copies
// its audio, the compiled summary is a LIE: it ends "→ stereo", and nothing is
// folded, summed or resampled -- a 5.1 track leaves as 5.1.
//
// Driven through e.Status(), which is the function the API and the WebSocket
// push both call, rather than through routing.CopySummary alone. A test of the
// helper would pass while status.go went on publishing the mix summary, which
// is exactly the shape of test this repo has been burned by.
//
// Mutation: delete the `if row.Audio.Copy` block in status.go. Observed to fail
// with the copy destination reporting "Tracks 1, 3 → stereo".
func TestACopyDestinationsCardDoesNotClaimItWasMixedToStereo(t *testing.T) {
	e, store := storeEngine(t)

	profile := routing.DefaultProfile()
	profile.Tracks = []routing.TrackSel{
		{Track: 0, Enabled: true, Gain: 1},
		{Track: 1, Enabled: false, Gain: 1},
		{Track: 2, Enabled: true, Gain: 1},
	}

	copyRow, err := store.CreateDestination(&db.Destination{
		Name: "archive", Kind: db.DestFile, URL: "archive.mkv", Enabled: false,
		AudioBitrate: 160, Profile: profile,
		Audio: db.AudioEncoding{Copy: true},
	})
	if err != nil {
		t.Fatalf("CreateDestination(copy): %v", err)
	}
	// The control. Without it a status layer that simply stopped reporting
	// summaries would pass.
	mixRow, err := store.CreateDestination(&db.Destination{
		Name: "mixed", Kind: db.DestFile, URL: "mixed.mkv", Enabled: false,
		AudioBitrate: 160, Profile: profile,
	})
	if err != nil {
		t.Fatalf("CreateDestination(mix): %v", err)
	}

	byID := map[int64]DestStatus{}
	for _, ds := range e.Status().Destinations {
		byID[ds.ID] = ds
	}

	got := byID[copyRow.ID]
	if got.Summary == "" {
		t.Fatal("the copy destination's card has no summary at all; the operator " +
			"is told nothing about what it carries")
	}
	if strings.Contains(got.Summary, "stereo") {
		t.Errorf("the copy destination's card says %q: it claims a fold to stereo "+
			"that does not happen", got.Summary)
	}
	// The selection still has to be visible -- which tracks go out is the whole
	// reason copy is not just "-map 0".
	for _, want := range []string{"1", "3"} {
		if !strings.Contains(got.Summary, want) {
			t.Errorf("the copy summary %q does not name track %s", got.Summary, want)
		}
	}
	if strings.Contains(got.Summary, "2") {
		t.Errorf("the copy summary %q names a track that is switched off", got.Summary)
	}

	if mix := byID[mixRow.ID]; !strings.Contains(mix.Summary, "stereo") {
		t.Errorf("the MIXING destination's card says %q; the copy rewrite has "+
			"leaked onto destinations that really do fold to stereo", mix.Summary)
	}
}
