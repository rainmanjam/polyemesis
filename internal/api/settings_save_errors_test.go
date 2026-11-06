package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A save's errors keep their statuses: a stale version 409, a refused document
// 400, and anything else the store's own answer.
func TestWriteSettingsSaveError(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want int
	}{
		{"conflict", errSettingsConflict, http.StatusConflict},
		{"bad request", badRequestError{"choose an ingest mode"}, http.StatusBadRequest},
		{"invalid", db.InvalidSettingsError{Err: errors.New("bad port")}, http.StatusBadRequest},
		{"not found", db.ErrNotFound, http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		if !writeSettingsSaveError(w, tt.err) {
			t.Errorf("%s: not answered", tt.name)
		}
		if w.Code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, w.Code, tt.want)
		}
	}
	if writeSettingsSaveError(httptest.NewRecorder(), nil) {
		t.Error("a nil error was answered")
	}
}

func TestWriteBadRequestOrStoreError(t *testing.T) {
	w := httptest.NewRecorder()
	writeBadRequestOrStoreError(w, badRequestError{"bad version"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad request: status %d, want 400", w.Code)
	}
	w = httptest.NewRecorder()
	writeBadRequestOrStoreError(w, db.ErrNotFound)
	if w.Code != http.StatusNotFound {
		t.Errorf("store error: status %d, want 404", w.Code)
	}
}
