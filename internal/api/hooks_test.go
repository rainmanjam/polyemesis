package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// A hook endpoint holds two secrets where an alert rule holds one: the URL
// (whose path is a capability) and the signing key (which lets anybody who has
// it forge deliveries). Both are easy to regress with an innocent change to a
// response struct, so every route that can return a hook is checked.

const hookURL = "https://ci.example.com/build/XXXXsecretXXXX"

func createHook(t *testing.T, h http.Handler, sign func(*http.Request), body map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/hooks", body, http.StatusCreated), &out)
	return out
}

func TestCreateReturnsThePlaintextSecretExactlyOnce(t *testing.T) {
	h, _, sign := sourceServer(t)

	created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": hookURL})
	secret, _ := created["secret"].(string)
	if secret == "" {
		t.Fatal("no plaintext secret in the create response; the operator has " +
			"nothing to paste into their receiver and no way to get it later")
	}
	id := int64(created["id"].(float64))

	// And never again, on any route.
	for _, p := range []string{
		"/api/v1/hooks",
		"/api/v1/hooks/" + strconv.FormatInt(id, 10),
	} {
		body := string(send(t, h, sign, http.MethodGet, p, nil, http.StatusOK))
		if strings.Contains(body, secret) {
			t.Errorf("%s re-issued the signing secret:\n%s", p, body)
		}
		if strings.Contains(body, "XXXXsecretXXXX") {
			t.Errorf("%s echoed the endpoint path:\n%s", p, body)
		}
		if !strings.Contains(body, alerts.Mask) {
			t.Errorf("%s returned no masked endpoint; the UI has nothing to show:\n%s", p, body)
		}
		if !strings.Contains(body, `"hasSecret":true`) {
			t.Errorf("%s does not say whether the hook is signed:\n%s", p, body)
		}
	}
}

// hookServer is sourceServer with a live dispatcher attached.
//
// The /test route makes a real outbound call, and a nil dispatcher answers 503
// before it gets there -- so the two tests that prove the STORED url is used
// would be asserting on the absence of a dispatcher rather than on the url.
// Dispatcher.Test is synchronous and takes the hook directly, so it needs
// neither a running Run loop nor a real Source.
//
// lastTestServer is how this package's helpers already reach the constructed
// Server; setting the field is the same package, not an export.
func hookServer(t *testing.T) (http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()
	h, store, sign := sourceServer(t)
	lastTestServer.hooks = hooks.NewDispatcher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		hooks.SourceFunc(func() ([]hooks.Hook, error) { return nil, nil }),
	)
	return h, store, sign
}

func TestUpdatingAHookWithTheMaskedURLKeepsTheRealOne(t *testing.T) {
	// The same trap alert rules have: every form renders the only URL it was
	// given -- the masked one -- and submits it back untouched. Storing that
	// would point the hook at a URL that has never existed, and firing would
	// stop with no error anywhere.
	//
	// THE DISCRIMINATOR CHANGED, and the reason is worth reading before
	// changing it back. This test used to fire a test delivery at an
	// unresolvable host and assert that alerts.Mask did NOT appear in the 502
	// body: a hook whose URL had become "https://ci.example.com/[redacted]"
	// produced a *url.Error quoting that string, mask and all.
	//
	// #160 made that proxy blind. Dispatcher.Test now returns the error through
	// alerts.ClientErrorText, which masks the endpoint PATH on purpose --
	// because net/http's wrapper quotes the whole URL and a Slack-shaped hook
	// keeps its entire credential there. So the masked-URL case and the
	// real-URL case now render IDENTICALLY, both as
	// "Post https://ci.example.com/[redacted]: ...". The old assertion did not
	// start failing because the behaviour regressed; it started failing because
	// it was measuring the leak it depended on.
	//
	// The replacement is stronger than what it replaces: the hook points at a
	// REAL server on a secret path, and the assertion is that the delivery
	// ARRIVED THERE. A hook pointed at the mask cannot arrive at all, and no
	// amount of error-text redaction can make a request that was never made
	// look like one that was.
	h, _, sign := hookServer(t)

	const secretPath = "/build/XXXXsecretXXXX"
	arrived := make(chan string, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()

	created := createHook(t, h, sign, map[string]any{
		"name": "deploy", "url": endpoint.URL + secretPath,
	})
	id := int64(created["id"].(float64))
	path := "/api/v1/hooks/" + strconv.FormatInt(id, 10)

	var shown map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &shown)
	masked, _ := shown["url"].(string)
	if !strings.Contains(masked, alerts.Mask) {
		t.Fatalf("url = %q, expected a masked one", masked)
	}
	if strings.Contains(masked, secretPath) {
		t.Fatalf("url = %q still carries the path, so the round trip below proves nothing", masked)
	}
	send(t, h, sign, http.MethodPut, path, map[string]any{
		"name": "renamed", "url": masked,
	}, http.StatusOK)

	// Behaviour, not the column: a test delivery goes to the STORED url.
	send(t, h, sign, http.MethodPost, path+"/test", nil, http.StatusOK)

	select {
	case got := <-arrived:
		if got != secretPath {
			t.Fatalf("the delivery arrived at %q, want %q: the stored URL was rewritten "+
				"by the masked value the form submitted back", got, secretPath)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no delivery arrived at the endpoint at all, so the stored URL is not the " +
			"one that was created -- which is exactly the regression this test exists for")
	}
}

