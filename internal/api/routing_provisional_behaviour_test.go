package api

import (
	"net/http"
	"testing"
)

// The provisional flag is asserted through the API, not read out of the source.
//
// The companion drift test pins that every preview path uses SourceKnown, which
// is worth having but is not the same claim: it proves the code was written, not
// that a client sees the field. This drives the real handler and reads the JSON.
//
// The engine here has never probed anything, which is the state the flag exists
// for -- a graph compiled from routing.DefaultSource(), six stereo tracks that
// exist so the editor has something to draw, that reconcileOutputs refuses to
// run. Before this the operator was handed that graph unlabelled.
func TestAnUnprobedEngineMarksItsRoutingPreviewProvisional(t *testing.T) {
	h, _, sign := sourceServer(t)
	makeDest(t, h, sign, "alpha")

	var rows []map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/destinations", nil, http.StatusOK), &rows)
	if len(rows) == 0 {
		t.Fatal("no destinations returned; nothing to assert about")
	}

	row := rows[0]
	// The preview is still THERE. Withholding it would break configuring a
	// destination before going live, which is when most people configure them --
	// the reasoning refuseIfSilent sets out directly above these handlers.
	if _, ok := row["routing"]; !ok {
		if e, bad := row["routingError"]; bad {
			t.Fatalf("the preview was refused rather than flagged: %v", e)
		}
		t.Fatal("no compiled routing in the response at all")
	}
	prov, ok := row["routingProvisional"]
	if !ok {
		t.Fatal("a routing graph compiled from the unprobed placeholder was returned " +
			"with no routingProvisional flag. The engine will not run that graph; " +
			"presenting it unlabelled makes the placeholder look authoritative and " +
			"puts the operator's screen at odds with the running process")
	}
	if prov != true {
		t.Errorf("routingProvisional = %v, want true on an unprobed engine", prov)
	}
}

// And the single-destination path, which is a separate handler with its own
// copy of the decision -- the kind that gets updated in one place and not the
// other.
func TestFetchingOneDestinationAlsoMarksItProvisional(t *testing.T) {
	h, _, sign := sourceServer(t)
	id := makeDest(t, h, sign, "bravo")

	var one map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet,
		"/api/v1/destinations/"+itoa(id), nil, http.StatusOK), &one)

	if _, ok := one["routing"]; !ok {
		t.Fatal("no compiled routing on the single-destination path")
	}
	if one["routingProvisional"] != true {
		t.Errorf("routingProvisional = %v on GET /destinations/{id}, want true; the "+
			"list path flags it and this one must agree", one["routingProvisional"])
	}
}
