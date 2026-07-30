package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// The chat API is a thin window onto internal/chat's Hub. It deliberately owns
// no policy: the Hub already decides what a fan-out send means, what a degraded
// platform is called, and which message ids are the same message seen twice.
// Re-deriving any of that here would give the operator two answers to the same
// question.
//
// Live messages do not come through these routes at all — they arrive on the
// WebSocket as events.TypeChat and events.TypeChatState. What is here is the
// scrollback a freshly opened pane needs, and the three things a pane does that
// a socket cannot: send, delete, and ask who is connected right now.

// chatUnavailable is what the two mutating routes answer when no Hub was wired.
// A 503 rather than a 404 for the same reason jobs uses one: the route exists,
// the capability may be back after the next start, and a client that sees 404
// concludes the server is too old and stops asking.
const chatUnavailable = "chat is not running on this server"

const (
	// chatDefaultLimit is roughly a screenful of scrollback plus the room to
	// scroll up through it.
	chatDefaultLimit = 300
	// chatMaxLimit bounds what one request can ask for. Above this the page
	// spends longer parsing JSON than the operator spends reading it.
	chatMaxLimit = 2000
)

// chatLimit is one platform's published maximum message length.
//
// It is advisory and the UI treats it that way. polyemesis does not refuse a
// long message on the strength of this table — a platform that raised its limit
// would then be un-sendable for no reason the operator could see — it warns,
// sends, and lets the platform be the authority on its own rules.
type chatLimit struct {
	Platform db.Platform `json:"platform"`
	MaxChars int         `json:"maxChars"`
}

// chatOverview is everything a chat pane needs on first paint.
type chatOverview struct {
	// Configured is false when no Hub is wired at all. It is distinct from an
	// empty Statuses, which means a Hub is running with no account attached —
	// the operator's next action differs between the two.
	Configured bool           `json:"configured"`
	Statuses   []chat.Status  `json:"statuses"`
	Stats      *chat.Stats    `json:"stats,omitempty"`
	Messages   []chat.Message `json:"messages"`
	Limits     []chatLimit    `json:"limits"`
	// Stored says the scrollback came from the database rather than from the
	// Hub's live ring, so a pane opened before anything connected can say
	// "history from a previous session" instead of implying it is live.
	Stored bool `json:"stored,omitempty"`
}

// chatSendRequest is the send box's payload.
type chatSendRequest struct {
	Text string `json:"text"`
}

// chatSendResponse reports every platform individually. Partial success is the
// normal case and the UI says exactly which half worked, so the counts are
// pre-tallied here rather than recounted in TypeScript.
type chatSendResponse struct {
	Results []chat.SendResult `json:"results"`
	Sent    int               `json:"sent"`
	Failed  int               `json:"failed"`
	Skipped int               `json:"skipped"`
}

// chatSendLimits is the published maximum for each platform polyemesis can talk
// back to. Facebook is absent because it is receive-only, which is a property of
// the adapter rather than a number we are missing.
func chatSendLimits() []chatLimit {
	return []chatLimit{
		{Platform: db.PlatformKick, MaxChars: chat.KickMaxMessage},
		{Platform: db.PlatformTwitch, MaxChars: chat.TwitchMaxMessage},
		{Platform: db.PlatformYouTube, MaxChars: chat.YouTubeMaxMessage},
	}
}

// requireChat guards a handler that cannot work without a Hub.
func (s *Server) requireChat(w http.ResponseWriter) bool {
	if s.chat == nil {
		writeError(w, http.StatusServiceUnavailable, chatUnavailable)
		return false
	}
	return true
}

func chatLimitParam(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return chatDefaultLimit
	}
	if n > chatMaxLimit {
		return chatMaxLimit
	}
	return n
}

