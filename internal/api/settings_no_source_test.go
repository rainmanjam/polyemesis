package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The settings ingest editor on an install that has no source.
//
// THE BUG THIS IS ABOUT is one this handler already fixed once, in the other
// direction. settings.ingest stopped being read by anything when sources
// arrived -- the engine takes its ingest from the source row -- so the editor
// on the settings page accepted an SRT port, stored it, returned 200 and had no
// effect whatsoever. The write-through to the default source is what gave it
// meaning back.
//
// With no source there is nothing to write it through TO, and the `err == nil`
// on DefaultSourceID meant the whole write-through was skipped in silence: the
// dead editor was back, on the DEFAULT tab of the settings page, on the first
// screen a first-time operator opens. That is MUST NOT #3.
//
// AND THE THING IT MUST NOT DO INSTEAD. PUT /settings carries no requireSource
// and must never carry one: the same document holds the listeners, recording,
// chat, automod and alerts, all of which an operator legitimately configures
// before creating their first source. Refusing the whole route would take the
// settings page away from exactly the install that has nothing else to do yet.
// So the refusal is scoped to the one field, and the rest of the document is
// saved either way -- which is what the last test here pins.

func TestChangingTheIngestWithNoSourceIsRefusedRatherThanSavedIntoNothing(t *testing.T) {
	s, h, auth := freshInstallServer(t)

	before, err := s.store.GetSettings()
	if err != nil {
		t.Fatalf("read the stored settings: %v", err)
	}

	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", map[string]any{
		"ingest": map[string]any{
			"mode": string(db.IngestSRT),
			"srt":  map[string]any{"latencyMs": 450},
		},
	})
	auth(r)
	w := do(t, h, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT /settings with a changed ingest returned %d on an install with no "+
			"source, want 503. A 200 here is a success toast for an edit that reaches "+
			"nothing, which is the dead editor this handler already fixed once.\nbody: %s",
			w.Code, w.Body.String())
	}
	var refusal apiError
	if err := json.Unmarshal(w.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("decode the refusal: %v: %s", err, w.Body.String())
	}
	if refusal.Code != codeNoSource {
		t.Fatalf("refused with code %q, want %q: without it the settings page cannot tell "+
			"this from the server being broken.\nbody: %s",
			refusal.Code, codeNoSource, w.Body.String())
	}

	// AND THE STORED DOCUMENT, which is the half a status code cannot show. An
	// ingest that is refused and stored anyway redisplays on the next load as
	// though it had taken, and the operator has no way to tell it did not.
	after, err := s.store.GetSettings()
	if err != nil {
		t.Fatalf("re-read the stored settings: %v", err)
	}
	if !ingestEqual(before.Ingest, after.Ingest) {
		t.Fatalf("the refused ingest was stored anyway: mode %q -> %q. The form will "+
			"redisplay it on the next load, so the refusal is a toast the operator can "+
			"dismiss and nothing else.", before.Ingest.Mode, after.Ingest.Mode)
	}
}

// The differential, without which the test above is discharged by a handler
// that refuses every ingest edit for ever.
func TestChangingTheIngestReachesTheSourceOnceOneExists(t *testing.T) {
	h, store, auth := renditionServer(t, defaultTools())

	id, err := store.DefaultSourceID()
	if err != nil {
		t.Fatalf("this fixture has no source, so this differential compares two refusals: %v", err)
	}

	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", map[string]any{
		"ingest": map[string]any{
			"mode": string(db.IngestSRT),
			"srt":  map[string]any{"latencyMs": 450},
		},
	})
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("PUT /settings on an install that HAS a source returned %d, so the "+
			"refusal is a property of the route rather than of the install: %s",
			w.Code, w.Body.String())
	}

	src, err := store.GetSource(id)
	if err != nil {
		t.Fatalf("read the source back: %v", err)
	}
	if src.Ingest.SRT.LatencyMS != 450 {
		t.Fatalf("the source's SRT latency is %d, want 450. The settings editor stored "+
			"the block and never wrote it through, which is the dead editor arriving "+
			"from the other side.", src.Ingest.SRT.LatencyMS)
	}
}

// A save that does not touch the ingest is an ordinary save, with or without a
// source. This is the half that keeps the refusal from turning into
// requireSource by the back door.
func TestASettingsSaveThatLeavesTheIngestAloneStillWorksWithNoSource(t *testing.T) {
	s, h, auth := freshInstallServer(t)

	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", map[string]any{
		"recording": map[string]any{"enabled": true},
	})
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("PUT /settings returned %d for a save that never mentioned the ingest. "+
			"An operator configuring recording, chat, alerts or the listeners before "+
			"creating their first source is doing something perfectly ordinary, and this "+
			"page is the only thing they have to do yet.\nbody: %s", w.Code, w.Body.String())
	}
	stored, err := s.store.GetSettings()
	if err != nil {
		t.Fatalf("read the stored settings: %v", err)
	}
	if !stored.Recording.Enabled {
		t.Fatal("the save answered 200 and stored nothing")
	}
}

// The refusal saves the rest of the document, and this is the assertion that
// makes that a promise rather than an implementation detail.
//
// The settings page PUTs the WHOLE document, so a first-time operator who
// changes the recording directory and the SRT port on the same visit sends both
// in one request. Refusing before the store write would lose the half that was
// perfectly savable, and would do it silently -- the response says nothing
// about recording at all.
func TestARefusedIngestStillSavesEverythingElseInTheSameRequest(t *testing.T) {
	s, h, auth := freshInstallServer(t)

	// The positive control for the second half below. The fixture starts the
	// manager with rtmpPort 0 -- the explicit off switch -- and the repair that
	// makes the document savable writes a real port to the STORE without
	// reconciling, so the listener is genuinely down until a save brings it up.
	if s.mgr.ListenerBound(db.IngestRTMP) {
		t.Fatal("the fixture already has an RTMP listener, so the assertion below cannot " +
			"tell a reconcile that ran from one that did not")
	}

	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", map[string]any{
		"ingest":    map[string]any{"mode": string(db.IngestSRT)},
		"recording": map[string]any{"enabled": true},
	})
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}

	stored, err := s.store.GetSettings()
	if err != nil {
		t.Fatalf("read the stored settings: %v", err)
	}
	if !stored.Recording.Enabled {
		t.Fatal("the recording setting in the same request was thrown away with the " +
			"ingest block. Nothing in the response mentions recording, so the operator " +
			"has no way to learn that half their save did not happen.")
	}
	if stored.Ingest.Mode == db.IngestSRT {
		t.Fatal("the ingest block was stored after all, so the refusal only affected " +
			"the status line")
	}

	// AND THE HALF THAT IS NOT IN THE DATABASE, which is what fixes the
	// refusal's position in the handler rather than leaving it to taste.
	//
	// Storing a setting and applying it are two different events: the settings
	// document is written inside UpdateSettings, but the listeners, chat
	// retention, automod matrix and alert budget are only applied by the code
	// AFTER it. A refusal written at the top of that stretch -- which reads
	// better and is the obvious place to put it -- stores every one of those
	// and applies none, which is the silent no-op this handler warns about
	// three times over. Nothing above can see the difference; this can.
	if !s.mgr.ListenerBound(db.IngestRTMP) {
		t.Fatal("the RTMP listener is still down after a save that stored its port, so " +
			"the refusal returned before the reconcile. Every other setting in the same " +
			"document is now in the database and in force nowhere -- an operator who set " +
			"a chat retention window or an automod matrix alongside the ingest gets both " +
			"stored and neither applied, until the next restart.")
	}
}
