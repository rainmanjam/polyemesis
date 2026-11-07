package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// repliableAdapter records the Reply it receives.
type repliableAdapter struct {
	platform db.Platform
	mu       sync.Mutex
	id       string
	bot      bool
	err      error
}

func (a *repliableAdapter) Platform() db.Platform { return a.platform }
func (a *repliableAdapter) Account() string       { return "acct" }
func (a *repliableAdapter) Run(ctx context.Context, _ chat.Sink) error {
	<-ctx.Done()
	return nil
}
func (a *repliableAdapter) Send(context.Context, string) (chat.Message, error) {
	return chat.Message{}, nil
}
func (a *repliableAdapter) Reply(_ context.Context, _, id string, asBot bool) (chat.Message, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.id, a.bot = id, asBot
	return chat.Message{}, a.err
}

func TestChatSendReplyRoutesToOnePlatform(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	kick := &repliableAdapter{platform: db.PlatformKick}
	other := &repliableAdapter{platform: db.PlatformYouTube}
	for _, a := range []chat.Adapter{kick, other} {
		if err := hub.Attach(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	sign := login(t, h)

	post := func(body map[string]any) (int, chatSendResponse, string) {
		r := jsonRequest(t, http.MethodPost, "/api/v1/chat/send", body)
		sign(r)
		w := do(t, h, r)
		var out chatSendResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	target := map[string]string{"platform": "kick", "account": "acct", "id": "m-7"}

	code, out, raw := post(map[string]any{"text": "hi", "replyTo": target, "asBot": true})
	if code != 200 || out.Sent != 1 || len(out.Results) != 1 {
		t.Fatalf("code=%d body=%s", code, raw)
	}
	if kick.id != "m-7" || !kick.bot || other.id != "" {
		t.Fatalf("kick=%+v other=%+v", kick, other)
	}

	// A refusal is a result, not an error status.
	kick.err = context.DeadlineExceeded
	code, out, _ = post(map[string]any{"text": "hi", "replyTo": target})
	if code != 200 || out.Failed != 1 || out.Results[0].Detail == "" {
		t.Fatalf("failure: code=%d out=%+v", code, out)
	}

	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{
		"no platform":   {map[string]any{"text": "hi", "replyTo": map[string]string{"id": "m"}}, 400},
		"no id":         {map[string]any{"text": "hi", "replyTo": map[string]string{"platform": "kick"}}, 400},
		"empty text":    {map[string]any{"text": " ", "replyTo": target}, 400},
		"not connected": {map[string]any{"text": "hi", "replyTo": map[string]string{"platform": "twitch", "id": "m"}}, 409},
	} {
		if c, _, raw := post(tc.body); c != tc.want {
			t.Errorf("%s: code=%d want %d (%s)", name, c, tc.want, raw)
		}
	}
}
