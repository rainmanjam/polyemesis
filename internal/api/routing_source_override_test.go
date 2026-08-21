package api

// Finding #9, poka-yoke audit 2026-08-21: handleCompileRouting and
// handleApplyPreset compiled against s.eng().Source() -- engines[0], correct
// only on a single-source install -- the same bug PR #478 fixed for four
// sibling handlers with engineForSource/sourceForDestination.
//
// The fix is routingSourceOverride, an optional ?destinationId= that resolves
// through the same engineForSource/sourceForDestination pair those four
// handlers use (proven correct against a genuine second engine by
// destination_source_scope_test.go), scoped by the destination's OWN
// SourceID rather than the caller's.
//
// WHAT THIS FILE CANNOT PROVE, stated because a fixture engine here is never
// probed: an unprobed engine's Source() is routing.DefaultSource() -- the same
// six-track placeholder -- REGARDLESS of which source it belongs to (see
// engine/silence.go's effectiveSourceKnown). Two different engines therefore
// answer with byte-identical routing.Source values in this fixture, so no
// HTTP response body can show "the second engine's layout, not the first's"
// by content. What IS observable, and what these tests pin, is that the
// parameter is actually consulted: a destinationId that does not resolve to a
// real row is refused rather than silently ignored, which is new behaviour
// the old bare s.eng().Source() call could not produce -- see the mutation
// note on each test.

import (
	"net/http"
	"strconv"
	"testing"
)

// TestRoutingCompileConsultsDestinationID pins that ?destinationId= is
// actually read rather than decorative.
//
// MUTATION: revert handleCompileRouting to call s.eng().Source() directly
// (dropping routingSourceOverride) and this fails -- an unknown or malformed
// destinationId would then be silently ignored and every case below would
// answer 200 instead of the status asserted.
func TestRoutingCompileConsultsDestinationID(t *testing.T) {
	h, _, sign := sourceServer(t)
	ownID := makeDest(t, h, sign, "owner")

	profile := map[string]any{
		"mode":   "simple",
		"tracks": []map[string]any{{"track": 0, "gain": 1, "enabled": true}},
	}

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"no destinationId: today's behaviour, unchanged", "", http.StatusOK},
		{"a real destination's id: resolves and compiles",
			"?destinationId=" + strconv.FormatInt(ownID, 10), http.StatusOK},
		{"an id naming no destination: refused, not silently defaulted",
			"?destinationId=999999999", http.StatusNotFound},
		{"a non-numeric id: refused as a bad request",
			"?destinationId=not-a-number", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodPost, "/api/v1/routing/compile"+tc.query,
				map[string]any{"profile": profile})
			sign(r)
			w := do(t, h, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestRoutingApplyPresetConsultsDestinationID is the same claim against
// handleApplyPreset, whose body is decoded directly as routing.PresetOpts --
// the reason destinationId travels as a query parameter and not a body field
// here as well; both handlers share the one device.
//
// MUTATION: revert handleApplyPreset to call s.eng().Source() directly and
// this fails the same way -- the two error cases would answer 200.
func TestRoutingApplyPresetConsultsDestinationID(t *testing.T) {
	h, _, sign := sourceServer(t)
	ownID := makeDest(t, h, sign, "owner")

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"no destinationId: today's behaviour, unchanged", "", http.StatusOK},
		{"a real destination's id: resolves and compiles",
			"?destinationId=" + strconv.FormatInt(ownID, 10), http.StatusOK},
		{"an id naming no destination: refused, not silently defaulted",
			"?destinationId=999999999", http.StatusNotFound},
		{"a non-numeric id: refused as a bad request",
			"?destinationId=not-a-number", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodPost, "/api/v1/routing/presets/mic-only"+tc.query, nil)
			sign(r)
			w := do(t, h, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
