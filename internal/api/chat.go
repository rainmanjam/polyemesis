package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

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

// handleChatHideMessage takes a message off a feed without destroying it.
//
// Two different scopes, and the difference is the whole point:
//
//	scope=platform  the platform hides it from viewers. Only Facebook can, and
//	                only because its live chat is a comment thread with an
//	                is_hidden field.
//	scope=local     polyemesis stops showing it. Works everywhere, including
//	                platforms with no moderation API at all, because it asks
//	                nobody's permission — and every viewer still sees it.
//
// The response says which happened in words, not just a status. An operator who
// believes a local hide removed a message from their audience's screens has been
// misled by their own tool, and that is worse than the tool refusing.
func (s *Server) handleChatHideMessage(w http.ResponseWriter, r *http.Request) {
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

	// Local is the default deliberately. It is the one that cannot fail and
	// cannot overreach, so a caller that omits the parameter gets the harmless
	// half rather than an unintended platform write.
	switch scope := strings.TrimSpace(q.Get("scope")); scope {
	case "", "local":
		if err := s.chat.HideLocally(platform, account, id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "hidden",
			"scope":  "local",
			"detail": "Hidden in polyemesis only. Everyone watching on " + string(platform) +
				" can still see this message.",
		})

	case "platform":
		// hidden=false is how a mistaken hide is undone. Only the platform
		// scope can be reversed; a local hide is forgotten, not flagged.
		hidden := strings.TrimSpace(q.Get("hidden")) != "false"
		if err := s.chat.Hide(r.Context(), platform, account, id, hidden); err != nil {
			// The sentence is the payload, as with delete.
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		detail := "Hidden from the public thread on " + string(platform) +
			". Its author and their friends may still see it; that is the platform's rule, not ours."
		if !hidden {
			detail = "Restored to the public thread on " + string(platform) +
				". It will not reappear in this pane — polyemesis does not re-fetch what it has dropped."
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "hidden", "scope": "platform", "detail": detail,
		})

	default:
		writeError(w, http.StatusBadRequest,
			`scope must be "local" (hide it here only) or "platform" (hide it from viewers)`)
	}
}

// handleChatBan removes a person from one platform's chat.
//
// The duration is in SECONDS on the wire, and exactly one unit exists in this
// API on purpose. The platforms disagree — YouTube and Twitch count seconds,
// Kick counts minutes — and each adapter converts at the last moment. A caller
// here never has to know, and cannot get it wrong.
//
// Omitting the duration, or sending 0, is a PERMANENT ban. That is the platforms'
// own convention on all three, so it is kept rather than invented around.
func (s *Server) handleChatBan(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	q := r.URL.Query()
	platform := db.Platform(strings.TrimSpace(q.Get("platform")))
	account := strings.TrimSpace(q.Get("account"))
	userID := strings.TrimSpace(q.Get("userId"))
	if platform == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "platform and userId are required")
		return
	}

	var d time.Duration
	if raw := strings.TrimSpace(q.Get("seconds")); raw != "" {
		secs, err := strconv.Atoi(raw)
		if err != nil || secs < 0 {
			writeError(w, http.StatusBadRequest,
				"seconds must be a whole number of seconds, or omitted for a permanent ban")
			return
		}
		d = time.Duration(secs) * time.Second
	}

	if err := s.chat.Ban(r.Context(), platform, account, userID, d, strings.TrimSpace(q.Get("reason"))); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The verb is reported back, because "banned" and "timed out for 10m" are
	// different things to have just done and the caller should not have to infer
	// which from the request it sent.
	out := map[string]string{"status": "banned", "scope": "permanent"}
	if d > 0 {
		out["status"] = "timed out"
		out["scope"] = d.String()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleChatUnban lifts a ban or an unexpired timeout.
//
// It does not restore the messages that the ban retracted. Those are gone from
// this server's history and nothing re-fetches them; the response says so rather
// than leaving an operator to wonder why the chat did not come back.
func (s *Server) handleChatUnban(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	q := r.URL.Query()
	platform := db.Platform(strings.TrimSpace(q.Get("platform")))
	account := strings.TrimSpace(q.Get("account"))
	userID := strings.TrimSpace(q.Get("userId"))
	if platform == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "platform and userId are required")
		return
	}
	if err := s.chat.Unban(r.Context(), platform, account, userID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "unbanned",
		"detail": "They can post again. Their earlier messages do not come back — polyemesis " +
			"does not re-fetch what it has dropped.",
	})
}

