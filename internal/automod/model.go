package automod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// The model checker: an external API, asked only about what the first two
// checkers could not settle.
//
// Three properties matter more than the classification itself, and all three
// are about what happens when the API is having a bad day:
//
//   - FAIL OPEN. A timeout, a 500, a rate-limit or an expired key means the
//     message passes and is flagged for a human. This mirrors the rule the
//     codebase already holds for hardware detection: detection that could not
//     run must never be the thing that stops your stream. A moderation outage
//     must not silence a chat.
//   - NEVER ON THE HOT PATH. The message displays immediately; a verdict may
//     retract it afterwards. Blocking chat on a 300ms-2s round trip makes the
//     product feel broken, and the retraction path already exists.
//   - BOUNDED SPEND. A per-hour ceiling the operator can see. An unbounded
//     per-message API call is a surprise invoice.
//
// The transport is plain net/http rather than a vendor SDK, consistent with
// driving FFmpeg through os/exec: a stable, inspectable interface beats a
// binding, and it keeps the dependency count where CONTRIBUTING.md wants it.

// ModelConfig configures the connector.
type ModelConfig struct {
	Enabled bool `json:"enabled"`
	// Endpoint is the chat-completions URL. Any OpenAI-compatible API works,
	// including a locally hosted one -- which is the deployment an operator who
	// does not want chat leaving the building will choose.
	//
	// NOT validated against private address ranges, and that is deliberate: the
	// commonest configuration this feature is built for is exactly a private
	// address -- Ollama or vLLM on 127.0.0.1 or a LAN host -- so the usual SSRF
	// blocklist would reject the recommended deployment.
	//
	// The threat model that makes this acceptable: only an authenticated admin
	// can set it, and an admin already holds strictly greater power than an
	// outbound POST (they can edit destinations, read tokens, and restart the
	// process). It is therefore not a privilege boundary, and treating it as one
	// would cost the local-model deployment for no gain. An operator exposing
	// the admin UI to untrusted users has a much larger problem than this field.
	Endpoint string `json:"endpoint"`
	// Model is the model name passed through to the API.
	Model string `json:"model"`
	// APIKey is held encrypted at rest in the existing secretbox, alongside the
	// OAuth tokens, and is never returned by the API or written to a log.
	APIKey string `json:"-"`
	// Timeout bounds one call. Short on purpose: a verdict that arrives after
	// the moment has passed is worth nothing, and the fail-open path is not a
	// failure state.
	Timeout time.Duration `json:"timeout"`
	// MaxCallsPerHour is the spend ceiling. Zero means unlimited, which is
	// offered but not the default.
	MaxCallsPerHour int `json:"maxCallsPerHour"`
	// Action is what a positive verdict asks for.
	Action Action `json:"action"`
	// TimeoutSeconds for ActionTimeout.
	TimeoutSeconds int `json:"timeoutSeconds"`
	// MinConfidence below which a verdict is ignored entirely.
	MinConfidence float64 `json:"minConfidence"`
	// Instruction is the operator's description of what to catch. Their words,
	// because "what counts as abuse here" is a property of a community and not
	// something this product can decide for them.
	Instruction string `json:"instruction"`
}

// DefaultModelConfig is off, with a conservative ceiling if switched on.
func DefaultModelConfig() ModelConfig {
	return ModelConfig{
		Enabled:         false,
		Model:           "gpt-4o-mini",
		Timeout:         4 * time.Second,
		MaxCallsPerHour: 500,
		Action:          ActionFlag,
		TimeoutSeconds:  300,
		MinConfidence:   0.8,
		Instruction:     "Flag harassment, threats, slurs and targeted abuse. Ordinary criticism, banter and strong language are not abuse.",
	}
}

// ModelStats is what the operator sees about spend and health.
type ModelStats struct {
	CallsThisHour int       `json:"callsThisHour"`
	Ceiling       int       `json:"ceiling"`
	Failures      int       `json:"failures"`
	LastError     string    `json:"lastError,omitempty"`
	LastCallAt    time.Time `json:"lastCallAt,omitzero"`
}

// Model is the connector.
type Model struct {
	cfg    ModelConfig
	client *http.Client

	mu         sync.Mutex
	windowFrom time.Time
	calls      int
	failures   int
	lastErr    string
	lastCall   time.Time
	now        func() time.Time
}

// NewModel returns a connector. A nil-safe zero value is deliberate: every
// caller has to work with the model switched off, which is the default.
func NewModel(cfg ModelConfig) *Model {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 4 * time.Second
	}
	return &Model{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		now:    time.Now,
	}
}

