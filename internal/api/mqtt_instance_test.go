package api

import (
	"net/http"
	"regexp"
	"testing"
)

// Two installs on one broker with the default instance shared a client id and
// a topic tree (poka-yoke audit 2026-11-04). Switching MQTT on with the default
// now gives this install an instance of its own; an install already publishing
// keeps its topics where its subscribers expect them.
func TestFirstEnablingMQTTGivesThisInstallItsOwnInstance(t *testing.T) {
	h, _, sign := sourceServer(t)
	mqttWith := func(edit func(m map[string]any)) map[string]any {
		var doc map[string]any
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
		edit(doc["mqtt"].(map[string]any))
		return doc
	}
	instance := func() string {
		var doc map[string]any
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
		return doc["mqtt"].(map[string]any)["instance"].(string)
	}
	if got := instance(); got != "polyemesis" {
		t.Fatalf("a fresh install's instance is %q, want the default", got)
	}

	send(t, h, sign, http.MethodPut, "/api/v1/settings", mqttWith(func(m map[string]any) {
		m["enabled"] = true
		m["brokerUrl"] = "mqtt://127.0.0.1:1883"
	}), http.StatusOK)
	first := instance()
	if !regexp.MustCompile(`^polyemesis-[0-9a-f]{6}$`).MatchString(first) {
		t.Fatalf("instance after first enabling MQTT = %q, want polyemesis-<6 hex>", first)
	}

	// Already on: whatever the operator sets now is kept, the default included.
	send(t, h, sign, http.MethodPut, "/api/v1/settings", mqttWith(func(m map[string]any) {
		m["instance"] = "polyemesis"
	}), http.StatusOK)
	if got := instance(); got != "polyemesis" {
		t.Errorf("an enabled install's chosen instance was rewritten to %q", got)
	}
}
