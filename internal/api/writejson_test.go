package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/events"
)

// The WebSocket half of the same failure: writeEvent returned the marshal error,
// and both of its callers close the socket on any error. One unencodable
// loudness frame therefore disconnected every console tab at once, and they
// reconnected into the same frame. An event that cannot be encoded is dropped
// and logged; the socket survives. The conn is nil on purpose -- a dropped
// event must not reach it.
func TestWriteEventDropsAnUnencodableFrameInsteadOfClosingTheSocket(t *testing.T) {
	ev := events.Event{Type: events.TypeLoudness, Time: time.Now(), Data: map[string]float64{"momentaryLufs": math.NaN()}}
	if err := writeEvent(nil, ev, false); err != nil {
		t.Fatalf("writeEvent = %v, want nil so the socket stays open", err)
	}
}

// A value encoding/json refuses -- NaN is the one that happened, from a loudness
// meter reading digital silence -- used to reach the wire as the success status
// with an empty body, because the status line was written before the encode
// was tried and the encode error was discarded. A client saw "200, nothing",
// which parses as neither the payload nor an error.
func TestWriteJSONRefusesToSendSuccessWithoutABody(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]any{"momentaryLufs": math.NaN()})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a body that could not be encoded", rec.Code)
	}
	var body apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Fatalf("body = %q, want the API's error shape", rec.Body.String())
	}
}

func TestWriteJSONHappyPathUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusCreated, map[string]string{"status": "ok"})
	if rec.Code != http.StatusCreated || rec.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
}
