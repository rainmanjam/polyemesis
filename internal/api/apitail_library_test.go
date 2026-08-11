package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// #171 denied GET /library/search to read-scoped tokens because db.TranscriptHit
// carries Text, Context and Speaker: a read token that iterated common words
// would reconstruct whole transcripts without ever naming a /transcript route.
// The LISTING routes stayed reachable, on the judgement that "3 tracks, 412
// segments, Presenter" is metadata and the words are content.
//
// That is a split, not a wall, and the thing worth pinning is the split rather
// than a 200. GET /library/recordings/{id} calls ListTranscriptTracks, whose
// whole reason to exist beside GetTranscript is that it returns no segment text
// at all. One edit swapping the two -- the kind that reads like a
// simplification -- would put verbatim speech behind a route nobody ever put on
// the deny list, and every status assertion in the package would still pass.

// The planted words. Ordinary English sentences, deliberately: a high-entropy
// sentinel would be scanned for by the leak harness AND by gitleaks, and
// transcripts are human speech. "kettle" is the search term because it is a
// normal word that will not appear in a filename, a speaker label or a JSON
// key by accident.
const (
	apitailSpokenFirst  = "the kettle boiled over onto the tiles"
	apitailSpokenSecond = "and nobody wiped it up afterwards"
	apitailSearchTerm   = "kettle"
	apitailSpeaker      = "Presenter"
)

