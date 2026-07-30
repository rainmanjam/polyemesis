package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ytStub is a stand-in for the YouTube Data API that counts calls, because
// "how many times did we hit the API" is the property this adapter is judged
// on.
type ytStub struct {
	*httptest.Server
	calls atomic.Int64
}

func newYTStub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, call int64)) *ytStub {
	t.Helper()
	s := &ytStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		handler(w, r, n)
	}))
	t.Cleanup(s.Close)
	return s
}

func ytAdapter(t *testing.T, base string, opts func(*YouTubeConfig)) *YouTubeAdapter {
	t.Helper()
	cfg := YouTubeConfig{
		AccountRef: "UC-me",
		Channel:    "My Channel",
		Token:      StaticToken("yt-token"),
		APIBase:    base,
		Now:        fixedClock(atPacific(2026, 3, 1, 12, 0)),
	}
	if opts != nil {
		opts(&cfg)
	}
	a, err := NewYouTube(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestYouTubeMessageNormalisation(t *testing.T) {
	a := ytAdapter(t, "http://example.invalid", nil)

	mk := func(kind, published, text string, owner, mod, sponsor, verified bool) ytChatItem {
		var it ytChatItem
		it.ID = "msg-1"
		it.Snippet.Type = kind
		it.Snippet.PublishedAt = published
		it.Snippet.DisplayMessage = text
		it.AuthorDetails.ChannelID = "UC-them"
		it.AuthorDetails.DisplayName = "Viewer"
		it.AuthorDetails.IsChatOwner = owner
		it.AuthorDetails.IsChatModerator = mod
		it.AuthorDetails.IsChatSponsor = sponsor
		it.AuthorDetails.IsVerified = verified
		return it
	}

	tests := []struct {
		name  string
		item  ytChatItem
		wantK bool
		check func(t *testing.T, m Message)
	}{
		{
			name:  "a plain message carries its id, author and timestamp",
			item:  mk("textMessageEvent", "2026-03-01T20:00:00Z", "hello", false, false, false, false),
			wantK: true,
			check: func(t *testing.T, m Message) {
				if m.ID != "msg-1" || m.Author.Name != "Viewer" || m.Author.ID != "UC-them" {
					t.Fatalf("identity = %+v", m)
				}
				if !m.At.Equal(time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)) {
					t.Fatalf("timestamp = %v", m.At)
				}
				if m.Platform != db.PlatformYouTube {
					t.Fatalf("platform = %q", m.Platform)
				}
			},
		},
		{
			name:  "the role booleans become both flags and badges",
			item:  mk("textMessageEvent", "2026-03-01T20:00:00Z", "hi", true, true, true, true),
			wantK: true,
			check: func(t *testing.T, m Message) {
				if !m.Author.Broadcaster || !m.Author.Moderator || !m.Author.Subscriber {
					t.Fatalf("roles = %+v", m.Author)
				}
				if len(m.Author.Badges) != 4 {
					t.Fatalf("badges = %+v, want owner, moderator, member and verified", m.Author.Badges)
				}
			},
		},
		{
			name:  "a Super Chat is shown rather than dropped, and is marked",
			item:  mk("superChatEvent", "2026-03-01T20:00:00Z", "£5 thanks!", false, false, false, false),
			wantK: true,
			check: func(t *testing.T, m Message) {
				found := false
				for _, b := range m.Author.Badges {
					if b.ID == "superChatEvent" && b.Label == "Super Chat" {
						found = true
					}
				}
				if !found {
					t.Fatalf("badges = %+v, want the Super Chat marker", m.Author.Badges)
				}
			},
		},
		{
			name:  "an unreadable timestamp falls back to the clock rather than dropping the message",
			item:  mk("textMessageEvent", "not a time", "hi", false, false, false, false),
			wantK: true,
			check: func(t *testing.T, m Message) {
				if m.At.IsZero() {
					t.Fatal("no timestamp at all")
				}
			},
		},
		{
			name:  "an empty message is not a message",
			item:  mk("textMessageEvent", "2026-03-01T20:00:00Z", "   ", false, false, false, false),
			wantK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := a.messageFrom(tc.item)
			if ok != tc.wantK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantK)
			}
			if ok {
				tc.check(t, m)
			}
		})
	}
}

