package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// PUT /destinations/{id} says how the save took effect only when there is an
// honest answer. E3 (the 2026-11-04 demo) was a music-rights save that raised
// "is restarting with the new mix" while the engine kept the process; the
// answer now comes from Engine.AppliedSince, whose live and restart cases are
// pinned in internal/engine (TestAppliedSinceReadsTheProcessNotTheChange),
// where a running child can be stood in. This fixture's destinations never run
// -- FFmpeg cannot exec and every destination is held awaiting the first ingest
// probe -- which is exactly the case asserted here: nothing running, so the
// answer is neither "live" nor "restart", and the key is absent.
//
// MUTATION: in destinationApplied, answer "restart" when the engine has no
// answer (the page's old guess). Observed: FAIL, applied = "restart" on a
// destination with no process.
func TestDestinationPutClaimsNoRestartForADestinationNotRunning(t *testing.T) {
	_, h, _, sign := engineServer(t, defaultTools(), Options{})
	id := makeDest(t, h, sign, "put-applied")
	path := "/api/v1/destinations/" + itoa(id)

	put := func(over map[string]any) map[string]any {
		t.Helper()
		p := map[string]any{
			"mode": "simple", "tracks": trackSel(0), "matrix": []any{},
			"normalize": "off", "sampleRate": 48000,
		}
		for k, v := range over {
			p[k] = v
		}
		raw := send(t, h, sign, http.MethodPut, path, map[string]any{"profile": p}, http.StatusOK)
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("PUT answered non-JSON: %v: %s", err, raw)
		}
		if _, ok := out["destination"]; !ok {
			t.Fatalf("PUT answered without the destination: %s", raw)
		}
		return out
	}

	// Disabled: created that way.
	if out := put(map[string]any{"excludeRoles": []string{"music"}}); out["applied"] != nil {
		t.Errorf("a save on a disabled destination answered applied=%v (reason %v); want no applied key",
			out["applied"], out["reason"])
	}

	// Enabled, and held: it has no process until the ingest is probed.
	send(t, h, sign, http.MethodPost, path+"/start", nil, http.StatusOK)
	if out := put(map[string]any{"normalize": "limiter"}); out["applied"] != nil {
		t.Errorf("a save on a destination with no process answered applied=%v (reason %v); want no applied key",
			out["applied"], out["reason"])
	}
}
