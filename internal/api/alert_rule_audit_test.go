package api

import (
	"bytes"
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
	"github.com/rainmanjam/polyemesis/internal/config"
)

// DELETING THE ONLY ALERT RULE USED TO LEAVE NO RECORD AT ALL.
//
// The create, edit and delete handlers had no log call and raised no event,
// and MONITORING.md said so in as many words: "an attacker who deletes your
// only alert rule leaves no record of the deletion". A channel that stops
// receiving alerts reads exactly like a quiet night. Each change now writes a
// log line (name, redacted URL, client address) and raises
// alerts.rule_changed, and a deleted rule is sent that event itself on its way
// out, because the queue would deliver it only to the rules that remain.

// ruleAuditServer is testServer signed in, with its audit events and
// farewells captured and its log kept.
func ruleAuditServer(t *testing.T) (*Server, http.Handler, func(*http.Request), *[]alerts.Event, *[]alerts.Rule, *bytes.Buffer) {
	t.Helper()
	s, h, _ := testServer(t, config.Config{})
	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	sign := login(t, h)
	got := captureAudit(t, s)
	var farewells []alerts.Rule
	s.farewellSink = func(r alerts.Rule, ev alerts.Event) {
		if ev.Type != alerts.TypeAlertRuleChanged {
			t.Errorf("a deleted rule was sent %s, want %s", ev.Type, alerts.TypeAlertRuleChanged)
		}
		farewells = append(farewells, r)
	}
	t.Cleanup(func() { s.farewellSink = nil })
	return s, h, sign, got, &farewells, &logs
}

func ruleChangesIn(evs []alerts.Event) []alerts.Event {
	var out []alerts.Event
	for _, ev := range evs {
		if ev.Type == alerts.TypeAlertRuleChanged {
			out = append(out, ev)
		}
	}
	return out
}

// Mutation: drop the s.auditRuleChange call from handleDeleteAlertRule.
// Observed to fail with "rule changes raised = [created edited], want three".
func TestEveryAlertRuleChangeIsLoggedAndRaised(t *testing.T) {
	_, h, sign, got, farewells, logs := ruleAuditServer(t)

	created := createRule(t, h, sign, map[string]any{
		"name": "on-call", "url": testWebhook, "format": "slack",
	})
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)
	send(t, h, sign, http.MethodPut, "/api/v1/alerts/rules/"+id,
		map[string]any{"name": "on-call", "url": testWebhook, "format": "discord"}, http.StatusOK)
	send(t, h, sign, http.MethodDelete, "/api/v1/alerts/rules/"+id, nil, http.StatusOK)

	changes := ruleChangesIn(*got)
	var said []string
	for _, ev := range changes {
		said = append(said, fieldOfEvent(ev, fieldChange))
	}
	if strings.Join(said, " ") != "created edited deleted" {
		t.Fatalf("rule changes raised = %v, want three: created edited deleted", said)
	}
	for _, ev := range changes {
		if fieldOfEvent(ev, fieldRuleName) != "on-call" {
			t.Errorf("%s names rule %q, want on-call", ev.Title, fieldOfEvent(ev, fieldRuleName))
		}
		if fieldOfEvent(ev, fieldAddress) != "203.0.113.5" {
			t.Errorf("%s names address %q, want the client's", ev.Title, fieldOfEvent(ev, fieldAddress))
		}
	}
	if changes[2].Severity != alerts.SeverityCritical {
		t.Errorf("a delete is %s, want critical: it is the one that takes a channel out", changes[2].Severity)
	}

	// The log is the record a request cannot delete. It names the rule and
	// its redacted URL, and never the secret path.
	text := logs.String()
	for _, want := range []string{"alert rule created", "alert rule edited", "alert rule deleted"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q line in the log:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "remote=203.0.113.5") || !strings.Contains(text, alerts.Mask) {
		t.Errorf("the log lines do not carry the address and the redacted URL:\n%s", text)
	}
	if strings.Contains(text, "XXXXsecretXXXX") {
		t.Errorf("the log carries the webhook secret:\n%s", text)
	}

	if len(*farewells) != 1 || (*farewells)[0].Name != "on-call" {
		t.Errorf("farewells = %+v, want the deleted rule told once", *farewells)
	}
}

