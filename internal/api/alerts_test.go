package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// The alert-rule routes were the largest untested family in this package, and
// they handle the one piece of operator input in it that is a SECRET: a Slack
// or Discord webhook URL carries its credential in the path, so anyone holding
// the URL can post as you.
//
// Two behaviours therefore matter more than the CRUD around them -- the URL is
// never echoed back, and a client handing the redacted form back is understood
// to mean "unchanged" rather than being stored verbatim. Both are easy to
// regress with an innocent-looking change to a response struct.

const testWebhook = "https://hooks.example.com/services/T00000/B11111/XXXXsecretXXXX"

func createRule(t *testing.T, h http.Handler, sign func(*http.Request), body map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/alerts/rules", body, http.StatusCreated), &out)
	return out
}

func TestAlertRuleNeverEchoesTheWebhookURL(t *testing.T) {
	h, _, sign := sourceServer(t)

	created := createRule(t, h, sign, map[string]any{
		"name": "ops", "url": testWebhook, "format": "slack",
	})
	id := int64(created["id"].(float64))

	// Every route that can return a rule, because it only takes one to leak.
	paths := []string{
		"/api/v1/alerts/rules",
		"/api/v1/alerts/rules/" + strconv.FormatInt(id, 10),
	}
	for _, p := range paths {
		body := string(send(t, h, sign, http.MethodGet, p, nil, http.StatusOK))
		if strings.Contains(body, "XXXXsecretXXXX") {
			t.Errorf("%s echoed the webhook secret back:\n%s", p, body)
		}
		if !strings.Contains(body, alerts.Mask) {
			t.Errorf("%s returned no masked endpoint; the UI has nothing to show:\n%s", p, body)
		}
	}
	// And the create response itself, which is the first place it could escape.
	if raw, ok := created["url"].(string); ok && strings.Contains(raw, "XXXXsecretXXXX") {
		t.Error("the create response echoed the webhook secret")
	}
}

func TestUpdatingARuleWithTheMaskedURLKeepsTheRealOne(t *testing.T) {
	// The trap this guards: every form renders the only URL it was given, which
	// is the masked one, and submits it back untouched. Storing that string
	// would point the rule at a URL that has never existed -- and alerting
	// would stop with no error anywhere.
	h, store, sign := sourceServer(t)

	created := createRule(t, h, sign, map[string]any{
		"name": "ops", "url": testWebhook, "format": "slack",
	})
	id := int64(created["id"].(float64))
	path := "/api/v1/alerts/rules/" + strconv.FormatInt(id, 10)

	// Read it back exactly as a browser would, then submit that.
	var shown map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &shown)
	// The masked value comes back under "url" -- Rule.MarshalJSON overrides the
	// json:"-" field with the redacted form, so a handler that simply encodes a
	// Rule cannot leak the real one. That is also exactly why a form hands the
	// mask back: to the client it IS the url field.
	masked, _ := shown["url"].(string)
	if masked == "" || !strings.Contains(masked, alerts.Mask) {
		t.Fatalf("expected a masked endpoint to hand back, got %q", masked)
	}

	send(t, h, sign, http.MethodPut, path, map[string]any{
		"name": "ops renamed", "url": masked,
	}, http.StatusOK)

	stored, err := store.GetAlertRule(id)
	if err != nil {
		t.Fatalf("GetAlertRule: %v", err)
	}
	if stored.URL != testWebhook {
		t.Errorf("the masked URL was stored verbatim: %q\nalerting would silently stop", stored.URL)
	}
	if stored.Name != "ops renamed" {
		t.Errorf("the rest of the update was lost: name = %q", stored.Name)
	}
}

