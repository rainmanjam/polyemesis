package main

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func mqttCfg() db.MQTTSettings {
	return db.MQTTSettings{
		Enabled: true, BrokerURL: "mqtt://broker.example:1883", Username: "poly",
		Prefix: "polyemesis", Instance: "studio", KeepAliveSec: 30,
		IntervalSecond: 10, Discovery: true,
	}
}

// The signature decides whether a settings change reaches the live connection.
// A field that changes the connection and is absent from it means the runner
// keeps a stale link alive with nothing reporting it.
func TestEveryConnectionChangingFieldIsInTheMQTTSignature(t *testing.T) {
	base := mqttSig(mqttCfg(), "hunter22")
	for _, tc := range []struct {
		name string
		mut  func(*db.MQTTSettings)
		pw   string
	}{
		{"broker URL", func(c *db.MQTTSettings) { c.BrokerURL = "mqtt://other:1883" }, "hunter22"},
		{"username", func(c *db.MQTTSettings) { c.Username = "other" }, "hunter22"},
		{"prefix", func(c *db.MQTTSettings) { c.Prefix = "home/av" }, "hunter22"},
		{"instance", func(c *db.MQTTSettings) { c.Instance = "other" }, "hunter22"},
		{"client id", func(c *db.MQTTSettings) { c.ClientID = "explicit" }, "hunter22"},
		{"keep-alive", func(c *db.MQTTSettings) { c.KeepAliveSec = 60 }, "hunter22"},
		{"tls skip verify", func(c *db.MQTTSettings) { c.TLSSkipVerify = true }, "hunter22"},
		{"discovery", func(c *db.MQTTSettings) { c.Discovery = false }, "hunter22"},
		{"disabled", func(c *db.MQTTSettings) { c.Enabled = false }, "hunter22"},
		// The one a length-based signature cannot see, and the one operators
		// actually do: rotate to another password of the same shape.
		{"password, same length", func(*db.MQTTSettings) {}, "correct1"},
		{"password, different length", func(*db.MQTTSettings) {}, "a-much-longer-password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := mqttCfg()
			tc.mut(&c)
			if mqttSig(c, tc.pw) == base {
				t.Errorf("changing the %s does not change the signature, so the runner "+
					"keeps the old connection and the change never takes effect", tc.name)
			}
		})
	}
}

// The complement: an unrelated field must NOT cycle a healthy connection.
func TestAnUnrelatedSettingDoesNotCycleTheConnection(t *testing.T) {
	c := mqttCfg()
	before := mqttSig(c, "hunter22")
	// The publish interval changes the loop's ticker, not the link.
	c.IntervalSecond = 30
	if mqttSig(c, "hunter22") != before {
		t.Error("changing the publish interval reconnects to the broker, which drops " +
			"the retained-availability story for no reason")
	}
}
