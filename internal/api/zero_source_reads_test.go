package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// The zero-source READ contract, driven through the real router.
//
// An install that has not created its first source has no engine, so every one
// of these handlers holds a nil *engine.Engine. Before this commit each of them
// dereferenced it, which means the first page a new operator opened took the
// dashboard, the telemetry socket and the Prometheus scrape down together --
// and left them with no screen that could explain why.
//
// READS ONLY, DELIBERATELY, and this is the sentence to read before adding to
// the table below. The routes that MUTATE a pipeline -- annotations, failover,
// destination start/stop, clip capture -- are not here because they are not yet
// safe to exercise: their refusal is the boundary guard, which is the next
// commit in this stack. They are not forgotten, and the walk of EVERY
// registered route lands with that guard, where a 503 is a pass rather than a
// panic.
func zeroSourceServer(t *testing.T) (*Server, http.Handler, func(*http.Request)) {
	t.Helper()
	s, h, _, auth := managerServerWithoutEngines(t, defaultTools())
	// The fixture is the whole experiment. An engine here would make every
	// assertion below a test of the ordinary path wearing a fresh-install
	// costume.
	if s.eng() != nil {
		t.Fatal("the fixture has an engine running; nothing below would be testing " +
			"an install with no source")
	}
	return s, h, auth
}

// The reads an operator's first browser tab makes, and the ones a scrape and a
// monitoring page make behind it.
var zeroSourceReads = []struct {
	path string
	want int
	// why names what the route is FOR, so a failure says what the operator
	// lost rather than only which URL returned 500.
	why string
}{
	{"/api/v1/setup", http.StatusOK, "the first screen, and the only one reachable before signing in"},
	{"/api/v1/status", http.StatusOK, "the dashboard's snapshot"},
	{"/api/v1/source", http.StatusOK, "the ingest card"},
	{"/api/v1/stats", http.StatusOK, "the bitrate and host graphs"},
	{"/api/v1/levels", http.StatusOK, "the audio meters"},
	{"/api/v1/system", http.StatusOK, "the page that says what this box can do"},
	{"/api/v1/processes", http.StatusOK, "the monitoring page's process list"},
	{"/api/v1/loudness", http.StatusOK, "the compliance readout"},
	{"/api/v1/schedules/runs", http.StatusOK, "what the scheduler last did"},
	{"/api/v1/alerts/meta", http.StatusOK, "the alert rule editor's pickers"},
	// The empty list, and only the empty list. An operator opens this page
	// before creating anything, which is the case being pinned.
	//
	// It is NOT a claim that a destination survives its source. It cannot:
	// ON DELETE CASCADE takes destinations and renditions with the programme
	// (schema.sql:110/153), and a row that somehow held source_id IS NULL is
	// REFUSED by scanDestination (db/destinations.go:190-199) with "belongs to
	// no programme", which this route returns as a 500. That population is
	// exactly PR 4's `orphans` seeding witness, so the migration branch will
	// meet an install where this route, /renditions and /metadata all 500
	// until the seed runs. Recorded here rather than rediscovered there.
	{"/api/v1/destinations", http.StatusOK, "the destinations list an operator opens before creating one"},
	{"/api/v1/clips", http.StatusOK, "the clips already on disk, which outlive the buffer and the programme"},
	{"/api/v1/metrics", http.StatusOK, "the Prometheus scrape"},
	// A 404 is the right answer and a 500 is not: with nothing supervised
	// there is no process by that name, which is a different statement from
	// the server falling over while looking.
	{"/api/v1/processes/ingest/logs", http.StatusNotFound, "one process's log tail"},
}

func TestEveryZeroSourceReadStillAnswers(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	for _, tc := range zeroSourceReads {
		t.Run(tc.path, func(t *testing.T) {
			r := jsonRequest(t, http.MethodGet, tc.path, nil)
			auth(r)
			w := do(t, h, r)
			if w.Code != tc.want {
				t.Fatalf("GET %s returned %d, want %d, on an install with no source. "+
					"This is %s. Body: %s", tc.path, w.Code, tc.want, tc.why, w.Body.String())
			}
		})
	}
}

// The status payload, not merely its status code. The dashboard iterates both
// slices without checking them, because ui/src/lib/types.ts says it does not
// have to.
func TestZeroSourceStatusCarriesEmptyArraysRatherThanNulls(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	r := jsonRequest(t, http.MethodGet, "/api/v1/status", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/status: %d: %s", w.Code, w.Body.String())
	}
	assertEmptyStatusSlices(t, w.Body.Bytes())
}

