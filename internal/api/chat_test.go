package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// storeChat writes scrollback straight into the table, which is what a hub that
// ran earlier today would have left behind.
func storeChat(t *testing.T, store *db.DB, msgs ...db.ChatMessage) {
	t.Helper()
	if _, err := store.AppendChatMessages(msgs); err != nil {
		t.Fatalf("append chat: %v", err)
	}
}

func chatRow(p db.Platform, id, author, text string, at time.Time) db.ChatMessage {
	return db.ChatMessage{
		Platform:   p,
		Account:    string(p) + "-ref",
		MessageID:  id,
		Channel:    "#test",
		AuthorName: author,
		Text:       text,
		At:         at,
	}
}

func TestChatRoutesRequireASession(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"overview", http.MethodGet, "/api/v1/chat"},
		{"scrollback", http.MethodGet, "/api/v1/chat/messages"},
		{"send", http.MethodPost, "/api/v1/chat/send"},
		{"delete", http.MethodDelete, "/api/v1/chat/messages?platform=kick&id=1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, jsonRequest(t, tc.method, tc.path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
		})
	}
}

func TestChatOverviewSaysNotConfiguredRatherThanFailing(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodGet, "/api/v1/chat", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}

	var view chatOverview
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Configured {
		t.Error("configured = true with no hub wired")
	}
	if view.Statuses == nil || view.Messages == nil {
		t.Error("statuses and messages must be empty arrays, never null")
	}
	// The limits are static knowledge, so a server with no chat running still
	// answers "YouTube caps at 200" — the composer can warn before anything is
	// ever connected.
	if len(view.Limits) != 3 {
		t.Fatalf("limits = %d, want one per sending platform", len(view.Limits))
	}
}

func TestChatOverviewServesStoredScrollbackOldestFirst(t *testing.T) {
	_, h, store := testServer(t, config.Config{})
	base := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	storeChat(t, store,
		chatRow(db.PlatformTwitch, "t1", "ana", "first", base),
		chatRow(db.PlatformKick, "k1", "bo", "second", base.Add(time.Minute)),
		chatRow(db.PlatformYouTube, "y1", "cy", "third", base.Add(2*time.Minute)),
	)
	sign := login(t, h)

	r := jsonRequest(t, http.MethodGet, "/api/v1/chat", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}

	var view chatOverview
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !view.Stored {
		t.Error("stored = false, but the scrollback came from the table")
	}
	want := []string{"first", "second", "third"}
	if len(view.Messages) != len(want) {
		t.Fatalf("messages = %d, want %d", len(view.Messages), len(want))
	}
	for i, text := range want {
		if view.Messages[i].Text != text {
			t.Errorf("messages[%d].Text = %q, want %q", i, view.Messages[i].Text, text)
		}
	}
}

