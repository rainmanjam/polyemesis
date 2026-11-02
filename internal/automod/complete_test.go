package automod

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// fakeKey is a plain-words stand-in for an operator's key: low entropy on
// purpose, so the secret scanner has nothing to flag.
const fakeKey = "the-fake-provider-key"

type seen struct {
	auth   string
	body   map[string]any
	called int
}

func fakeProvider(t *testing.T, status int, content string) (*httptest.Server, *seen) {
	t.Helper()
	s := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.called++
		s.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &s.body)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, s
}

func completeCfg(endpoint string) ModelConfig {
	cfg := DefaultModelConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	cfg.Model = "fake-model"
	cfg.APIKey = fakeKey
	cfg.KeyBinding = db.NormalizeEndpoint(endpoint)
	cfg.Timeout = 2 * time.Second
	return cfg
}

// Complete is the same connector automod uses, asked a different question: the
// operator's endpoint and model, the key only to the endpoint it is bound to,
// JSON mode, temperature zero, and the reply's content handed back verbatim.
func TestCompleteAsksTheConfiguredProviderWithItsBoundKey(t *testing.T) {
	srv, s := fakeProvider(t, http.StatusOK, `{"scores":[]}`)
	got, err := Complete(context.Background(), completeCfg(srv.URL), NewBudget(), "the system", "the user")
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"scores":[]}` {
		t.Errorf("content %q", got)
	}
	if s.auth != "Bearer "+fakeKey {
		t.Errorf("Authorization %q", s.auth)
	}
	if s.body["model"] != "fake-model" || s.body["temperature"] != float64(0) {
		t.Errorf("request body %v", s.body)
	}
	msgs, _ := s.body["messages"].([]any)
	if len(msgs) != 2 || !strings.Contains(toJSON(msgs), "the system") || !strings.Contains(toJSON(msgs), "the user") {
		t.Errorf("messages %v", msgs)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// The key binding holds here exactly as it holds for moderation: a key bound
// to another endpoint is not sent, and nothing is called.
//
// MUTATION: Complete skips keyBound. Observed: FAIL, the fake receives the key.
func TestCompleteNeverSendsAKeyToAnEndpointItIsNotBoundTo(t *testing.T) {
	srv, s := fakeProvider(t, http.StatusOK, `{}`)
	cfg := completeCfg(srv.URL)
	cfg.KeyBinding = db.NormalizeEndpoint("https://elsewhere.example/v1/chat/completions")
	if _, err := Complete(context.Background(), cfg, NewBudget(), "s", "u"); !errors.Is(err, errKeyNotBound) {
		t.Errorf("err = %v, want errKeyNotBound", err)
	}
	if s.called != 0 {
		t.Errorf("the endpoint was called %d times with a key bound elsewhere", s.called)
	}
}

func TestCompleteRefusesWhatItCannotUse(t *testing.T) {
	srv, _ := fakeProvider(t, http.StatusTooManyRequests, `{}`)
	if _, err := Complete(context.Background(), completeCfg(srv.URL), NewBudget(), "s", "u"); err == nil ||
		!strings.Contains(err.Error(), "429") {
		t.Errorf("a 429 = %v", err)
	}
	off := completeCfg(srv.URL)
	off.Enabled = false
	if _, err := Complete(context.Background(), off, NewBudget(), "s", "u"); !errors.Is(err, ErrModelOff) {
		t.Errorf("a disabled connector = %v, want ErrModelOff", err)
	}

	// A redirect is not followed: the key would go with it.
	var hops int
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	if _, err := Complete(context.Background(), completeCfg(redir.URL), NewBudget(), "s", "u"); err == nil || hops != 1 {
		t.Errorf("redirect: err=%v hops=%d, want a refusal after one hop", err, hops)
	}
}

// The hourly ceiling applies: a spent budget is a refusal before any call.
func TestCompleteSpendsTheBudgetItIsGiven(t *testing.T) {
	srv, s := fakeProvider(t, http.StatusOK, `{}`)
	cfg := completeCfg(srv.URL)
	cfg.MaxCallsPerHour = 1
	b := NewBudget()
	if _, err := Complete(context.Background(), cfg, b, "s", "u"); err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(context.Background(), cfg, b, "s", "u"); !errors.Is(err, ErrBudgetSpent) {
		t.Errorf("second call = %v, want ErrBudgetSpent", err)
	}
	if s.called != 1 {
		t.Errorf("called %d times past a ceiling of one", s.called)
	}
}

// An endpoint carrying a key in its query string does not leak it through a
// transport error.
func TestCompleteRedactsTheEndpointFromATransportError(t *testing.T) {
	cfg := completeCfg("http://127.0.0.1:1/v1/chat/completions?api_key=the-fake-provider-key")
	cfg.APIKey = ""
	_, err := Complete(context.Background(), cfg, NewBudget(), "s", "u")
	if err == nil || strings.Contains(err.Error(), fakeKey) {
		t.Errorf("transport error = %v", err)
	}
}
