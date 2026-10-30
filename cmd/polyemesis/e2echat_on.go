//go:build e2echat

package main

// THIS FILE IS NOT IN ANY RELEASE BUILD. It compiles only under
// `-tags e2echat`, for the Docker end-to-end test of the chat overlay, which
// needs chat to arrive from somewhere and has no live platform account.
//
// It attaches two fake adapters (twitch and youtube, account "e2e") and listens
// on 127.0.0.1 only -- reachable from inside the container with `docker exec`,
// never published -- for POST /inject {"platform","id","name","text"}. A
// message injected here takes exactly the path a platform's does: Hub.deliver,
// dedupe, history, automod, the overlay gate.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

const e2eChatAddr = "127.0.0.1:18299"

type e2eAdapter struct {
	platform db.Platform
	in       chan chat.Message
}

func (a *e2eAdapter) Platform() db.Platform { return a.platform }
func (a *e2eAdapter) Account() string       { return "e2e" }
func (a *e2eAdapter) Run(ctx context.Context, sink chat.Sink) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case m := <-a.in:
			sink.Deliver(m)
		}
	}
}

func startE2EChat(ctx context.Context, hub *chat.Hub, log *slog.Logger) {
	adapters := map[db.Platform]*e2eAdapter{}
	for _, p := range []db.Platform{db.PlatformTwitch, db.PlatformYouTube} {
		a := &e2eAdapter{platform: p, in: make(chan chat.Message, 64)}
		if err := hub.Attach(ctx, a); err != nil {
			log.Error("e2echat: attach", "platform", p, "err", err)
			return
		}
		adapters[p] = a
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /inject", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Platform, ID, Name, Text string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a := adapters[db.Platform(req.Platform)]
		if a == nil {
			http.Error(w, "unknown platform", http.StatusBadRequest)
			return
		}
		a.in <- chat.Message{ID: req.ID, Text: req.Text,
			Author: chat.Author{ID: "e2e-" + req.Name, Name: req.Name}}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Addr: e2eChatAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.ListenAndServe() }()
	go func() { <-ctx.Done(); _ = srv.Close() }()
	log.Warn("e2echat build: fake chat adapters attached; this binary is for testing only",
		"inject", e2eChatAddr)
}
