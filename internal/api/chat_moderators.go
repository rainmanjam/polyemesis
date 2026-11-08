package api

import (
	"net/http"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Chat moderators by channel id: YouTube's liveChatModerators, which only the
// chat's channel owner may call. These are the non-X halves of the shared
// /chat/moderators routes (chat_x.go dispatches here for any platform but X), so
// a platform without the capability answers with the usual "use the dashboard"
// sentence.

type chatModeratorsView struct {
	Moderators []chat.ChatModerator `json:"moderators"`
}

// handleYouTubeListChatModerators reads the moderators of one platform account's chat.
func (s *Server) handleYouTubeListChatModerators(w http.ResponseWriter, r *http.Request) {
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

// handleYouTubeAddChatModerator makes a channel a moderator.
func (s *Server) handleYouTubeAddChatModerator(w http.ResponseWriter, r *http.Request) {
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
	m, err := s.chat.AddChannelModerator(r.Context(), platform, strings.TrimSpace(q.Get("account")), channelID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleYouTubeRemoveChatModerator removes a moderator by the id the list returned.
func (s *Server) handleYouTubeRemoveChatModerator(w http.ResponseWriter, r *http.Request) {
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
	if err := s.chat.RemoveChannelModerator(r.Context(), platform, strings.TrimSpace(q.Get("account")), id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