func TestTestDeliveryReturnsWhatTheEndpointSaid(t *testing.T) {
	// An operator testing a hook is testing a machine contract. "sent" tells
	// them nothing about whether their signature verification agrees, so the
	// response carries the exact body and signature that were sent.
	h, _, sign := hookServer(t)
	created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": hookURL})
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)

	// hookURL does not resolve, so this is the unreachable path -- and the
	// response must still say what was attempted rather than only "failed".
	var out map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/hooks/"+id+"/test", nil,
		http.StatusBadGateway), &out)
	msg, ok := out["error"].(string)
	if !ok {
		t.Fatalf("no error explaining the failure: %v", out)
	}
	// AND THE PATH IS NOT IN IT. This assertion is one line and it was missing,
	// on a test already holding the exact response that would have carried the
	// leak. Dispatcher.Test redacts correctly today -- but both of its redacting
	// calls could be deleted and every package still passed, while the two
	// structurally identical twins (Dispatcher.deliver and the alerts notifier)
	// each fail a named test when mutated the same way. A redaction whose only
	// proof is that someone wrote it is the shape this PR spent five defects
	// removing; the asymmetry is what made it worth closing rather than noting.
	//
	// The URL PATH is the secret: a webhook endpoint's path is frequently the
	// whole credential, which is why hookURL ends in one.
	if strings.Contains(msg, "XXXXsecretXXXX") {
		t.Errorf("POST /hooks/{id}/test handed back the endpoint path, which IS the "+
			"credential for most webhook providers: %s", msg)
	}
}

func TestHooksMetaListsEveryTrigger(t *testing.T) {
	h, _, sign := sourceServer(t)
	body := string(send(t, h, sign, http.MethodGet, "/api/v1/hooks/meta", nil, http.StatusOK))
	for _, want := range []string{
		"ingest.published", "ingest.disconnected",
		"destination.up", "destination.down",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("meta is missing %s; the editor cannot offer a trigger it "+
				"is not told about:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"test"`) {
		t.Errorf("meta offers the test trigger as subscribable:\n%s", body)
	}
}

// countingDoer answers every delivery with 200 so a DeliveryRecord exists for
// the route below to serve. A stub rather than a listener: the assertion is
// about what the API returns, not about HTTP.
type countingDoer struct{}

func (countingDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("thanks")),
		Header:     http.Header{},
	}, nil
}

// The delivery log is the answer to "did my hook fire", so the route has to
// return a hook's actual attempts.
//
// This asserted only http.StatusOK, under the name TestDeliveriesRouteExists.
// api.go registers the embedded SPA as the root mux's NotFound handler, so an
// unrouted /api/v1/... path is answered by web.Handler with index.html and a
// 200 -- measured: commenting out `r.Get("/hooks/{id}/deliveries", ...)` left
// the old test passing. An empty JSON array would not have been enough either:
// that is exactly what the nil-dispatcher branch returns, so a route serving
// nothing and a route serving no records were indistinguishable.
//
// Mutation: comment out `r.Get("/hooks/{id}/deliveries", s.handleHookDeliveries)`
// in api.go. Observed FAIL on a committed tree, in both UI configurations --
// with internal/web/dist/index.html present (the SPA answers 200 with HTML,
// which does not decode) and with it absent, as CI runs (the fallback answers
// 404 "UI not built").
func TestDeliveriesRouteReturnsTheAttemptsItMade(t *testing.T) {
	h, _, sign := sourceServer(t)
	created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": hookURL})
	id := int64(created["id"].(float64))
	path := "/api/v1/hooks/" + strconv.FormatInt(id, 10) + "/deliveries"

	// A live dispatcher with one running worker, keyed to the hook that was just
	// created, so Deliveries(id) has something real to report.
	d := hooks.NewDispatcher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		hooks.SourceFunc(func() ([]hooks.Hook, error) {
			return []hooks.Hook{hooks.Hook{
				ID: id, Name: "deploy", Enabled: true,
				URL: "https://example.com/h", Secret: "s3cr3t",
			}.Normalized()}, nil
		}),
		hooks.WithDoer(countingDoer{}),
		hooks.WithReloadInterval(5*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)
	lastTestServer.hooks = d

	d.Publish(hooks.Event{
		Trigger: hooks.TriggerIngestPublished,
		Source:  hooks.SourceRef{ID: 1, Name: "Main"},
	})

	// Polled through the ROUTE, so the wait is on what an operator would see.
	var got []hooks.DeliveryRecord
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := jsonRequest(t, http.MethodGet, path, nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, want 200: %.80s", path, w.Code, w.Body.String())
		}
		got = nil
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("GET %s did not return a JSON array (%v); the SPA fallback "+
				"answered instead of the deliveries route: %.80s", path, err, w.Body.String())
		}
		if len(got) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("deliveries = %+v, want the one attempt that was made", got)
	}
	rec := got[0]
	if rec.HookID != id {
		t.Errorf("hookId = %d, want %d: the route served some other hook's log", rec.HookID, id)
	}
	if rec.Trigger != hooks.TriggerIngestPublished {
		t.Errorf("trigger = %q, want ingest.published", rec.Trigger)
	}
	if rec.Status != http.StatusOK || rec.Attempts != 1 {
		t.Errorf("status=%d attempts=%d, want 200 in one attempt", rec.Status, rec.Attempts)
	}
	if rec.ID == "" {
		t.Error("no delivery id; the operator cannot match this row against their receiver's log")
	}

	// And a non-numeric id is a 400 from the handler -- a status neither the SPA
	// nor the "UI not built" fallback can produce, so this pins the route in
	// either configuration even if the log were empty.
	var bad map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/hooks/abc/deliveries", nil,
		http.StatusBadRequest), &bad)
	if _, ok := bad["error"]; !ok {
		t.Errorf("no error field explaining the 400: %v", bad)
	}
}