func TestChatMessagesFilterByPlatform(t *testing.T) {
	_, h, store := testServer(t, config.Config{})
	base := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	storeChat(t, store,
		chatRow(db.PlatformTwitch, "t1", "ana", "twitch line", base),
		chatRow(db.PlatformKick, "k1", "bo", "kick line", base.Add(time.Minute)),
	)
	sign := login(t, h)

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"every platform", "", 2},
		{"one platform", "?platform=kick", 1},
		{"a platform with nothing stored", "?platform=facebook", 0},
		// A platform this build has never heard of must answer empty rather
		// than 400: the caller learns nothing from a rejection it cannot fix.
		{"an unknown platform", "?platform=myspace", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodGet, "/api/v1/chat/messages"+tc.query, nil)
			sign(r)
			w := do(t, h, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
			}
			var resp struct {
				Messages []chat.Message `json:"messages"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(resp.Messages) != tc.want {
				t.Fatalf("messages = %d, want %d", len(resp.Messages), tc.want)
			}
		})
	}
}

func TestChatMutationsAreUnavailableWithoutAHub(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"send", http.MethodPost, "/api/v1/chat/send", map[string]string{"text": "hello"}},
		{"delete", http.MethodDelete, "/api/v1/chat/messages?platform=kick&id=1", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, tc.method, tc.path, tc.body)
			sign(r)
			w := do(t, h, r)
			// 503 rather than 404: the route exists and the capability may be
			// back after the next start.
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestChatSendReportsWhyItWasNotAttempted(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	sign := login(t, h)

	tests := []struct {
		name string
		body map[string]string
		want int
	}{
		{"empty text", map[string]string{"text": "   "}, http.StatusBadRequest},
		// A hub with nothing attached is a conflict, not a bad request: the
		// message was fine, there was simply nowhere to put it.
		{"no platform attached", map[string]string{"text": "hello"}, http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodPost, "/api/v1/chat/send", tc.body)
			sign(r)
			w := do(t, h, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", w.Code, tc.want, w.Body.String())
			}
			var e apiError
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if e.Error == "" {
				t.Error("the refusal carried no sentence for the operator")
			}
		})
	}
}

func TestChatSendNeverEnforcesAPlatformLengthLimit(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	sign := login(t, h)

	// Far past every published limit. The platform is the authority on its own
	// rules; refusing here would be a check wrong in the restrictive direction,
	// so this must get as far as "nothing is attached" rather than "too long".
	long := make([]byte, chat.KickMaxMessage*3)
	for i := range long {
		long[i] = 'a'
	}
	r := jsonRequest(t, http.MethodPost, "/api/v1/chat/send",
		map[string]string{"text": string(long)})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (nothing attached); body %s", w.Code, w.Body.String())
	}
}

func TestChatDeleteExplainsInsteadOfSilentlyFailing(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	storeChat(t, store, chatRow(db.PlatformKick, "k1", "bo", "kick line", time.Now()))
	sign := login(t, h)

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"no id", "?platform=kick", http.StatusBadRequest},
		{"no platform", "?id=k1", http.StatusBadRequest},
		// The platform is not attached, which the hub says in words. The point
		// of the assertion is that a sentence comes back at all.
		{"platform not attached", "?platform=kick&account=kick-ref&id=k1", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodDelete, "/api/v1/chat/messages"+tc.query, nil)
			sign(r)
			w := do(t, h, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", w.Code, tc.want, w.Body.String())
			}
			var e apiError
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if e.Error == "" {
				t.Error("the refusal carried no sentence for the operator")
			}
		})
	}

	// Nothing was deleted locally, because the platform never agreed.
	rows, err := store.RecentChatMessages(10)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored messages = %d, want the row to survive a refused delete", len(rows))
	}
}

func TestChatLimitParamClamps(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"absent", "", chatDefaultLimit},
		{"not a number", "?limit=lots", chatDefaultLimit},
		{"zero", "?limit=0", chatDefaultLimit},
		{"negative", "?limit=-5", chatDefaultLimit},
		{"in range", "?limit=42", 42},
		{"above the ceiling", "?limit=999999", chatMaxLimit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodGet, "/api/v1/chat"+tc.query, nil)
			if got := chatLimitParam(r); got != tc.want {
				t.Fatalf("chatLimitParam = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestChatSendLimitsMatchTheAdapters(t *testing.T) {
	want := map[db.Platform]int{
		db.PlatformKick:    chat.KickMaxMessage,
		db.PlatformTwitch:  chat.TwitchMaxMessage,
		db.PlatformYouTube: chat.YouTubeMaxMessage,
	}
	got := chatSendLimits()
	if len(got) != len(want) {
		t.Fatalf("limits = %d, want %d", len(got), len(want))
	}
	for _, l := range got {
		if want[l.Platform] != l.MaxChars {
			t.Errorf("%s max = %d, want %d", l.Platform, l.MaxChars, want[l.Platform])
		}
	}
	// Facebook is receive-only, so it has no send limit to publish. Listing it
	// with a zero would read as "no limit", which is the opposite of true.
	for _, l := range got {
		if l.Platform == db.PlatformFacebook {
			t.Error("facebook is receive-only and must not appear in the send limits")
		}
	}
}

// The moderator's user card, which is polyemesis's answer to Twitch's — built
// from our own scrollback because no platform publishes a chat-history API.
func TestChatUserCardShowsOnePersonsMessages(t *testing.T) {
	_, h, store := testServer(t, config.Config{})
	base := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	// Built with explicit author IDs rather than through chatRow, because the
	// card keys on author_id and chatRow only sets a name. That is the right
	// shape for both: a display name is not an identity, and on every platform
	// here a name can change while the id cannot.
	withAuthor := func(m db.ChatMessage, id string) db.ChatMessage {
		m.AuthorID = id
		return m
	}
	storeChat(t, store,
		withAuthor(chatRow(db.PlatformTwitch, "t1", "ana", "hello", base), "u-ana"),
		withAuthor(chatRow(db.PlatformTwitch, "t2", "bo", "someone else", base.Add(time.Minute)), "u-bo"),
		withAuthor(chatRow(db.PlatformTwitch, "t3", "ana", "again", base.Add(2*time.Minute)), "u-ana"),
	)
	sign := login(t, h)

	r := jsonRequest(t, http.MethodGet, "/api/v1/chat/users?platform=twitch&authorId=u-ana", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}

	var card chatUserCard
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(card.Messages) != 2 {
		t.Fatalf("messages = %d, want ana's 2 and not bo's: %+v", len(card.Messages), card.Messages)
	}
	if card.Messages[0].Text != "hello" || card.Messages[1].Text != "again" {
		t.Fatalf("order = %+v, want oldest first", card.Messages)
	}
	// The caveat is not decoration. A moderator reading "2 messages" as a
	// complete record judges a pattern from a sample, so the response has to
	// say where the number came from.
	if card.RetentionNote == "" {
		t.Error("no retention note; the card would present a bounded window as a total")
	}
}

func TestChatUserCardNeedsBothIdentifiers(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	for _, q := range []string{"", "?platform=twitch", "?authorId=ana"} {
		r := jsonRequest(t, http.MethodGet, "/api/v1/chat/users"+q, nil)
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusBadRequest {
			t.Errorf("GET /chat/users%s = %d, want 400: a card for nobody in particular "+
				"would pool unrelated people into one person", q, w.Code)
		}
	}
}

// A card must open even with no history, because its BUTTONS are the point.
// Refusing the card when the scrollback is empty would take the moderation
// actions away at exactly the moment somebody is trying to use them.
func TestChatUserCardOpensWithNoHistory(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodGet, "/api/v1/chat/users?platform=twitch&authorId=nobody", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with an empty card", w.Code)
	}
	var card chatUserCard
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if card.Messages == nil {
		t.Error("messages = null; the UI would have to guard where an empty list would do")
	}
}
