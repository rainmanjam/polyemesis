package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The last source can be deleted (db.DeleteSource), so the engine set can empty
// between requireSource's check and the handler behind it. The handler must act
// on the engine the middleware resolved, not re-read the set and find nothing.
//
// Mutation: make requireSource stop carrying the engine in the request context
// and this fails with a nil engine.
func TestHandlerBehindRequireSourceGetsTheEngineTheGuardResolved(t *testing.T) {
	s, _, _, _ := managerServer(t, defaultTools())
	src, err := s.store.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}

	var got bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The race: the programme goes away after the guard passed.
		if err := s.store.DeleteSource(src); err != nil {
			t.Fatalf("DeleteSource: %v", err)
		}
		if err := s.mgr.Sync(); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if s.engOrNil() != nil {
			t.Fatal("fixture: the engine set did not empty, so there is no race to test")
		}
		eng, ok := s.scopedEngine(w, r)
		got = ok && eng != nil
	})
	rec := httptest.NewRecorder()
	s.requireSource(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if !got {
		t.Fatalf("handler saw no engine after the guard passed (status %d)", rec.Code)
	}
}

// Two endpoints used to mean two things by "the default programme": GET /system
// (and the transcript and OBS paths) the source the first RUNNING engine
// serves, GET /settings and the settings PUT the first source row. With the
// first row engineless, the settings page showed and wrote one programme's
// ingest while /system advertised another's.
//
// The decision (#551's): the default is the on-air programme, the first row
// only when nothing runs. Mutation: overlayDefaultSourceIngest back to
// s.store.DefaultSourceID() fails the GET /settings assertion.
func TestSettingsAndSystemAgreeOnTheDefaultProgramme(t *testing.T) {
	s, h, _, auth := managerServer(t, defaultTools())
	onAir := s.mgr.Default().SourceID()
	ghost := engineless(t, s)
	ghost.Position = -1
	ghost.Ingest.Mode = db.IngestRTMP
	ghost.Ingest.RTMP.App = "ghost"
	if err := s.store.UpdateSource(ghost); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	onAirRow, err := s.store.GetSource(onAir)
	if err != nil {
		t.Fatal(err)
	}
	onAirRow.Ingest.Mode = db.IngestSRT
	if err := s.store.UpdateSource(onAirRow); err != nil {
		t.Fatal(err)
	}

	get := func(path string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		auth(req)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET %s: %v (%s)", path, err, rec.Body.String())
		}
		return out
	}
	sys := get("/api/v1/system")
	if got := int64(sys["ingestSourceId"].(float64)); got != onAir {
		t.Fatalf("GET /system names source %d, want the on-air %d", got, onAir)
	}
	settings := get("/api/v1/settings")
	ingest, _ := settings["ingest"].(map[string]any)
	if ingest["mode"] != string(db.IngestSRT) {
		t.Errorf("GET /settings ingest mode = %v; /system describes the on-air source, "+
			"whose mode is %q", ingest["mode"], db.IngestSRT)
	}
}
