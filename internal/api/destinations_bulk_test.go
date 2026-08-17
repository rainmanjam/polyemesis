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
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// bulkAnswer mirrors the response body.
type bulkAnswer struct {
	Action  string           `json:"action"`
	Results []bulkDestResult `json:"results"`
}

func TestBulkStartAndStopReportOneRowPerDestination(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	names := []string{"youtube", "twitch", "backup"}
	ids := make(map[int64]string, len(names))
	for _, name := range names {
		r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", map[string]any{
			"name": name, "kind": "rtmp",
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
		r := jsonRequest(t, http.MethodPost, path, nil)
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
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/stop-all", nil)
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
			"name": "dest" + itoa(int64(i)), "kind": "rtmp",
			"url": "rtmp://example.invalid/live", "streamKey": "k",
		})
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusOK && w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
	}

	timed := func(path string) time.Duration {
		t.Helper()
		r := jsonRequest(t, http.MethodPost, path, nil)
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
