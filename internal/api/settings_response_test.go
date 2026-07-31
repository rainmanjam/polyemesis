package api

import (
	"encoding/json"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The response shape is load-bearing for the UI, which types it as Settings.
func TestSettingsResponseKeepsSettingsAtTheTopLevel(t *testing.T) {
	raw, err := json.Marshal(settingsResponse{Settings: db.DefaultSettings()})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ingest", "recording", "chat", "alerts", "meters", "reload"} {
		if _, ok := m[key]; !ok {
			t.Errorf("response has no top-level %q; the UI assigns this straight into its Settings state", key)
		}
	}
	if _, nested := m["settings"]; nested {
		t.Error(`response nests the settings under "settings"; three UI pages would blank their forms`)
	}
}
