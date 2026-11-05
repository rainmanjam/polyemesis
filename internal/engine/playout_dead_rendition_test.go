package engine

import (
	"strings"
	"testing"
)

// #11 of the 2026-11-04 lingering-state audit. A playout rung naming a deleted
// rendition used to report "is not running", which sends the operator looking
// for why it will not start. It never will; the message says so and where to
// fix it. A rendition that exists and is merely not running keeps the old text.
func TestAPlayoutRungOnADeletedRenditionSaysItIsGone(t *testing.T) {
	e, _ := storeEngine(t)
	t.Cleanup(e.Stop)
	gone := int64(424242)
	_, err := e.playoutUpstream(&gone)
	if err == nil || !strings.Contains(err.Error(), "no longer exists") ||
		!strings.Contains(err.Error(), "Playout page") {
		t.Fatalf("a rung on a deleted rendition reported %v", err)
	}
}
