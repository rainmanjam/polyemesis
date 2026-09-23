package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A settings save made from a stale read is refused instead of reverting what
// somebody else saved in between.
//
// THE BUG: PUT /settings takes the whole document and the settings page sends
// the whole document on every save, so the last writer won on EVERY field. A
// tab opened with an auto-ban armed; another operator disarmed it mid-raid; the
// first tab saved a recording retention change -- and the ban was armed again,
// with a success toast, and nothing anywhere said so.
//
// GET /settings now carries a `version` of the document it served, and a PUT
// that sends it back is refused with 409 when the stored document has moved
// on. A client that sends no version is not checked, which is what keeps every
// existing script working -- and the console cannot forget to send it, because
// it round-trips the document it read and the version is part of it.
func TestAStaleSettingsSaveCannotRevertAnotherOperatorsChange(t *testing.T) {
	h, store, sign := sourceServer(t)

	read := func() map[string]any {
		var doc map[string]any
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
		return doc
	}

	// Somebody arms an auto-ban on Twitch.
	armed := read()
	armed["automod"].(map[string]any)["enabled"] = true
	armed["automod"].(map[string]any)["on"] = map[string]any{"twitch/ban/history": true}
	send(t, h, sign, http.MethodPut, "/api/v1/settings", armed, http.StatusOK)

	// Tab A opens Settings with the ban armed.
	tabA := read()
	if v, _ := tabA["version"].(string); v == "" {
		t.Fatalf("GET /settings carries no version, so a save cannot say what it was based on")
	}

	// Tab B disarms it.
	tabB := read()
	tabB["automod"].(map[string]any)["on"] = nil
	send(t, h, sign, http.MethodPut, "/api/v1/settings", tabB, http.StatusOK)

	// Tab A, still holding the armed document, saves an unrelated change.
	tabA["recording"].(map[string]any)["maxAgeHours"] = 48
	body := send(t, h, sign, http.MethodPut, "/api/v1/settings", tabA, http.StatusConflict)
	var refusal struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &refusal)
	if refusal.Code != codeSettingsConflict {
		t.Errorf("the refusal carries code %q, want %q -- the console branches on the code", refusal.Code, codeSettingsConflict)
	}

	st, err := store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.Automod.On["twitch/ban/history"] {
		t.Fatal("a stale save re-armed an auto-ban another operator had disarmed")
	}
	if st.Recording.MaxAgeHours == 48 {
		t.Fatal("the refused save was stored anyway")
	}

	// The version a SAVE answers with is good for the next save: a page that
	// saves twice without reloading must not conflict with itself.
	fresh := read()
	fresh["recording"].(map[string]any)["maxAgeHours"] = 48
	var saved map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPut, "/api/v1/settings", fresh, http.StatusOK), &saved)
	delete(saved, "reload")
	saved["recording"].(map[string]any)["maxAgeHours"] = 72
	send(t, h, sign, http.MethodPut, "/api/v1/settings", saved, http.StatusOK)

	// And a client that sends no version is not checked.
	legacy := read()
	delete(legacy, "version")
	legacy["recording"].(map[string]any)["maxAgeHours"] = 96
	send(t, h, sign, http.MethodPut, "/api/v1/settings", legacy, http.StatusOK)
}
