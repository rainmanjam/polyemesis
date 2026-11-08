package api

import (
	"net/http"
	"net/url"
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

// channelModeratorTarget reads the platform and account and checks the
// platform can manage moderators BEFORE the parameters are looked at, so a
// Twitch or Kick request is told "cannot manage moderators on <platform>" and
// not that a parameter is missing. A refusal made before calling the platform
// is 400; a failure of the platform itself, from the handlers below, is 502
// (the same split as X's, via writeChatXError).
func (s *Server) channelModeratorTarget(w http.ResponseWriter, r *http.Request) (db.Platform, string, url.Values, bool) {
	platform, account, ok := s.chatTarget(w, r)
	if !ok {
		return "", "", nil, false
	}
	if err := s.chat.CanManageChannelModerators(platform, account); err != nil {
		writeChatXError(w, err)
		return "", "", nil, false
	}
	return platform, account, r.URL.Query(), true
}

// handleYouTubeListChatModerators reads the moderators of one platform account's chat.
func (s *Server) handleYouTubeListChatModerators(w http.ResponseWriter, r *http.Request) {
	platform, account, _, ok := s.channelModeratorTarget(w, r)
	if !ok {
		return
	}
	mods, err := s.chat.Moderators(r.Context(), platform, account)
	if err != nil {
		writeChatXError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, chatModeratorsView{Moderators: mods})
}

// handleYouTubeAddChatModerator makes a channel a moderator.
func (s *Server) handleYouTubeAddChatModerator(w http.ResponseWriter, r *http.Request) {
	platform, account, q, ok := s.channelModeratorTarget(w, r)
	if !ok {
		return
	}
	channelID := strings.TrimSpace(q.Get("channelId"))
	if channelID == "" {
		writeError(w, http.StatusBadRequest, "platform and channelId are required")
		return
	}
	m, err := s.chat.AddChannelModerator(r.Context(), platform, account, channelID)
	if err != nil {
		writeChatXError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleYouTubeRemoveChatModerator removes a moderator by the id the list returned.
func (s *Server) handleYouTubeRemoveChatModerator(w http.ResponseWriter, r *http.Request) {
	platform, account, q, ok := s.channelModeratorTarget(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(q.Get("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "platform and id are required")
		return
	}
	if err := s.chat.RemoveChannelModerator(r.Context(), platform, account, id); err != nil {
		writeChatXError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