// EVERY array on the status payload, not only the two the UI types today.
//
// loudness is the field this helper was missing: it has no omitempty, and both
// GET /api/v1/loudness and Engine.Loudness normalise it to [] on the same
// install in the same instant, so a null from here would be the one producer in
// the tree that disagreed with the others -- and it would stay invisible until
// types.ts grew the field it is already being sent, at which point the first
// load on a fresh install does null.map and blanks.
func assertEmptyStatusSlices(t *testing.T, body []byte) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode status: %v: %s", err, body)
	}
	for _, field := range []string{"renditions", "destinations", "loudness"} {
		raw, ok := got[field]
		if !ok {
			t.Fatalf("status carried no %q at all: %s", field, body)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			t.Fatalf("decode status %q: %v: %s", field, err, body)
		}
		if string(raw) == "null" || arr == nil {
			t.Fatalf("status carried a null in %q where every other producer of that "+
				"field sends an array: %s", field, body)
		}
		if len(arr) != 0 {
			t.Fatalf("status invented %q rows on an install with no source: %s", field, body)
		}
	}
}

// sources on the setup status is what lets a browser tell "nothing is on air"
// from "there is nothing to put on air" before it has signed in. Both cases are
// asserted, because a field hard-coded to 0 would pass the zero-source half on
// its own.
func TestTheSetupStatusReportsHowManySourcesExist(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		_, h, _ := zeroSourceServer(t)
		if got := setupSources(t, h); got != 0 {
			t.Fatalf("setup status reported %d sources on an empty install", got)
		}
	})
	t.Run("one", func(t *testing.T) {
		h, _, _ := renditionServer(t, defaultTools())
		if got := setupSources(t, h); got != 1 {
			t.Fatalf("setup status reported %d sources on an install with exactly one; "+
				"the field is not derived from the store", got)
		}
	})
}

