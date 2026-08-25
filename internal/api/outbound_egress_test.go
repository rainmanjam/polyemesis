package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// THE TWO PRINCIPAL-LESS OUTBOUND PAYLOAD EGRESSES (#169).
//
// The coverage ledger's shape list is (method, pattern, SHAPE) because the two
// things that escaped the previous audit were both shapes rather than routes.
// It already carries mqtt-retained-topic: a payload this process sends OUTWARD,
// to a party this process chose, with NO PRINCIPAL to redact for and never any.
// Two more egresses of exactly that shape were absent from it --
// internal/hooks.Dispatcher's webhook POST and internal/alerts.Notifier's alert
// POST -- and "absent from the ledger" is the finding, not "leaking today".
//
// A principal-less egress cannot be covered by the read-token value sweep,
// which is the whole of the rest of this ledger's method: there is no read
// token, no admin, no differential to draw. The sweep's question -- "does this
// principal receive what that one does not" -- has no meaning here. So the
// question these two ask instead is the one the argv leak answered wrongly:
// DOES A STORED CREDENTIAL REACH THE WIRE AT ALL, for any principal, including
// none.
//
// EACH OF THESE OPENS WITH A POSITIVE CONTROL, and that is not decoration. Both
// bodies below are scanned with allSentinels(), which is an ABSENCE check --
// and a capture that recorded nothing, an endpoint that was never reached, or a
// delivery that silently failed would satisfy an absence check having read zero
// bytes. That is the vacuous-guard shape this repository has shipped nine times.
// The control is a marker planted in the one field the payload is BUILT from,
// asserted to arrive, before anything is asserted absent.

// egressRecorder captures the whole of what arrived, which is what a shape
// inspection needs and what the existing alert and hook fixtures do not do:
// apitailEndpoint records r.URL.Path, and the hook fixture in hooks_test.go
// records r.URL.Path. A path is not a payload.
// egressCapture keeps the header block and the body APART, because the two
// halves are read for different things: the sentinel sweep wants everything
// concatenated, and the shape inspectors have to PARSE the body to say what
// shape they witnessed.
type egressCapture struct{ header, body string }

type egressRecorder struct {
	mu     sync.Mutex
	got    []egressCapture
	status int
}

func egressEndpoint(t *testing.T) (*httptest.Server, *egressRecorder) {
	t.Helper()
	rec := &egressRecorder{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The HEADERS travel with it and are part of the same egress: the
		// signature header is derived from the hook's secret, and a scheme that
		// sent the secret instead of a MAC over it would put a stored credential
		// on the wire in a place a body scan never looks.
		var hdr strings.Builder
		for k, vs := range r.Header {
			for _, v := range vs {
				hdr.WriteString(k + ": " + v + "\n")
			}
		}
		rec.mu.Lock()
		rec.got = append(rec.got, egressCapture{header: hdr.String(), body: string(body)})
		rec.mu.Unlock()
		w.WriteHeader(rec.status)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// all returns everything that has arrived so far, headers included, joined, so
// one scan covers every attempt including the retries.
func (r *egressRecorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, c := range r.got {
		b.WriteString(c.header)
		b.WriteString(c.body)
		b.WriteString("\n")
	}
	return b.String()
}

// lastBody is the most recent payload alone, for a caller that has to parse it.
func (r *egressRecorder) lastBody() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.got) == 0 {
		return ""
	}
	return r.got[len(r.got)-1].body
}

// lastHeaders is the header block of the most recent payload.
func (r *egressRecorder) lastHeaders() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.got) == 0 {
		return ""
	}
	return r.got[len(r.got)-1].header
}

func (r *egressRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

// waitForEgress blocks until n payloads have arrived, and FAILS rather than
// letting an absence check run against an empty capture.
func waitForEgress(t *testing.T, rec *egressRecorder, n int, what string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if rec.count() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s: %d payloads arrived, wanted %d. Every assertion after this point "+
		"is an absence check, and an absence check against bytes that never arrived "+
		"is the vacuous guard this ledger exists to refuse.", what, rec.count(), n)
}

// assertNoStoredCredentialOnTheWire is the shape inspection itself.
func assertNoStoredCredentialOnTheWire(t *testing.T, egress, payload string) {
	t.Helper()
	if strings.TrimSpace(payload) == "" {
		t.Fatalf("%s: the captured payload is empty", egress)
	}
	for _, secret := range allSentinels() {
		if strings.Contains(payload, secret) {
			t.Errorf("%s put a stored credential on the wire: %s\n"+
				"This is a PRINCIPAL-LESS egress -- there is nobody to redact for and "+
				"no scope that could refuse it -- so the credential has to not be in "+
				"the payload at all. See #169 and the mqtt-retained-topic row it is "+
				"modelled on.\npayload: %s", egress, secret, payload)
		}
	}
}

