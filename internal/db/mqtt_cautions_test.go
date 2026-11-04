package db

import (
	"strings"
	"testing"
)

// Plaintext MQTT with credentials to a remote host, and "accept self-signed"
// on a scheme with no TLS, used to pass without a word (poka-yoke audit
// 2026-11-04). They are cautions, not refusals: plain mqtt:// on a LAN is the
// ordinary Home Assistant setup.
func TestMQTTCautions(t *testing.T) {
	base := MQTTSettings{Enabled: true, Username: "ha", HasPassword: true}
	for _, tc := range []struct {
		name string
		edit func(*MQTTSettings)
		want []string // substrings, one per expected caution
	}{
		{"plaintext with credentials to a remote host", func(m *MQTTSettings) { m.BrokerURL = "mqtt://broker.example.com:1883" }, []string{"unencrypted"}},
		{"tcp counts as plaintext", func(m *MQTTSettings) { m.BrokerURL = "tcp://broker.example.com:1883" }, []string{"unencrypted"}},
		{"ws counts as plaintext", func(m *MQTTSettings) { m.BrokerURL = "ws://broker.example.com/mqtt" }, []string{"unencrypted"}},
		{"a LAN broker is the normal case", func(m *MQTTSettings) { m.BrokerURL = "mqtt://192.168.1.10:1883" }, nil},
		{"loopback", func(m *MQTTSettings) { m.BrokerURL = "mqtt://127.0.0.1:1883" }, nil},
		{"localhost", func(m *MQTTSettings) { m.BrokerURL = "mqtt://localhost:1883" }, nil},
		{"TLS to a remote host", func(m *MQTTSettings) { m.BrokerURL = "mqtts://broker.example.com:8883" }, nil},
		{"no credentials", func(m *MQTTSettings) {
			m.BrokerURL = "mqtt://broker.example.com:1883"
			m.Username, m.HasPassword = "", false
		}, nil},
		{"skip-verify on a plain scheme does nothing", func(m *MQTTSettings) {
			m.BrokerURL = "mqtt://192.168.1.10:1883"
			m.TLSSkipVerify = true
		}, []string{"no TLS"}},
		{"skip-verify on TLS is meaningful", func(m *MQTTSettings) {
			m.BrokerURL = "mqtts://broker.example.com:8883"
			m.TLSSkipVerify = true
		}, nil},
		{"disabled says nothing", func(m *MQTTSettings) {
			m.BrokerURL = "mqtt://broker.example.com:1883"
			m.Enabled = false
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			tc.edit(&m)
			got := m.Cautions()
			if len(got) != len(tc.want) {
				t.Fatalf("Cautions() = %q, want %d caution(s)", got, len(tc.want))
			}
			for i, w := range tc.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("caution %q does not say %q", got[i], w)
				}
			}
		})
	}
}
