package api

// The install-wide recording toggle: GET and POST /api/v1/recording.
//
// The Record key flips one bit, Recording.Enabled, and must not disturb the
// rest of the settings document. These tests pin the state the key shows
// (off, recording, not running, halted by disk), the admin gate on the write,
// and that a toggle changes nothing but that bit.

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

type recordingBody struct {
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"`
	Halted    bool   `json:"halted"`
	Reason    string `json:"reason"`
	Installed bool   `json:"installWide"`
}

// getRecording reads the state as the given caller. A nil sign is anonymous.
func getRecording(t *testing.T, h http.Handler, sign func(*http.Request)) (int, recordingBody) {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/recording", nil)
	if sign != nil {
		sign(r)
	}
	w := do(t, h, r)
	var body recordingBody
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return w.Code, body
}

func setRecording(t *testing.T, h http.Handler, sign func(*http.Request), enabled bool) int {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/recording", map[string]any{"enabled": enabled})
	sign(r)
	return do(t, h, r).Code
}

// settingsDocument reads GET /settings as a generic document, for comparing
// everything but the recording block before and after a toggle.
func settingsDocument(t *testing.T, h http.Handler, sign func(*http.Request)) map[string]any {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/settings", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /settings: status = %d, body %s", w.Code, w.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return doc
}

func TestRecordingStateIsReadableAnonymouslyNever(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())
	read := createScopedToken(t, h, sign, "streamdeck", db.ScopeRead)

	if code, _ := getRecording(t, h, nil); code != http.StatusUnauthorized {
		t.Errorf("anonymous GET /recording: status = %d, want 401", code)
	}
	if code, _ := getRecording(t, h, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+read) }); code != http.StatusOK {
		t.Errorf("read token GET /recording: status = %d, want 200", code)
	}
}

// The write is admin-only. A read token must be refused, and the setting must
// not move on a refused request.
func TestRecordingToggleIsAdminOnly(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())
	read := createScopedToken(t, h, sign, "streamdeck", db.ScopeRead)
	asRead := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+read) }

	if setRecording(t, h, sign, false) != http.StatusOK {
		t.Fatal("admin could not switch recording off, so the fixture is not in a known state")
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/recording", map[string]any{"enabled": true})
	asRead(r)
	if w := do(t, h, r); w.Code != http.StatusForbidden {
		t.Fatalf("read token POST /recording: status = %d, want 403, body %s", w.Code, w.Body.String())
	}
	if _, body := getRecording(t, h, sign); body.Enabled {
		t.Error("a refused read-token toggle still switched recording on")
	}
}

// Recording.Enabled is the only thing the toggle may write. Every other block
// of the settings document must read back exactly as it did before.
func TestRecordingToggleChangesOnlyTheRecordingFlag(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())
	before := settingsDocument(t, h, sign)

	if setRecording(t, h, sign, true) != http.StatusOK {
		t.Fatal("POST /recording enabled=true was not accepted")
	}
	after := settingsDocument(t, h, sign)

	rec, _ := after["recording"].(map[string]any)
	if rec == nil || rec["enabled"] != true {
		t.Fatalf("recording.enabled after POST true = %v, want true", after["recording"])
	}
	for k, v := range before {
		if k == "recording" || k == "version" {
			continue
		}
		if !reflect.DeepEqual(v, after[k]) {
			t.Errorf("POST /recording changed %q: before %v, after %v", k, v, after[k])
		}
	}
}

func TestRecordingStateOffWhenTheSettingIsOff(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())
	if setRecording(t, h, sign, false) != http.StatusOK {
		t.Fatal("POST /recording enabled=false was not accepted")
	}
	code, body := getRecording(t, h, sign)
	if code != http.StatusOK {
		t.Fatalf("GET /recording: status = %d", code)
	}
	if body.State != "off" || body.Enabled || body.Halted {
		t.Errorf("recording off = %+v, want state off, enabled false, not halted", body)
	}
	if !body.Installed {
		t.Error("installWide is false; the key must be told the toggle is install-wide")
	}
}

// Enabled with the free-space floor breached is neither "recording" nor "off".
// It is the case the Record key must show as HALTED: DISK.
func TestRecordingStateHaltedByDiskWhenTheFloorStopsIt(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	if setRecording(t, h, sign, true) != http.StatusOK {
		t.Fatal("POST /recording enabled=true was not accepted")
	}
	engines := s.mgr.Engines()
	if len(engines) == 0 {
		t.Fatal("the fixture started no engine, so no storage guard exists to halt")
	}
	// A floor no volume can clear, which is how this fills a disk without one.
	halt := db.RecordingSettings{Enabled: true, SegmentSeconds: 3600, MinFreeGB: 1e9}
	for _, eng := range engines {
		eng.Recordings().CheckFreeSpace(halt)
	}

	_, body := getRecording(t, h, sign)
	if body.State != "halted_disk" || !body.Halted || !body.Enabled {
		t.Fatalf("enabled and halted by the floor = %+v, want state halted_disk, halted, enabled", body)
	}
	if body.Reason == "" {
		t.Error("halted_disk carries no reason, so the key cannot say why it stopped")
	}

	// Switched off, the same halt is no longer a state the key shows.
	if setRecording(t, h, sign, false) != http.StatusOK {
		t.Fatal("POST /recording enabled=false was not accepted")
	}
	_, body = getRecording(t, h, sign)
	if body.State != "off" {
		t.Errorf("after switching off the state is %q, want off", body.State)
	}
}

func TestRecordingToggleRefusesAMissingEnabledField(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())
	r := jsonRequest(t, http.MethodPost, "/api/v1/recording", map[string]any{})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusBadRequest {
		t.Fatalf("POST /recording with no enabled field: status = %d, want 400, body %s", w.Code, w.Body.String())
	}
}
