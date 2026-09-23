package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// A document a read token was shown cannot be written back over the real
// credentials. Readiness review row 16.
//
// readSafeDestination hands a read token backupStreamKey as the literal
// alerts.Mask, and docs/API.md invites read -> edit -> PUT. Replayed with an
// admin credential, that PUT sealed "[redacted]" as the backup key and answered
// 200; nothing failed until a failover needed the backup. Now it is a 400 that
// names the field, and the stored key is untouched.
//
// Mutation: drop the redactionPlaceholderProblems call in Destination.Validate.
func TestAPUTCarryingTheRedactionPlaceholderIsRefusedAndKeepsTheRealKey(t *testing.T) {
	h, store, sign := sourceServer(t)

	var out struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations", map[string]any{
		"name": "with a backup", "kind": "rtmp", "platform": "custom",
		"url": "rtmp://primary.example/app", "streamKey": "primary-key-placeholder",
		"backupUrl": "rtmp://backup.example/app", "backupStreamKey": "backup-key-placeholder",
		"enabled": false, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": trackSel(0), "matrix": []any{},
			"normalize": "off", "sampleRate": 48000,
		},
	}, http.StatusCreated), &out)
	id := out.Destination.ID
	if id == 0 {
		t.Fatal("create returned no id")
	}

	for _, tc := range []struct {
		field string
		value string
	}{
		{"backupStreamKey", alerts.Mask},
		// A URL is masked in part, so a value CONTAINING the placeholder is
		// the same round-trip.
		{"backupUrl", "rtmp://backup.example/" + alerts.Mask},
		{"streamKey", alerts.Mask},
	} {
		t.Run(tc.field, func(t *testing.T) {
			msg := mustJSONError(t, h, sign, http.MethodPut, destPath(id),
				map[string]any{tc.field: tc.value}, http.StatusBadRequest)
			if !strings.Contains(msg, tc.field) {
				t.Errorf("refusal %q does not name the field %s", msg, tc.field)
			}
		})
	}

	got, err := store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.BackupStreamKey != "backup-key-placeholder" || got.StreamKey != "primary-key-placeholder" ||
		got.BackupURL != "rtmp://backup.example/app" {
		t.Errorf("a refused PUT changed the stored credentials: key=%q backupKey=%q backupUrl=%q",
			got.StreamKey, got.BackupStreamKey, got.BackupURL)
	}

	// The real value still saves -- the refusal is about the placeholder, not
	// about editing the field.
	send(t, h, sign, http.MethodPut, destPath(id),
		map[string]any{"backupStreamKey": "rotated-backup-placeholder"}, http.StatusOK)
}
