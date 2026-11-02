package automod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Complete is the moderation connector asked something other than "is this
// abusive": one chat completion, the content of its reply returned as-is.
//
// It exists so a feature that wants the operator's model -- highlight scoring
// is the first -- uses the provider they ALREADY configured and the defences
// that connector already has, rather than a second client that would have to
// rediscover each of them:
//
//   - the key goes only to the endpoint it is bound to (keyBound), checked
//     before any connection is made;
//   - a redirect is never followed, because the key would follow it
//     (refuseRedirect);
//   - a transport error never carries the endpoint's query string, which is
//     where a proxied key lives (redactEndpoint);
//   - the reply is read through the same 1 MiB bound;
//   - the call is charged to a Budget, refused when it is spent.
//
// Only the OpenAI-compatible protocol: Jev answers typed moderation questions
// and has no free-form completion to offer.
//
// The CALLER owns what the content means. Treat it as model-authored text that
// may have been steered by whatever was in user -- parse numbers out of it, and
// keep none of its prose.
func Complete(ctx context.Context, cfg ModelConfig, budget *Budget, system, user string) (string, error) {
	if !cfg.Enabled || cfg.Endpoint == "" {
		return "", ErrModelOff
	}
	if budget != nil && !budget.reserve(cfg.MaxCallsPerHour) {
		return "", ErrBudgetSpent
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultModelConfig().Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature":     0,
		"response_format": map[string]any{"type": "json_object"},
	}
	if cfg.ReasoningEffort != "" {
		body["reasoning_effort"] = cfg.ReasoningEffort
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(buf))
	if err != nil {
		return "", redactEndpoint(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		if err := keyBound(cfg.Endpoint, cfg.KeyBinding); err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: refuseRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return "", redactEndpoint(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("model API returned %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("model response was not JSON: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("model returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}

// ErrModelOff is a Complete call on a connector that is switched off or has
// no endpoint.
var ErrModelOff = errors.New("no model provider is configured and switched on")

// ErrBudgetSpent is a Complete call past the hourly ceiling.
var ErrBudgetSpent = errors.New("the hourly model budget is spent")