// handleChatSettings applies channel-wide chat rules on one platform.
//
// PATCH, and pointer fields all the way down: an omitted field means "leave it
// alone". A body of plain values could not express "turn slow mode on and touch
// nothing else" — it would send zeros for everything unset and switch off
// follower-only mode as a side effect.
//
// Per-platform rather than fan-out. Only Twitch publishes an API for any of
// this, and "slow mode on" applied to one of four platforms while reporting
// success would be a half-truth.
func (s *Server) handleChatSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	q := r.URL.Query()
	platform := db.Platform(strings.TrimSpace(q.Get("platform")))
	if platform == "" {
		writeError(w, http.StatusBadRequest, "platform is required")
		return
	}
	var body chat.ChatSettings
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.chat.UpdateChatSettings(r.Context(), platform,
		strings.TrimSpace(q.Get("account")), body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// chatUserCard is what a moderator sees before deciding.
type chatUserCard struct {
	Platform db.Platform `json:"platform"`
	AuthorID string      `json:"authorId"`
	// Name and the role flags come from the most recent message rather than
	// from a platform lookup: they are what this person looked like when they
	// last spoke, which is the thing being judged.
	Name        string `json:"name,omitempty"`
	Color       string `json:"color,omitempty"`
	Moderator   bool   `json:"moderator,omitempty"`
	Subscriber  bool   `json:"subscriber,omitempty"`
	Broadcaster bool   `json:"broadcaster,omitempty"`

	Messages []chat.Message `json:"messages"`
	// Truncated says the limit was reached, so "12 messages" is a floor and not
	// a count. The distinction matters: a moderator reading a bounded window as
	// a complete record judges a pattern from a sample.
	Truncated bool `json:"truncated"`
	// RetentionNote explains, in words, why this history is as deep as it is.
	// Without it the card silently understates a talkative person as quiet.
	RetentionNote string `json:"retentionNote"`
}

// handleChatUser answers with one person's recent messages and their roles.
//
// This is the equivalent of Twitch's moderator card, and it is built from
// polyemesis's own store because NO platform publishes a user-chat-history API.
// Twitch's card is a web-app feature over internal endpoints; Helix has Get
// Chatters and Get Moderators, neither of which is a history. The others have
// nothing.
//
// Being local makes it work identically on all four platforms — something
// Twitch's own card cannot do — at the cost of depth, which is why the response
// carries RetentionNote and Truncated rather than presenting a slice as a total.
func (s *Server) handleChatUser(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	platform := db.Platform(strings.TrimSpace(q.Get("platform")))
	authorID := strings.TrimSpace(q.Get("authorId"))
	if platform == "" || authorID == "" {
		writeError(w, http.StatusBadRequest, "platform and authorId are required")
		return
	}
	limit := chatLimitParam(r)

	out := chatUserCard{
		Platform: platform,
		AuthorID: authorID,
		Messages: []chat.Message{},
		RetentionNote: "Read from this server's own scrollback, not from " + string(platform) +
			". No platform offers an API for a viewer's chat history, so this is as far back as " +
			"polyemesis kept — see the chat retention setting.",
	}
	if s.store == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}

	rows, err := s.store.ChatMessagesByAuthor(platform, authorID, limit)
	if err != nil {
		// Same posture as the rest of this file: an unreadable scrollback
		// answers empty rather than failing, so the card still opens and its
		// moderation actions still work. Refusing to show the card because
		// history is unavailable would take away the buttons too.
		s.log.Debug("chat user history unavailable", "err", err)
		writeJSON(w, http.StatusOK, out)
		return
	}
	for _, row := range rows {
		out.Messages = append(out.Messages, chat.FromDB(row))
	}
	out.Truncated = len(rows) >= limit
	if n := len(rows); n > 0 {
		last := rows[n-1]
		out.Name = last.AuthorName
		out.Color = last.AuthorColor
		out.Moderator = last.Moderator
		out.Subscriber = last.Subscriber
		out.Broadcaster = last.Broadcaster
	}
	writeJSON(w, http.StatusOK, out)
}