// apitailLibraryFixture builds its OWN server rather than using plantedServer.
//
// Excuse #163 records that the shared fixture's library routes are observed 404
// precisely because it holds no recording; adding one here would flip that
// observation to 200 and fail the ledger for every other test in the package.
// The isolation is the point, not a convenience.
//
// Returns the handler, the signer for the session principal, and the id of the
// recording the transcript was planted on. UpsertRecording does not fill in the
// id -- it is keyed on filename and returns nothing -- so the row is re-read.
func apitailLibraryFixture(t *testing.T) (http.Handler, func(*http.Request), int64) {
	t.Helper()
	h, store, sign := sourceServer(t)

	if err := store.UpsertRecording(&db.Recording{
		Filename:   "apitail-library.mkv",
		StartedAt:  time.Now().Add(-time.Hour),
		FinishedAt: time.Now(),
		Bytes:      4096,
		DurationMS: 60000,
		Tracks:     2,
	}); err != nil {
		t.Fatalf("UpsertRecording: %v", err)
	}
	recs, err := store.ListRecordings()
	if err != nil {
		t.Fatalf("ListRecordings: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("the fixture holds %d recordings, want exactly 1", len(recs))
	}
	id := recs[0].ID

	if err := store.SaveTranscript(&db.Transcript{
		RecordingID: id,
		Tracks: []db.TranscriptTrack{{
			RecordingID: id, Track: 0, Speaker: apitailSpeaker,
			Language: "en", Model: "small", Backend: "whisper",
			Segments: []db.TranscriptSegment{
				{StartMS: 0, EndMS: 3000, Text: apitailSpokenFirst},
				{StartMS: 3000, EndMS: 6000, Text: apitailSpokenSecond},
			},
		}},
	}); err != nil {
		t.Fatalf("SaveTranscript: %v", err)
	}
	return h, sign, id
}

// apitailNoWordsLeaked asserts on RAW RESPONSE BYTES rather than on a decoded
// struct.
//
// TranscriptTrack.Segments is `json:"segments,omitempty"`, so a
// decode-and-check-nil survives the field being renamed, re-nested or moved to
// a sibling key -- it would go on reporting nil for a field that no longer
// exists while the words travelled under another name. The bytes cannot lie
// about what left the process.
func apitailNoWordsLeaked(t *testing.T, body []byte, principal, route string) {
	t.Helper()
	for _, spoken := range []string{apitailSpokenFirst, apitailSpokenSecond, apitailSearchTerm} {
		if strings.Contains(string(body), spoken) {
			t.Errorf("%s got verbatim transcript text back from %s.\n"+
				"  leaked: %q\n"+
				"#171 denied GET /library/search to this principal because "+
				"db.TranscriptHit carries the words. This route was left reachable "+
				"on the judgement that it returns track METADATA and no speech; it "+
				"is now returning speech, through a route that was never on the "+
				"deny list.\n  body: %s", principal, route, spoken, body)
			return
		}
	}
}

func TestAReadTokenSeesTheTranscriptShapeAndNeverTheWords(t *testing.T) {
	h, sign, id := apitailLibraryFixture(t)
	route := "/api/v1/library/recordings/" + strconv.FormatInt(id, 10)

	// POSITIVE CONTROL, and it is a t.Fatal rather than a t.Error on purpose.
	//
	// Every assertion below is an ABSENCE. An absence proves nothing unless the
	// thing is really there to leak, and a fixture that silently failed to
	// store the segments -- a schema change, an FTS trigger, a typo in the
	// track index -- would make this whole test pass while checking that an
	// empty store is empty. So the words are read back verbatim first, through
	// the one route that is allowed to have them, by the one principal that is
	// allowed to ask.
	searchBody := send(t, h, sign, http.MethodGet,
		"/api/v1/library/search?q="+apitailSearchTerm, nil, http.StatusOK)
	if !strings.Contains(string(searchBody), apitailSpokenFirst) {
		t.Fatalf("POSITIVE CONTROL FAILED: the session principal searched "+
			"/library/search?q=%s and did not get back %q, so the planted "+
			"transcript is not in the store and every absence asserted below "+
			"would be vacuous.\n  body: %s",
			apitailSearchTerm, apitailSpokenFirst, searchBody)
	}

	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	r := jsonRequest(t, http.MethodGet, route, nil)
	bearer(read)(r)
	w := do(t, h, r)
	apitailReached(t, w, "a read-scoped token", "GET "+route)
	if w.Code != http.StatusOK {
		t.Fatalf("a read token got %d from GET %s. The listing routes were left "+
			"reachable by #171; only /library/search and the two /transcript "+
			"routes were denied. Body: %s", w.Code, route, w.Body.String())
	}
	body := w.Body.Bytes()

	// The half that must still work. A test that only asserted the absence
	// would pass against a handler that returned nothing at all.
	var out struct {
		TranscriptTracks []struct {
			Track      int    `json:"track"`
			Speaker    string `json:"speaker"`
			Count      int    `json:"count"`
			DurationMS int64  `json:"durationMs"`
		} `json:"transcriptTracks"`
		Recording struct {
			ID            int64 `json:"id"`
			HasTranscript bool  `json:"hasTranscript"`
		} `json:"recording"`
	}
	decodeInto(t, body, &out)

	if len(out.TranscriptTracks) != 1 {
		t.Fatalf("GET %s returned %d transcript tracks, want 1. The recordings "+
			"page renders \"1 track, 2 segments\" from this and has nothing to "+
			"show: %s", route, len(out.TranscriptTracks), body)
	}
	tr := out.TranscriptTracks[0]
	// Speaker IS legitimately part of the listing -- GET /library still returns
	// the bare list of labels in the archive, which is who appears rather than
	// what was said. #171's line is drawn at verbatim segment text, so the
	// speaker is asserted PRESENT here, not absent.
	if tr.Speaker != apitailSpeaker {
		t.Errorf("transcript track speaker = %q, want %q -- the attribution the "+
			"whole per-track design exists to carry", tr.Speaker, apitailSpeaker)
	}
	if tr.Count != 2 {
		t.Errorf("transcript track count = %d, want 2 -- the segment count is "+
			"filled by ListTranscriptTracks precisely so the page can say how "+
			"much was said without loading a word of it", tr.Count)
	}
	if tr.DurationMS != 6000 {
		t.Errorf("transcript track durationMs = %d, want 6000 (0..3000 plus "+
			"3000..6000)", tr.DurationMS)
	}
	if !out.Recording.HasTranscript {
		t.Error("hasTranscript is false on a recording that has one; a read " +
			"token can no longer tell which recordings are searchable, which " +
			"is exactly what #171 said it should keep")
	}

	// The half that must not.
	apitailNoWordsLeaked(t, body, "a read-scoped token", "GET "+route)
}

// TestTheSessionViewCarriesNoTranscriptTextEither covers the route T3 does not.
//
// GET /library/sessions/{id} expands its recordings through a different code
// path, and an expansion that started loading transcripts would put the words
// in front of a read token without touching handleGetLibraryRecording at all.
func TestTheSessionViewCarriesNoTranscriptTextEither(t *testing.T) {
	h, sign, id := apitailLibraryFixture(t)

	var created struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/library/sessions",
		map[string]any{"title": "Tuesday rehearsal", "recordings": []int64{id}},
		http.StatusCreated), &created)

	route := "/api/v1/library/sessions/" + strconv.FormatInt(created.ID, 10)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	r := jsonRequest(t, http.MethodGet, route, nil)
	bearer(read)(r)
	w := do(t, h, r)
	apitailReached(t, w, "a read-scoped token", "GET "+route)
	if w.Code != http.StatusOK {
		t.Fatalf("a read token got %d from GET %s: %s", w.Code, route, w.Body.String())
	}
	body := w.Body.Bytes()

	var out struct {
		Session struct {
			DisplayTitle string `json:"displayTitle"`
		} `json:"session"`
		Recordings []struct {
			ID int64 `json:"id"`
		} `json:"recordings"`
	}
	decodeInto(t, body, &out)

	if out.Session.DisplayTitle == "" {
		t.Error("the session view carries no displayTitle. It is resolved " +
			"server-side so an untitled session reads the same everywhere; " +
			"blank means every caller invents its own fallback again")
	}
	if len(out.Recordings) != 1 || out.Recordings[0].ID != id {
		t.Fatalf("the session view expanded %v, want the one recording %d: %s",
			out.Recordings, id, body)
	}

	apitailNoWordsLeaked(t, body, "a read-scoped token", "GET "+route)
}
