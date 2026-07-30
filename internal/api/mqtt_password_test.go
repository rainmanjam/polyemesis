package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// The MQTT broker password shipped with storage, a reader, tests and a runner
// that used it -- and no way whatsoever to set it. An operator could enable
// MQTT and could not authenticate to a broker that required a password.
//
// This is the endpoint that closes that, and the assertions below are the ones
// that make it safe to have.
func TestMQTTPasswordCanBeSetAndCleared(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	put := func(pw string) int {
		r := jsonRequest(t, http.MethodPut, "/api/v1/settings/mqtt-password",
			map[string]string{"password": pw})
		sign(r)
		return do(t, h, r).Code
	}
	hasPassword := func() bool {
		r := jsonRequest(t, http.MethodGet, "/api/v1/settings", nil)
		sign(r)
		w := do(t, h, r)
		return strings.Contains(w.Body.String(), `"hasPassword":true`)
	}

	if hasPassword() {
		t.Fatal("a fresh install reports a stored MQTT password")
	}
	if code := put("s3cr3t-broker-password"); code != http.StatusOK {
		t.Fatalf("setting the password returned %d", code)
	}
	if !hasPassword() {
		t.Error("GET /settings does not report the password that was just stored")
	}

	// It must round-trip through the store, or the endpoint is a no-op that
	// returns 200.
	got, err := s.store.GetMQTTPassword(s.box)
	if err != nil || got != "s3cr3t-broker-password" {
		t.Errorf("stored password reads back as (%q, %v)", got, err)
	}

	// Clearing is how an operator moves to an anonymous broker without leaving
	// a stale credential behind to be offered to it.
	if code := put(""); code != http.StatusOK {
		t.Fatalf("clearing returned %d", code)
	}
	if hasPassword() {
		t.Error("the password survived being cleared")
	}
}

// The settings blob is served to every browser that opens Settings. This is the
// reason the password lives on its own route rather than as a field on it.
func TestTheMQTTPasswordNeverLeavesTheServer(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	const secret = "do-not-serve-this-anywhere"
	r := jsonRequest(t, http.MethodPut, "/api/v1/settings/mqtt-password",
		map[string]string{"password": secret})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("storing: %d", w.Code)
	}

	r = jsonRequest(t, http.MethodGet, "/api/v1/settings", nil)
	sign(r)
	body := do(t, h, r).Body.String()

	if strings.Contains(body, secret) {
		t.Errorf("GET /settings returned the broker password: %s", body)
	}
	// And no field is even shaped to carry it, so a future struct change cannot
	// start leaking one quietly.
	for _, bad := range []string{`"password"`, `"passwordEnc"`} {
		if strings.Contains(body, bad) {
			t.Errorf("GET /settings has a %s field in the MQTT block", bad)
		}
	}
	// The positive case: the flag the settings page actually needs is present.
	if !strings.Contains(body, `"hasPassword"`) {
		t.Error("GET /settings has no hasPassword flag, so the page cannot show that one is set")
	}
}
