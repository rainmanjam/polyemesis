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

// A stalled destination is not up. Its process is running -- the state series
// still says so -- but an operator's dashboard reads _up, and before this it
// read 1 with a frozen non-zero bitrate for the whole of a stall (exploratory
// row 7). The scrape now carries the engine's verdict (DestStatus.Stalled, the
// supervisor's flag with the source arriving): up 0, stalled 1, and the bitrate
// the supervisor reports for a stalled run, which is 0.
func TestAStalledDestinationIsNotUpOnTheScrape(t *testing.T) {
	stalled := engine.DestStatus{
		ID: 1, Name: "D-default", Enabled: true, Stalled: true,
		Process: &supervisor.Status{
			State: supervisor.StateRunning, Stalled: true, StalledSec: 40,
			Progress: ffmpeg.Progress{OutTimeMS: 117_973, TotalSize: 39_000_000},
		},
	}
	live := engine.DestStatus{
		ID: 2, Name: "Live", Enabled: true,
		Process: &supervisor.Status{
			State:    supervisor.StateRunning,
			Progress: ffmpeg.Progress{OutTimeMS: 117_973, BitrateKbps: 2651.6},
		},
	}
	out := metrics.Render(metrics.Snapshot{
		Destinations: []metrics.Destination{metricsDestination(stalled), metricsDestination(live)},
	})
	for series, want := range map[string]string{
		`polyemesis_destination_up{id="1",name="D-default"}`:                      "0",
		`polyemesis_destination_stalled{id="1",name="D-default"}`:                 "1",
		`polyemesis_destination_bitrate_bits_per_second{id="1",name="D-default"}`: "0",
		`polyemesis_destination_state{id="1",name="D-default",state="running"}`:   "1",
		`polyemesis_destination_up{id="2",name="Live"}`:                           "1",
		`polyemesis_destination_stalled{id="2",name="Live"}`:                      "0",
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

// A destination is not down because the SOURCE is. With the ingest lost every
// destination's output freezes, the supervisor marks each process stalled, and
// before this the scrape turned that into up=0 on every running destination --
// so `polyemesis_destination_up == 0 and polyemesis_destination_enabled == 1`,
// the alert MONITORING.md says to write first, paged once per destination for
// what the programme's ingest series already say once -- its bitrate at 0, NOT
// polyemesis_ingest_up, which stays 1 while an SRT or RTMP listener waits for a
// publisher that went away (Engine.IngestLive). The hooks and alerts watchers
// have both refused that reading since they were written; the engine now
// decides it once (DestStatus.Stalled), and the scrape follows the engine.
func TestAStallCausedByALostIngestDoesNotTakeADestinationDown(t *testing.T) {
	d := engine.DestStatus{
		ID: 1, Name: "D-default", Enabled: true,
		// The process is frozen, as every one is with nothing arriving...
		Process: &supervisor.Status{
			State: supervisor.StateRunning, Stalled: true, StalledSec: 30,
			Progress: ffmpeg.Progress{OutTimeMS: 117_973},
		},
		// ...and the engine, seeing the source gone, did not call it the
		// destination's stall.
		Stalled: false,
	}
	out := metrics.Render(metrics.Snapshot{Destinations: []metrics.Destination{metricsDestination(d)}})
	for series, want := range map[string]string{
		`polyemesis_destination_up{id="1",name="D-default"}`:      "1",
		`polyemesis_destination_stalled{id="1",name="D-default"}`: "0",
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

// A DESTINATION NAMES ITS PROGRAMME ON THE SCRAPE. The slow-output query in
// MONITORING.md has to leave out a destination whose output stopped because
// its SOURCE did -- up stays 1 then, by design -- and that takes a join from
// the destination to its programme's ingest series. Destination and ingest
// series are both labelled id and name, but the ids are of different things,
// so without source_id there was nothing to join on, and the query paged once
// per destination for one lost ingest.
func TestTheScrapeNamesEachDestinationsProgramme(t *testing.T) {
	src := int64(4)
	out := metrics.Render(metrics.Snapshot{Destinations: []metrics.Destination{
		metricsDestination(engine.DestStatus{ID: 9, Name: "Twitch", Kind: "rtmp", Platform: "twitch", SourceID: &src}),
	}})
	want := `polyemesis_destination_info{id="9",name="Twitch",kind="rtmp",platform="twitch",source_id="4"} 1`
	if !strings.Contains(out, want+"\n") {
		t.Errorf("scrape is missing %s:\n%s", want, out)
	}
}
