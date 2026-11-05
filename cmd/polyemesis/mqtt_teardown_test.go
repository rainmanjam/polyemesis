package main

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// #9 of the 2026-11-04 lingering-state audit: which reconnects clear what.
// Only a move of the topic tree retires it; a password, broker, keep-alive or
// discovery change keeps it, because the new connection is about to write the
// very same topics -- and docs/MQTT.md tells operators to toggle discovery to
// re-announce, which must not delete their Home Assistant entities.
func TestOnlyAMovedTopicTreeIsRetiredOnReconnect(t *testing.T) {
	r, store, ctx := reconcileFixture(t)
	enableMQTT(t, store, nil)
	r.reconcile(ctx)
	if c, _ := liveClient(r); c == nil {
		t.Fatal("no live client to reconnect from")
	}
	base := db.MQTTSettings{Enabled: true, BrokerURL: "mqtt://127.0.0.1:1", Prefix: "polyemesis",
		Instance: "studio", KeepAliveSec: 30, IntervalSecond: 10}
	for _, tc := range []struct {
		name   string
		mutate func(*db.MQTTSettings)
		want   teardownMode
	}{
		{"broker", func(m *db.MQTTSettings) { m.BrokerURL = "mqtt://127.0.0.1:2" }, teardownKeep},
		{"keep-alive", func(m *db.MQTTSettings) { m.KeepAliveSec = 60 }, teardownKeep},
		{"discovery toggle", func(m *db.MQTTSettings) { m.Discovery = !m.Discovery }, teardownKeep},
		{"prefix", func(m *db.MQTTSettings) { m.Prefix = "studio-tools" }, teardownRetire},
		{"instance", func(m *db.MQTTSettings) { m.Instance = "studio two" }, teardownRetireDevice},
	} {
		cfg := base
		tc.mutate(&cfg)
		if got := r.reconnectMode(cfg); got != tc.want {
			t.Errorf("%s change: teardown mode %d, want %d", tc.name, got, tc.want)
		}
	}
}