// TestOutboundHookPayloadCarriesNoStoredCredential inspects
// internal/hooks.Dispatcher's outbound POST -- the shape at dispatch.go's
// deliver, which #169 records as structurally identical to the retained MQTT
// topic that the ledger already lists.
//
// THE REAL DELIVERY PATH, not Dispatcher.Test. Test takes a hook straight from
// the caller and is synchronous; deliver is the one that runs from the intake
// queue, builds the Envelope, compiles the hook's own secret set and retries.
// The two build their bodies the same way and only one of them is what an
// operator's receiver ever sees.
func TestOutboundHookPayloadCarriesNoStoredCredential(t *testing.T) {
	h, _, sign := plantedServer(t)
	endpoint, rec := egressEndpoint(t)

	// A hook created through the real route, so the URL under test is a stored
	// one. The path is a capability in a Slack-shaped webhook, which is why
	// EndpointSecrets treats it as a credential; this one is an ordinary word,
	// because gitleaks scans this branch and a plausible token in a fixture is a
	// build failure waiting to happen.
	const hookPath = "/receiver/marmalade"
	created := createHook(t, h, sign, map[string]any{
		// Local endpoint on purpose; takes the SSRF opt-in rather than
		// weakening the guard. See poka-yoke audit #4.
		"name": "ledger-egress", "url": endpoint.URL + hookPath,
		"allowPrivateTarget": true,
	})
	id := int64(created["id"].(float64))
	secret, _ := created["secret"].(string)
	if secret == "" {
		t.Fatal("no signing secret was minted, so the header assertion below has nothing to check")
	}

	d := hooks.NewDispatcher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		hooks.SourceFunc(func() ([]hooks.Hook, error) {
			return []hooks.Hook{hooks.Hook{
				ID: id, Name: "ledger-egress", Enabled: true,
				URL: endpoint.URL + hookPath, Secret: secret,
				MaxAttempts: 1, TimeoutSeconds: 5,
				// Built directly rather than through the API, so it must carry
				// the opt-in itself or safeDialContext refuses the dial.
				AllowPrivateTarget: true,
			}.Normalized()}, nil
		}),
		hooks.WithReloadInterval(5*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)
	serverUnderTest(t, h).hooks = d

	// (1) THE POSITIVE CONTROL. A marker in the field the payload is built from,
	// asserted to ARRIVE. alerts.Redact masks URL-shaped credentials and leaves
	// a bare word alone, so this reaches the wire -- which is the point: this
	// egress carries PROCESS STATE VERBATIM, and #169's "what would make it
	// unsafe" is exactly that. Without this line, everything below is an
	// absence check over a capture that may never have been written to.
	const marker = "EGRESS-CONTROL-hook-3f21"
	d.Publish(hooks.Event{
		Trigger: hooks.TriggerIngestPublished,
		Source:  hooks.SourceRef{ID: 1, Name: "Main"},
		Reason:  marker,
	})
	waitForEgress(t, rec, 1, "the hook endpoint")
	if !strings.Contains(rec.all(), marker) {
		t.Fatalf("the control marker %q did not reach the hook endpoint, so the "+
			"capture is not reading this egress and no absence check below means "+
			"anything.\ncaptured: %s", marker, rec.all())
	}

	// (2) AND NOW THE THING ITSELF: a destination error carrying the planted
	// publish URL with the stream key on the end, which is the exact shape
	// FFmpeg's stderr arrives in and the exact shape that put the argv on the
	// MQTT topic. Redacted on the way IN, at Dispatcher.Publish, which is the
	// property this asserts from the far end of the wire rather than from
	// beside the call.
	d.Publish(hooks.Event{
		Trigger:     hooks.TriggerDestinationDown,
		Source:      hooks.SourceRef{ID: 1, Name: "Main"},
		Destination: &hooks.DestinationRef{ID: 1, Name: "twitch"},
		Error: "rtmp://ingest.example/app/" + sentinelDestKey +
			": Broken pipe (Connection reset by peer)",
		Reason: "the child exited: rtmp://ingest.example/app/" + sentinelDestKey,
	})
	waitForEgress(t, rec, 2, "the hook endpoint after the error event")

	payload := rec.all()
	assertNoStoredCredentialOnTheWire(t, "the outbound hook payload", payload)

	// (3) THE HOOK'S OWN SECRET IS NOT ON THE WIRE EITHER. It is a stored
	// credential like any other -- anybody holding it can forge deliveries -- and
	// the signature header exists precisely so the secret does not have to
	// travel. A scheme that sent it instead of a MAC over the body would leave
	// every body scan clean and every receiver forgeable.
	if strings.Contains(payload, secret) {
		t.Errorf("the outbound hook payload carries the hook's own signing secret, "+
			"which the signature header exists to avoid sending.\npayload: %s", payload)
	}
	if !strings.Contains(strings.ToLower(payload), "signature") {
		t.Errorf("no signature header reached the receiver, so (3) above is passing "+
			"because nothing was signed rather than because the secret stayed "+
			"home.\npayload: %s", payload)
	}
}

