package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The cautions are said where an operator reads the log for MQTT: on every
// connect. Cautions() alone is tested in internal/db; this is the wiring.
// The prefix is invalid on purpose, so mqtt.Connect refuses before any socket
// is opened -- the caution comes first regardless.
func TestConnectLogsTheMQTTCautions(t *testing.T) {
	var buf bytes.Buffer
	r := &mqttRunner{log: slog.New(slog.NewTextHandler(&buf, nil)), version: "test"}
	r.connect(context.Background(), db.MQTTSettings{
		Enabled:       true,
		BrokerURL:     "mqtt://127.0.0.1:1",
		TLSSkipVerify: true,
		Prefix:        "not#valid",
		Instance:      "caution-test",
	}, "", "sig")
	if !strings.Contains(buf.String(), "has no TLS") {
		t.Errorf("connect did not log the skip-verify caution:\n%s", buf.String())
	}
}
