package api

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/metrics"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// NO STREAM KEY REACHES A LABEL. A refused publish is explained by the very
// stderr line that names the publish URL, key included, and that line rides on
// DestStatus.Reason. The scrape copies the cause CODE and the hold flag and
// nothing else, so a scrape -- which every Prometheus and Grafana downstream
// stores -- never holds the key, even if the line was somehow not scrubbed.
func TestTheScrapeCarriesTheCauseAndNeverTheStderrLineOrTheKey(t *testing.T) {
	const key = "live_8812_SECRETKEYabcdef"
	failed := engine.DestStatus{
		ID: 9, Name: "YouTube", Enabled: true,
		Process: &supervisor.Status{
			State:    supervisor.StateFailed,
			Restarts: 3,
			Failures: map[failreason.Cause]int{failreason.KeyRejected: 3},
			Reason: &failreason.Reason{
				Cause: failreason.KeyRejected,
				Line:  "[rtmp @ 0x1] Error opening output rtmp://a.rtmp.youtube.com/live2/" + key + ": I/O error",
				Held:  true, Quick: true, Rejections: 3, At: time.Now(),
			},
		},
	}
	// The engine's verdict, which is what the scrape reads; same unscrubbed
	// line, so this test does not lean on the supervisor's scrubbing.
	failed.Reason = failed.Process.Reason

	out := metrics.Render(metrics.Snapshot{
		Destinations: []metrics.Destination{metricsDestination(failed)},
	})

	for _, leak := range []string{key, "rtmp://", "Error opening output"} {
		if strings.Contains(out, leak) {
			t.Fatalf("the scrape carries %q from the failure line:\n%s", leak, out)
		}
	}
	for series, want := range map[string]string{
		`polyemesis_destination_failure_reason{id="9",name="YouTube",cause="key_rejected"}`: "1",
		`polyemesis_destination_held{id="9",name="YouTube"}`:                                "1",
		`polyemesis_destination_failures_total{id="9",name="YouTube",cause="key_rejected"}`: "3",
	} {
		if !strings.Contains(out, series+" "+want+"\n") {
			t.Errorf("want %s %s in the scrape:\n%s", series, want, out)
		}
	}

	known := map[string]bool{}
	for _, c := range failreason.AllCauses() {
		known[string(c)] = true
	}
	for _, m := range regexp.MustCompile(`cause="([^"]*)"`).FindAllStringSubmatch(out, -1) {
		if !known[m[1]] {
			t.Errorf("cause label %q is not a published cause", m[1])
		}
	}
	// No label value anywhere looks like a URL: the only free-text labels are
	// names the operator typed.
	for _, m := range regexp.MustCompile(`="([^"]*)"`).FindAllStringSubmatch(out, -1) {
		if strings.Contains(m[1], "://") {
			t.Errorf("label value %q looks like a URL", m[1])
		}
	}
}

// A destination that has never failed, and one with no process at all, read
// zero everywhere -- not absent.
func TestAHealthyDestinationReadsZeroOnEveryCause(t *testing.T) {
	out := metrics.Render(metrics.Snapshot{
		Destinations: []metrics.Destination{
			metricsDestination(engine.DestStatus{ID: 1, Name: "Live", Process: &supervisor.Status{State: supervisor.StateRunning}}),
			metricsDestination(engine.DestStatus{ID: 2, Name: "Idle"}),
		},
	})
	for _, line := range strings.Split(out, "\n") {
		if (strings.HasPrefix(line, "polyemesis_destination_failure") || strings.HasPrefix(line, "polyemesis_destination_held")) &&
			!strings.HasSuffix(line, " 0") {
			t.Errorf("healthy destination: %s", line)
		}
	}
	if n := strings.Count(out, "polyemesis_destination_failures_total{"); n != 2*len(failreason.AllCauses()) {
		t.Errorf("failures_total series = %d, want %d", n, 2*len(failreason.AllCauses()))
	}
}
