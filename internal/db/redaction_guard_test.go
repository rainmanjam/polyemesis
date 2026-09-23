package db

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// Every field the read-safe views in internal/api blank or mask refuses the
// placeholder they put there, and names itself. Readiness review row 16.
//
// One row per field, because the failure this prevents is per field: a field
// left off the list round-trips "[redacted]" into storage exactly as before and
// nothing says so. Each case also asserts the control -- the same record with a
// real value validates -- so a refusal cannot pass by rejecting everything.
//
// Mutation: drop any one credentialField from Destination.Validate,
// IngestSettings.problems or Settings.Validate, and its row fails.

func TestADestinationFieldCarryingTheRedactionPlaceholderIsRefusedByName(t *testing.T) {
	if err := validDest().Validate(); err != nil {
		t.Fatalf("control: the unmodified destination does not validate: %v", err)
	}
	for _, tc := range []struct {
		path string
		set  func(*Destination)
	}{
		{"streamKey", func(d *Destination) { d.StreamKey = alerts.Mask }},
		{"backupStreamKey", func(d *Destination) {
			d.BackupURL = "rtmp://backup.example/live"
			d.BackupStreamKey = alerts.Mask
		}},
		{"url", func(d *Destination) { d.URL = "rtmp://ingest.example/" + alerts.Mask }},
		{"backupUrl", func(d *Destination) { d.BackupURL = "rtmp://" + alerts.Mask + "@backup.example/live" }},
		{"extraInputArgs", func(d *Destination) { d.ExtraInputArgs = alerts.Mask }},
		{"extraOutputArgs", func(d *Destination) { d.ExtraOutputArgs = alerts.Mask }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			d := validDest()
			tc.set(d)
			err := d.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.path+" carries the redaction placeholder") {
				t.Fatalf("Validate() = %v, want a refusal naming %s", err, tc.path)
			}
		})
	}
}

func TestASettingsFieldCarryingTheRedactionPlaceholderIsRefusedByName(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatalf("control: default settings do not validate: %v", err)
	}
	for _, tc := range []struct {
		path string
		set  func(*Settings)
	}{
		{"ingest.srt.passphrase", func(s *Settings) { s.Ingest.SRT.Passphrase = alerts.Mask }},
		{"ingest.rtmp.streamKey", func(s *Settings) { s.Ingest.RTMP.StreamKey = alerts.Mask }},
		{"ingest.pull.url", func(s *Settings) { s.Ingest.Pull.URL = "srt://cam.example:9000?passphrase=" + alerts.Mask }},
		{"failover.backup.srt.passphrase", func(s *Settings) { s.Failover.Backup.SRT.Passphrase = alerts.Mask }},
		{"failover.backup.rtmp.streamKey", func(s *Settings) { s.Failover.Backup.RTMP.StreamKey = alerts.Mask }},
		{"failover.backup.pull.url", func(s *Settings) { s.Failover.Backup.Pull.URL = "rtsp://" + alerts.Mask + "@cam.example/live" }},
		{"mqtt.brokerUrl", func(s *Settings) { s.MQTT.BrokerURL = "mqtt://" + alerts.Mask + "@broker.example:1883" }},
		{"automod.model.endpoint", func(s *Settings) {
			s.Automod.Model.Endpoint = "https://llm.example/v1/chat/completions?api_key=" + alerts.Mask
		}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			s := DefaultSettings()
			tc.set(&s)
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.path+" carries the redaction placeholder") {
				t.Fatalf("Validate() = %v, want a refusal naming %s", err, tc.path)
			}
		})
	}
}

// A source validates its ingest block through the same problems() settings
// uses, so the refusal reaches PUT /sources/{id} without a list of its own.
func TestASourceIngestCarryingTheRedactionPlaceholderIsRefused(t *testing.T) {
	src := &Source{Name: "Main", Ingest: DefaultSettings().Ingest}
	if err := validateSource(src); err != nil {
		t.Fatalf("control: %v", err)
	}
	src.Ingest.RTMP.StreamKey = alerts.Mask
	if err := validateSource(src); err == nil || !strings.Contains(err.Error(), "ingest.rtmp.streamKey") {
		t.Fatalf("validateSource() = %v, want a refusal naming ingest.rtmp.streamKey", err)
	}
}

// The message never carries the rest of the value: a partly masked URL still
// holds the parts that were not masked, and this lands in a 400 body and a log.
func TestTheRefusalDoesNotEchoTheValue(t *testing.T) {
	probs := redactionPlaceholderProblems(credentialField{"url", "rtmp://visible-host.example/" + alerts.Mask})
	if len(probs) != 1 {
		t.Fatalf("got %d problems, want 1", len(probs))
	}
	if strings.Contains(probs[0], "visible-host") {
		t.Errorf("the refusal echoes the value: %q", probs[0])
	}
	if got := redactionPlaceholderProblems(credentialField{"url", "rtmp://host.example/live"}); len(got) != 0 {
		t.Errorf("a real value was refused: %v", got)
	}
}
