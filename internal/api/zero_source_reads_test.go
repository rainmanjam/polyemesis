package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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
	{"/api/v1/destinations", http.StatusOK, "the destinations list, which outlives its source as an orphan row"},
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

func assertEmptyStatusSlices(t *testing.T, body []byte) {
	t.Helper()
	var got struct {
		Renditions   []json.RawMessage `json:"renditions"`
		Destinations []json.RawMessage `json:"destinations"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode status: %v: %s", err, body)
	}
	if got.Renditions == nil || got.Destinations == nil {
		t.Fatalf("status carried a null where the UI type promises an array: %s", body)
	}
	if len(got.Renditions) != 0 || len(got.Destinations) != 0 {
		t.Fatalf("status invented rows on an install with no source: %s", body)
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
