package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// PUT /source/annotations writes to TWO places and the handler's own comment
// admits the hazard in the second one.
//
// The source row is the half the ENGINE reads -- annotations used to live in
// settings.Ingest, and when the engine started reading its ingest from the
// source row instead they silently stopped arriving: role exclusion stopped
// dropping the music track and stems went back to being named track1, track2,
// track3. That is a recorded regression, and nothing in this package asserted
// the fixed half afterwards.
//
// The settings mirror is the half a CLIENT reads back from GET /settings, and
// it is written through UpdateSettings rather than PutSettings for a stated
// reason: the mirror rewrites the whole blob, so a raw write would discard
// every unrelated field somebody else had just saved.
//
// A test asserting only "the PUT returned 200" -- or only that the annotations
// came back -- misses both. This one asserts the change happened, the mirror
// happened, and nothing else moved.

// apitailUnrelatedSetting is a field the annotations handler has no business
// touching. MQTT.Instance is free text and MQTT stays DISABLED in the fixture,
// so MQTTSettings.problems returns nil and no validator can object to the value
// on the way back through UpdateSettings. It is not a credential and carries no
// entropy: it exists to be recognised, not to be secret.
const apitailUnrelatedSetting = "the-canary-instance"

func TestAnnotationsLandOnTheSourceAndTheSettingsMirror(t *testing.T) {
	h, store, sign := sourceServer(t)

	// Planted BEFORE the PUT, so the whole-blob rewrite has something of
	// somebody else's to lose.
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.MQTT.Enabled = false
	st.MQTT.Instance = apitailUnrelatedSetting
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("plant the unrelated setting: %v", err)
	}

	const route = "/api/v1/source/annotations"
	first := []map[string]any{
		{"track": 1, "role": "music", "label": "Backing bed"},
		{"track": 2, "role": "mic", "label": "Host mic", "language": "en"},
	}

	r := jsonRequest(t, http.MethodPut, route, map[string]any{"annotations": first})
	sign(r)
	w := do(t, h, r)
	apitailReached(t, w, "the session principal", "PUT "+route)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT %s: status %d, body %s", route, w.Code, w.Body.String())
	}

	// (a) The half the ENGINE reads. This is the one the audio acceptance suite
	// caught going missing.
	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	apitailWantAnnotations(t, "the SOURCE row (internal/engine reads its ingest "+
		"from here; when this stopped being written, role exclusion stopped "+
		"dropping the music track and stems went back to track1/track2/track3)",
		src.Ingest.Annotations)

	// (b) The half a CLIENT reads back.
	after, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after the PUT: %v", err)
	}
	apitailWantAnnotations(t, "the SETTINGS mirror (what GET /settings shows, "+
		"and what an install that later drops back to a single source keeps)",
		after.Ingest.Annotations)

	// (c) And nothing else moved. The mirror rewrites the whole settings
	// document; a raw PutSettings of a freshly-built blob would take this with
	// it and no assertion about the annotations themselves would notice.
	if after.MQTT.Instance != apitailUnrelatedSetting {
		t.Errorf("an unrelated settings field was rewritten by a PUT to %s.\n"+
			"  mqtt.instance before: %q\n  mqtt.instance after:  %q\n"+
			"The mirror rewrites the WHOLE document, which is why it goes "+
			"through UpdateSettings' read-modify-write rather than PutSettings.",
			route, apitailUnrelatedSetting, after.MQTT.Instance)
	}

	// (d) A refusal must refuse before it writes. Validation runs with
	// routing's own validator and BEFORE the store is touched, so a bad role
	// leaves the previous annotations exactly where they were -- an operator
	// who mistypes one role does not lose the other five.
	bad := jsonRequest(t, http.MethodPut, route, map[string]any{
		"annotations": []map[string]any{{"track": 1, "role": "definitely-not-a-role"}},
	})
	sign(bad)
	bw := do(t, h, bad)
	apitailReached(t, bw, "the session principal", "PUT "+route+" (invalid role)")
	if bw.Code != http.StatusBadRequest {
		t.Fatalf("PUT %s with an unknown role: status %d, want 400. Body: %s",
			route, bw.Code, bw.Body.String())
	}
	survivor, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("GetSource after the refusal: %v", err)
	}
	apitailWantAnnotations(t, "the SOURCE row AFTER a refused PUT (a rejected "+
		"request must not have half-written; validation runs before the store "+
		"is touched)", survivor.Ingest.Annotations)
}

// apitailWantAnnotations asserts the two-row set the test above sends, naming
// where it was looking so a failure says which half of the write went missing.
func apitailWantAnnotations(t *testing.T, where string, got []routing.TrackAnnotation) {
	t.Helper()
	want := []routing.TrackAnnotation{
		{Track: 1, Role: routing.RoleMusic, Label: "Backing bed"},
		{Track: 2, Role: routing.RoleMic, Label: "Host mic", Language: "en"},
	}
	if len(got) != len(want) {
		t.Errorf("%s holds %d annotations, want %d: %+v", where, len(got), len(want), got)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s holds annotation %d = %+v, want %+v", where, i, got[i], want[i])
		}
	}
}
