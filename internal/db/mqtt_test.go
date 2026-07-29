package db

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/mqtt"
)

// DefaultSettings writes these two values as literals rather than importing
// internal/mqtt, because that import would link paho into the database layer.
// This test is the price of that decision: it is a test-only import, so it
// keeps the two in step without appearing in the production build graph.
func TestMQTTDefaultsMatchTheMQTTPackage(t *testing.T) {
	d := DefaultSettings().MQTT
	if d.Prefix != mqtt.DefaultPrefix {
		t.Errorf("default MQTT prefix is %q but mqtt.DefaultPrefix is %q; the duplicated literal has drifted",
			d.Prefix, mqtt.DefaultPrefix)
	}
	if d.KeepAliveSec != mqtt.DefaultKeepAliveSec {
		t.Errorf("default keep-alive is %d but mqtt.DefaultKeepAliveSec is %d; the duplicated literal has drifted",
			d.KeepAliveSec, mqtt.DefaultKeepAliveSec)
	}
}

// A fresh install must not be publishing anywhere, and must be one field away
// from working when the operator decides it should.
func TestMQTTIsOffByDefaultAndValid(t *testing.T) {
	s := DefaultSettings()
	if s.MQTT.Enabled {
		t.Error("MQTT is enabled on a fresh install; an upgrade must not start publishing to a broker nobody configured")
	}
	if err := s.Validate(); err != nil {
		t.Errorf("default settings do not validate: %v", err)
	}
	// Switching it on with only a broker URL must be enough.
	s.MQTT.Enabled = true
	s.MQTT.BrokerURL = "mqtt://broker.example:1883"
	if err := s.Validate(); err != nil {
		t.Errorf("enabling MQTT with only a broker URL does not validate: %v", err)
	}
}

func TestMQTTValidationRefusesWhatWouldNotWork(t *testing.T) {
	base := func() MQTTSettings {
		m := DefaultSettings().MQTT
		m.Enabled = true
		m.BrokerURL = "mqtt://broker.example:1883"
		return m
	}
	cases := []struct {
		name string
		mut  func(*MQTTSettings)
		want string
	}{
		{"no broker", func(m *MQTTSettings) { m.BrokerURL = "" }, "no broker URL"},
		{"unparseable broker", func(m *MQTTSettings) { m.BrokerURL = "://" }, "unparseable"},
		{"hostless broker", func(m *MQTTSettings) { m.BrokerURL = "mqtt://" }, "no host"},
		{"wrong scheme", func(m *MQTTSettings) { m.BrokerURL = "rtmp://host" }, "not one of mqtt"},
		{"credentials in the URL", func(m *MQTTSettings) { m.BrokerURL = "mqtt://user:pw@host:1883" }, "carries credentials"},
		{"dollar prefix", func(m *MQTTSettings) { m.Prefix = "$SYS" }, "must not begin with $"},
		{"wildcard prefix", func(m *MQTTSettings) { m.Prefix = "home/+" }, "wildcard or NUL"},
		{"interval too small", func(m *MQTTSettings) { m.IntervalSecond = 0 }, "publish interval"},
		{"interval too large", func(m *MQTTSettings) { m.IntervalSecond = 99999 }, "publish interval"},
		{"keep-alive of zero", func(m *MQTTSettings) { m.KeepAliveSec = 0 }, "keep-alive"},
		{"overlong prefix", func(m *MQTTSettings) { m.Prefix = strings.Repeat("a", MaxMQTTPrefixLength+1) }, "topic prefix is"},
		{"overlong instance", func(m *MQTTSettings) { m.Instance = strings.Repeat("a", MaxMQTTInstanceLength+1) }, "instance name is"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := base()
			c.mut(&m)
			probs := m.problems()
			if len(probs) == 0 {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(strings.Join(probs, "; "), c.want) {
				t.Errorf("problems were %v, want one mentioning %q", probs, c.want)
			}
		})
	}
}

// A half-configured block an operator is still filling in must not block saving
// an unrelated setting. Without this the whole settings page becomes unsavable
// the moment somebody ticks the box and starts typing.
func TestADisabledMQTTBlockIsNeverValidated(t *testing.T) {
	m := MQTTSettings{Enabled: false, BrokerURL: "not a url at all", Prefix: "$SYS", IntervalSecond: -5}
	if probs := m.problems(); len(probs) != 0 {
		t.Errorf("a disabled MQTT block reported %v; nothing about it can misbehave", probs)
	}
}

func TestMQTTPasswordIsSealedAndClearable(t *testing.T) {
	d, box := testDB(t), testBox(t)

	if pw, err := d.GetMQTTPassword(box); err != nil || pw != "" {
		t.Fatalf("a fresh install returned (%q, %v), want (\"\", nil); an anonymous broker is a normal deployment", pw, err)
	}
	if has, err := d.HasMQTTPassword(); err != nil || has {
		t.Fatalf("HasMQTTPassword on a fresh install = (%v, %v), want (false, nil)", has, err)
	}

	const secret = "s3cr3t-broker-password"
	if err := d.PutMQTTPassword(box, secret); err != nil {
		t.Fatalf("PutMQTTPassword: %v", err)
	}
	got, err := d.GetMQTTPassword(box)
	if err != nil || got != secret {
		t.Fatalf("GetMQTTPassword = (%q, %v), want the password back", got, err)
	}
	if has, _ := d.HasMQTTPassword(); !has {
		t.Error("HasMQTTPassword = false after storing one")
	}

	// The stored bytes must not be the password. Without this the round trip
	// above would pass just as happily against a column holding plaintext.
	var raw []byte
	if err := d.sql.QueryRow(`SELECT password_enc FROM mqtt_creds WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatalf("reading the raw column: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Error("the broker password is stored in plaintext")
	}

	// Clearing it is how an operator moves to an anonymous broker.
	if err := d.PutMQTTPassword(box, ""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if has, _ := d.HasMQTTPassword(); has {
		t.Error("the password survived being cleared; a stale credential would keep being sent to the new broker")
	}
}

// The settings blob is served to the settings page. A password in it would be
// handed to every browser that opened Settings.
func TestMQTTSettingsCarryNoPasswordField(t *testing.T) {
	raw, err := json.Marshal(DefaultSettings().MQTT)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, bad := range []string{"password\":", "passwordEnc", "secret"} {
		if strings.Contains(body, bad) {
			t.Errorf("the serialised MQTT settings contain %q: %s", bad, body)
		}
	}
	if !strings.Contains(body, "hasPassword") {
		t.Error("the serialised MQTT settings have no hasPassword flag, so the page cannot show that one is set")
	}
}
