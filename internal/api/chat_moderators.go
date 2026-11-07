package api

import (
	"net/http"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Chat moderators: who may moderate a live chat. Only YouTube publishes an API
// for it (liveChatModerators), and only the chat's channel owner may call it, so
// the routes are per platform account and a platform without the capability
// answers with the usual "use the dashboard" sentence.

type chatModeratorsView struct {
	Moderators []chat.ChatModerator `json:"moderators"`
}

// handleListChatModerators reads the moderators of one platform account's chat.
func (s *Server) handleListChatModerators(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	q := r.URL.Query()
	platform := db.Platform(strings.TrimSpace(q.Get("platform")))
	if platform == "" {
		writeError(w, http.StatusBadRequest, "platform is required")
		return
	}
	mods, err := s.chat.Moderators(r.Context(), platform, strings.TrimSpace(q.Get("account")))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, chatModeratorsView{Moderators: mods})
}

// handleAddChatModerator makes a channel a moderator.
func (s *Server) handleAddChatModerator(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	q := r.URL.Query()
	platform := db.Platform(strings.TrimSpace(q.Get("platform")))
	channelID := strings.TrimSpace(q.Get("channelId"))
	if platform == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "platform and channelId are required")
		return
	}
	m, err := s.chat.AddModerator(r.Context(), platform, strings.TrimSpace(q.Get("account")), channelID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleRemoveChatModerator removes a moderator by the id the list returned.
func (s *Server) handleRemoveChatModerator(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	q := r.URL.Query()
	platform := db.Platform(strings.TrimSpace(q.Get("platform")))
	id := strings.TrimSpace(q.Get("id"))
	if platform == "" || id == "" {
		writeError(w, http.StatusBadRequest, "platform and id are required")
		return
	}
	if err := s.chat.RemoveModerator(r.Context(), platform, strings.TrimSpace(q.Get("account")), id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
