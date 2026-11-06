package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// GET /system and GET /settings both lay the default source's ingest block over
// the stored settings. On a store fault they must say the same thing: fail,
// rather than one of them describing the stale singleton ingest as current.
func TestSystemAndSettingsAgreeWhenTheSourceStoreFails(t *testing.T) {
	_, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	if _, err := store.SQL().Exec(`ALTER TABLE sources RENAME TO sources_gone`); err != nil {
		t.Fatalf("break the sources table: %v", err)
	}

	for _, path := range []string{"/api/v1/settings", "/api/v1/system"} {
		req := jsonRequest(t, http.MethodGet, path, nil)
		sign(req)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code < 500 {
			t.Errorf("GET %s = %d on a failing source store, want a 5xx: it must not serve the stale ingest block", path, rr.Code)
		}
	}
}
