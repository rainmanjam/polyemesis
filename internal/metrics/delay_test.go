package metrics

import "testing"

// THE BROADCAST DELAY IS ON THE SCRAPE (#11). The configured delay for every
// destination, and while a line exists its fill, its RAM and disk separately
// (the two bounds the design promises), what it has dropped, and whether a
// stopped destination is still airing its tail.
func TestTheScrapeCarriesTheBroadcastDelayAndItsBuffer(t *testing.T) {
	got := parse(t, Render(Snapshot{Destinations: []Destination{
		{ID: 4, Name: "YouTube", Delay: DestDelay{Seconds: 30, Fill: 1,
			MemBytes: 64 << 20, DiskBytes: 5 << 20, DroppedBytes: 1316, Draining: true}},
		{ID: 5, Name: "Twitch"},
	}}))
	for series, want := range map[string]string{
		`polyemesis_destination_delay_seconds{id="4",name="YouTube"}`:                    "30",
		`polyemesis_destination_delay_buffer_fill{id="4",name="YouTube"}`:                "1",
		`polyemesis_destination_delay_buffer_bytes{id="4",name="YouTube",tier="memory"}`: "6.7108864e+07",
		`polyemesis_destination_delay_buffer_bytes{id="4",name="YouTube",tier="disk"}`:   "5.24288e+06",
		`polyemesis_destination_delay_dropped_bytes_total{id="4",name="YouTube"}`:        "1316",
		`polyemesis_destination_delay_draining{id="4",name="YouTube"}`:                   "1",
		`polyemesis_destination_delay_seconds{id="5",name="Twitch"}`:                     "0",
		`polyemesis_destination_delay_draining{id="5",name="Twitch"}`:                    "0",
	} {
		v, ok := got[series]
		if !ok {
			t.Errorf("series %s is missing", series)
			continue
		}
		if v != want {
			t.Errorf("%s = %s, want %s", series, v, want)
		}
	}
}