func TestYouTubePollDeliversAndStopsWhenTheChatEnds(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			fmt.Fprint(w, `{"pollingIntervalMillis":5000,"nextPageToken":"p2","items":[
				{"id":"a","snippet":{"type":"textMessageEvent","publishedAt":"2026-03-01T19:59:00Z","displayMessage":"first"},
				 "authorDetails":{"channelId":"UC1","displayName":"One"}},
				{"id":"b","snippet":{"type":"textMessageEvent","publishedAt":"2026-03-01T19:59:30Z","displayMessage":"second"},
				 "authorDetails":{"channelId":"UC2","displayName":"Two"}}]}`)
			return
		}
		fmt.Fprint(w, `{"offlineAt":"2026-03-01T20:05:00Z","items":[]}`)
	})

	var got []Message
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	})

	err := a.pump(context.Background(), "chat-1", SinkFunc(func(m Message) { got = append(got, m) }))
	if err != nil {
		t.Fatalf("pump returned %v, want nil for a chat that ended", err)
	}
	if len(got) != 2 || got[0].Text != "first" {
		t.Fatalf("delivered %+v", got)
	}
	if h := a.Health(); h.State != StateStopped {
		t.Fatalf("health = %+v, want stopped once the broadcast ended", h)
	}
	if h := a.Health(); h.Quota == nil || h.Quota.Used == 0 {
		t.Fatal("the quota spend was not reported")
	}
}

func TestYouTubeFirstPollDoesNotReplayOldHistory(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			// One comment from two hours ago and one from a minute ago.
			fmt.Fprint(w, `{"items":[
				{"id":"old","snippet":{"type":"textMessageEvent","publishedAt":"2026-03-01T18:00:00Z","displayMessage":"ancient"},
				 "authorDetails":{"channelId":"UC1","displayName":"One"}},
				{"id":"new","snippet":{"type":"textMessageEvent","publishedAt":"2026-03-01T19:59:00Z","displayMessage":"recent"},
				 "authorDetails":{"channelId":"UC2","displayName":"Two"}}]}`)
			return
		}
		fmt.Fprint(w, `{"offlineAt":"now","items":[]}`)
	})

	var got []Message
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Now = fixedClock(time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC))
		c.Sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	})

	if err := a.pump(context.Background(), "chat-1", SinkFunc(func(m Message) { got = append(got, m) })); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "recent" {
		t.Fatalf("delivered %+v, want only the recent message", got)
	}
}

// The failure this whole adapter is built to prevent: quota gone, and the loop
// keeps calling the API anyway.
func TestYouTubeStopsCallingTheAPIOnceTheQuotaIsGone(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"message":"quota","errors":[{"reason":"quotaExceeded"}]}}`)
	})

	sleeps := 0
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Sleep = func(ctx context.Context, _ time.Duration) bool {
			sleeps++
			// Stand in for the hours until the reset: after a few checks, the
			// server is shutting down.
			return sleeps < 5
		}
	})

	if err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {})); err != nil {
		t.Fatalf("pump returned %v, want nil", err)
	}
	if n := stub.calls.Load(); n != 1 {
		t.Fatalf("the API was called %d times after quotaExceeded; it must be called once and then left alone", n)
	}

	h := a.Health()
	if h.State != StateDegraded {
		t.Fatalf("state = %q, want degraded", h.State)
	}
	if !strings.Contains(h.Detail, "quota") || !strings.Contains(h.Detail, "resets at") {
		t.Fatalf("detail = %q, want an explanation naming the reset time", h.Detail)
	}
	if !strings.Contains(h.Detail, "Other platforms are unaffected") {
		t.Fatalf("detail = %q, want it to say the other platforms still work", h.Detail)
	}
	if h.Quota == nil || !h.Quota.Paused {
		t.Fatal("the quota report did not show the pause")
	}
}

func TestYouTubeRejectedTokenIsFatal(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials"}}`)
	})

	a := ytAdapter(t, stub.URL, nil)
	err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {}))
	if !IsFatal(err) {
		t.Fatalf("pump returned %v, want a fatal error", err)
	}
	if strings.Contains(err.Error(), "yt-token") {
		t.Fatal("the access token leaked into the error")
	}
	if !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("error %q does not tell the operator what to do", err)
	}
}

func TestYouTubeTransientRateLimitIsRetriedNotAbandoned(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`)
			return
		}
		fmt.Fprint(w, `{"offlineAt":"now","items":[]}`)
	})

	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	})
	if err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {})); err != nil {
		t.Fatal(err)
	}
	if n := stub.calls.Load(); n < 2 {
		t.Fatalf("the API was called %d times; a rate limit must be waited out and retried", n)
	}
}

func TestYouTubeSendReturnsThePlatformsIDSoTheEchoDeduplicates(t *testing.T) {
	var body map[string]any
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"id":"sent-77","snippet":{"publishedAt":"2026-03-01T20:00:00Z"}}`)
	})

	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) { c.LiveChatID = "chat-1" })
	m, err := a.Send(context.Background(), "hello everyone")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "sent-77" {
		t.Fatalf("echo id = %q, want YouTube's own id", m.ID)
	}
	snippet, _ := body["snippet"].(map[string]any)
	if snippet["liveChatId"] != "chat-1" {
		t.Fatalf("request body = %+v", body)
	}
}

