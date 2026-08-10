package api

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// The endpoint whose CREDENTIAL IS ITS PATH. Nothing about the host says so,
// which is exactly why masking only the obvious-looking things never caught it.
const (
	discloseURL    = "https://hooks.slack.com/services/T00000000/B00000000/xoxbHOOKPATHsecret77"
	disclosePath   = "/services/T00000000/B00000000/xoxbHOOKPATHsecret77"
	discloseSecret = "xoxbHOOKPATHsecret77"
	// The signing secret is the second literal a hook worker declares. An
	// endpoint that rejects a signature is a plausible place for it to come
	// back.
	discloseSigning = "hook-signing-secret-value-2f8b"
)

// failingDoer answers every delivery with one transport failure, wrapped the
// way net/http wraps it: a *url.Error carrying the FULL request URL. It also
// serves an endpoint BODY on the response path, which is the other shape.
type failingDoer struct {
	err  error
	body string
}

func (f failingDoer) Do(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     http.Header{},
	}, nil
}

func hookTransportFailures() []struct {
	name  string
	doer  failingDoer
	inner string
} {
	return []struct {
		name  string
		doer  failingDoer
		inner string
	}{
		{
			name:  "DNS",
			doer:  failingDoer{err: &url.Error{Op: "Post", URL: discloseURL, Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "hooks.slack.com"}}}},
			inner: "no such host",
		},
		{
			name:  "refused",
			doer:  failingDoer{err: &url.Error{Op: "Post", URL: discloseURL, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}}},
			inner: "connection refused",
		},
		{
			name:  "timeout",
			doer:  failingDoer{err: &url.Error{Op: "Post", URL: discloseURL, Err: errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")}},
			inner: "Client.Timeout",
		},
		{
			name:  "TLS",
			doer:  failingDoer{err: &url.Error{Op: "Post", URL: discloseURL, Err: x509.UnknownAuthorityError{}}},
			inner: "x509",
		},
		{
			// Not a transport failure at all: the endpoint answered, and put
			// the credential in its OWN body. This is the shape alerts.Redact
			// is worst at -- JSON -- and the reason the exact SecretSet exists
			// alongside ClientErrorText rather than instead of it.
			name: "the endpoint echoes the credential back in a JSON body",
			doer: failingDoer{body: `{"error":"no webhook at ` + disclosePath +
				`","signature":"` + discloseSigning + `"}`},
			inner: "",
		},
	}
}

