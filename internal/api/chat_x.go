package api

import (
	"net/http"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// chatTarget reads the platform and account every platform-addressed chat route names. It answers 400 itself.
func (s *Server) chatTarget(w http.ResponseWriter, r *http.Request) (db.Platform, string, bool) {
	if !s.requireChat(w) {
		return "", "", false
	}
	q := r.URL.Query()
	platform := db.Platform(strings.TrimSpace(q.Get("platform")))
	if platform == "" {
		writeError(w, http.StatusBadRequest, "platform is required")
		return "", "", false
	}
	return platform, strings.TrimSpace(q.Get("account")), true
}

// handleChatMutes lists the viewers an account has muted, with the end of each timed mute. Only X publishes it.
func (s *Server) handleChatMutes(w http.ResponseWriter, r *http.Request) {
	platform, account, ok := s.chatTarget(w, r)
	if !ok {
		return
	}
	mutes, err := s.chat.ListMutes(r.Context(), platform, account)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if mutes == nil {
		mutes = []chat.ChatMute{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mutes": mutes})
}

// moderatorsBody is the moderator routes' answer. AppliesToAll says, for a client to show, that X's moderators act
// on every broadcast the account hosts and are not set per broadcast.
type moderatorsBody struct {
	Moderators   []chat.ChatModerator `json:"moderators,omitempty"`
	IDs          []string             `json:"ids,omitempty"`
	AppliesToAll bool                 `json:"appliesToAllBroadcasts"`
}

// handleChatModerators lists the account's chat moderators.
func (s *Server) handleChatModerators(w http.ResponseWriter, r *http.Request) {
	platform, account, ok := s.chatTarget(w, r)
	if !ok {
		return
	}
	mods, err := s.chat.ListModerators(r.Context(), platform, account)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if mods == nil {
		mods = []chat.ChatModerator{}
	}
	writeJSON(w, http.StatusOK, moderatorsBody{Moderators: mods, AppliesToAll: true})
}

// handleChatAddModerator appoints a chat moderator by the platform's user id (the author id on a chat message).
func (s *Server) handleChatAddModerator(w http.ResponseWriter, r *http.Request) {
	platform, account, ok := s.chatTarget(w, r)
	if !ok {
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		writeError(w, http.StatusBadRequest, "userId is required")
		return
	}
	ids, err := s.chat.AddModerator(r.Context(), platform, account, userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, moderatorsBody{IDs: ids, AppliesToAll: true})
}

// handleChatRemoveModerator removes a chat moderator.
func (s *Server) handleChatRemoveModerator(w http.ResponseWriter, r *http.Request) {
	platform, account, ok := s.chatTarget(w, r)
	if !ok {
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		writeError(w, http.StatusBadRequest, "userId is required")
		return
	}
	ids, err := s.chat.RemoveModerator(r.Context(), platform, account, userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, moderatorsBody{IDs: ids, AppliesToAll: true})
}