func TestUpdatingARuleWithARealURLReplacesIt(t *testing.T) {
	// The other half: a genuinely new URL must still get through, or the rule
	// could never be repointed after a webhook was rotated.
	h, store, sign := sourceServer(t)

	created := createRule(t, h, sign, map[string]any{
		"name": "ops", "url": testWebhook, "format": "slack",
	})
	id := int64(created["id"].(float64))
	replacement := "https://hooks.example.com/services/T99999/B99999/YYYYotherYYYY"

	send(t, h, sign, http.MethodPut, "/api/v1/alerts/rules/"+strconv.FormatInt(id, 10),
		map[string]any{"name": "ops", "url": replacement}, http.StatusOK)

	stored, err := store.GetAlertRule(id)
	if err != nil {
		t.Fatalf("GetAlertRule: %v", err)
	}
	if stored.URL != replacement {
		t.Errorf("URL = %q, want the replacement %q", stored.URL, replacement)
	}
}

func TestANewRuleIsEnabled(t *testing.T) {
	// Somebody who just typed a webhook URL in wants it to alert. A rule that
	// arrives switched off is a silent no-op the operator has no reason to
	// suspect.
	h, _, sign := sourceServer(t)
	created := createRule(t, h, sign, map[string]any{
		"name": "ops", "url": testWebhook, "format": "slack",
	})
	if on, _ := created["enabled"].(bool); !on {
		t.Error("a new rule is disabled; it would never fire and nothing would say so")
	}
}

func TestAlertRuleValidationRefusesWhatCannotDeliver(t *testing.T) {
	h, _, sign := sourceServer(t)
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"no url", map[string]any{"name": "ops", "format": "slack"}},
		{"empty name", map[string]any{"name": "", "url": testWebhook, "format": "slack"}},
		{"not a url", map[string]any{"name": "ops", "url": "not-a-url", "format": "slack"}},
		{"unknown format", map[string]any{"name": "ops", "url": testWebhook, "format": "carrier-pigeon"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			send(t, h, sign, http.MethodPost, "/api/v1/alerts/rules", tc.body, http.StatusBadRequest)
		})
	}
}

func TestAlertRuleCRUDRoundTrip(t *testing.T) {
	h, _, sign := sourceServer(t)

	var before []map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/alerts/rules", nil, http.StatusOK), &before)

	created := createRule(t, h, sign, map[string]any{
		"name": "ops", "url": testWebhook, "format": "slack",
		"minSeverity": "warning", "debounceSeconds": 30,
	})
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)

	var after []map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/alerts/rules", nil, http.StatusOK), &after)
	if len(after) != len(before)+1 {
		t.Fatalf("list has %d rules after creating one, want %d", len(after), len(before)+1)
	}

	send(t, h, sign, http.MethodDelete, "/api/v1/alerts/rules/"+id, nil, http.StatusOK)
	// Gone means gone: a delete that reports success and leaves the row
	// readable is worse than one that fails loudly.
	send(t, h, sign, http.MethodGet, "/api/v1/alerts/rules/"+id, nil, http.StatusNotFound)
}

func TestAlertRuleRoutesRejectAnUnknownID(t *testing.T) {
	h, _, sign := sourceServer(t)
	send(t, h, sign, http.MethodGet, "/api/v1/alerts/rules/99999", nil, http.StatusNotFound)
	send(t, h, sign, http.MethodDelete, "/api/v1/alerts/rules/99999", nil, http.StatusNotFound)
	send(t, h, sign, http.MethodPut, "/api/v1/alerts/rules/99999",
		map[string]any{"name": "x", "url": testWebhook}, http.StatusNotFound)
	// A non-numeric id is a bad request, not a 404: the route matched, the
	// argument did not parse.
	send(t, h, sign, http.MethodGet, "/api/v1/alerts/rules/abc", nil, http.StatusBadRequest)
}

func TestAlertsMetaListsWhatARuleCanSubscribeTo(t *testing.T) {
	// The UI builds its event and severity pickers from this. An empty response
	// renders an empty dropdown, which looks like "no events exist" rather than
	// like a broken endpoint.
	h, _, sign := sourceServer(t)
	var meta struct {
		Events     []any `json:"events"`
		Severities []any `json:"severities"`
		Formats    []any `json:"formats"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/alerts/meta", nil, http.StatusOK), &meta)
	if len(meta.Events) == 0 {
		t.Error("no event types offered; the rule form would have an empty picker")
	}
	if len(meta.Formats) == 0 {
		t.Error("no formats offered")
	}
}