// storedChat reads scrollback out of the database, oldest first.
//
// Every failure answers with no messages rather than with an error: a server
// whose chat table predates this build, or whose disk is momentarily unhappy,
// should still open a chat pane that works from here on. Losing yesterday's
// scrollback is a worse-than-nothing trade only if it also loses today's.
func (s *Server) storedChat(p db.Platform, limit int) []chat.Message {
	if s.store == nil {
		return nil
	}
	var (
		rows []db.ChatMessage
		err  error
	)
	if p == "" {
		rows, err = s.store.RecentChatMessages(limit)
	} else {
		rows, err = s.store.RecentChatMessagesFor(p, limit)
	}
	if err != nil {
		s.log.Debug("chat scrollback unavailable", "err", err)
		return nil
	}
	out := make([]chat.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, chat.FromDB(row))
	}
	return out
}

// handleChatOverview answers with connection state and scrollback in one read,
// so opening the pane is one request rather than three.
func (s *Server) handleChatOverview(w http.ResponseWriter, r *http.Request) {
	limit := chatLimitParam(r)
	out := chatOverview{
		Statuses: []chat.Status{},
		Messages: []chat.Message{},
		Limits:   chatSendLimits(),
	}
	if s.chat != nil {
		out.Configured = true
		out.Statuses = s.chat.Statuses()
		stats := s.chat.Stats()
		out.Stats = &stats
		out.Messages = s.chat.History(limit)
	}
	// The Hub's ring starts empty on every restart while the table does not,
	// so a pane opened a minute after a restart would otherwise show a blank
	// history for a chat that has been running all day.
	if len(out.Messages) == 0 {
		if stored := s.storedChat("", limit); len(stored) > 0 {
			out.Messages = stored
			out.Stored = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleChatMessages is the scrollback on its own, optionally for one platform.
// The pane uses it to page back further than the first paint asked for.
func (s *Server) handleChatMessages(w http.ResponseWriter, r *http.Request) {
	limit := chatLimitParam(r)
	platform := db.Platform(strings.TrimSpace(r.URL.Query().Get("platform")))

	// The database is the authority here rather than the Hub's ring: the ring
	// holds only what this process has seen, and "show me more" is exactly the
	// request the ring cannot answer.
	msgs := s.storedChat(platform, limit)
	if msgs == nil {
		msgs = []chat.Message{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "stored": true})
}

// handleChatSend fans one message out to every connected platform.
func (s *Server) handleChatSend(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	var req chatSendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "nothing to send")
		return
	}

	// No length check. Each platform's limit is published in the overview so
	// the composer can warn, but the platform itself is the authority on what
	// it will accept, and refusing here would silently cost the operator a
	// message every platform would have taken.
	results, err := s.chat.Send(r.Context(), req.Text)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	out := chatSendResponse{Results: results}
	for _, res := range results {
		switch {
		case res.OK:
			out.Sent++
		case res.Skipped:
			out.Skipped++
		default:
			out.Failed++
		}
	}
	// 200 even when every platform failed: the request was carried out and the
	// per-platform verdicts are the answer. A status code cannot say "Twitch
	// took it and YouTube did not", and collapsing it to one would throw away
	// the half that worked.
	writeJSON(w, http.StatusOK, out)
}

// handleChatDeleteMessage removes one message on the platform that issued it.
//
// Addressed by query parameters rather than by path segments because a message
// id is an opaque platform-issued string and an account ref can be anything the
// platform calls an account; neither survives a path segment reliably.
func (s *Server) handleChatDeleteMessage(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	q := r.URL.Query()
	platform := db.Platform(strings.TrimSpace(q.Get("platform")))
	account := strings.TrimSpace(q.Get("account"))
	id := strings.TrimSpace(q.Get("id"))
	if platform == "" || id == "" {
		writeError(w, http.StatusBadRequest, "platform and id are required")
		return
	}

	// The platform is asked first and the local copy is only removed once it
	// agreed. The other order would leave a message deleted in polyemesis and
	// still visible to every viewer, which is the failure this button exists to
	// prevent.
	if err := s.chat.Delete(r.Context(), platform, account, id); err != nil {
		// The sentence is the payload — "polyemesis cannot delete twitch
		// messages; use the twitch dashboard" is the whole answer, and the
		// status code only says the request will not succeed as written.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The local scrollback and the broadcast to every other browser are the
	// Hub's job now, on the same path an upstream deletion takes. Doing it here
	// as well was how one moderator action could leave two operators looking at
	// two different rooms.
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
