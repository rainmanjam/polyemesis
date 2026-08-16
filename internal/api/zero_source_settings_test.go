package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// PUT /settings on an install with no source.
//
// This endpoint is the one the whole zero-source design most easily gets wrong
// in BOTH directions, and it is the only one where getting it wrong is invisible
// to the operator.
//
// # Direction one: it must not be guarded
//
// PUT /settings deliberately carries no requireSource. The settings document
// holds the listeners, the recording configuration, chat, automod and the alert
// rules, every one of which an operator legitimately configures before they
// create their first programme. A middleware here would refuse the second screen
// of a first run.
//
// # Direction two: the ingest half must not go silently dead
//
// The engine reads its ingest from the SOURCE row -- effectiveSettings does
// `settings.Ingest = src.Ingest` -- so writing settings.ingest and nothing else
// has no effect on anything. handlePutSettings already knew that and wrote the
// block through to the default source; what it did not do was notice when there
// was no default source to write it to. The guard was `if id, err := ...; err ==
// nil`, so zero sources took the same branch as a broken store, and both meant
// "carry on quietly": the endpoint accepted an ingest block, returned 200, and
// changed nothing.
//
// That is the exact bug the write-through was added to fix, reappearing on the
// DEFAULT tab of a first-time operator's first screen -- someone who has no
// reason yet to suspect that a saved setting might not be a setting.
func TestSavingAnIngestWithNoSourceIsRefusedRatherThanQuietlyDropped(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	body := settingsBodyWithIngest(t, h, auth, "srt")
	// See the differential below: the fixture's rtmpPort 0 is refused by the
	// save path for reasons that have nothing to do with sources, and would
	// mask the refusal under test with a 400.
	lst, ok := body["listeners"].(map[string]any)
	if !ok {
		lst = map[string]any{}
	}
	lst["rtmpPort"] = testenv.FreeTCPPort(t)
	body["listeners"] = lst

	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", body)
	auth(r)
	w := do(t, h, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT /settings with a changed ingest and no source returned %d, want 503. "+
			"A 200 here is the settings page reporting success for an ingest that reaches "+
			"nothing.\nbody: %s", w.Code, w.Body.String())
	}
	var e apiError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode the refusal: %v: %s", err, w.Body.String())
	}
	if e.Code != codeNoSource {
		t.Fatalf("refused with code %q, want %q: the settings page has to tell this apart "+
			"from a validation failure to know which field to highlight.\nbody: %s",
			e.Code, codeNoSource, w.Body.String())
	}
	// The other settings really were stored before this refusal, and an operator
	// who reads "could not save" and re-enters the whole page has been lied to
	// in the opposite direction.
	if !strings.Contains(e.Error, "saved") || !strings.Contains(e.Error, "Sources page") {
		t.Fatalf("the refusal neither says the rest was saved nor names the screen that "+
			"ends this state: %q", e.Error)
	}
}

// The differential: a save that does NOT touch the ingest still works.
//
// Without this the fix above is discharged by refusing every settings save on a
// fresh install, which would make the settings page unusable at exactly the
// moment an operator is setting up their recording directory and their alerts.
func TestSavingEverythingExceptTheIngestStillWorksWithNoSource(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	// Read the stored document and put it straight back with one non-ingest
	// field changed. Reading it first is what makes this a test of the ingest
	// comparison rather than of whatever the defaults happen to be.
	body := settingsBodyWithIngest(t, h, auth, "")
	rec, ok := body["recording"].(map[string]any)
	if !ok {
		rec = map[string]any{}
	}
	rec["enabled"] = true
	body["recording"] = rec
	// The fixture switches RTMP off with port 0, which the SAVE path refuses as
	// out of range even though the stored document holds it. Nothing to do with
	// sources: it is the same 400 an install with a programme would get. A port
	// this test owns keeps the refusal under test the only one in play, and
	// nothing binds it -- the manager here has no engines.
	lst, ok := body["listeners"].(map[string]any)
	if !ok {
		lst = map[string]any{}
	}
	lst["rtmpPort"] = testenv.FreeTCPPort(t)
	body["listeners"] = lst

	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", body)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /settings with the stored ingest unchanged returned %d, want 200. "+
			"An operator configuring recording, chat or alerts before creating their first "+
			"programme must not be refused.\nbody: %s", w.Code, w.Body.String())
	}
}

// settingsBodyWithIngest reads GET /settings and returns it as a mutable map,
// with ingest.mode set to mode when mode is non-empty.
//
// Round-tripping the STORED document rather than composing a fresh one is the
// point: the handler compares the submitted ingest against what is stored, so a
// hand-written body would differ in fields nobody meant to change and the
// "unchanged" case would silently become a "changed" one.
func settingsBodyWithIngest(t *testing.T, h http.Handler, auth func(*http.Request), mode string) map[string]any {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/settings", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /settings on an install with no source returned %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode settings: %v: %s", err, w.Body.String())
	}
	if mode != "" {
		ing, ok := body["ingest"].(map[string]any)
		if !ok {
			ing = map[string]any{}
		}
		if ing["mode"] == mode {
			t.Fatalf("the fixture already stores ingest mode %q, so setting it again "+
				"changes nothing and this test would prove nothing", mode)
		}
		ing["mode"] = mode
		body["ingest"] = ing
	}
	return body
}
