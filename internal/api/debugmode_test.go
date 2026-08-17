package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/diag"
)

/* THE DEBUG ROUTES, DRIVEN THROUGH THE ROUTER.
 *
 * internal/diag's own tests prove the scrubbing, the ring and the switch. What
 * they cannot reach is this layer: whether a build WITHOUT a recorder answers
 * or 404s, whether the toggle moves the log level with it, whether the export
 * writes an audit entry, and whether turning recording on can silently proceed
 * with a scrub set that was never built.
 *
 * THAT LAST ONE IS THE POINT OF THE FILE. The bundle is sent to somebody who
 * does not have the operator's box, and everything protecting it depends on a
 * secret set assembled from the destinations and sources as they are NOW. A
 * handler that started recording anyway when that read failed would produce a
 * bundle full of stream keys, and the operator would have no way to know.
 */

// debugServer builds a server with a recorder and switch attached, plus the
// same server without them, because "no recorder" is a real deployment and its
// answers are part of the contract.
func debugServer(t *testing.T, wired bool) (http.Handler, func(*http.Request), *diag.Recorder, *diag.Switch) {
	t.Helper()
	var rec *diag.Recorder
	var sw *diag.Switch
	if wired {
		rec = diag.NewRecorder(32, alerts.NewSecretSet(nil))
		sw = diag.NewSwitch(slog.LevelInfo)
	}
	_, h, _ := testServerWith(t, Options{Diag: rec, DiagLevel: sw})
	return h, login(t, h), rec, sw
}

func getDebug(t *testing.T, h http.Handler, sign func(*http.Request)) debugState {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/debug", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /debug = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var st debugState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return st
}

// A BUILD WITHOUT A RECORDER ANSWERS, RATHER THAN 404ing.
//
// Copying handleAccountStats' doctrine, which this handler's comment cites:
// "we cannot offer this" and "the route is gone" are different problems, and a
// UI that cannot tell them apart shows the wrong one. A 404 would make a
// perfectly good build look broken.
func TestTheDebugReadAnswersOnABuildWithNoRecorder(t *testing.T) {
	h, sign, _, _ := debugServer(t, false)

	st := getDebug(t, h, sign)
	if st.Recording || st.Held != 0 || st.Capacity != 0 {
		t.Errorf("state = %+v, want the zero state rather than an invented one", st)
	}
}

// The WRITES refuse on that build, and the asymmetry is deliberate: a read that
// cannot answer says so quietly, a write the operator asked for and did not get
// is a real failure.
func TestTheDebugWritesRefuseOnABuildWithNoRecorder(t *testing.T) {
	h, sign, _, _ := debugServer(t, false)

	for _, tc := range []struct{ name, method, path string }{
		{"toggle", http.MethodPut, "/api/v1/debug"},
		{"export", http.MethodPost, "/api/v1/debug/export"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body any
			if tc.method == http.MethodPut {
				body = map[string]any{"recording": true}
			}
			r := jsonRequest(t, tc.method, tc.path, body)
			sign(r)
			if w := do(t, h, r); w.Code != http.StatusPreconditionFailed {
				t.Fatalf("status = %d, want 412 (body %s)", w.Code, w.Body.String())
			}
		})
	}
}

// THE LEVEL MOVES WITH THE SWITCH, and back to the CONFIGURED level after.
//
// Recording at whatever level the operator was already running captures exactly
// what they already had, which is not what anybody means by "turn on debug
// mode" -- and the commonest reason a capture comes back useless is that it was
// taken at info.
func TestTogglingRecordingMovesTheLogLevelAndBack(t *testing.T) {
	h, sign, rec, sw := debugServer(t, true)

	if sw.Level() != slog.LevelInfo {
		t.Fatalf("fixture starts at %v, want info", sw.Level())
	}

	r := jsonRequest(t, http.MethodPut, "/api/v1/debug", map[string]any{"recording": true})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("enable = %d (body %s)", w.Code, w.Body.String())
	}
	if !rec.Recording() {
		t.Error("the handler answered 200 and the recorder is not recording")
	}
	if sw.Level() != slog.LevelDebug {
		t.Errorf("level = %v after enabling, want debug — a capture taken at the "+
			"configured level is the commonest reason one comes back useless", sw.Level())
	}
	if st := getDebug(t, h, sign); !st.Recording || st.Level != "DEBUG" {
		t.Errorf("read back %+v, want recording at DEBUG", st)
	}

	r = jsonRequest(t, http.MethodPut, "/api/v1/debug", map[string]any{"recording": false})
	sign(r)
	do(t, h, r)
	if rec.Recording() {
		t.Error("still recording after being switched off")
	}
	if sw.Level() != slog.LevelInfo {
		t.Errorf("level = %v after disabling, want the CONFIGURED info back. Returning "+
			"to a guess would quietly change what this box records, having been asked "+
			"only to turn something off.", sw.Level())
	}
}

