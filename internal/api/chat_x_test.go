package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// xChatFixture is a server with one X chat attached.
func xChatFixture(t *testing.T) (http.Handler, func(*http.Request), *xModerationAdapter) {
	t.Helper()
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &xModerationAdapter{moderatableAdapter{platform: db.PlatformX, account: "acct"}}
	if err := hub.Attach(ctx, a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return h, login(t, h), a
}

func TestChatMutesAndModeratorsRoutes(t *testing.T) {
	h, sign, _ := xChatFixture(t)
	raw := send(t, h, sign, http.MethodGet, "/api/v1/chat/mutes?platform=x&account=acct", nil, http.StatusOK)
	if !strings.Contains(string(raw), xLedgerMuted) {
		t.Errorf("mutes body %s lacks the muted viewer", raw)
	}
	raw = send(t, h, sign, http.MethodGet, "/api/v1/chat/moderators?platform=x&account=acct", nil, http.StatusOK)
	if !strings.Contains(string(raw), `"appliesToAllBroadcasts":true`) || !strings.Contains(string(raw), xLedgerModerator) {
		t.Errorf("moderators body %s lacks the list or the all-broadcasts flag", raw)
	}
	raw = send(t, h, sign, http.MethodPost, "/api/v1/chat/moderators?platform=x&account=acct&userId=4093", nil, http.StatusOK)
	if !strings.Contains(string(raw), `"ids":["4093"]`) {
		t.Errorf("add body = %s", raw)
	}
	raw = send(t, h, sign, http.MethodDelete, "/api/v1/chat/moderators?platform=x&account=acct&userId=4093", nil, http.StatusOK)
	// Removing the last one answers "ids":[] and not a missing key, so a client can tell "none" from "not told".
	if !strings.Contains(string(raw), `"ids":[]`) {
		t.Errorf("remove body = %s, want an empty ids list", raw)
	}
}

// An empty moderator list is "moderators":[], never an absent key.
func TestChatModeratorsEmptyListIsAnEmptyArray(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, &emptyModsAdapter{xModerationAdapter{moderatableAdapter{platform: db.PlatformX, account: "acct"}}}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	raw := send(t, h, login(t, h), http.MethodGet, "/api/v1/chat/moderators?platform=x&account=acct", nil, http.StatusOK)
	if !strings.Contains(string(raw), `"moderators":[]`) || !strings.Contains(string(raw), `"appliesToAllBroadcasts":true`) {
		t.Errorf("body = %s, want an empty array and the all-broadcasts flag", raw)
	}
}

type emptyModsAdapter struct{ xModerationAdapter }

func (a *emptyModsAdapter) ListModerators(context.Context) ([]chat.ChatModerator, error) {
	return nil, nil
}

func TestChatXRoutesRefuseMissingAndUnsupported(t *testing.T) {
	h, sign, _ := xChatFixture(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/chat/mutes"},
		{http.MethodGet, "/api/v1/chat/moderators"},
		{http.MethodPost, "/api/v1/chat/moderators?platform=x&account=acct"},
		{http.MethodDelete, "/api/v1/chat/moderators?platform=x&account=acct"},
	} {
		send(t, h, sign, c.method, c.path, nil, http.StatusBadRequest)
	}
	// Not connected, and connected but without the capability, answer an error in words.
	send(t, h, sign, http.MethodGet, "/api/v1/chat/mutes?platform=twitch&account=acct", nil, http.StatusBadGateway)
	send(t, h, sign, http.MethodGet, "/api/v1/chat/moderators?platform=x&account=other", nil, http.StatusBadGateway)
	send(t, h, sign, http.MethodPost, "/api/v1/chat/moderators?platform=x&account=other&userId=1", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodDelete, "/api/v1/chat/moderators?platform=x&account=other&userId=1", nil, http.StatusBadRequest)
}

func TestChatBanWithMessageIDMutesAndDeletesInOneCall(t *testing.T) {
	h, sign, a := xChatFixture(t)
	send(t, h, sign, http.MethodPost,
		"/api/v1/chat/bans?platform=x&account=acct&userId=555&seconds=60&messageId=1761904800123456789", nil, http.StatusOK)
	calls := a.bans()
	if len(calls) != 1 || calls[0].userID != "555" || calls[0].d != time.Minute {
		t.Errorf("adapter bans = %+v, want one 60s ban for 555", calls)
	}
	// A platform with no single call is refused in words, not done as a plain ban.
	h2, sign2, _, tw := moderationFixture(t)
	send(t, h2, sign2, http.MethodPost,
		"/api/v1/chat/bans?platform=twitch&account=acct&userId=u-1&messageId=9", nil, http.StatusBadRequest)
	if n := len(tw.bans()); n != 0 {
		t.Errorf("a refused mute-and-delete still banned %d time(s)", n)
	}
}

// X is read for viewers through its own seam, and the count X did not send stays nil.
func TestAnalyticsReadsXViewersThroughItsOwnSeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2/broadcasts" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"b1","state":"RUNNING","title":"Live now","total_watching":"17"}]}`)
	}))
	t.Cleanup(srv.Close)
	s, _, _ := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL), oauth.WithXLivestream(true))})
	if !s.hasViewerCount(db.PlatformX) || s.hasViewerCount(db.PlatformFacebook) {
		t.Fatalf("hasViewerCount: x=%v facebook=%v, want true/false", s.hasViewerCount(db.PlatformX), s.hasViewerCount(db.PlatformFacebook))
	}
	st, err := (analyticsSource{s}).readX(context.Background(), "tok")
	if err != nil || !st.Live || st.Viewers == nil || *st.Viewers != 17 || st.Title != "Live now" {
		t.Errorf("readX = %+v, %v; want live, 17 viewers", st, err)
	}
}

func TestAnalyticsXReadRefusedWhenTheGateIsClosed(t *testing.T) {
	s, _, _ := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithXLivestream(false))})
	// A closed gate keeps X a chat-only target, so the sampler never reads it and shows no error row.
	if s.hasViewerCount(db.PlatformX) {
		t.Error("hasViewerCount(x) is true with the Livestream gate closed")
	}
	if _, err := (analyticsSource{s}).readX(context.Background(), "tok"); err == nil {
		t.Error("readX answered with the Livestream gate closed")
	}
}