// modelVerdict is what the model is asked to return. A tiny fixed schema rather
// than free prose, because a moderation decision has to be machine-readable to
// be acted on and auditable to be defended.
type modelVerdict struct {
	Abusive    bool    `json:"abusive"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// Check asks the model about one message.
//
// It returns findings and an error. The error is for the operator's stats, NOT
// for the caller to act on: on any error the findings are empty and the message
// passes, which is the fail-open contract. A caller that treats the error as
// "block this" would invert the whole design.
func (m *Model) Check(ctx context.Context, text string) ([]Finding, error) {
	if m == nil || !m.cfg.Enabled || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	if !m.reserve() {
		return nil, fmt.Errorf("hourly ceiling of %d calls reached", m.cfg.MaxCallsPerHour)
	}

	v, err := m.ask(ctx, text)
	m.record(err)
	if err != nil {
		// Fail open, and say so out loud. Silence here would be
		// indistinguishable from "the model saw it and was fine with it".
		return nil, err
	}
	if !v.Abusive || v.Confidence < m.cfg.MinConfidence {
		return nil, nil
	}
	return []Finding{{
		Checker:        CheckerModel,
		Action:         m.cfg.Action,
		TimeoutSeconds: m.cfg.TimeoutSeconds,
		Confidence:     v.Confidence,
		Reason:         strings.TrimSpace(v.Reason),
	}}, nil
}

// reserve takes one call from the hourly budget, or reports that it cannot.
func (m *Model) reserve() bool {
	if m.cfg.MaxCallsPerHour <= 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if now.Sub(m.windowFrom) >= time.Hour {
		m.windowFrom = now
		m.calls = 0
	}
	if m.calls >= m.cfg.MaxCallsPerHour {
		return false
	}
	m.calls++
	return true
}

func (m *Model) record(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCall = m.now()
	if err != nil {
		m.failures++
		// The message only. An API error can echo the request, and the request
		// carries a chat message.
		//
		// This line used to add "the key is never in it either way", and that
		// was wrong twice over. The sealed key is not in it, true -- but the
		// ENDPOINT was, verbatim, because net/http puts the request URL in
		// *url.Error, and the endpoint is free text an operator pasted that
		// commonly carries ?api_key=. redactEndpoint is what makes the sentence
		// true; without it this field is a credential.
		m.lastErr = err.Error()
	}
}

// Stats reports spend and health for the operator.
func (m *Model) Stats() ModelStats {
	if m == nil {
		return ModelStats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return ModelStats{
		CallsThisHour: m.calls,
		Ceiling:       m.cfg.MaxCallsPerHour,
		Failures:      m.failures,
		LastError:     m.lastErr,
		LastCallAt:    m.lastCall,
	}
}

func (m *Model) ask(ctx context.Context, text string) (modelVerdict, error) {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()

	body := map[string]any{
		"model": m.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": m.systemPrompt()},
			{"role": "user", "content": text},
		},
		"temperature": 0,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return modelVerdict{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.Endpoint, bytes.NewReader(buf))
	if err != nil {
		return modelVerdict{}, redactEndpoint(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if m.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return modelVerdict{}, redactEndpoint(err)
	}
	defer resp.Body.Close()

	// Bounded read. A misconfigured endpoint returning a large body must not be
	// able to exhaust memory on the moderation path.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return modelVerdict{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return modelVerdict{}, fmt.Errorf("model API returned %d", resp.StatusCode)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return modelVerdict{}, fmt.Errorf("model response was not JSON: %w", err)
	}
	if len(out.Choices) == 0 {
		return modelVerdict{}, fmt.Errorf("model returned no choices")
	}

	var v modelVerdict
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &v); err != nil {
		return modelVerdict{}, fmt.Errorf("model verdict was not the requested JSON: %w", err)
	}
	return v, nil
}

// redactEndpoint takes the operator's endpoint back out of a transport error.
//
// net/http wraps a failed request in *url.Error, whose message is the request
// URL verbatim -- and this URL is free text an operator pasted into a settings
// field. internal/api's redact.go already masks automod.model.endpoint out of
// GET /settings, for a reason stated there at length: a self-hosted or proxied
// inference endpoint most often arrives as
// https://host/v1/chat/completions?api_key=sk-..., and a key in a query string
// is still a key. That reasoning was applied to the settings blob and stopped
// there.
//
// The same string leaves through here on every failed call: into
// ModelStats.LastError, which is what the operator's spend panel shows, and
// into internal/chat's fail-open warning, which is written once per message for
// as long as the endpoint is unreachable. #310 was that exact shape -- a
// refused destination wrote its stream key to server.log on every retry -- and
// the fail-open contract makes this one noisier, because a model that cannot be
// reached is retried on the next message rather than backed off.
//
// Structural rather than lexical: the URL is masked AS a URL, by the same
// function that masks it for the settings endpoint, so the two cannot come to
// different conclusions about what counts as a credential.
func redactEndpoint(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	// A new value rather than a mutation. Both call sites hand us the error
	// net/http built, unwrapped, so nothing is lost by rebuilding it -- and
	// editing an error somebody else may still hold is not ours to do.
	//
	// The host survives on purpose. An operator whose moderation has quietly
	// stopped needs to know WHICH endpoint stopped answering, and an error that
	// said only "the request failed" would trade one silent failure for
	// another.
	return &url.Error{Op: ue.Op, URL: alerts.RedactURL(ue.URL), Err: ue.Err}
}

func (m *Model) systemPrompt() string {
	return strings.TrimSpace(`
You are a chat moderation assistant for a live stream.

` + m.cfg.Instruction + `

Reply with JSON only, matching exactly:
{"abusive": <true|false>, "confidence": <0.0-1.0>, "reason": "<short reason>"}

The reason is shown to a human moderator, so keep it to one short sentence and
describe what was wrong rather than restating the message.`)
}
