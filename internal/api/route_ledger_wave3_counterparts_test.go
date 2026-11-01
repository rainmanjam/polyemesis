package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The two wave 3 routes that a read token is refused, each given a counterpart proof
// rather than a bare Want, so neither adds to counterpartlessExcusedCeiling.
//
// A Want that observes a 403 says what the read token got today. It says nothing about
// what the route carries, which is the part that would leak if that 403 ever became a
// 200. Each proof below plants the thing the route exists to hand out, shows that a
// principal entitled to it receives it, and scans what the read token received for it.

// transcriptExportSpoken is the planted speech. Ordinary English for the reason
// apitailSpokenFirst gives: a transcript is human speech, and a high-entropy literal
// would be flagged by gitleaks for no benefit. It is a proof-local sentinel, so the
// runner scans the read principal's bytes for it as well as for the standing list.
const transcriptExportSpoken = "the lighthouse keeper read the tide tables aloud"

// rawBodyOf drives one GET and returns the body whatever the status, and the status.
// bodyOf refuses a non-200, and the read principal's answer here is meant to be a 403.
func rawBodyOf(t *testing.T, h http.Handler, sign func(*http.Request), path string) (int, string) {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, path, nil)
	sign(r)
	w := do(t, h, r)
	return w.Code, w.Body.String()
}

// proveTranscriptCaptionExport discharges GET /library/recordings/{id}/transcript/export.
//
// The route renders the transcript's words into a caption file. The proof plants a
// transcript on its own server (not plantedServer: see apitailLibraryFixture for why a
// recording there would flip every library observation in the package), exports it in
// all three formats as the admin, and requires the planted speech in each. The read
// token's three answers are the bytes the runner scans.
func proveTranscriptCaptionExport(t *testing.T) proofResult {
	h, store, sign := sourceServer(t)
	plantRecordingOnDisk(t, store, serverUnderTest(t, h).eng().Recordings().Dir(), db.Recording{
		Filename:   "ledger-caption-export.mkv",
		StartedAt:  time.Now().Add(-time.Hour),
		FinishedAt: time.Now(),
		Bytes:      4096,
		DurationMS: 60000,
		Tracks:     1,
	}, 1)
	recs, err := store.ListRecordings()
	if err != nil || len(recs) != 1 {
		t.Fatalf("the caption export fixture holds %d recordings (%v), want exactly 1", len(recs), err)
	}
	id := recs[0].ID
	if err := store.SaveTranscript(&db.Transcript{
		RecordingID: id,
		Tracks: []db.TranscriptTrack{{
			RecordingID: id, Track: 0, Speaker: "Presenter",
			Language: "en", Model: "small", Backend: "whisper",
			Segments: []db.TranscriptSegment{{StartMS: 0, EndMS: 3000, Text: transcriptExportSpoken}},
		}},
	}); err != nil {
		t.Fatalf("SaveTranscript: %v", err)
	}
	read := createScopedToken(t, h, sign, "ledger-caption-export", db.ScopeRead)

	base := "/api/v1/library/recordings/" + itoa(id) + "/transcript/export?format="
	var high, low []string
	for _, f := range []string{"srt", "vtt", "json"} {
		code, body := rawBodyOf(t, h, sign, base+f)
		if code != http.StatusOK || !strings.Contains(body, transcriptExportSpoken) {
			t.Fatalf("the admin's %s export answered %d without the planted speech, so the "+
				"read principal would be denied nothing: %s", f, code, truncateForFailure(body))
		}
		high = append(high, body)
		code, body = rawBodyOf(t, h, bearer(read), base+f)
		if code != http.StatusForbidden {
			t.Errorf("the read token's %s export answered %d, want 403", f, code)
		}
		low = append(low, body)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, base+"srt"),
		High:      strings.Join(high, "\n"),
		Read:      strings.Join(low, "\n"),
		Sentinels: []string{transcriptExportSpoken},
	}
}

// proveYouTubeVerticalStatus discharges GET /destinations/{id}/youtube-vertical.
//
// The pair's vertical half is a destination with a stream key of its own, minted by
// YouTube. The admin's destination list shows that key, which is the positive control.
// The status route must not carry it to ANY principal -- the admin's own answer is
// scanned here too, since the sub-row is drawn from it -- and the read token's answer
// is what the runner scans.
func proveYouTubeVerticalStatus(t *testing.T) proofResult {
	_, h, _, sign, _, horizontalID := verticalCreate(t)
	read := createScopedToken(t, h, sign, "ledger-youtube-vertical", db.ScopeRead)
	path := "/api/v1/destinations/" + itoa(horizontalID) + "/youtube-vertical"

	code, admin := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK {
		t.Fatalf("the admin's vertical status answered %d: %s", code, truncateForFailure(admin))
	}
	if strings.Contains(admin, verticalFakeKey) {
		t.Errorf("the vertical status carries the vertical half's stream key even to the admin: %s",
			truncateForFailure(admin))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's vertical status answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      bodyOf(t, h, sign, "/api/v1/destinations"),
		Read:      low,
		Sentinels: []string{verticalFakeKey},
	}
}
