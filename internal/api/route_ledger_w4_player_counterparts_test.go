package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Wave 4 #9's two public-player routes, each given a counterpart proof rather than a
// bare Want, so neither adds to counterpartlessExcusedCeiling.
//
// Both sit outside every authenticated group, like /playout/public and the poster, so
// the property that matters is not a 403 but that a read token is a STRANGER here:
// whatever the operator's authority would buy on these routes, a read token's answer
// must carry none of it.

// offlineClipBytes is the planted clip. Ordinary English rather than a high-entropy
// literal, for the reason transcriptExportSpoken gives; it is a proof-local sentinel,
// so the runner scans the read principal's bytes for it.
const offlineClipBytes = "the ferry left the quay at a quarter past nine"

// proveOfflineClipFixture is a server whose playout is ENABLED but NOT PUBLIC, with a
// finished MP4 export chosen as the offline clip. Only the operator may see it.
func proveOfflineClipFixture(t *testing.T) (*Server, http.Handler, func(*http.Request), string) {
	t.Helper()
	s, h := playoutTestServer(t, enabledPlayout(false),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "ledger-offline-clip", db.ScopeRead)

	dir := s.clipExportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mp4 := filepath.Join(dir, "ledger-offline.mp4")
	if err := os.WriteFile(mp4, []byte(offlineClipBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	job := seedDoneClipExport(t, s.store, mp4)
	r := jsonRequest(t, http.MethodPut, "/api/v1/playout/publish", map[string]any{"offlineClipJob": job.ID})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("choosing the offline clip answered %d: %s", w.Code, w.Body.String())
	}
	return s, h, sign, read
}

// provePlayoutOfflineClip discharges GET /playout/offline-clip.
//
// The route streams an operator-chosen export. The operator previews it on a stream
// that is not published -- the positive control is that the admin's bytes ARE the
// clip -- and the read token, judged as a stranger, must receive none of it.
func provePlayoutOfflineClip(t *testing.T) proofResult {
	_, h, sign, read := proveOfflineClipFixture(t)
	const path = playoutOfflineClipPath

	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || high != offlineClipBytes {
		t.Fatalf("the operator's offline clip answered %d %q; without the clip in the admin's "+
			"bytes the read principal would be denied nothing", code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusNotFound {
		t.Errorf("the read token's offline clip answered %d, want the stranger's 404", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      high,
		Read:      low,
		Sentinels: []string{offlineClipBytes},
	}
}

// provePlayoutBeaconGate discharges POST /playout/beacon.
//
// A beacon's answer carries nothing by design -- 204, or an error string -- so the
// credential at stake is the one the GATE guards: the playback token. The admin's
// GET /playout shows it in the clear (the positive control, as provePlayoutPublicView
// uses), and the read token's beacon -- on a published, protected stream, where a
// stranger is challenged -- must neither carry it nor be handed the playback cookie
// that would stand in for it.
func provePlayoutBeaconGate(t *testing.T) proofResult {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "ledger-beacon", db.ScopeRead)

	r := httptest.NewRequest(http.MethodPost, playoutBeaconPath, strings.NewReader(validBeaconBody))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.9:5555"
	bearer(read)(r)
	w := do(t, h, r)
	for _, c := range w.Result().Cookies() {
		if c.Name == playoutTokenCookie {
			t.Errorf("a read token's beacon was handed the playback cookie")
		}
	}
	return proofResult{
		Pattern: patternDriven(t, h, http.MethodPost, playoutBeaconPath),
		High:    bodyOf(t, h, sign, "/api/v1/playout"),
		Read:    w.Body.String(),
	}
}
