package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// A destination whose sink stopped reading is running and connected, so its
// state cannot say anything is wrong. Before this its warnings were null for
// the whole stall (exploratory row 7); the card now says it is stalled.
func TestAStalledDestinationCarriesAWarning(t *testing.T) {
	p := &supervisor.Status{State: supervisor.StateRunning, Stalled: true, StalledSec: 42.7}
	ds := DestStatus{Process: p, Stalled: destinationStalled(p, true, true)}
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
		p                          *supervisor.Status
		ingestConfigured, arriving bool
		want                       bool
	}{
		"frozen, source arriving": {frozen, true, true, true},
		// With the source gone every output stops; the ingest is what is
		// wrong, and ingest.lost / polyemesis_ingest_up say so once.
		"frozen, source lost": {frozen, true, false, false},
		// No ingest to blame, so the process's own flag stands.
		"frozen, no ingest configured": {frozen, false, false, true},
		"no process":                   {nil, true, true, false},
		"delivering":                   {&supervisor.Status{State: supervisor.StateRunning}, true, true, false},
		"stopped with a stale flag":    {&supervisor.Status{State: supervisor.StateStopped, Stalled: true}, true, true, false},
	} {
		if got := destinationStalled(tc.p, tc.ingestConfigured, tc.arriving); got != tc.want {
			t.Errorf("%s: stalled = %v, want %v", name, got, tc.want)
		}
		ds := DestStatus{Process: tc.p, Stalled: destinationStalled(tc.p, tc.ingestConfigured, tc.arriving)}
		if w := stallWarning(ds); (w != "") != tc.want {
			t.Errorf("%s: warning = %q, want one only when stalled", name, w)
		}
	}
}
