package api

// The bulk pair, at the boundary, asserting the two decisions that a refactor
// would otherwise drop silently.
//
//  1. A BULK RESULT IS REPORTED PER DESTINATION, NEVER AS ONE BOOLEAN. The
//     shape is the assertion: a list with one row per destination, each naming
//     which it was and what happened. Eight destinations of which two refuse
//     must not arrive as "failed", and the only way to keep that true is to
//     have no place in the body where a single verdict could live.
//
//  2. STARTS ARE PACED AND STOPS ARE NOT. Nothing else in the tree can notice
//     if the gap between starts disappears -- the routes keep working, keep
//     answering, and keep passing every other test in this package. So the
//     elapsed time is measured. It is the slowest assertion here and it is the
//     one worth paying for.
//
// Neither assertion knows a platform number, and neither should ever be made to:
// bulkStartPacing is a pacing choice about this box, not a ceiling anybody
// published. See destinations_bulk.go.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bulkAnswer mirrors the response body.
type bulkAnswer struct {
	Action  string           `json:"action"`
	Results []bulkDestResult `json:"results"`
}

func TestBulkStartAndStopReportOneRowPerDestination(t *testing.T) {
	// Asserts the SHAPE of the response, not the pacing.
	withBulkPacing(t, time.Millisecond)
	h, _, sign := renditionServer(t, defaultTools())

	names := []string{"youtube", "twitch", "backup"}
	ids := make(map[int64]string, len(names))
	for _, name := range names {
		r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", map[string]any{
			"sourceId": onlySourceID(t, h, sign),
			"name":     name, "kind": "rtmp",
			"url": "rtmp://example.invalid/live", "streamKey": "k",
		})
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK && w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
		}
		var created struct {
			Destination struct {
				ID int64 `json:"id"`
			} `json:"destination"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Destination.ID == 0 {
			t.Fatalf("create %s: no id: %v (%s)", name, err, w.Body.String())
		}
		ids[created.Destination.ID] = name
	}

	post := func(path string) (bulkAnswer, time.Duration) {
		t.Helper()
		r := jsonRequest(t, http.MethodPost, path, bulkConfirmBodyFor(path))
		sign(r)
		started := time.Now()
		w := do(t, h, r)
		elapsed := time.Since(started)
		// 200 EVEN IF ROWS FAILED. The request reached every destination and
		// every one of them has an answer; collapsing that into a status code
		// is the boolean this shape exists to avoid.
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
		}
		var body bulkAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("POST %s: decode %v (%s)", path, err, w.Body.String())
		}
		return body, elapsed
	}

	// ---- the shape, on both verbs -----------------------------------------
	known := map[bulkOutcome]bool{
		bulkStarted: true, bulkStopped: true,
		bulkWarned: true, bulkFailed: true, bulkSkipped: true,
	}
	for _, tc := range []struct {
		path, action string
	}{
		{"/api/v1/destinations/start-all", "start"},
		{"/api/v1/destinations/stop-all", "stop"},
	} {
		body, _ := post(tc.path)
		if body.Action != tc.action {
			t.Errorf("POST %s answered action %q, want %q", tc.path, body.Action, tc.action)
		}
		if len(body.Results) != len(names) {
			t.Fatalf("POST %s answered %d rows for %d destinations. One row per "+
				"destination is the whole contract: a caller given fewer cannot say "+
				"which ones it is missing.", tc.path, len(body.Results), len(names))
		}
		seen := map[int64]bool{}
		for _, row := range body.Results {
			want, ok := ids[row.ID]
			if !ok {
				t.Errorf("POST %s reported id %d, which was never created", tc.path, row.ID)
				continue
			}
			seen[row.ID] = true
			if row.Name != want {
				t.Errorf("POST %s row %d is named %q, want %q. A row an operator "+
					"cannot match to a card on the page is a row they have to go "+
					"and find by hand.", tc.path, row.ID, row.Name, want)
			}
			if !known[row.Outcome] {
				t.Errorf("POST %s row %q has outcome %q, which is not one of the five "+
					"words ui/src/lib/types.ts BulkDestOutcome knows. The UI resolves a "+
					"tone and a label from that union, so an unknown word renders as "+
					"nothing at all.", tc.path, row.Name, row.Outcome)
			}
			if row.Outcome == bulkFailed && row.Message == "" {
				t.Errorf("POST %s row %q failed with no message. \"Failed\" alone sends "+
					"the operator to the card to learn what this response already knew.",
					tc.path, row.Name)
			}
		}
		for id, name := range ids {
			if !seen[id] {
				t.Errorf("POST %s never mentioned %q (id %d)", tc.path, name, id)
			}
		}
	}

	// ---- no single verdict anywhere in the body ---------------------------
	//
	// Driven rather than reasoned about: decode into a map and look. A future
	// edit that adds {"ok": false} beside the list would restore exactly the
	// boolean the list replaces, and every assertion above would still pass.
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/stop-all", map[string]any{"confirm": true})
	sign(r)
	var raw map[string]any
	if err := json.Unmarshal(do(t, h, r).Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k, v := range raw {
		if _, isBool := v.(bool); isBool {
			t.Errorf("the answer carries a top-level boolean %q = %v. A bulk result is "+
				"reported per destination, never as one boolean -- the same doctrine the "+
				"metadata composer states at Dashboard.tsx:140. Whatever this field means, "+
				"it is a verdict over rows that disagree with each other.", k, v)
		}
	}
}

// The pacing decision, measured. Removing the gap breaks nothing else.
func TestBulkStartIsPacedAndBulkStopIsNot(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	const n = 3
	for i := 0; i < n; i++ {
		r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", map[string]any{
			"sourceId": onlySourceID(t, h, sign),
			"name":     "dest" + itoa(int64(i)), "kind": "rtmp",
			"url": "rtmp://example.invalid/live", "streamKey": "k",
		})
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusOK && w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
	}

	timed := func(path string) time.Duration {
		t.Helper()
		r := jsonRequest(t, http.MethodPost, path, bulkConfirmBodyFor(path))
		sign(r)
		started := time.Now()
		if w := do(t, h, r); w.Code != http.StatusOK {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
		}
		return time.Since(started)
	}

	// The gap goes before every start but the first, so n destinations cost
	// (n-1) gaps. A lower bound only: reconciles and spawns take their own time
	// on top, and asserting an upper bound would make this flake on a loaded
	// machine for no gain.
	// A LITERAL FLOOR, NOT ONE DERIVED FROM bulkStartPacing, AND THAT WAS THE
	// WHOLE DEFECT. This bound used to read (n-1)*bulkStartPacing, which moves
	// with the thing it is measuring: weakening the interval a hundredfold, from
	// two seconds to twenty milliseconds, destroyed the property and the test
	// still passed, because the expectation shrank with it. Only exactly zero
	// was caught, and then only by the stop assertion below.
	//
	// Three destinations at the intended pace cost two gaps, so four seconds is
	// the honest floor with a second of slack for a loaded machine. If the
	// interval is deliberately changed, this number is changed by hand -- which
	// is the reviewable act, and the reason it is written out rather than
	// computed.
	const wantFloor = 3 * time.Second
	want := wantFloor
	if n != 3 {
		t.Fatalf("this test's literal floor assumes 3 destinations, got %d", n)
	}
	if got := timed("/api/v1/destinations/start-all"); got < want {
		t.Errorf("starting %d destinations took %v, want at least %v. The starts are "+
			"NOT being paced: %d children spawning inside one scheduler tick contend "+
			"with the ingest that is already running, and the same connections arrive "+
			"at their platforms as one clap rather than spread out. See bulkStartPacing "+
			"-- and note it is a pacing choice about this box, not any platform's "+
			"published ceiling, so the fix is to restore the gap and not to add a cap.",
			n, got, want, n)
	}

	// Tearing down is local: a stop signals a child this box owns and waits for
	// it, and doing that n times reaches nobody else's server. A gap here would
	// be pure delay in front of an operator who has decided to come off air.
	// Also literal. Bounded against the constant, this passed while the constant
	// was 20ms and the stops were taking longer than a real pacing interval.
	const stopCeiling = 1500 * time.Millisecond
	if got := timed("/api/v1/destinations/stop-all"); got >= stopCeiling {
		t.Errorf("stopping %d destinations took %v, which is at least one pacing "+
			"interval (%v). Stops are deliberately not paced -- there is nothing for a "+
			"gap to spread out.", n, got, stopCeiling)
	}
}

// THE TEST THE SHAPE TEST WAS NOT. Its sibling above proves the response is a
// list with one row per destination and no top-level boolean, which is worth
// proving and is not the same claim as "the rows say what happened".
//
// It could not make that second claim: its three fixtures are identical, so
// every row takes one branch. Two mutations survived it — making a refusing
// destination report as cleanly started, and making EVERY row report failed —
// and either would have shipped a control whose per-row reporting, the entire
// reason this is not one boolean, was decorative.
//
// Constructed effects rather than fixtures, because the interesting cases are
// exactly the ones a healthy test rig will not produce on demand.
func TestBulkOutcomesSayWhatActuallyHappened(t *testing.T) {
	tests := []struct {
		name        string
		effectErr   string
		effectWarn  string
		enabled     bool
		wantOutcome bulkOutcome
		wantMessage string
	}{
		{"a refused destination is failed, and carries the platform's words",
			"connection refused by rtmp://example.invalid", "", true, bulkFailed,
			"connection refused by rtmp://example.invalid"},
		{"a refusal while stopping is still a failure",
			"could not signal the child", "", false, bulkFailed, "could not signal the child"},
		{"an unreaped stop is a warning, not a failure",
			"", "the process did not exit within the grace period", false, bulkWarned,
			"the process did not exit within the grace period"},
		{"a clean start says started and says nothing else", "", "", true, bulkStarted, ""},
		{"a clean stop says stopped", "", "", false, bulkStopped, ""},
		{"an error outranks a warning, because the error is the thing that stopped it working",
			"refused", "also slow", true, bulkFailed, "refused"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := classifyBulkEffect(tc.effectErr, tc.effectWarn, tc.enabled)
			if got != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q. An operator reads this word to decide "+
					"whether to go and look at that destination.", got, tc.wantOutcome)
			}
			if msg != tc.wantMessage {
				t.Errorf("message = %q, want %q", msg, tc.wantMessage)
			}
		})
	}
}

// The mixed case stated as one assertion, because it is the sentence the
// feature exists for: eight destinations where two refuse must read as neither
// "worked" nor "failed".
func TestAMixedBulkResultIsNeitherSuccessNorFailure(t *testing.T) {
	rows := []struct{ errText string }{{""}, {"refused"}, {""}, {"refused"}, {""}}
	var started, failed int
	for _, r := range rows {
		switch out, _ := classifyBulkEffect(r.errText, "", true); out {
		case bulkStarted:
			started++
		case bulkFailed:
			failed++
		}
	}
	if started != 3 || failed != 2 {
		t.Fatalf("started=%d failed=%d, want 3 and 2. A bulk result that collapses to "+
			"one verdict hides which destinations an operator has to go and fix.",
			started, failed)
	}
}

// EVERY DESTINATION ON EVERY PLATFORM, AND A CUSTOM RTMP TARGET THAT HAS NO
// PLATFORM AT ALL.
//
// The bulk routes read whatever ListDestinations returns and filter nothing --
// there is no platform check anywhere in destinations_bulk.go, and Platform
// appears there only as a field on the result row. That is the intended
// behaviour and it is worth a test rather than a comment, because the
// confirmation copy necessarily says a lot about YouTube: ending a YouTube
// broadcast is permanent in a way that stopping a Twitch push is not, so the
// warning leads with it. A reader of that copy could reasonably wonder whether
// the control is YouTube-shaped. It is not, and this fails if it ever becomes so.
func TestBulkActsOnEveryPlatformAndOnDestinationsWithNone(t *testing.T) {
	// This test is about WHICH destinations are reached, not about the gap
	// between them, so it does not pay for the gap. Five destinations at the
	// production pace is eight seconds of sleeping, twice.
	withBulkPacing(t, time.Millisecond)
	h, _, sign := renditionServer(t, defaultTools())

	// A spread of real platforms plus a plain RTMP target that belongs to no
	// platform -- the case a platform-keyed implementation would silently skip.
	want := map[string]string{
		"yt main":     "youtube",
		"twitch main": "twitch",
		"kick main":   "kick",
		"fb main":     "facebook",
		"my own box":  "",
	}
	for name, platform := range want {
		body := map[string]any{
			"name": name, "kind": "rtmp",
			"url": "rtmp://example.invalid/live", "streamKey": "k",
		}
		if platform != "" {
			body["platform"] = platform
		}
		withOnlySource(t, h, sign, body)
		r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", body)
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusOK && w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
		}
	}

	for _, route := range []string{"stop-all", "start-all"} {
		path := "/api/v1/destinations/" + route
		r := jsonRequest(t, http.MethodPost, path, bulkConfirmBodyFor(path))
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body.String())
		}
		var got bulkAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: decode: %v", route, err)
		}
		seen := map[string]bool{}
		for _, row := range got.Results {
			seen[row.Name] = true
		}
		for name, platform := range want {
			if !seen[name] {
				which := platform
				if which == "" {
					which = "no platform at all"
				}
				t.Errorf("%s skipped %q (%s). This control acts on EVERY destination; "+
					"a platform-keyed implementation would quietly leave some of an "+
					"operator's outputs running after they pressed Stop all.",
					route, name, which)
			}
		}
		if len(got.Results) != len(want) {
			t.Errorf("%s returned %d rows, want %d", route, len(got.Results), len(want))
		}
	}
}

// bulkConfirmBodyFor is the body every OTHER test in this file needs on
// stop-all now that it requires one -- see TestStopAllRefusesWithoutConfirm.
// Centralised here so a future third bulk route does not need every existing
// call site taught about it by hand.
func bulkConfirmBodyFor(path string) any {
	if strings.HasSuffix(path, "/stop-all") {
		return map[string]any{"confirm": true}
	}
	return nil
}

// Finding #2, poka-yoke audit 2026-08-21: stop-all permanently ends every live
// YouTube broadcast on the install and used to be gated only by a UI dialog an
// API caller never sees. MUTATION: comment out the `if !req.Confirm` refusal
// in handleStopAllDestinations and this fails, because the bare POST below
// would then reach bulkSetDestinationsEnabled and answer 200.
func TestStopAllRefusesWithoutConfirm(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	withOnlySource(t, h, sign, map[string]any{
		"name": "yt main", "kind": "rtmp",
		"url": "rtmp://example.invalid/live", "streamKey": "k",
	})
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", map[string]any{
		"sourceId": onlySourceID(t, h, sign),
		"name":     "yt main", "kind": "rtmp",
		"url": "rtmp://example.invalid/live", "streamKey": "k",
	})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}

	for _, tc := range []struct {
		name string
		body any
	}{
		{"no body at all", nil},
		{"confirm explicitly false", map[string]any{"confirm": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/stop-all", tc.body)
			sign(r)
			w := do(t, h, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400. stop-all ends every live broadcast on "+
					"the install permanently, and an API caller that never confirmed must "+
					"not be able to trigger that: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "confirm") {
				t.Errorf("refusal %q does not say what the caller needs to do", w.Body.String())
			}
		})
	}

	// The positive case: with confirm true, the same route still works.
	r = jsonRequest(t, http.MethodPost, "/api/v1/destinations/stop-all",
		map[string]any{"confirm": true})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("confirmed stop-all: status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestStopAllRefusesMalformedJSON covers the decode-error branch of
// handleStopAllDestinations: a body that IS present but does not parse as
// bulkStopRequest must be answered with the decode error, distinctly from
// "you must confirm" -- a caller who sent garbage needs to be told the body
// was garbage, not that they forgot a field they may well have included.
func TestStopAllRefusesMalformedJSON(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	withOnlySource(t, h, sign, map[string]any{
		"name": "yt main", "kind": "rtmp",
		"url": "rtmp://example.invalid/live", "streamKey": "k",
	})

	r := httptest.NewRequest(http.MethodPost, "/api/v1/destinations/stop-all",
		strings.NewReader("{not valid json"))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.5:44444"
	sign(r)

	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a malformed body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid request body") {
		t.Errorf("refusal %q does not say the body itself was the problem", w.Body.String())
	}
}

// TestStopAllRefusesAnOversizedBody covers readJSONBody's own guard: a body
// past the 1MiB ceiling must fail the READ, before anything is decoded or any
// confirm field is even looked at.
func TestStopAllRefusesAnOversizedBody(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	withOnlySource(t, h, sign, map[string]any{
		"name": "yt main", "kind": "rtmp",
		"url": "rtmp://example.invalid/live", "streamKey": "k",
	})

	oversized := bytes.Repeat([]byte(" "), (1<<20)+16)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/destinations/stop-all",
		bytes.NewReader(oversized))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.5:44444"
	sign(r)

	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a body over the size ceiling: %s", w.Code, w.Body.String())
	}
}

// Finding #3, poka-yoke audit 2026-08-21: start-all and stop-all used to be
// one bare boolean apart. This does not (and cannot) catch a future author
// writing the wrong named constant at the call site, but it does pin the
// observable contract the type exists to protect: POST .../start-all always
// enables and POST .../stop-all (once confirmed) always disables, so a
// reviewer diffing the two one-line handlers is checking a fact this test
// enforces, not trusting the diff by eye. MUTATION: swap bulkStart and
// bulkStop in the two handler bodies and this fails, because "start-all"
// would then report bulkStopped/bulkFailed instead of bulkStarted.
func TestBulkActionIsNotTransposedBetweenStartAndStop(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", map[string]any{
		"sourceId": onlySourceID(t, h, sign),
		"name":     "only dest", "kind": "rtmp",
		"url": "rtmp://example.invalid/live", "streamKey": "k",
	})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}

	callAndOutcome := func(path string, body any) bulkOutcome {
		t.Helper()
		r := jsonRequest(t, http.MethodPost, path, body)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
		}
		var got bulkAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("POST %s: decode: %v", path, err)
		}
		if len(got.Results) != 1 {
			t.Fatalf("POST %s: %d rows, want 1", path, len(got.Results))
		}
		return got.Results[0].Outcome
	}

	if got := callAndOutcome("/api/v1/destinations/start-all", nil); got != bulkStarted {
		t.Errorf("start-all reported outcome %q, want %q -- this is the bug: the boolean "+
			"reached bulkSetDestinationsEnabled backwards", got, bulkStarted)
	}
	if got := callAndOutcome("/api/v1/destinations/stop-all",
		map[string]any{"confirm": true}); got != bulkStopped {
		t.Errorf("stop-all reported outcome %q, want %q -- this is the bug: the boolean "+
			"reached bulkSetDestinationsEnabled backwards", got, bulkStopped)
	}
}

// withBulkPacing lowers the start gap for a test that needs several
// destinations started but is not measuring the gap, and restores it after.
//
// Safe because nothing in this package calls t.Parallel -- verified, zero call
// sites -- so no two tests are reading this at once. If that ever changes, this
// becomes a race and the detector will say so, which is the right way for it to
// be found.
func withBulkPacing(t *testing.T, d time.Duration) {
	t.Helper()
	prev := bulkStartPacing
	bulkStartPacing = d
	t.Cleanup(func() { bulkStartPacing = prev })
}
