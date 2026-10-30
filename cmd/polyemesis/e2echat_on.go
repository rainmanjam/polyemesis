//go:build e2echat

package main

// THIS FILE IS NOT IN ANY RELEASE BUILD. It compiles only under
// `-tags e2echat`, for the Docker end-to-end tests of the chat overlay and the
// chat poll, which need chat to arrive from somewhere and have no live
// platform account.
//
// It attaches three fake adapters (twitch, youtube and kick, account "e2e")
// and listens on 127.0.0.1 only -- reachable from inside the container with
// `docker exec`, never published -- for:
//
//	POST /inject   {"platform","id","name","user"?,"text"}
//	               a message, taking exactly the path a platform's does:
//	               Hub.deliver, dedupe, history, automod, the overlay gate and
//	               the poll counter. user is the author id (default "e2e-"+name).
//	POST /platform {"platform","action":"delete"|"ban"|"clear","id"?,"user"?}
//	               moderation done ON THE PLATFORM's side, reported the way a
//	               platform reports it (Retractor / RetractorAll).
//	GET  /sent     every text polyemesis sent into a fake chat, per platform.
//	POST /youtube-poll {"tally"?:[n,…],"fail"?:bool,"quota"?:[units,reserve]}
//	               sets what the fake YouTube API answers a live poll with.
//	GET  /youtube-poll the live polls created and how many were closed.
//
// The YouTube fake can run a native live poll. It does that through a REAL
// chat.YouTubeAdapter -- its request shapes, its tally parsing and its quota
// budget -- pointed at a fake YouTube Data API on 127.0.0.1:18300.
//
// The fakes can also send, delete and ban, so an operator's moderation from
// the console and the poll's announcements take the hub's real paths.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

const (
	e2eChatAddr    = "127.0.0.1:18299"
	e2eYouTubeAddr = "127.0.0.1:18300"
)

type e2eAdapter struct {
	platform db.Platform
	in       chan chat.Message
	ctl      chan func(chat.Sink)

	mu   sync.Mutex
	sent []string

	// poller is the real YouTube adapter used for native polls; nil on the
	// other platforms.
	poller *chat.YouTubeAdapter
}

func (a *e2eAdapter) CreatePoll(ctx context.Context, q string, opts []string) (string, error) {
	if a.poller == nil {
		return "", errors.New("this fake cannot run a native poll")
	}
	return a.poller.CreatePoll(ctx, q, opts)
}

func (a *e2eAdapter) ClosePoll(ctx context.Context, id string) ([]int, error) {
	if a.poller == nil {
		return nil, errors.New("this fake cannot run a native poll")
	}
	return a.poller.ClosePoll(ctx, id)
}

// fakeYouTube is the YouTube Data API's live-poll half: insert and transition.
type fakeYouTube struct {
	mu      sync.Mutex
	tally   []int
	fail    bool
	created []map[string]any
	closed  int
}

func (f *fakeYouTube) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/liveChatMessages":
		if f.fail {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"errors":[{"reason":"liveChatDisabled"}]}}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.created = append(f.created, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("e2e-poll-%d", len(f.created))})
	case "/liveChatMessages/transition":
		f.closed++
		opts := make([]map[string]string, len(f.tally))
		for i, n := range f.tally {
			opts[i] = map[string]string{"tally": strconv.Itoa(n)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"snippet": map[string]any{
			"pollDetails": map[string]any{"metadata": map[string]any{"status": "closed", "options": opts}}}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
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
		case f := <-a.ctl:
			f(sink)
		}
	}
}

// Send records the text. The zero Message: like Twitch, the platform would
// echo it back, and the fake does not.
func (a *e2eAdapter) Send(_ context.Context, text string) (chat.Message, error) {
	a.mu.Lock()
	a.sent = append(a.sent, text)
	a.mu.Unlock()
	return chat.Message{}, nil
}
func (a *e2eAdapter) Delete(context.Context, string) error                     { return nil }
func (a *e2eAdapter) Ban(context.Context, string, time.Duration, string) error { return nil }
func (a *e2eAdapter) Unban(context.Context, string) error                      { return nil }

func startE2EChat(ctx context.Context, hub *chat.Hub, log *slog.Logger) {
	yt := &fakeYouTube{}
	ytSrv := &http.Server{Addr: e2eYouTubeAddr, Handler: http.HandlerFunc(yt.serve), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = ytSrv.ListenAndServe() }()
	go func() { <-ctx.Done(); _ = ytSrv.Close() }()
	poller, err := chat.NewYouTube(chat.YouTubeConfig{
		AccountRef: "e2e", Channel: "e2e", Token: chat.StaticToken("e2e-not-a-token"),
		APIBase: "http://" + e2eYouTubeAddr, LiveChatID: "e2e-chat",
	})
	if err != nil {
		log.Error("e2echat: youtube poller", "err", err)
		return
	}

	adapters := map[db.Platform]*e2eAdapter{}
	for _, p := range []db.Platform{db.PlatformTwitch, db.PlatformYouTube, db.PlatformKick} {
		a := &e2eAdapter{platform: p, in: make(chan chat.Message, 64), ctl: make(chan func(chat.Sink), 16)}
		if p == db.PlatformYouTube {
			a.poller = poller
		}
		if err := hub.Attach(ctx, a); err != nil {
			log.Error("e2echat: attach", "platform", p, "err", err)
			return
		}
		adapters[p] = a
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /inject", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Platform, ID, Name, User, Text string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a := adapters[db.Platform(req.Platform)]
		if a == nil {
			http.Error(w, "unknown platform", http.StatusBadRequest)
			return
		}
		user := req.User
		if user == "" {
			user = "e2e-" + req.Name
		}
		a.in <- chat.Message{ID: req.ID, Text: req.Text, Author: chat.Author{ID: user, Name: req.Name}}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /platform", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Platform, Action, ID, User string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a := adapters[db.Platform(req.Platform)]
		if a == nil {
			http.Error(w, "unknown platform", http.StatusBadRequest)
			return
		}
		var f func(chat.Sink)
		switch req.Action {
		case "delete":
			f = func(s chat.Sink) {
				if rt, ok := s.(chat.Retractor); ok {
					rt.Retract(req.ID)
				}
			}
		case "ban", "clear":
			user := req.User
			if req.Action == "clear" {
				user = ""
			}
			f = func(s chat.Sink) {
				if rt, ok := s.(chat.RetractorAll); ok {
					rt.RetractUser(user)
				}
			}
		default:
			http.Error(w, "action must be delete, ban or clear", http.StatusBadRequest)
			return
		}
		a.ctl <- f
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /sent", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string][]string{}
		for p, a := range adapters {
			a.mu.Lock()
			out[string(p)] = append([]string{}, a.sent...)
			a.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /youtube-poll", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tally []int `json:"tally"`
			Fail  *bool `json:"fail"`
			Quota []int `json:"quota"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		yt.mu.Lock()
		if req.Tally != nil {
			yt.tally = req.Tally
		}
		if req.Fail != nil {
			yt.fail = *req.Fail
		}
		yt.mu.Unlock()
		if len(req.Quota) == 2 {
			poller.SetQuota(req.Quota[0], req.Quota[1])
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /youtube-poll", func(w http.ResponseWriter, _ *http.Request) {
		yt.mu.Lock()
		defer yt.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"created": yt.created, "closed": yt.closed})
	})
	srv := &http.Server{Addr: e2eChatAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.ListenAndServe() }()
	go func() { <-ctx.Done(); _ = srv.Close() }()
	log.Warn("e2echat build: fake chat adapters attached; this binary is for testing only",
		"inject", e2eChatAddr)
}
