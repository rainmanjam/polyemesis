package playout

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A VARIANT REFUSED ITS RELAY NAME STARTS NOTHING AND GIVES ITS PORT BACK. #711.
//
// Hub.Subscribe used to be a bare map assignment that could not fail, so this
// path did not exist. Now it can fail, and it has to take the same
// release-and-bail path the port refusal beside it already takes -- otherwise
// closing the name collision opens a port leak on the pool of 500 shared across
// every engine.
//
// The refusal is recorded rather than returned, exactly as a failed port
// allocation is: the other variants in the ladder go on running, and the
// operator is told which one is missing and why.
func TestAVariantRefusedItsRelayNameLeavesNoPortBehind(t *testing.T) {
	h := newHarness(t)

	// The incumbent: something already reading under the name this variant
	// will ask for. That is the only state in which a second subscribe is
	// wrong, and before #711 it was silently granted.
	name := "hd"
	if _, err := h.hub.Subscribe("playout:"+name, 20999); err != nil {
		t.Fatalf("the incumbent could not subscribe: %v", err)
	}
	before := h.ports.leaked()

	if err := h.Reconcile(baseSettings(db.PlayoutVariant{Name: name, Enabled: true}), h.resolve); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if n := len(h.spawns.all()); n != 0 {
		t.Errorf("%d variant process(es) were spawned despite the relay name being "+
			"taken. Each one runs with a correct command line, writes an empty "+
			"playlist, and receives no packets at all", n)
	}
	if got := h.ports.leaked(); got != before {
		t.Errorf("%d port(s) held, was %d: a variant that refuses to start must give "+
			"its port back, or every reconcile against a taken name burns one of the "+
			"500 shared across all engines", got, before)
	}
	if h.hub.count() != 1 {
		t.Errorf("the hub holds %d subscriber(s): the refusal disturbed the "+
			"incumbent, which is the outcome it exists to prevent", h.hub.count())
	}
}