// YouTube deletion needs no new OAuth scope, which is the whole reason it is
// worth building: liveChatMessages.delete accepts auth/youtube, and that is what
// this app has always requested. Every connected account can already do it.
func TestYouTubeDeleteAddressesTheRightMessage(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	})

	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) { c.LiveChatID = "chat-1" })
	if err := a.Delete(context.Background(), "msg-42"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %s, want DELETE", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/liveChatMessages") {
		t.Fatalf("path = %q, want the liveChatMessages resource", gotPath)
	}
	// The id travels as a query parameter. Sending it as a path segment would
	// 404 against a resource that does not exist.
	if gotQuery != "id=msg-42" {
		t.Fatalf("query = %q, want id=msg-42", gotQuery)
	}
}

// An empty id must never reach the API.
//
// This matters more than it looks. Twitch's equivalent endpoint treats a missing
// message id as "delete EVERY message in the room", and while YouTube's does not
// document that behaviour, a moderation call built from an empty string is a bug
// wherever it lands. Refusing locally costs nothing and removes the question.
func TestYouTubeDeleteRefusesAnEmptyID(t *testing.T) {
	var called bool
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) { c.LiveChatID = "chat-1" })
	if err := a.Delete(context.Background(), "   "); err == nil {
		t.Fatal("a blank message id was accepted")
	}
	if called {
		t.Fatal("a blank message id reached the API")
	}
}

// A 403 from this endpoint is almost always authority, not quota: the connected
// account is not the broadcaster and not a moderator. Saying "reconnect the
// account" there would send an operator to fix something that was never wrong.
func TestYouTubeDeleteSeparatesAuthorityFromQuota(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"errors":[{"reason":"forbidden"}],"message":"forbidden"}}`)
	})

	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) { c.LiveChatID = "chat-1" })
	err := a.Delete(context.Background(), "msg-42")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "moderator") {
		t.Fatalf("error = %q, want it to name the moderator requirement", err)
	}
	if IsFatal(err) {
		t.Fatal("a permissions 403 was marked Fatal, which would stop the adapter reconnecting")
	}
}

func TestYouTubeSendRefusalsAreActionable(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(a *YouTubeAdapter)
		text    string
		wantSub string
	}{
		{
			name:    "an overlong message names the limit",
			setup:   func(a *YouTubeAdapter) { a.chatID = "chat-1" },
			text:    strings.Repeat("a", YouTubeMaxMessage+1),
			wantSub: "200",
		},
		{
			name:    "with no live chat open it says so",
			setup:   func(a *YouTubeAdapter) {},
			text:    "hello",
			wantSub: "no live chat",
		},
		{
			name: "a spent quota names the reset time",
			setup: func(a *YouTubeAdapter) {
				a.chatID = "chat-1"
				a.budget.pause()
			},
			text:    "hello",
			wantSub: "resets at",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := ytAdapter(t, "http://example.invalid", nil)
			tc.setup(a)
			_, err := a.Send(context.Background(), tc.text)
			if err == nil {
				t.Fatal("the send was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestYouTubeWaitsForABroadcastWithoutSpendingTheDaysQuota(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		fmt.Fprint(w, `{"items":[]}`)
	})

	sleeps := 0
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Sleep = func(ctx context.Context, _ time.Duration) bool {
			sleeps++
			return sleeps < 3
		}
	})

	if err := a.Run(context.Background(), SinkFunc(func(Message) {})); err != nil {
		t.Fatal(err)
	}
	if n := stub.calls.Load(); n > 3 {
		t.Fatalf("looked for a broadcast %d times in three sleeps", n)
	}
	if h := a.Health(); h.State != StateDegraded || !strings.Contains(h.Detail, "waiting") {
		t.Fatalf("health = %+v, want a clear waiting state", h)
	}
}

func TestNewYouTubeWithoutATokenIsAConfigurationState(t *testing.T) {
	_, err := NewYouTube(YouTubeConfig{})
	if err == nil {
		t.Fatal("a missing token was accepted")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error %q does not read as a configuration state", err)
	}
}