func setupSources(t *testing.T, h http.Handler) int {
	t.Helper()
	w := do(t, h, jsonRequest(t, http.MethodGet, "/api/v1/setup", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/setup: %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Sources *int `json:"sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode setup status: %v: %s", err, w.Body.String())
	}
	if got.Sources == nil {
		t.Fatalf("setup status carried no sources count at all: %s", w.Body.String())
	}
	return *got.Sources
}

// The same count on the scrape, for the alert that has to distinguish an
// install nobody configured from a broadcast that ended: every other series
// reads zero in both cases.
func TestTheScrapeReportsHowManySourcesExist(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		_, h, auth := zeroSourceServer(t)
		if got := scrapeSources(t, h, auth); got != "0" {
			t.Fatalf("polyemesis_sources = %s on an empty install", got)
		}
	})
	t.Run("one", func(t *testing.T) {
		h, _, auth := renditionServer(t, defaultTools())
		if got := scrapeSources(t, h, auth); got != "1" {
			t.Fatalf("polyemesis_sources = %s on an install with exactly one source", got)
		}
	})
}

func scrapeSources(t *testing.T, h http.Handler, auth func(*http.Request)) string {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/metrics", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d: %s", w.Code, w.Body.String())
	}
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if name, value, ok := strings.Cut(line, " "); ok && name == "polyemesis_sources" {
			return value
		}
	}
	t.Fatalf("no polyemesis_sources sample in the exposition: %s", w.Body.String())
	return ""
}

// The stats payload is read by the graphs, which draw nothing for a series
// they have never had a reading of. An invented zero sample would draw a flat
// line claiming a measured silence.
func TestZeroSourceStatsReportsNoSamplesAndAnIdleRelay(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	r := jsonRequest(t, http.MethodGet, "/api/v1/stats", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/stats: %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Bitrate []json.RawMessage `json:"bitrate"`
		Relay   struct {
			RxPackets uint64 `json:"rxPackets"`
			Port      int    `json:"port"`
		} `json:"relay"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode stats: %v: %s", err, w.Body.String())
	}
	if len(got.Bitrate) != 0 {
		t.Fatalf("the bitrate series carried %d samples with nothing arriving: %s",
			len(got.Bitrate), w.Body.String())
	}
	if got.Relay.RxPackets != 0 || got.Relay.Port != 0 {
		t.Fatalf("the relay reported traffic on a hub that does not exist: %s", w.Body.String())
	}
}

// The WebSocket's OPENING BURST, which is the frame set a freshly opened page
// renders from. It is a separate test because it is a separate code path: three
// events are assembled before the first tick, and a panic in any of them takes
// the socket down at upgrade rather than returning a status code.
func TestTheWebSocketOpeningBurstSurvivesZeroSources(t *testing.T) {
	s, h, auth := zeroSourceServer(t)
	s.revokedMu.Lock()
	s.wsPingEvery = wsTestPing
	s.revokedMu.Unlock()

	tok := createScopedToken(t, h, auth, "watcher", db.ScopeRead)

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := dialWS(t, srv, http.Header{"Authorization": {"Bearer " + tok}})

	// The status frame specifically: it is the one carrying the two slices the
	// UI iterates, and the burst sends it first.
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("the socket delivered no status frame on an install with no source: %v", err)
		}
		var ev struct {
			Type events.Type     `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(msg, &ev); err != nil {
			t.Fatalf("decode event: %v: %s", err, msg)
		}
		if ev.Type == events.TypeStatus {
			assertEmptyStatusSlices(t, ev.Data)
			return
		}
	}
}

// One POST, and the reason it belongs in a file about reads.
//
// Making Engine.Alerts nil-receiver safe is what lets GET /alerts/meta answer,
// and the same change reaches this route -- where the notifier it hands back is
// nil and the handler's own refusal, which predates all of this, is what turns
// that into a 503. The danger was never a panic here. It was the opposite: a
// nil notifier whose Publish is nil-receiver safe would have reported "sent"
// for a webhook nobody sent, and the operator would have gone looking for the
// fault in their Discord server.
func TestTestingAnAlertRuleRefusesWhenNoNotifierIsRunning(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	r := jsonRequest(t, http.MethodPost, "/api/v1/alerts/rules", map[string]any{
		"name": "ops", "url": "https://example.invalid/hook", "format": "slack",
	})
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create alert rule: %d: %s", w.Code, w.Body.String())
	}
	var rule struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rule); err != nil {
		t.Fatalf("decode created rule: %v: %s", err, w.Body.String())
	}

	r = jsonRequest(t, http.MethodPost,
		"/api/v1/alerts/rules/"+strconv.FormatInt(rule.ID, 10)+"/test", nil)
	auth(r)
	w = do(t, h, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("the test-send returned %d with no notifier running. A 200 here tells "+
			"the operator a message was delivered that nothing sent: %s",
			w.Code, w.Body.String())
	}
}

// The clips an operator already captured are FILES, and files outlive the
// programme that made them.
//
// /clips reads recordings/clips, one directory per install, the same path
// engine.New hands the rolling buffer. Both routes used to reach
// Engine.clipDir, which RLocks its receiver, so both 500'd on an install with
// no source -- and the operator who most needs them is precisely the one who
// has just deleted their last programme and is trying to get the material off
// the box. A blanket refusal here would blank a listing of files that are
// still on disk, which is the same objection MUST NOT #7 raises for /library
// and /recordings.
func TestClipsOnDiskAreListedAndDownloadableWithNoSource(t *testing.T) {
	s, h, auth := zeroSourceServer(t)

	if err := os.MkdirAll(s.clipDir(), 0o755); err != nil {
		t.Fatalf("create the clips directory: %v", err)
	}
	name := clips.Prefix + "20260301-200000" + clips.Ext
	body := []byte("not really MPEG-TS, but it is a real file with a real size")
	if err := os.WriteFile(filepath.Join(s.clipDir(), name), body, 0o644); err != nil {
		t.Fatalf("plant a clip: %v", err)
	}

	r := jsonRequest(t, http.MethodGet, "/api/v1/clips", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/clips: %d on an install with no source: %s", w.Code, w.Body.String())
	}
	var listed struct {
		Clips []struct {
			Name string `json:"name"`
		} `json:"clips"`
		Usage struct {
			Count     int   `json:"count"`
			UsedBytes int64 `json:"usedBytes"`
			MaxBytes  int64 `json:"maxBytes"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode clips: %v: %s", err, w.Body.String())
	}
	if len(listed.Clips) != 1 || listed.Clips[0].Name != name {
		t.Fatalf("the listing lost a clip that is on disk: %s", w.Body.String())
	}
	// The retention is reported too, and against the defaults engine.New would
	// have configured -- a zeroed usage block would render as a full disk.
	if listed.Usage.Count != 1 || listed.Usage.UsedBytes != int64(len(body)) ||
		listed.Usage.MaxBytes <= 0 {
		t.Fatalf("usage was not measured off the directory: %s", w.Body.String())
	}

	r = jsonRequest(t, http.MethodGet, "/api/v1/clips/"+name+"/download", nil)
	auth(r)
	w = do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET the clip download: %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != string(body) {
		t.Fatalf("the download served %q, not the file that is on disk", w.Body.String())
	}
}

