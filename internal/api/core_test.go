package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The busiest routes in the product were the least covered ones: the dashboard
// reads /status, /system, /stats and /levels on a timer, the settings page
// round-trips the whole configuration blob, and every destination control goes
// through start/stop/restart. None of them had a test.
//
// These are deliberately about PROPERTIES rather than shapes -- that a settings
// round-trip preserves what was written, that a password change needs the old
// password, that reordering sticks. A shape assertion passes on a handler that
// returns a well-formed lie.

func TestSettingsRoundTripPreservesWhatWasWritten(t *testing.T) {
	// The Sources page once sent server-computed fields back in its PUT and
	// every save 400'd: the control flipped and silently reverted. Reading the
	// value back is the only assertion that would have caught it -- a 200 would
	// not have, and neither would a toast.
	h, _, sign := sourceServer(t)

	var before map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &before)

	rec, _ := before["recording"].(map[string]any)
	if rec == nil {
		t.Fatal("settings carried no recording block")
	}
	rec["segmentSeconds"] = 900

	send(t, h, sign, http.MethodPut, "/api/v1/settings", before, http.StatusOK)

	var after map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &after)
	got, _ := after["recording"].(map[string]any)
	if got == nil || got["segmentSeconds"] != float64(900) {
		t.Errorf("segmentSeconds did not persist: %v", got["segmentSeconds"])
	}
}

func TestSettingsPutRefusesAnInvalidConfiguration(t *testing.T) {
	// A settings blob that cannot start must be refused at the API, in front of
	// the operator. Accepting it means the next reconcile fails instead, which
	// surfaces as a child process that will not start rather than a form error.
	h, _, sign := sourceServer(t)

	var s map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &s)
	ing, _ := s["ingest"].(map[string]any)
	srt, _ := ing["srt"].(map[string]any)
	srt["port"] = 0 // binds a random ephemeral port and reports success

	send(t, h, sign, http.MethodPut, "/api/v1/settings", s, http.StatusBadRequest)
}

func TestChangingThePasswordNeedsTheCurrentOne(t *testing.T) {
	h, _, sign := sourceServer(t)

	send(t, h, sign, http.MethodPost, "/api/v1/auth/password",
		map[string]string{"current": "not-the-password", "new": "NewPassword!9xz"},
		http.StatusUnauthorized)

	// And the real one works, so the check above is not passing by refusing
	// everything.
	send(t, h, sign, http.MethodPost, "/api/v1/auth/password",
		map[string]string{"current": testPassword, "new": "NewPassword!9xz"},
		http.StatusOK)
}

func TestChangingThePasswordRefusesAWeakOne(t *testing.T) {
	h, _, sign := sourceServer(t)
	send(t, h, sign, http.MethodPost, "/api/v1/auth/password",
		map[string]string{"current": testPassword, "new": "short"},
		http.StatusBadRequest)
}

func TestSetupCannotBeReplayedOnceAnAdminExists(t *testing.T) {
	// An exposed port must not be a takeover. Asserted at the endpoint rather
	// than through the UI, because "the form is gone" is not the same guarantee
	// as "the endpoint refuses".
	h, _, _ := sourceServer(t)
	r := jsonRequest(t, http.MethodPost, "/api/v1/setup",
		map[string]string{"username": "intruder", "password": "Intruder!9xzq"})
	w := do(t, h, r)
	if w.Code < 400 {
		t.Fatalf("setup replayed with status %d; the install could be taken over", w.Code)
	}
}

func TestSetupStatusReportsThatSetupIsDone(t *testing.T) {
	// Unauthenticated on purpose: it is what the login page reads to decide
	// whether to show "create account" or "sign in".
	h, _, _ := sourceServer(t)
	r := jsonRequest(t, http.MethodGet, "/api/v1/setup", nil)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("setup status: %d", w.Code)
	}
	var st map[string]any
	decodeInto(t, w.Body.Bytes(), &st)
	if done, _ := st["configured"].(bool); !done {
		if needed, _ := st["needsSetup"].(bool); needed {
			t.Error("reports setup still needed after an admin exists; the UI would offer to create a second one")
		}
	}
}

func TestDashboardReadsAnswerOnAFreshInstall(t *testing.T) {
	// The dashboard polls all of these on a timer from first load, before
	// anything has ever streamed. A 500 on any of them is a broken page on the
	// very first thing a new user sees.
	h, _, sign := sourceServer(t)
	for _, p := range []string{
		"/api/v1/status",
		"/api/v1/system",
		"/api/v1/stats",
		"/api/v1/levels",
		"/api/v1/source",
		"/api/v1/loudness",
		"/api/v1/destinations",
		"/api/v1/recordings",
	} {
		t.Run(p, func(t *testing.T) {
			raw := send(t, h, sign, http.MethodGet, p, nil, http.StatusOK)
			if !json.Valid(raw) {
				t.Fatalf("%s returned invalid JSON: %s", p, raw)
			}
		})
	}
}

