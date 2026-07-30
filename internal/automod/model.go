package automod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
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
		// carries a chat message; the key is never in it either way.
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
		return modelVerdict{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return modelVerdict{}, err
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

func (m *Model) systemPrompt() string {
	return strings.TrimSpace(`
You are a chat moderation assistant for a live stream.

` + m.cfg.Instruction + `

Reply with JSON only, matching exactly:
{"abusive": <true|false>, "confidence": <0.0-1.0>, "reason": "<short reason>"}

The reason is shown to a human moderator, so keep it to one short sentence and
describe what was wrong rather than restating the message.`)
}