// TestOutboundAlertPayloadCarriesNoStoredCredential inspects
// internal/alerts.Notifier's outbound POST -- the second of #169's two.
//
// DRIVEN THROUGH POST /alerts/rules/{id}/test, which is Notifier.Test, which
// calls the same n.post -> n.attempt that the coalescing deliver path calls
// with the same encoder and the same request builder. What that route does NOT
// exercise is the COALESCED body: deliver's Items list is built from events the
// engine raised, and Test's is one synthetic item. That residual is stated in
// the shape row's note rather than papered over -- see emittedShapes. What is
// covered is the egress: real bytes, over a real socket, out of the shipped
// encoder, on a server whose every credential column holds a sentinel.
//
// The alerter is constructed inside engine.New with no seam for a stub client
// (see hook_disclosure_test.go), which is why this goes through the route
// rather than building a Notifier the way the hook test builds a Dispatcher.
func TestOutboundAlertPayloadCarriesNoStoredCredential(t *testing.T) {
	h, store, sign := plantedServer(t)
	endpoint, rec := egressEndpoint(t)

	// One attempt, through the real production knob, so a delivery that fails
	// answers now rather than after the 1s + 2s + 4s ladder.
	serverUnderTest(t, h).mgr.SetAlertRetry(1)

	// THE POSITIVE CONTROL IS THE RULE NAME, and it is load-bearing rather than
	// cosmetic: Notifier.Test builds the alert text as "If you can read this,
	// <rule name> is wired up correctly", so a marker in the name is a marker in
	// the payload. If it does not arrive, the capture is not reading this
	// egress and every absence check below is vacuous.
	const marker = "EGRESS-CONTROL-alert-3f21"
	created := createRule(t, h, sign, map[string]any{
		"name": marker, "url": endpoint.URL + "/receiver/marmalade", "format": "slack",
		// The shortest coalescing window the rule form allows. Rule.Debounce
		// defaults a zero to ten seconds, and ten seconds of CI per platform to
		// observe a property that has nothing to do with the window is a cost
		// this test should not impose.
		"debounceSeconds": 1,
	})
	id := int64(created["id"].(float64))
	send(t, h, sign, http.MethodPost,
		"/api/v1/alerts/rules/"+strconv.FormatInt(id, 10)+"/test", nil, http.StatusOK)

	waitForEgress(t, rec, 1, "the alert endpoint")
	if !strings.Contains(rec.all(), marker) {
		t.Fatalf("the control marker %q did not reach the alert endpoint, so the "+
			"capture is not reading this egress.\ncaptured: %s", marker, rec.all())
	}

	// AND THEN THE COALESCED PATH, which is the one every real alert takes and
	// the one Notifier.Test does not exercise: Test builds one synthetic Item
	// from a fixed sentence, while deliver's Items come from events the engine
	// raised. Only the second can carry process state, and process state is
	// #169's whole worry.
	//
	// A notifier built here rather than the engine's. e.alerter is constructed
	// inside engine.New with no seam and its Run loop is not driven by this
	// fixture -- measured: the published event never left. It reads the SAME
	// store the route above wrote the rule to, so the rule under test is still a
	// stored one and the encoder, the request builder and Event.Redacted are all
	// the shipped ones.
	rules, err := store.AlertRules()
	if err != nil || len(rules) == 0 {
		t.Fatalf("the rule created through the route is not in the store (%v, %d rules); "+
			"the notifier below would have nothing to deliver to", err, len(rules))
	}
	n := alerts.New(
		slog.New(slog.NewTextHandler(io.Discard, nil)), store,
		alerts.WithFlushInterval(5*time.Millisecond),
		alerts.WithRetry(1, time.Millisecond, time.Millisecond),
	)
	nctx, ncancel := context.WithCancel(context.Background())
	t.Cleanup(ncancel)
	go n.Run(nctx)

	// The destination's publish URL with the stream key on the end: the shape
	// FFmpeg's stderr arrives in, and the shape that put the argv on the MQTT
	// topic. The marker rides along so the control and the assertion read the
	// same bytes.
	const coalescedMarker = "EGRESS-CONTROL-alert-coalesced-3f21"
	n.Publish(alerts.Event{
		Type: alerts.TypeDestinationDown, Severity: alerts.SeverityCritical,
		Key: "egress-ledger", Title: "twitch is down " + coalescedMarker,
		Text: "rtmp://ingest.example/app/" + sentinelDestKey +
			": Broken pipe (Connection reset by peer)",
	})
	waitForEgress(t, rec, 2, "the alert endpoint after a coalesced delivery")
	if !strings.Contains(rec.all(), coalescedMarker) {
		t.Fatalf("the coalesced delivery did not reach the endpoint, so the scan "+
			"below reads only the synthetic test body.\ncaptured: %s", rec.all())
	}

	assertNoStoredCredentialOnTheWire(t, "the outbound alert payload", rec.all())
}
