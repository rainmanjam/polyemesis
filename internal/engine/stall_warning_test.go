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
	w := stallWarning(p, true)
	if !strings.Contains(w, "Stalled") || !strings.Contains(w, "42s") {
		t.Fatalf("warning = %q, want it to say stalled and for how long", w)
	}
}

func TestStallWarningStaysQuietWhenThereIsNothingToSay(t *testing.T) {
	for name, tc := range map[string]struct {
		p        *supervisor.Status
		arriving bool
	}{
		"no process": {nil, true},
		"delivering": {&supervisor.Status{State: supervisor.StateRunning}, true},
		// With the source gone every output stops; the ingest is what is wrong,
		// and a warning per destination would point at the platforms instead.
		"source lost": {&supervisor.Status{State: supervisor.StateRunning, Stalled: true, StalledSec: 30}, false},
	} {
		if w := stallWarning(tc.p, tc.arriving); w != "" {
			t.Errorf("%s: warning = %q, want none", name, w)
		}
	}
}
