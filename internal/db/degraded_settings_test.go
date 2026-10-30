package db

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// The degraded-link thresholds (#6) ship off, with numbers already in them, and
// the defaults validate: an upgrade must not change a running pipeline, and an
// operator turning it on should not first have to design a form.
func TestDegradedLinkFailoverShipsOffWithThresholdsThatValidate(t *testing.T) {
	s := DefaultSettings()
	d := s.Ingest.Degraded
	if d.Enabled {
		t.Fatal("degraded-link failover is on by default; an upgrade would start switching sources nobody asked it to")
	}
	if d.MinBitrateKbps <= 0 || d.MaxRTTMs <= 0 || d.MaxLossPercent <= 0 {
		t.Errorf("default thresholds %+v leave a check off; turning the feature on should be one switch", d)
	}
	s.Failover.Enabled = true
	s.Ingest.Degraded.Enabled = true
	if err := s.Validate(); err != nil {
		t.Errorf("the defaults with degraded-link failover switched on do not validate: %v", err)
	}
}

// A settings row written before #6 existed decodes with a zero
// DegradedSettings. That has to stay saveable -- otherwise the first unrelated
// edit after an upgrade is refused over a field the operator has never seen --
// and its zero sustain has to resolve to the default rather than to "trip on
// the first bad sample".
func TestASettingsRowFromBeforeDegradedLinkFailoverStillValidates(t *testing.T) {
	s := DefaultSettings()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	// Strip the new block the way an old row simply lacks it.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m["ingest"].(map[string]any), "degraded")
	raw, _ = json.Marshal(m)
	var old Settings
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if err := old.Validate(); err != nil {
		t.Fatalf("a pre-#6 settings row no longer validates: %v", err)
	}
	if got := old.Ingest.Degraded.Sustain(); got != DefaultDegradedSustainSeconds {
		t.Errorf("a zero sustain resolved to %ds, want the default %ds", got, DefaultDegradedSustainSeconds)
	}
}

func TestDegradedLinkThresholdsRefuseWhatCannotMeanAnything(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*DegradedSettings)
		want string
	}{
		{"a negative bitrate", func(d *DegradedSettings) { d.MinBitrateKbps = -1 }, "bitrate threshold"},
		{"a bitrate no encoder sends", func(d *DegradedSettings) { d.MinBitrateKbps = MaxDegradedBitrateKbps + 1 }, "bitrate threshold"},
		{"a negative RTT", func(d *DegradedSettings) { d.MaxRTTMs = -5 }, "RTT threshold"},
		{"an RTT past ten seconds", func(d *DegradedSettings) { d.MaxRTTMs = MaxDegradedRTTMs + 1 }, "RTT threshold"},
		{"a loss over 100%", func(d *DegradedSettings) { d.MaxLossPercent = 100.5 }, "loss threshold"},
		{"a NaN loss", func(d *DegradedSettings) { d.MaxLossPercent = math.NaN() }, "loss threshold"},
		{"a negative sustain", func(d *DegradedSettings) { d.SustainSeconds = -1 }, "sustain time"},
		{"a sustain past five minutes", func(d *DegradedSettings) { d.SustainSeconds = MaxDegradedSustainSeconds + 1 }, "sustain time"},
		{"switched on with nothing to measure", func(d *DegradedSettings) {
			d.Enabled = true
			d.MinBitrateKbps, d.MaxRTTMs, d.MaxLossPercent = 0, 0, 0
		}, "every threshold is off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultSettings()
			tc.mut(&s.Ingest.Degraded)
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// A threshold set to zero is OFF, not "trip below zero": bitrate alone is a
// complete configuration, because it is the one check every ingest mode can
// make.
func TestEachDegradedLinkThresholdCanBeSwitchedOffAlone(t *testing.T) {
	s := DefaultSettings()
	s.Failover.Enabled = true
	s.Ingest.Degraded = DegradedSettings{Enabled: true, MinBitrateKbps: 500}
	if err := s.Validate(); err != nil {
		t.Errorf("bitrate-only thresholds do not validate: %v", err)
	}
}