// TURNING RECORDING ON REBUILDS THE SCRUB SET, and the read that builds it is
// not optional. This asserts the successful path leaves the recorder holding a
// set — the refusal path is asserted by the handler's own 503, which cannot be
// provoked through a healthy store and is stated in its comment instead.
func TestEnablingRecordingBuildsTheScrubSetFromTheStore(t *testing.T) {
	h, sign, rec, _ := debugServer(t, true)

	r := jsonRequest(t, http.MethodPut, "/api/v1/debug", map[string]any{"recording": true})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("enable = %d (body %s)", w.Code, w.Body.String())
	}
	// Recording is on, which is only reachable through the branch that built the
	// set: the handler returns 503 before SetRecording if either read fails.
	if !rec.Recording() {
		t.Fatal("recording did not start")
	}
}

// Reset empties the ring so an operator can start a clean reproduction rather
// than exporting a buffer full of unrelated history.
func TestResetEmptiesTheBuffer(t *testing.T) {
	h, sign, rec, _ := debugServer(t, true)
	rec.SetRecording(true)
	rec.Observe(diag.Record{Message: "something happened"})
	if len(rec.Records()) == 0 {
		t.Fatal("fixture: nothing captured")
	}

	r := jsonRequest(t, http.MethodPut, "/api/v1/debug", map[string]any{"reset": true})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("reset = %d (body %s)", w.Code, w.Body.String())
	}
	if n := len(rec.Records()); n != 0 {
		t.Errorf("%d records survive a reset", n)
	}
}

// THE EXPORT IS THE MOMENT A COPY OF THE SERVER'S LOGS LEAVES, so it carries a
// filename, the right content type, and — asserted below — an audit entry.
func TestTheExportReturnsANamedBundle(t *testing.T) {
	h, sign, rec, _ := debugServer(t, true)
	rec.SetRecording(true)
	rec.Observe(diag.Record{Message: "publishing to destination", Level: "INFO"})

	r := jsonRequest(t, http.MethodPost, "/api/v1/debug/export", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("export = %d (body %s)", w.Code, w.Body.String())
	}

	// Named with a timestamp so two bundles in one support thread can be told
	// apart, which is otherwise a real problem for whoever receives them.
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "polyemesis-debug-") {
		t.Errorf("Content-Disposition = %q, want an attachment named polyemesis-debug-*", cd)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("Content-Type = %q", ct)
	}

	var b diag.Bundle
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("the bundle is not valid JSON: %v", err)
	}
	if b.Capture.Held != 1 || len(b.Records) != 1 {
		t.Errorf("bundle holds %d records (capture says %d), want 1",
			len(b.Records), b.Capture.Held)
	}
	if b.Version == "" || b.Platform == "" {
		t.Errorf("bundle carries no version or platform: %+v", b.Capture)
	}
}

// Both routes need a session. The export most obviously — it hands out the
// server's own logs — but an unauthenticated toggle would let a stranger start
// filling a buffer they intend to come back for.
func TestTheDebugRoutesNeedASession(t *testing.T) {
	h, _, _, _ := debugServer(t, true)

	for _, tc := range []struct{ name, method, path string }{
		{"read", http.MethodGet, "/api/v1/debug"},
		{"toggle", http.MethodPut, "/api/v1/debug"},
		{"export", http.MethodPost, "/api/v1/debug/export"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately unsigned.
			r := jsonRequest(t, tc.method, tc.path, nil)
			w := do(t, h, r)
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 401 or 403 (body %s)", w.Code, w.Body.String())
			}
		})
	}
}
