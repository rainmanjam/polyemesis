package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

func automodServer(t *testing.T) (http.Handler, func(*http.Request)) {
	t.Helper()
	_, h, _ := testServer(t, config.Config{DataDir: t.TempDir()})
	return h, login(t, h)
}

// The matrix must arrive with availability DERIVED, not stored. A UI that
// cannot tell an impossible cell from an unticked one renders a switch that
// silently does nothing.
func TestAutomodMatrixCarriesAvailabilityAndReasons(t *testing.T) {
	h, auth := automodServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/automod/matrix", nil)
	r.RemoteAddr = "203.0.113.5:44444"
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var got struct {
		Cells []struct {
			Platform  string `json:"platform"`
			Action    string `json:"action"`
			Checker   string `json:"checker"`
			Auto      bool   `json:"auto"`
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
		} `json:"cells"`
		Actions   []string `json:"actions"`
		Checkers  []string `json:"checkers"`
		Platforms []string `json:"platforms"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := len(got.Actions) * len(got.Checkers) * len(got.Platforms)
	if want == 0 || len(got.Cells) != want {
		t.Fatalf("got %d cells, want %d (%d actions x %d checkers x %d platforms)",
			len(got.Cells), want, len(got.Actions), len(got.Checkers), len(got.Platforms))
	}
	// Upstream hide is Facebook-only, and the unavailable cells must say why.
	for _, c := range got.Cells {
		if c.Action != "hide" || c.Platform == "facebook" {
			continue
		}
		if c.Available {
			t.Fatalf("%s claims an upstream hide it does not have", c.Platform)
		}
		if c.Reason == "" {
			t.Fatalf("%s gives no reason for an unavailable cell", c.Platform)
		}
	}
}

// A fresh install must have nothing automatic beyond flagging.
func TestAutomodMatrixDefaultsToNothingAutomatic(t *testing.T) {
	h, auth := automodServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/automod/matrix", nil)
	r.RemoteAddr = "203.0.113.5:44444"
	auth(r)
	w := do(t, h, r)

	var got struct {
		Summary map[string]int `json:"summary"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	for p, n := range got.Summary {
		if n != 0 {
			t.Fatalf("%s starts with %d automatic actions, want 0", p, n)
		}
	}
}

// The key is sealed and must never come back out through the settings blob.
func TestAutomodKeyIsSetButNeverReturned(t *testing.T) {
	const secret = "sk-automod-do-not-leak"
	h, auth := automodServer(t)

	put := jsonRequest(t, http.MethodPut, "/api/v1/settings/automod-key",
		map[string]any{"key": secret})
	auth(put)
	if w := do(t, h, put); w.Code != http.StatusOK {
		t.Fatalf("set key: status %d, body %s", w.Code, w.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	get.RemoteAddr = "203.0.113.5:44444"
	auth(get)
	w := do(t, h, get)
	body := w.Body.String()
	if strings.Contains(body, secret) {
		t.Fatal("the model API key was returned by GET /settings")
	}
	if !strings.Contains(body, `"hasApiKey":true`) {
		t.Fatalf("hasApiKey was not reported as set: %s", body)
	}

	// Clearing it must take the flag back down, so an operator can tell.
	clear := jsonRequest(t, http.MethodPut, "/api/v1/settings/automod-key",
		map[string]any{"key": ""})
	auth(clear)
	do(t, h, clear)
	get2 := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	get2.RemoteAddr = "203.0.113.5:44444"
	auth(get2)
	if !strings.Contains(do(t, h, get2).Body.String(), `"hasApiKey":false`) {
		t.Fatal("clearing the key did not clear the flag")
	}
}

// Automod config rides inside Settings, so it must survive a round trip.
//
// Uses sourceServer rather than the bare testServer, because PUT /settings
// reconciles against a running engine and the minimal harness has none.
func TestAutomodSettingsRoundTrip(t *testing.T) {
	h, _, sign := sourceServer(t)

	var before map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &before)

	am, ok := before["automod"].(map[string]any)
	if !ok {
		t.Fatal("settings carried no automod block")
	}
	am["enabled"] = true
	am["on"] = map[string]any{"twitch/delete/rules": true}

	send(t, h, sign, http.MethodPut, "/api/v1/settings", before, http.StatusOK)

	var got struct {
		Summary map[string]int `json:"summary"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/automod/matrix", nil, http.StatusOK), &got)
	if got.Summary["twitch"] != 1 {
		t.Fatalf("the stored cell did not survive the round trip: %+v", got.Summary)
	}
	// And arming one platform must not arm another.
	if got.Summary["kick"] != 0 {
		t.Fatalf("enabling a Twitch cell also armed Kick: %+v", got.Summary)
	}
}

// A stored key from a newer version must be DROPPED rather than misread as an
// action this build happens to know.
func TestAnUnknownStoredCellIsIgnored(t *testing.T) {
	h, _, sign := sourceServer(t)

	var before map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &before)
	am := before["automod"].(map[string]any)
	am["enabled"] = true
	am["on"] = map[string]any{
		"twitch/teleport/rules":   true,
		"myspace/delete/rules":    true,
		"twitch/delete/astrology": true,
	}
	send(t, h, sign, http.MethodPut, "/api/v1/settings", before, http.StatusOK)

	var got struct {
		Summary map[string]int `json:"summary"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/automod/matrix", nil, http.StatusOK), &got)
	for p, n := range got.Summary {
		if n != 0 {
			t.Fatalf("an unrecognised cell became an automatic action on %s: %+v", p, got.Summary)
		}
	}
}

// A rule that does not compile must DEGRADE automod, not stop chat. Refusing to
// moderate at all because one regex is malformed is the wrong trade in both
// directions: the operator loses the working rules AND the history detectors.
func TestABadRuleDoesNotStopAutomod(t *testing.T) {
	h, _, sign := sourceServer(t)

	var before map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &before)
	am := before["automod"].(map[string]any)
	am["enabled"] = true
	am["rules"] = []map[string]any{
		{"id": 1, "name": "fine", "enabled": true, "pattern": "spam", "action": "delete"},
		{"id": 2, "name": "broken", "enabled": true, "pattern": "([unclosed", "action": "delete"},
	}
	// The save must still succeed: the setting is stored, and the engine is
	// rebuilt without the rules rather than the request failing.
	send(t, h, sign, http.MethodPut, "/api/v1/settings", before, http.StatusOK)

	// And the matrix still answers, which is the proof automod is still running.
	var got struct {
		Enabled bool `json:"enabled"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/automod/matrix", nil, http.StatusOK), &got)
	if !got.Enabled {
		t.Fatal("automod switched itself off because one rule would not compile")
	}
}