// A disabled rule is not sent its farewell: somebody switched it off, and a
// message arriving on a channel the operator muted would be this server
// overriding them.
func TestADisabledRuleIsNotSentItsFarewell(t *testing.T) {
	_, h, sign, _, farewells, _ := ruleAuditServer(t)
	created := createRule(t, h, sign, map[string]any{
		"name": "muted", "url": testWebhook, "format": "slack", "enabled": false,
	})
	id := strconv.FormatInt(int64(created["id"].(float64)), 10)
	send(t, h, sign, http.MethodDelete, "/api/v1/alerts/rules/"+id, nil, http.StatusOK)
	if len(*farewells) != 0 {
		t.Errorf("a disabled rule was sent its farewell: %+v", *farewells)
	}
}

// A delete that fails raises nothing: the rule is still there and still
// delivering, so there is nothing to tell anybody.
func TestDeletingAMissingRuleRaisesNothing(t *testing.T) {
	_, h, sign, got, farewells, _ := ruleAuditServer(t)
	mustJSONError(t, h, sign, http.MethodDelete, "/api/v1/alerts/rules/99999", nil, http.StatusNotFound)
	if len(ruleChangesIn(*got)) != 0 || len(*farewells) != 0 {
		t.Errorf("a failed delete raised %v and farewelled %v", typesOf(*got), *farewells)
	}
}

// THE FAREWELL REACHES THE ENDPOINT, end to end, on a server with no
// programme. Rules are install-wide and are deleted before the first source
// exists; the delivery must not need an engine's notifier.
//
// Mutation: return from farewell before the goroutine. Observed to fail with
// "the deleted rule's endpoint heard nothing".
func TestADeletedRuleIsToldItWasDeleted(t *testing.T) {
	heard := make(chan string, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		heard <- string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(endpoint.Close)

	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	// Through the store, because AllowPrivateTarget is how a test reaches its
	// own loopback listener past the SSRF guard, and the API does not take it.
	rule, err := store.CreateAlertRule(&alerts.Rule{
		Name: "last one standing", Enabled: true, URL: endpoint.URL,
		Format: alerts.FormatJSON, AllowPrivateTarget: true,
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if s.engOrNil() != nil {
		t.Fatal("the fixture has an engine, so the no-programme path is not the one exercised")
	}
	send(t, h, sign, http.MethodDelete, "/api/v1/alerts/rules/"+strconv.FormatInt(rule.ID, 10), nil, http.StatusOK)

	select {
	case body := <-heard:
		if !strings.Contains(body, `"alerts.rule_changed"`) || !strings.Contains(body, "last one standing") {
			t.Errorf("the deleted rule was sent %s, want its own deletion", body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the deleted rule's endpoint heard nothing: the channel an attacker " +
			"silenced goes quiet with no reason given")
	}
}

func fieldOfEvent(ev alerts.Event, name string) string {
	for _, f := range ev.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// lockedLog is a log sink the farewell goroutine can write while the test
// reads it.
type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A farewell the endpoint refuses is logged, with the rule's redacted URL. A
// 404 is permanent to the notifier, so this is one attempt and no backoff.
func TestAFarewellTheEndpointRefusesIsLogged(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(endpoint.Close)

	s, h, store := testServer(t, config.Config{})
	logs := &lockedLog{}
	s.log = slog.New(slog.NewTextHandler(logs, nil))
	sign := login(t, h)
	rule, err := store.CreateAlertRule(&alerts.Rule{
		Name: "gone already", Enabled: true, URL: endpoint.URL,
		Format: alerts.FormatJSON, AllowPrivateTarget: true,
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	send(t, h, sign, http.MethodDelete, "/api/v1/alerts/rules/"+strconv.FormatInt(rule.ID, 10), nil, http.StatusOK)

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), "could not tell a deleted alert rule it was deleted") {
		if time.Now().After(deadline) {
			t.Fatalf("no log line for the refused farewell:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
