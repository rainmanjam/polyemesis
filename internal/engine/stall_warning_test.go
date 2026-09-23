package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/stats"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// A destination whose sink stopped reading is running and connected, so its
// state cannot say anything is wrong. Before this its warnings were null for
// the whole stall (exploratory row 7); the card now says it is stalled.
func TestAStalledDestinationCarriesAWarning(t *testing.T) {
	p := &supervisor.Status{State: supervisor.StateRunning, Stalled: true, StalledSec: 42.7}
	ds := DestStatus{Process: p, Stalled: destinationStalled(p, true, time.Hour)}
	w := stallWarning(ds)
	if !strings.Contains(w, "Stalled") || !strings.Contains(w, "42s") {
		t.Fatalf("warning = %q, want it to say stalled and for how long", w)
	}
}

// DestStatus.Stalled is the one verdict both the card's warning and
// polyemesis_destination_up read, so this table is where "a lost source is not
// a destination stalling" is decided for both. Before it, the scrape read the
// supervisor's per-process flag and put up=0 on every destination whenever the
// ingest dropped, paging once per destination for one lost source.
func TestAStallIsTheDestinationsOnlyWhileTheSourceIsArriving(t *testing.T) {
	frozen := &supervisor.Status{State: supervisor.StateRunning, Stalled: true, StalledSec: 30}
	for name, tc := range map[string]struct {
		p                *supervisor.Status
		ingestConfigured bool
		sourceLive       time.Duration
		want             bool
	}{
		"frozen, source arriving": {frozen, true, time.Hour, true},
		// With the source gone every output stops; the ingest is what is
		// wrong, and ingest.lost / a zero ingest bitrate say so once.
		"frozen, source lost": {frozen, true, 0, false},
		// Back a moment ago: the process's stall began with the outage, and
		// the destination has not yet had StallAfter of source to move on.
		"frozen, source just returned":   {frozen, true, supervisor.StallAfter - time.Second, false},
		"frozen, source back StallAfter": {frozen, true, supervisor.StallAfter, true},
		// No ingest to blame, so the process's own flag stands.
		"frozen, no ingest configured": {frozen, false, 0, true},
		"no process":                   {nil, true, time.Hour, false},
		"delivering":                   {&supervisor.Status{State: supervisor.StateRunning}, true, time.Hour, false},
		"stopped with a stale flag":    {&supervisor.Status{State: supervisor.StateStopped, Stalled: true}, true, time.Hour, false},
	} {
		if got := destinationStalled(tc.p, tc.ingestConfigured, tc.sourceLive); got != tc.want {
			t.Errorf("%s: stalled = %v, want %v", name, got, tc.want)
		}
		ds := DestStatus{Process: tc.p, Stalled: destinationStalled(tc.p, tc.ingestConfigured, tc.sourceLive)}
		if w := stallWarning(ds); (w != "") != tc.want {
			t.Errorf("%s: warning = %q, want one only when stalled", name, w)
		}
	}
}

// How long the primary has been arriving is measured from the start of the
// CURRENT run of live samples, not the first live sample in the history: a
// source that dropped and came back has been back only since it came back.
func TestSourceLiveForCountsOnlyTheCurrentRun(t *testing.T) {
	now := time.Now()
	at := func(ago time.Duration, kbps float64) stats.Sample {
		return stats.Sample{Time: now.Add(-ago), Kbps: kbps}
	}
	for name, tc := range map[string]struct {
		samples []stats.Sample
		want    time.Duration
	}{
		"never live":         {nil, 0},
		"live throughout":    {[]stats.Sample{at(9*time.Second, 900), at(5*time.Second, 900), at(time.Second, 900)}, 9 * time.Second},
		"back after a drop":  {[]stats.Sample{at(9*time.Second, 900), at(3*time.Second, 0), at(2*time.Second, 900), at(time.Second, 900)}, 2 * time.Second},
		"dropped just now":   {[]stats.Sample{at(9*time.Second, 900), at(time.Second, 0)}, 0},
		"last sample is old": {[]stats.Sample{at(time.Minute, 900)}, 0},
	} {
		if got := sourceLiveFor(tc.samples, nil, now); got != tc.want {
			t.Errorf("%s: live for %v, want %v", name, got, tc.want)
		}
	}
}
