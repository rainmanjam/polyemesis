package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// GET /settings, then PUT the same document back unchanged, must change
// nothing -- above all not the default source's ingest.
//
// THE BUG: settings.ingest is written THROUGH to the default source on a save,
// but the Sources page writes the source row directly and never touches the
// blob. So the two drift: the source says rtmp, the blob still says whatever it
// said before (on most installs, the unset mode). GET served the blob; the
// settings page PUTs the whole document back on every save; the write-through
// saw "blob ingest != source ingest" and overwrote the source's mode with "".
// Publishes were refused while /sources said running and /health said ok, and
// the operator had only saved a recording retention.
func TestASettingsRoundTripLeavesTheDefaultSourcesIngestAlone(t *testing.T) {
	h, store, auth := renditionServer(t, defaultTools())

	id, err := store.DefaultSourceID()
	if err != nil {
		t.Fatalf("this fixture has no default source: %v", err)
	}
	src, err := store.GetSource(id)
	if err != nil {
		t.Fatalf("read the source: %v", err)
	}
	// What the Sources page does: choose the mode on the source row itself.
	src.Ingest.Mode = db.IngestRTMP
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("set the source's mode: %v", err)
	}

	r := jsonRequest(t, http.MethodGet, "/api/v1/settings", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /settings: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mode := got["ingest"].(map[string]any)["mode"]; mode != string(db.IngestRTMP) {
		t.Errorf("GET /settings reports ingest.mode %v while the default source runs %q: "+
			"the settings page is showing an ingest the server is not using", mode, db.IngestRTMP)
	}

	r = jsonRequest(t, http.MethodPut, "/api/v1/settings", got)
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("PUT /settings of the document GET returned: %d %s", w.Code, w.Body.String())
	}

	after, err := store.GetSource(id)
	if err != nil {
		t.Fatalf("re-read the source: %v", err)
	}
	if after.Ingest.Mode != db.IngestRTMP {
		t.Fatalf("an unchanged GET->PUT round-trip moved the default source's ingest mode "+
			"from %q to %q. Every save of the settings page does this, and publishes are "+
			"refused afterwards while health says ok.", db.IngestRTMP, after.Ingest.Mode)
	}
}
