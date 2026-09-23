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

// A save that STORED the document and then answered with an error must still
// hand back the version it stored, or the page's next save conflicts with the
// page's own change.
//
// THE BUG: the no_source refusal is the common case. A first-time operator on
// the default tab changes the ingest and a recording setting together; the
// recording half is stored, the ingest half is refused with 503 no_source, and
// the answer carried no version. The page kept the version it had read, so its
// next save was refused with 409 "changed by someone else" -- about a change
// the operator had just made themselves. The same shape applies to every exit
// after the store: the ingest write-through failing, and the reconcile failing.
//
// The contract checked here is the one the console relies on: the version on
// the error is exactly what GET /settings now serves, and a save that sends it
// is accepted.
func TestAnErrorAfterTheStoreStillCarriesTheStoredVersion(t *testing.T) {
	_, h, auth := freshInstallServer(t)

	get := func() map[string]any {
		r := jsonRequest(t, http.MethodGet, "/api/v1/settings", nil)
		auth(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d: %s", w.Code, w.Body.String())
		}
		var doc map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	put := func(doc map[string]any) (int, map[string]any) {
		r := jsonRequest(t, http.MethodPut, "/api/v1/settings", doc)
		auth(r)
		w := do(t, h, r)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}

	doc := get()
	doc["ingest"].(map[string]any)["mode"] = "srt"
	doc["recording"].(map[string]any)["maxAgeHours"] = 48
	code, refusal := put(doc)
	if code != http.StatusServiceUnavailable || refusal["code"] != codeNoSource {
		t.Fatalf("first save = %d %v, want 503 no_source", code, refusal)
	}
	stored, _ := refusal["version"].(string)
	if stored == "" {
		t.Fatalf("the no_source refusal carries no version, although the recording half "+
			"of the same save was stored. The page keeps the version it read, and its next "+
			"save is refused as a conflict with the operator's own change.\nbody: %v", refusal)
	}
	if served, _ := get()["version"].(string); served != stored {
		t.Fatalf("the refusal's version %q is not what GET /settings serves (%q), so a "+
			"save that sends it would still conflict", stored, served)
	}

	// The page saves again from the same draft, with the version it was
	// handed. This is the save that used to be refused with 409: it is refused
	// again for the ingest, honestly, and not as somebody else's change.
	doc["version"] = stored
	doc["recording"].(map[string]any)["maxAgeHours"] = 72
	code, again := put(doc)
	if code == http.StatusConflict {
		t.Fatalf("second save from the same page = 409 %v: the operator's own earlier "+
			"save is being reported as somebody else's", again)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("second save = %d %v, want the same 503 no_source", code, again)
	}

	// And once the operator puts the ingest back, the save simply succeeds.
	doc["version"], _ = again["version"].(string)
	doc["ingest"].(map[string]any)["mode"] = get()["ingest"].(map[string]any)["mode"]
	if code, body := put(doc); code != http.StatusOK {
		t.Fatalf("third save = %d %v, want 200", code, body)
	}
}