// The name filter is still the name filter on the no-engine path.
//
// Read what this does and does not prove, because the two halves of MUST NOT
// #6 are checked in different places. clips.Resolve refuses a name that is not
// a clip name BEFORE it joins anything, so this case would pass against any
// base at all, "" included -- it pins that the fallback still goes through
// Resolve rather than joining the parameter itself.
//
// That the base is a REAL directory is pinned by the test above, which reads
// back a file it planted under recordings/clips: a base nil-safed to "" would
// resolve against the process's working directory and find nothing there.
// Mutation-verified in that direction -- s.clipDir() -> "" fails that test and
// not this one.
func TestTheClipDownloadStillRefusesToEscapeItsDirectoryWithNoSource(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	for _, name := range []string{"../../polyemesis.db", "..%2f..%2fpolyemesis.db"} {
		r := jsonRequest(t, http.MethodGet, "/api/v1/clips/"+name+"/download", nil)
		auth(r)
		w := do(t, h, r)
		if w.Code == http.StatusOK {
			t.Fatalf("the download answered 200 for %q, so the name was confined against "+
				"nothing: %s", name, w.Body.String())
		}
	}
}

// The preview playlist, which is one of the FIRST requests a browser makes.
//
// Dashboard.tsx mounts PreviewPlayer with the preview switched on before
// GET /settings has answered, so hls.js asks for /hls/index.m3u8 on the first
// render of a fresh install. hlsHandler called Engine.PreviewRequested, which
// locks its receiver, so chi's Recoverer turned the first screen into a 500
// plus a panic stack in server.log -- repeating, because hls.js retries a
// failed manifest load.
//
// 404 is the answer, not 503: it is what a live install returns in the seconds
// before the encoder has written a playlist, and it is what the player already
// knows how to wait through. The route is registered OUTSIDE /api/v1, so no
// middleware in the authenticated group can reach it.
func TestThePreviewPlaylistAnswersRatherThanPanicsWithNoSource(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := jsonRequest(t, method, "/hls/index.m3u8", nil)
		auth(r)
		w := do(t, h, r)
		if w.Code == http.StatusInternalServerError {
			t.Fatalf("%s /hls/index.m3u8 returned 500 on an install with no source. "+
				"This is the dashboard's first render, and hls.js retries it: %s",
				method, w.Body.String())
		}
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s /hls/index.m3u8 returned %d, want 404 -- the same answer a live "+
				"install gives before the encoder has written a playlist: %s",
				method, w.Code, w.Body.String())
		}
	}
}

// The clip editor over a recording whose track count was never measured.
//
// Recordings outlive their source by design (ON DELETE SET NULL), so this
// route is opened exactly when there may be no engine -- and its fallback for
// an unmeasured recording read the LIVE ingest's layout through Engine.Source,
// which dereferences its receiver. The default fixture's recordings carry
// tracks=1, which is why a walk of every route did not surface this.
//
// The answer is "no tracks known", not a refusal: post-production over files
// that are still on disk has to keep working. What is given up is offering
// routing.DefaultSource()'s six placeholder tracks for a recording that has
// none of them.
func TestTheClipEditorAnswersForAnUnmeasuredRecordingWithNoSource(t *testing.T) {
	s, h, auth := zeroSourceServer(t)

	base := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	rec := db.Recording{
		Filename:   "2026-03-01-2000-part1.mkv",
		StartedAt:  base,
		FinishedAt: base.Add(10 * time.Minute),
		Bytes:      4 << 20,
		DurationMS: 600_000,
		// THE WHOLE EXPERIMENT. Zero is what the recorder leaves when it never
		// measured the file, and it is what sends clipTracks to the fallback.
		Tracks: 0,
	}
	if err := s.store.UpsertRecording(&rec); err != nil {
		t.Fatalf("plant an unmeasured recording: %v", err)
	}
	// UpsertRecording does not hand back the id it assigned, so the row is read
	// out again rather than addressed as 1 -- see plantRows, which does the same.
	planted, err := s.store.ListRecordings()
	if err != nil || len(planted) != 1 {
		t.Fatalf("read back the planted recording: %v (%d rows)", err, len(planted))
	}
	if err := os.WriteFile(filepath.Join(s.cfg.RecordingsDir(), rec.Filename),
		[]byte("a real file on disk"), 0o644); err != nil {
		t.Fatalf("plant the recording's file: %v", err)
	}

	r := jsonRequest(t, http.MethodGet,
		"/api/v1/clipper/recordings/"+strconv.FormatInt(planted[0].ID, 10), nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET the clip editor for an unmeasured recording: %d on an install with "+
			"no source: %s", w.Code, w.Body.String())
	}
	var view struct {
		Tracks []json.RawMessage `json:"tracks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the clip editor view: %v: %s", err, w.Body.String())
	}
	if view.Tracks == nil {
		t.Fatalf("tracks came back null rather than an empty array: %s", w.Body.String())
	}
	if len(view.Tracks) != 0 {
		t.Fatalf("the editor offered %d tracks for a recording nothing measured, on an "+
			"install with no ingest to have measured them: %s",
			len(view.Tracks), w.Body.String())
	}
}
