package metrics

import "testing"

// A destination whose sink stopped reading stays running (up=1), keeps zero
// restarts, and keeps reporting the bitrate FFmpeg last printed -- which is an
// average over the whole run, frozen in the last progress block before the
// write blocked. Nothing on the scrape moved, so no rule could see the stall
// (exploratory CH-02). The output time and bytes FFmpeg reports are counters
// that stop advancing the moment delivery does, which rate() can see.
func TestAStalledDestinationHasSeriesThatStopMoving(t *testing.T) {
	s := testSnapshot()
	s.Destinations[0].OutTimeMS = 61_500
	s.Destinations[0].OutputBytes = 34_000_000
	got := parse(t, Render(s))

	for series, want := range map[string]string{
		`polyemesis_destination_output_seconds_total{id="9",name="Twitch"}`: "61.5",
		`polyemesis_destination_output_bytes_total{id="9",name="Twitch"}`:   "3.4e+07",
		// A destination with no process reports zero, not a missing series.
		`polyemesis_destination_output_seconds_total{id="3",name="Archive"}`: "0",
	} {
		if v, ok := got[series]; !ok {
			t.Errorf("series %s is missing from the exposition", series)
		} else if v != want {
			t.Errorf("%s = %s, want %s", series, v, want)
		}
	}
}
