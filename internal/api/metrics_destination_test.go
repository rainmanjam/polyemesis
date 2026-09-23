package api

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/metrics"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// THE SCRAPE CARRIES WHAT THE PROCESS REPORTED. The output-time and output-byte
// counters are how an alert sees a destination whose sink stopped reading
// (exploratory CH-02), and metrics' own test only proves they render from a
// snapshot built by hand. This one starts from the engine's status -- what a
// running FFmpeg's last -progress block left there -- so a scrape row that
// forgets to copy them reads zero here and not only in production.
func TestTheScrapeCarriesADestinationsOutputProgress(t *testing.T) {
	running := engine.DestStatus{
		ID: 9, Name: "Twitch", Enabled: true,
		Process: &supervisor.Status{
			State: supervisor.StateRunning,
			Progress: ffmpeg.Progress{
				OutTimeMS: 61_500, TotalSize: 34_000_000, BitrateKbps: 4500,
			},
		},
	}
	stopped := engine.DestStatus{ID: 3, Name: "Archive"}
	out := metrics.Render(metrics.Snapshot{
		Destinations: []metrics.Destination{metricsDestination(running), metricsDestination(stopped)},
	})

	for series, want := range map[string]string{
		`polyemesis_destination_output_seconds_total{id="9",name="Twitch"}`:  "61.5",
		`polyemesis_destination_output_bytes_total{id="9",name="Twitch"}`:    "3.4e+07",
		`polyemesis_destination_output_seconds_total{id="3",name="Archive"}`: "0",
	} {
		found := false
		for _, line := range strings.Split(out, "\n") {
			if v, ok := strings.CutPrefix(line, series+" "); ok {
				found = true
				if v != want {
					t.Errorf("%s = %s, want %s", series, v, want)
				}
			}
		}
		if !found {
			t.Errorf("series %s is missing from the scrape:\n%s", series, out)
		}
	}
}
