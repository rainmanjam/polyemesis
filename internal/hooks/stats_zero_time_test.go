package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A DELIVERY THAT NEVER HAPPENED HAS NO TIMESTAMP, and the browser must not be
// handed one.
//
// Stats.LastSent carried `json:"lastSent,omitempty"`, which does nothing:
// encoding/json has no empty case for a struct, so the zero time went out as
// "0001-01-01T00:00:00Z" on every install that had delivered nothing. That is
// a non-empty string, so the automation page's `stats?.lastSent && ...` guard
// passed and rendered it through a local-time offset -- LAST DELIVERY
// 12/31/1, 16:07:02 -- beside six counters all reading 0.
//
// Asserted on the ENCODED BYTES rather than on a round-tripped struct: the
// defect is entirely in what goes on the wire, and unmarshalling back into a
// time.Time would turn the absent field and the zero field into the same
// answer, which is the very confusion being tested.
func TestStatsOmitsALastSentThatNeverHappened(t *testing.T) {
	b, err := json.Marshal(Stats{Sent: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "lastSent") {
		t.Errorf("a dispatcher that has sent nothing reports a lastSent, which the "+
			"automation page renders as a date in the year 1:\n  %s", b)
	}
	if strings.Contains(string(b), "0001-01-01") {
		t.Errorf("the zero time reached the wire:\n  %s", b)
	}
}

// The other half, and it is what stops the fix being "delete the field".
func TestStatsReportsALastSentThatDidHappen(t *testing.T) {
	at := time.Date(2026, 8, 26, 21, 15, 0, 0, time.UTC)
	b, err := json.Marshal(Stats{Sent: 3, LastSent: at})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"lastSent":"2026-08-26T21:15:00Z"`) {
		t.Errorf("a real delivery time is missing or reshaped:\n  %s", b)
	}
	// And the rest of the struct still marshals, which the embedded-alias trick
	// is the easiest thing in the world to get wrong.
	if !strings.Contains(string(b), `"sent":3`) {
		t.Errorf("the other counters were lost by the custom marshaller:\n  %s", b)
	}
}