func TestMetricsIsPrometheusExposition(t *testing.T) {
	// Scraped by something that is not a browser and will not tolerate JSON.
	h, _, sign := sourceServer(t)
	raw := string(send(t, h, sign, http.MethodGet, "/api/v1/metrics", nil, http.StatusOK))
	if !strings.Contains(raw, "# HELP") || !strings.Contains(raw, "# TYPE") {
		t.Fatalf("no exposition headers; a scraper would reject this:\n%s", raw)
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		t.Fatal("metrics returned JSON")
	}
}

// ------------------------------------------------------------- destinations

// trackSel is a simple-mode selection with one track enabled.
func trackSel(on int) []map[string]any {
	rows := make([]map[string]any, 0, 6)
	for i := 0; i < 6; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": i == on, "gain": 1.0})
	}
	return rows
}

func makeDest(t *testing.T, h http.Handler, sign func(*http.Request), name string) int64 {
	t.Helper()
	var out map[string]any
	raw := send(t, h, sign, http.MethodPost, "/api/v1/destinations", map[string]any{
		"name": name, "kind": "file", "url": name + ".mkv",
		"enabled": false, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": trackSel(0), "matrix": []any{},
			"normalize": "off", "sampleRate": 48000,
		},
	}, http.StatusCreated)
	decodeInto(t, raw, &out)
	// The create response wraps the row the same way the list does.
	if d, ok := out["destination"].(map[string]any); ok {
		return int64(d["id"].(float64))
	}
	id, ok := out["id"].(float64)
	if !ok {
		t.Fatalf("create response carried no id: %s", raw)
	}
	return int64(id)
}

func TestDestinationLifecycleRoutesAnswer(t *testing.T) {
	// FFmpeg cannot exec in this fixture, so start/restart cannot actually put
	// a process on air. What they must still do is answer rather than panic or
	// hang -- these are the buttons on the busiest page in the UI.
	h, _, sign := sourceServer(t)
	id := strconv.FormatInt(makeDest(t, h, sign, "one"), 10)

	send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+id, nil, http.StatusOK)
	for _, action := range []string{"start", "stop", "restart"} {
		r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/"+id+"/"+action, nil)
		sign(r)
		w := do(t, h, r)
		if w.Code >= 500 {
			t.Errorf("%s returned %d: %s", action, w.Code, w.Body.String())
		}
	}
	send(t, h, sign, http.MethodDelete, "/api/v1/destinations/"+id, nil, http.StatusOK)
	send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+id, nil, http.StatusNotFound)
}

func TestReorderingDestinationsPersists(t *testing.T) {
	// The order is what the dashboard renders and what an operator arranges to
	// match their mental layout. A reorder that returns 200 and does not stick
	// is invisible until the page is reloaded.
	h, _, sign := sourceServer(t)
	a := makeDest(t, h, sign, "alpha")
	b := makeDest(t, h, sign, "bravo")

	send(t, h, sign, http.MethodPut, "/api/v1/destinations/order",
		map[string]any{"ids": []int64{b, a}}, http.StatusOK)

	// Each row arrives wrapped as {"destination": ..., "routing": ...} so the UI
	// gets the compiled routing without a second round trip.
	var rows []struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/destinations", nil, http.StatusOK), &rows)
	if len(rows) < 2 {
		t.Fatalf("expected 2 destinations, got %d", len(rows))
	}
	if first := rows[0].Destination.ID; first != b {
		t.Errorf("first destination id = %d, want %d; the reorder did not persist", first, b)
	}
}

// Bodies, not statuses alone: the SPA fallback answers an unrouted /api/v1/...
// path with its own 404 whenever the UI has not been built, which is how CI's Go
// job runs. See mustJSONError.
//
// Mutation: comment out `r.Delete("/destinations/{id}", s.handleDeleteDestination)`.
func TestDestinationRoutesRejectAnUnknownID(t *testing.T) {
	h, _, sign := sourceServer(t)
	mustJSONError(t, h, sign, http.MethodGet, "/api/v1/destinations/99999", nil, http.StatusNotFound)
	mustJSONError(t, h, sign, http.MethodDelete, "/api/v1/destinations/99999", nil, http.StatusNotFound)
	mustJSONError(t, h, sign, http.MethodGet, "/api/v1/destinations/abc", nil, http.StatusBadRequest)
}

func TestSwitchSourceIsRefusedWhenFailoverIsOff(t *testing.T) {
	// Failover is off by default, so there is no tier to switch. The operator
	// asking for something this configuration cannot do is a 400, not a 500 --
	// and certainly not a silent success that leaves them believing the slate
	// is on air.
	h, _, sign := sourceServer(t)
	send(t, h, sign, http.MethodPost, "/api/v1/failover/source",
		map[string]string{"source": "slate"}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPost, "/api/v1/failover/source",
		map[string]string{"source": "nonsense"}, http.StatusBadRequest)
}
