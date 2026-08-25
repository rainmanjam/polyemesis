package automod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// modelServer returns a stub API that responds however the test needs.
func modelServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// respondVerdict builds a well-formed OpenAI-shaped reply, with a category the
// parser accepts. A verdict without one is now discarded (#495), so the
// well-formed helper has to carry one or every test here would exercise the
// rejection path instead of the thing it is about.
func respondVerdict(abusive bool, confidence float64, reason string) http.HandlerFunc {
	return respondCategorised(abusive, confidence, string(CategoryHarassment), reason)
}

// respondCategorised is the same, with the category under the test's control.
func respondCategorised(abusive bool, confidence float64, category, reason string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inner, _ := json.Marshal(modelVerdict{
			Abusive: abusive, Confidence: confidence, Category: category, Reason: reason,
		})
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": string(inner)}},
			},
		})
	}
}

func testModel(t *testing.T, srv *httptest.Server, tweak func(*ModelConfig)) *Model {
	t.Helper()
	cfg := DefaultModelConfig()
	cfg.Enabled = true
	cfg.Endpoint = srv.URL
	cfg.Action = ActionDelete
	if tweak != nil {
		tweak(&cfg)
	}
	return NewModel(cfg)
}

// ------------------------------------------------------------- fail open

// THE contract. Every way the API can fail must let the message through.
// A moderation outage must not silence a chat -- the same rule the codebase
// holds for hardware detection: detection that could not run must never be the
// thing that stops your stream.
func TestModelFailsOpenOnEveryFailureMode(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"500", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}},
		{"rate limited", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}},
		{"unauthorised", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}},
		{"not json", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "<html>a proxy error page</html>")
		}},
		{"no choices", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"choices":[]}`)
		}},
		{"verdict is not the requested schema", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]string{"content": "I think this is fine, actually."}},
				},
			})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := modelServer(t, c.handler)
			m := testModel(t, srv, nil)

			findings, err := m.Check(context.Background(), "anything")
			if err == nil {
				t.Fatal("no error reported; the operator would never know")
			}
			if len(findings) != 0 {
				t.Fatalf("failed open with findings, which would ACT on a failure: %+v", findings)
			}
		})
	}
}

// A timeout is the commonest failure and must behave the same way.
func TestModelFailsOpenOnTimeout(t *testing.T) {
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		respondVerdict(true, 1.0, "abuse")(w, r)
	})
	m := testModel(t, srv, func(c *ModelConfig) { c.Timeout = 30 * time.Millisecond })

	findings, err := m.Check(context.Background(), "anything")
	if err == nil {
		t.Fatal("a timeout was not reported")
	}
	if len(findings) != 0 {
		t.Fatalf("a timeout produced findings: %+v", findings)
	}
}

// And through the engine, which is what a caller actually holds: an error must
// leave the verdict empty so nothing is acted on.
func TestEngineFailsOpenThroughToTheVerdict(t *testing.T) {
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	m := testModel(t, srv, nil)

	matrix := Matrix{Enabled: true}
	matrix.Set(Key{Platform: db.PlatformTwitch, Action: ActionDelete, Checker: CheckerModel}, true)
	e := New(matrix, PlatformCaps{}, nil, nil, m)

	v, err := e.CheckModel(context.Background(), db.PlatformTwitch, "anything")
	if err == nil {
		t.Fatal("the engine hid the failure")
	}
	if len(v.Act) != 0 {
		t.Fatalf("the engine would act despite a failed check: %+v", v.Act)
	}
}

// ------------------------------------------------- the endpoint's own key

// A transport failure must not carry the endpoint's credential out with it.
//
// Found by scripts/acceptance-automod.sh, which pointed the connector at a real
// host on a closed port with a sentinel in the query string and read back what
// the operator would see. Both surfaces had it verbatim: the error handed to
// internal/chat, which writes it to server.log once per message for as long as
// the endpoint is down, and ModelStats.LastError, which is the spend panel.
//
// The endpoint is free text an operator pastes, and internal/api's redact.go
// already masks it out of GET /settings for exactly this reason -- a proxied or
// self-hosted inference endpoint most often arrives as
// .../chat/completions?api_key=sk-..., and a key in a query string is still a
// key. That reasoning had been applied to the settings blob and nowhere else.
// This is #310's shape a second time: a refused far end writing a credential to
// the log on every attempt.
//
// Proven able to fail against the committed tree by making redactEndpoint
// return its argument unchanged.
func TestATransportFailureDoesNotCarryTheEndpointsCredential(t *testing.T) {
	const sentinel = "sk-sentinel-must-not-be-logged"

	// A server started and immediately closed, so the port is a real port that
	// really refuses. A hostname that does not resolve would fail earlier, in
	// the resolver, and would not exercise the same wrapping.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := srv.URL + "/v1/chat/completions?api_key=" + sentinel
	srv.Close()

	cfg := DefaultModelConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	m := NewModel(cfg)

	findings, err := m.Check(context.Background(), "synthetic text, never a real message")
	if err == nil {
		t.Fatal("the connection was not refused; this test measured nothing")
	}
	if len(findings) != 0 {
		t.Fatalf("a refused connection produced findings: %+v", findings)
	}

	// The vacuity guard. Every assertion below is "the sentinel is absent", and
	// all of them would hold if the request had never been built, if the
	// endpoint had been dropped on the floor, or if the error had come back
	// empty. So: the sentinel was really in what we configured, and the error
	// still names the host it failed to reach.
	if !strings.Contains(endpoint, sentinel) {
		t.Fatal("the fixture endpoint has no sentinel in it")
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if !strings.Contains(err.Error(), host) {
		t.Fatalf("the error does not name the endpoint that failed, so an operator "+
			"cannot tell which one stopped answering: %v", err)
	}

	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("the endpoint's key is in the error internal/chat logs: %v", err)
	}
	if got := m.Stats().LastError; strings.Contains(got, sentinel) {
		t.Fatalf("the endpoint's key is in ModelStats.LastError, which the spend panel shows: %q", got)
	}
}

// ------------------------------------------------------------ the model

func TestModelActsOnlyAboveTheConfidenceFloor(t *testing.T) {
	srv := modelServer(t, respondVerdict(true, 0.5, "maybe"))
	m := testModel(t, srv, func(c *ModelConfig) { c.MinConfidence = 0.8 })
	if f, _ := m.Check(context.Background(), "x"); len(f) != 0 {
		t.Fatalf("acted below the confidence floor: %+v", f)
	}

	srv2 := modelServer(t, respondVerdict(true, 0.95, "clear abuse"))
	m2 := testModel(t, srv2, func(c *ModelConfig) { c.MinConfidence = 0.8 })
	f, err := m2.Check(context.Background(), "x")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(f) != 1 || f[0].Checker != CheckerModel {
		t.Fatalf("no model finding above the floor: %+v", f)
	}
	if f[0].Confidence != 0.95 {
		t.Fatalf("confidence not carried through: %v", f[0].Confidence)
	}
	if f[0].Reason == "" {
		t.Fatal("no reason recorded; a decision nobody can account for is worse than none")
	}
}

func TestModelDoesNothingWhenDisabled(t *testing.T) {
	called := false
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		respondVerdict(true, 1, "abuse")(w, r)
	})
	m := testModel(t, srv, func(c *ModelConfig) { c.Enabled = false })
	if f, err := m.Check(context.Background(), "x"); len(f) != 0 || err != nil {
		t.Fatalf("a disabled model produced %+v, %v", f, err)
	}
	if called {
		t.Fatal("a disabled model still called the API -- that is a bill")
	}
}

// An unbounded per-message API call is a surprise invoice.
func TestModelStopsAtTheHourlyCeiling(t *testing.T) {
	calls := 0
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		respondVerdict(false, 1, "fine")(w, r)
	})
	m := testModel(t, srv, func(c *ModelConfig) { c.MaxCallsPerHour = 3 })

	for i := 0; i < 3; i++ {
		if _, err := m.Check(context.Background(), "x"); err != nil {
			t.Fatalf("call %d failed early: %v", i+1, err)
		}
	}
	if _, err := m.Check(context.Background(), "x"); err == nil {
		t.Fatal("the ceiling did not stop the fourth call")
	}
	if calls != 3 {
		t.Fatalf("made %d API calls despite a ceiling of 3", calls)
	}
	if s := m.Stats(); s.CallsThisHour != 3 || s.Ceiling != 3 {
		t.Fatalf("stats do not show the spend: %+v", s)
	}
}

// The ceiling is per hour, so it has to reset.
func TestTheCeilingResetsAfterAnHour(t *testing.T) {
	srv := modelServer(t, respondVerdict(false, 1, "fine"))
	m := testModel(t, srv, func(c *ModelConfig) { c.MaxCallsPerHour = 1 })
	clk := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clk }

	if _, err := m.Check(context.Background(), "x"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := m.Check(context.Background(), "x"); err == nil {
		t.Fatal("the ceiling did not apply")
	}
	clk = clk.Add(61 * time.Minute)
	if _, err := m.Check(context.Background(), "x"); err != nil {
		t.Fatalf("the ceiling did not reset after an hour: %v", err)
	}
}

// The key must not reach the operator-visible stats. It is stored encrypted and
// is never returned by an API or written to a log.
func TestTheAPIKeyIsNeverInTheStats(t *testing.T) {
	const secret = "sk-do-not-leak-me"
	srv := modelServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	m := testModel(t, srv, func(c *ModelConfig) { c.APIKey = secret })
	m.Check(context.Background(), "x")

	blob, _ := json.Marshal(m.Stats())
	if bytesContains(blob, secret) {
		t.Fatalf("the API key appears in the stats: %s", blob)
	}
	// And the config must not serialise it either.
	cfgBlob, _ := json.Marshal(m.cfg)
	if bytesContains(cfgBlob, secret) {
		t.Fatalf("the API key serialises with the config: %s", cfgBlob)
	}
}

func bytesContains(b []byte, s string) bool {
	return len(s) > 0 && len(b) >= len(s) && jsonIndex(string(b), s) >= 0
}

func jsonIndex(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// ------------------------------------------------------------ the engine

// The engine finds things regardless of the matrix; the matrix decides what is
// acted on. Keeping both is what lets a human review what was found even when
// nothing was automatic.
func TestEngineFindsButDoesNotActWhenTheCellIsOff(t *testing.T) {
	rules, err := NewRuleSet([]Rule{
		{ID: 1, Name: "term", Enabled: true, Pattern: `forbidden`, Action: ActionDelete},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := New(Matrix{Enabled: true}, PlatformCaps{}, rules, nil, nil)

	v := e.CheckFast(db.PlatformTwitch, "u1", "this is forbidden")
	if !v.Flagged() {
		t.Fatal("nothing was found at all")
	}
	if len(v.Act) != 0 {
		t.Fatalf("acted with the cell off: %+v", v.Act)
	}

	// Switch the one cell on and the same message becomes actionable.
	m := Matrix{Enabled: true}
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionDelete, Checker: CheckerRules}, true)
	e.SetMatrix(m)
	v = e.CheckFast(db.PlatformTwitch, "u2", "this is forbidden")
	if len(v.Act) != 1 {
		t.Fatalf("did not act with the cell on: %+v", v)
	}
}

// A rules finding and a history finding are separate evidence and must be
// gated separately, which is the whole point of the checker axis.
func TestCheckersAreGatedIndependently(t *testing.T) {
	rules, _ := NewRuleSet([]Rule{
		{ID: 1, Name: "term", Enabled: true, Pattern: `forbidden`, Action: ActionDelete},
	})
	limits := DefaultHistoryLimits()
	limits.MaxMessages = 1
	limits.Action = ActionDelete
	h := NewHistory(limits)

	m := Matrix{Enabled: true}
	// Rules may delete; history may not.
	m.Set(Key{Platform: db.PlatformTwitch, Action: ActionDelete, Checker: CheckerRules}, true)
	e := New(m, PlatformCaps{}, rules, h, nil)

	e.CheckFast(db.PlatformTwitch, "u1", "hello")
	v := e.CheckFast(db.PlatformTwitch, "u1", "forbidden and also flooding")

	if len(v.Findings) < 2 {
		t.Fatalf("expected findings from both checkers: %+v", v.Findings)
	}
	for _, f := range v.Act {
		if f.Checker == CheckerHistory {
			t.Fatalf("acted on a history finding whose cell is off: %+v", f)
		}
	}
	if len(v.Act) != 1 || v.Act[0].Checker != CheckerRules {
		t.Fatalf("expected exactly the rules finding to be actionable: %+v", v.Act)
	}
}

// A nil checker contributes nothing rather than panicking: an operator with no
// rules and the model off still gets the history detectors.
func TestNilCheckersAreSafe(t *testing.T) {
	e := New(DefaultMatrix(), PlatformCaps{}, nil, nil, nil)
	v := e.CheckFast(db.PlatformTwitch, "u1", "hello")
	if len(v.Findings) != 0 || len(v.Act) != 0 {
		t.Fatalf("nil checkers produced %+v", v)
	}
	if _, err := e.CheckModel(context.Background(), db.PlatformTwitch, "hello"); err != nil {
		t.Fatalf("a nil model errored: %v", err)
	}
}