// TestHookDeliveriesDoNotDiscloseTheEndpointPath drives the REAL router with a
// READ-SCOPED token (#160).
//
// GET /api/v1/hooks/{id}/deliveries and GET /api/v1/hooks/meta are both in the
// ordinary authenticated group, so a read token reaches them. A read token is
// promised to be read-only; a Slack webhook URL in a delivery record turns it
// into "read-only, plus it can post into your Slack", which is the same class
// of escalation the source publish token had.
//
// Five modes, not one. The earlier round measured a DNS failure alone and read
// the result as "best effort is adequate here". Every transport failure is a
// *url.Error and every one of them carried the path; the fifth row is the
// endpoint's own body, which no wrapper-aware rule can reach.
func TestHookDeliveriesDoNotDiscloseTheEndpointPath(t *testing.T) {
	for _, tc := range hookTransportFailures() {
		t.Run(tc.name, func(t *testing.T) {
			h, _, sign := sourceServer(t)
			readTok := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

			created := createHook(t, h, sign, map[string]any{"name": "deploy", "url": discloseURL})
			id := int64(created["id"].(float64))

			d := hooks.NewDispatcher(
				slog.New(slog.NewTextHandler(io.Discard, nil)),
				hooks.SourceFunc(func() ([]hooks.Hook, error) {
					return []hooks.Hook{hooks.Hook{
						ID: id, Name: "deploy", Enabled: true,
						URL: discloseURL, Secret: discloseSigning,
						MaxAttempts: 1,
					}.Normalized()}, nil
				}),
				hooks.WithDoer(tc.doer),
				hooks.WithReloadInterval(5*time.Millisecond),
				hooks.WithSleep(func(context.Context, time.Duration) bool { return true }),
			)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go d.Run(ctx)
			lastTestServer.hooks = d

			d.Publish(hooks.Event{
				Trigger: hooks.TriggerIngestPublished,
				Source:  hooks.SourceRef{ID: 1, Name: "Main"},
			})

			// Read the delivery log THROUGH THE ROUTE, as the read token.
			path := "/api/v1/hooks/" + strconv.FormatInt(id, 10) + "/deliveries"
			var recs []hooks.DeliveryRecord
			var raw string
			deadline := time.Now().Add(5 * time.Second)
			for {
				r := jsonRequest(t, http.MethodGet, path, nil)
				r.Header.Set("Authorization", "Bearer "+readTok)
				w := do(t, h, r)
				if w.Code != http.StatusOK {
					t.Fatalf("GET %s as a read token: status %d, want 200 -- this route is "+
						"reachable by a read scope and that is the premise of the whole "+
						"test: %.120s", path, w.Code, w.Body.String())
				}
				raw = w.Body.String()
				recs = nil
				if err := json.Unmarshal([]byte(raw), &recs); err != nil {
					t.Fatalf("GET %s did not return a JSON array (%v): %.120s", path, err, raw)
				}
				if len(recs) > 0 || time.Now().After(deadline) {
					break
				}
				time.Sleep(2 * time.Millisecond)
			}
			if len(recs) == 0 {
				t.Fatal("no delivery was recorded, so this test asserted nothing")
			}

			for _, bad := range []string{discloseSecret, disclosePath, discloseSigning} {
				if strings.Contains(raw, bad) {
					t.Errorf("GET %s disclosed %q to a READ-scoped token:\n%s\n\n"+
						"The endpoint's path IS its credential. alerts.Redact does not mask "+
						"an https path segment and never did (#162 limit 2), so the Redact "+
						"call that used to stand here was a no-op on exactly this shape.",
						path, bad, raw)
				}
			}
			// The host is not the secret, and dropping it would leave the
			// operator unable to tell which endpoint failed.
			if !strings.Contains(raw, "hooks.slack.com") && tc.inner != "" {
				t.Errorf("GET %s no longer names the endpoint host:\n%s", path, raw)
			}
			// And the inner wording -- the only thing that distinguishes a
			// certificate problem from a timeout -- survives.
			if tc.inner != "" && !strings.Contains(raw, tc.inner) {
				t.Errorf("GET %s lost the diagnostic %q:\n%s", path, tc.inner, raw)
			}

			// The same field is copied into Stats.LastError and served at
			// /hooks/meta, which the read token also reaches.
			r := jsonRequest(t, http.MethodGet, "/api/v1/hooks/meta", nil)
			r.Header.Set("Authorization", "Bearer "+readTok)
			w := do(t, h, r)
			if w.Code != http.StatusOK {
				t.Fatalf("GET /api/v1/hooks/meta as a read token: status %d", w.Code)
			}
			for _, bad := range []string{discloseSecret, disclosePath, discloseSigning} {
				if strings.Contains(w.Body.String(), bad) {
					t.Errorf("GET /api/v1/hooks/meta disclosed %q to a READ-scoped token:\n%s",
						bad, w.Body.String())
				}
			}
		})
	}
}

// TestAlertsMetaIsReachableByAReadToken is the premise of the notifier half,
// stated where it can rot loudly.
//
// The delivery-side fix is proven in internal/alerts, against the real Notifier
// loop and the real Stats field, across the same four transport failures -- see
// TestNotifierStatsNeverDisclosesTheWebhookPath. What that test cannot state is
// WHO gets to read the field, because the route lives here. This does, and it
// is the reason the leak was an escalation rather than an inconvenience:
// engine.Alerts() is constructed inside engine.New with no seam for a stub
// client, so the two halves are asserted either side of a boundary neither can
// cross alone.
func TestAlertsMetaIsReachableByAReadToken(t *testing.T) {
	h, _, sign := sourceServer(t)
	readTok := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	r := jsonRequest(t, http.MethodGet, "/api/v1/alerts/meta", nil)
	r.Header.Set("Authorization", "Bearer "+readTok)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/alerts/meta as a read token: status %d, want 200. If this "+
			"route has been denied to read scope, the #160 escalation is closed a "+
			"different way and TestNotifierStatsNeverDisclosesTheWebhookPath should say "+
			"so: %.120s", w.Code, w.Body.String())
	}
	var out map[string]any
	decodeInto(t, w.Body.Bytes(), &out)
	stats, ok := out["stats"].(map[string]any)
	if !ok {
		t.Fatalf("no stats object in the meta response, so Notifier.Stats is no longer "+
			"served here and the notifier-side test is guarding a field nobody reads: %v", out)
	}
	if _, present := stats["lastError"]; present && stats["lastError"] == "" {
		t.Error("lastError is present but empty; omitempty should have dropped it")
	}
}
