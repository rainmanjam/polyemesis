package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// POST /alerts/rules/{id}/test is the one route in the alerting subsystem that
// makes an outbound call on demand, and the handler's own comment states the
// property that matters:
//
//	It reads the rule from the store rather than taking one from the body, so
//	the URL under test is the URL that will really be used -- a test that passes
//	against a URL the client supplied proves nothing about the stored one.
//
// Nothing checked that. A handler that took the endpoint from the request body
// would answer 200 {"status":"sent"}, the delivery would arrive, the operator
// would see a green tick, and the stored webhook -- the one every real alert
// goes to -- would never have been exercised. The "send test" button would be
// certifying a URL that exists only in the browser.
//
// The other half is the status. A failing endpoint is 502, not 500: the failure
// is the OPERATOR'S endpoint refusing the message, not this process breaking,
// and the UI wording depends on which of those it is being told.
//
// eng().Alerts() is non-nil in this fixture -- alerts.New runs in engine.New --
// so the 503 "the alert notifier is not running" branch cannot make any of this
// vacuous. apitailReached asserts that explicitly rather than assuming it.

// apitailRecorder is an endpoint that answers and remembers. Failure cases are
// built on a server that ANSWERS rather than on an unreachable port: the
// notifier retries transport errors with a 1s/2s/4s backoff, and a closed port
// costs seven seconds per case for no extra coverage.
type apitailRecorder struct {
	mu     sync.Mutex
	paths  []string
	status int
}

func apitailEndpoint(t *testing.T, status int) (*httptest.Server, *apitailRecorder) {
	t.Helper()
	rec := &apitailRecorder{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.mu.Unlock()
		w.WriteHeader(rec.status)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *apitailRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// The path segment is an ordinary distinctive word, deliberately NOT the
// Slack-shaped /services/T0.../B1.../<token> a real webhook carries: gitleaks
// scans this PR's own commits and a plausible-looking token in a fixture is a
// build failure waiting to happen. The rule's `format` is still "slack", which
// is what actually exercises the Slack encoder.
const (
	apitailStoredPath = "/marmalade"
	apitailDecoyPath  = "/decoy-the-client-made-up"
)

func TestTestingAnAlertRuleDeliversToTheStoredURLNotTheOneInTheBody(t *testing.T) {
	h, store, sign := sourceServer(t)

	// One attempt, so a failing endpoint answers now rather than after the
	// 1s + 2s + 4s retry ladder. This is the real production knob --
	// db.AlertSettings.RetryAttempts, applied by ApplyAlertSettings -- not a
	// test-only hook, so the code path under test is the shipped one.
	serverUnderTest(t, h).mgr.SetAlertRetry(1)

	stored, storedRec := apitailEndpoint(t, http.StatusOK)
	decoy, decoyRec := apitailEndpoint(t, http.StatusOK)

	created := createRule(t, h, sign, map[string]any{
		"name": "ops", "url": stored.URL + apitailStoredPath, "format": "slack",
	})
	id := int64(created["id"].(float64))
	route := "/api/v1/alerts/rules/" + strconv.FormatInt(id, 10) + "/test"

	// The body names a DIFFERENT endpoint. A handler that honoured it would
	// look identical from the outside: 200, {"status":"sent"}, a delivery
	// arriving somewhere.
	r := jsonRequest(t, http.MethodPost, route, map[string]any{
		"name": "ops", "url": decoy.URL + apitailDecoyPath, "format": "slack",
	})
	sign(r)
	w := do(t, h, r)
	apitailReached(t, w, "the session principal", "POST "+route)
	if w.Code != http.StatusOK {
		t.Fatalf("POST %s: status %d, want 200. Body: %s", route, w.Code, w.Body.String())
	}
	var out map[string]string
	decodeInto(t, w.Body.Bytes(), &out)
	if out["status"] != "sent" {
		t.Errorf("POST %s answered %v, want {\"status\":\"sent\"} -- the string "+
			"the console turns into its green tick", route, out)
	}

	// The assertion the status cannot make: the delivery arrived at the STORED
	// endpoint.
	if got := storedRec.seen(); len(got) != 1 || got[0] != apitailStoredPath {
		t.Errorf("the test alert did not arrive at the stored endpoint.\n"+
			"  stored path:   %q\n  arrived there: %v\n"+
			"The handler reads the rule from the store precisely so that the URL "+
			"under test is the URL real alerts will use.",
			apitailStoredPath, got)
	}
	if got := decoyRec.seen(); len(got) != 0 {
		t.Errorf("the test alert arrived at the endpoint named in the REQUEST BODY: %v.\n"+
			"The \"send test\" button would then certify a URL that exists only in "+
			"the browser, while the stored webhook every real alert goes to was "+
			"never exercised.", got)
	}

	// The rule row is unchanged by having been tested. A test that quietly
	// rewrote the rule from its own body would be the same bug seen from the
	// store side.
	rule, err := store.GetAlertRule(id)
	if err != nil {
		t.Fatalf("GetAlertRule: %v", err)
	}
	if rule.URL != stored.URL+apitailStoredPath {
		t.Errorf("testing the rule rewrote its endpoint.\n  before: %q\n  after:  %q",
			stored.URL+apitailStoredPath, rule.URL)
	}
	if rule.Name != "ops" {
		t.Errorf("testing the rule changed its name to %q", rule.Name)
	}
}

func TestAnAlertEndpointThatFailsIsABadGatewayNotAServerError(t *testing.T) {
	h, _, sign := sourceServer(t)
	serverUnderTest(t, h).mgr.SetAlertRetry(1)

	broken, brokenRec := apitailEndpoint(t, http.StatusInternalServerError)

	created := createRule(t, h, sign, map[string]any{
		"name": "broken", "url": broken.URL + apitailStoredPath, "format": "slack",
	})
	id := int64(created["id"].(float64))
	route := "/api/v1/alerts/rules/" + strconv.FormatInt(id, 10) + "/test"

	r := jsonRequest(t, http.MethodPost, route, nil)
	sign(r)
	w := do(t, h, r)
	apitailReached(t, w, "the session principal", "POST "+route)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("an endpoint answering HTTP 500 produced status %d, want 502.\n"+
			"502 and 500 say different things to the operator: 502 is \"your "+
			"webhook refused the message\", 500 is \"polyemesis broke\", and only "+
			"one of those is worth restarting the box over. Body: %s",
			w.Code, w.Body.String())
	}
	msg := mustJSONError(t, h, sign, http.MethodPost, route, nil, http.StatusBadGateway)
	if msg != "webhook returned HTTP 500" {
		t.Errorf("the 502 body said %q; the operator needs the status their own "+
			"endpoint returned, not a generic failure", msg)
	}
	// Two requests were made above; both must have been attempted exactly once
	// each, which is what proves the outbound call really happened rather than
	// the handler short-circuiting on some stored state.
	if got := brokenRec.seen(); len(got) != 2 {
		t.Errorf("the broken endpoint was contacted %d times (%v), want 2 -- one "+
			"per request, with the retry budget set to 1", len(got), got)
	}
}
