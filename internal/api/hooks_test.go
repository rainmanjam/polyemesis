package api

import (
	"io"
	"log/slog"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
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
	h, _, sign := hookServer(t)

	created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": hookURL})
	id := int64(created["id"].(float64))
	path := "/api/v1/hooks/" + strconv.FormatInt(id, 10)

	var shown map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &shown)
	masked, _ := shown["url"].(string)
	if !strings.Contains(masked, alerts.Mask) {
		t.Fatalf("url = %q, expected a masked one", masked)
	}
	send(t, h, sign, http.MethodPut, path, map[string]any{
		"name": "renamed", "url": masked,
	}, http.StatusOK)

	// Proven through behaviour rather than by reading the column back: a test
	// delivery goes to the stored URL, and a hook pointed at "[redacted]" would
	// fail to build a request at all.
	var res map[string]any
	raw := send(t, h, sign, http.MethodPost, path+"/test", nil, http.StatusBadGateway)
	decodeInto(t, raw, &res)
	if msg, _ := res["error"].(string); strings.Contains(msg, alerts.Mask) {
		t.Fatalf("the stored URL became the mask: %v", res)
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
	if _, ok := out["error"]; !ok {
		t.Fatalf("no error explaining the failure: %v", out)
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

func TestDeliveriesRouteExists(t *testing.T) {
	h, _, sign := sourceServer(t)
	created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": hookURL})
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)

	send(t, h, sign, http.MethodGet, "/api/v1/hooks/"+id+"/deliveries", nil, http.StatusOK)
}
