package metrics

import (
	"strings"
	"testing"
)

func TestPlayoutQoEIsOnTheScrape(t *testing.T) {
	lat := 4.5
	out := Render(Snapshot{Playout: &Playout{
		Viewers: 3, Beacons: 40, Untracked: 1, Startups: 3, StartupSeconds: 2.25,
		Rebuffers: 5, RebufferSeconds: 7.5,
		Errors:         map[string]int64{"network": 2},
		ErrorCodes:     []string{"network", "media"},
		Rejected:       map[string]int64{"rate_limited": 4},
		ActiveSessions: 2, ByVariant: map[string]int{"720p": 2, `bad"name`: 1},
		LatencySeconds: &lat,
		StartupBounds:  []float64{0.5, 1}, StartupBuckets: []int64{1, 2, 3},
		SessionsWithErrors: 2,
	}})
	for _, want := range []string{
		"polyemesis_playout_viewers 3\n",
		"polyemesis_playout_qoe_beacons_total 40\n",
		`polyemesis_playout_qoe_beacons_rejected_total{reason="invalid"} 0` + "\n",
		`polyemesis_playout_qoe_beacons_rejected_total{reason="rate_limited"} 4` + "\n",
		"polyemesis_playout_qoe_untracked_beacons_total 1\n",
		"polyemesis_playout_qoe_startups_total 3\n",
		"polyemesis_playout_qoe_startup_seconds_total 2.25\n",
		"polyemesis_playout_qoe_rebuffers_total 5\n",
		"polyemesis_playout_qoe_rebuffer_seconds_total 7.5\n",
		`polyemesis_playout_qoe_errors_total{code="network"} 2` + "\n",
		// Every code has a series before it first happens.
		`polyemesis_playout_qoe_errors_total{code="media"} 0` + "\n",
		"polyemesis_playout_qoe_sessions 2\n",
		`polyemesis_playout_qoe_variant_sessions{variant="720p"} 2` + "\n",
		`polyemesis_playout_qoe_variant_sessions{variant="bad\"name"} 1` + "\n",
		"polyemesis_playout_qoe_latency_seconds 4.5\n",
		`polyemesis_playout_qoe_startup_bucket_total{le="0.5"} 1` + "\n",
		`polyemesis_playout_qoe_startup_bucket_total{le="1"} 2` + "\n",
		`polyemesis_playout_qoe_startup_bucket_total{le="+Inf"} 3` + "\n",
		"polyemesis_playout_qoe_sessions_with_errors_total 2\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scrape is missing %q", want)
		}
	}
}

func TestPlayoutFamiliesAreOmittedWithoutAManagerAndLatencyWithoutReports(t *testing.T) {
	if out := Render(Snapshot{}); strings.Contains(out, "polyemesis_playout_") {
		t.Fatal("playout families published with no playout manager")
	}
	out := Render(Snapshot{Playout: &Playout{}})
	if strings.Contains(out, "\npolyemesis_playout_qoe_latency_seconds ") {
		t.Fatal("a latency sample was invented with no session reporting one")
	}
	if !strings.Contains(out, "# TYPE polyemesis_playout_qoe_latency_seconds gauge") {
		t.Fatal("the latency family header is missing")
	}
}
