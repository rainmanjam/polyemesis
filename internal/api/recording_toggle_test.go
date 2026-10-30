package api

// The install-wide recording toggle: GET and POST /api/v1/recording.
//
// The Record key flips one bit, Recording.Enabled, and must not disturb the
// rest of the settings document. These tests pin the state the key shows
// (off, recording, not running, halted by disk), the admin gate on the write,
// and that a toggle changes nothing but that bit.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
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

// Enabled with the floor clear: "recording" when an engine has its recorder
// child up, "not_running" when none does. The fixture with engines starts a
// recorder on the reconcile the toggle triggers; the one without engines has
// nothing to start it on.
func TestRecordingStateFollowsTheRecorderChild(t *testing.T) {
	t.Run("an engine with a recorder up is recording", func(t *testing.T) {
		s, h, _, sign := managerServer(t, defaultTools())
		if setRecording(t, h, sign, true) != http.StatusOK {
			t.Fatal("POST /recording enabled=true was not accepted")
		}
		up := false
		for _, e := range s.mgr.Engines() {
			up = up || e.RecorderRunning()
		}
		if !up {
			t.Fatal("no engine reports a recorder after the toggle's reconcile, so this case is not reached")
		}
		if _, body := getRecording(t, h, sign); body.State != "recording" || body.Halted {
			t.Errorf("enabled with a recorder up = %+v, want state recording, not halted", body)
		}
	})
	t.Run("no engine means no recorder: not_running", func(t *testing.T) {
		// Derived directly: this fixture's stored document does not validate
		// (it has no source to take an ingest from), so a write through the
		// store would be refused for reasons that have nothing to do with this.
		s, _, _, _ := managerServerWithoutEngines(t, defaultTools())
		body := s.recordingStateFor(true)
		if body.State != recordingStateNotRunning || !body.Enabled || body.Halted {
			t.Errorf("enabled with no recorder up = %+v, want state not_running, enabled, not halted", body)
		}
	})
}

// A toggle is a settings change, so it is audited the way a settings save is:
// once when the bit moves, never for a press that changed nothing.
func TestRecordingToggleIsAuditedOnlyWhenItChangesTheSetting(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	if setRecording(t, h, sign, false) != http.StatusOK {
		t.Fatal("fixture: could not switch recording off")
	}
	got := captureAudit(t, s)

	if setRecording(t, h, sign, false) != http.StatusOK {
		t.Fatal("POST /recording enabled=false was not accepted")
	}
	if len(*got) != 0 {
		t.Fatalf("a toggle that changed nothing raised %v", typesOf(*got))
	}

	if setRecording(t, h, sign, true) != http.StatusOK {
		t.Fatal("POST /recording enabled=true was not accepted")
	}
	if len(*got) != 1 || (*got)[0].Type != alerts.TypeSettingsChanged {
		t.Fatalf("switching recording on raised %v, want one %s", typesOf(*got), alerts.TypeSettingsChanged)
	}
	if !strings.Contains(fmt.Sprint((*got)[0].Fields), "recording") {
		t.Errorf("the audit event does not name the recording section: %+v", (*got)[0].Fields)
	}
}

// The settings page saves with the version it read. A toggle made after that
// read moves the version, so the page's save is refused as stale rather than
// writing its old recording bit back over the key's press.
func TestASettingsSaveFromBeforeAToggleIsRefusedAsStale(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())
	if setRecording(t, h, sign, false) != http.StatusOK {
		t.Fatal("fixture: could not switch recording off")
	}
	doc := settingsDocument(t, h, sign)
	version, _ := doc["version"].(string)
	if version == "" {
		t.Fatal("GET /settings carried no version, so a stale save cannot be detected")
	}

	if setRecording(t, h, sign, true) != http.StatusOK {
		t.Fatal("POST /recording enabled=true was not accepted")
	}

	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", map[string]any{
		"version":   version,
		"recording": doc["recording"],
	})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusConflict {
		t.Fatalf("save from a read taken before the toggle: status = %d, want 409, body %s", w.Code, w.Body.String())
	}
	if _, body := getRecording(t, h, sign); !body.Enabled {
		t.Error("a refused stale save still switched recording back off")
	}
}

// The toggle and a settings save run at the same time and BOTH survive. The
// save here is partial and unversioned (a script), so it decodes over the
// stored document; the toggle writes only its bit. A toggle that read the
// document, changed the bit and wrote the whole thing back outside the
// settings locks would, under interleaving, put back the time zone it read
// and lose the save -- which is what this catches.
func TestRecordingToggleAndASettingsSaveRacingBothSurvive(t *testing.T) {
	_, h, _, sign := managerServer(t, defaultTools())
	zones := []string{"Europe/London", "America/New_York", "Asia/Tokyo", "Australia/Sydney"}

	for i := 0; i < 24; i++ {
		zone := zones[i%len(zones)]
		enabled := i%2 == 0

		var wg sync.WaitGroup
		var toggleCode, saveCode int
		var saveBody string
		wg.Add(2)
		go func() {
			defer wg.Done()
			r := jsonRequest(t, http.MethodPost, "/api/v1/recording", map[string]any{"enabled": enabled})
			sign(r)
			toggleCode = do(t, h, r).Code
		}()
		go func() {
			defer wg.Done()
			r := jsonRequest(t, http.MethodPut, "/api/v1/settings", map[string]any{
				"display": map[string]any{"timeZone": zone},
			})
			sign(r)
			w := do(t, h, r)
			saveCode, saveBody = w.Code, w.Body.String()
		}()
		wg.Wait()
		if toggleCode != http.StatusOK {
			t.Fatalf("round %d: toggle status = %d", i, toggleCode)
		}
		if saveCode != http.StatusOK {
			t.Fatalf("round %d: settings save status = %d, body %s", i, saveCode, saveBody)
		}

		doc := settingsDocument(t, h, sign)
		rec, _ := doc["recording"].(map[string]any)
		disp, _ := doc["display"].(map[string]any)
		if rec["enabled"] != enabled {
			t.Fatalf("round %d: recording.enabled = %v, want %v: the save wrote the toggle back", i, rec["enabled"], enabled)
		}
		if disp["timeZone"] != zone {
			t.Fatalf("round %d: display.timeZone = %v, want %q: the toggle wrote the save back", i, disp["timeZone"], zone)
		}
	}
}

// A failed reconcile after the switch is STORED is a warning on the 200, as
// every other mutation reports it (#709), not a 500. The 500 read as "nothing
// happened" while the setting had changed, and a Stream Deck key pressed again
// on the strength of it toggled recording straight back.
//
// The reconcile is made to fail for real, after the write: the manager's Sync
// lists the programmes first, and the sources table is renamed out from under
// it, which the settings write never touches.
//
// MUTATION: restore `writeError(w, http.StatusInternalServerError, rw)` for a
// non-empty reconcile warning. Observed: FAIL, "POST /recording = 500".
func TestRecordingToggleReportsAFailedReconcileAsAWarningNotA500(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	store := s.store
	if _, err := store.SQL().Exec(`ALTER TABLE sources RENAME TO sources_gone`); err != nil {
		t.Fatalf("break the reconcile: %v", err)
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/recording", map[string]any{"enabled": true})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /recording = %d after the switch was stored: %s", w.Code, w.Body.String())
	}
	var body struct {
		Enabled  bool     `json:"enabled"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Enabled || len(body.Warnings) == 0 {
		t.Errorf("response = %s, want enabled with the reconcile warning", w.Body.String())
	}
	st, err := store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Recording.Enabled {
		t.Error("the switch was not stored; the reconcile was not what failed")
	}
}
