package api

// The Twitch channel tools over HTTP: announcements, shoutouts, pins, Shield
// Mode, warnings, blocked terms, chatters, AutoMod, roles, the banned list,
// unban requests and suspicious-user flags. The Helix calls live in
// internal/chat/twitch_tools.go; this file is the dispatch.
//
// FIVE ROUTES, NOT TWENTY-FIVE. Every tool is /chat/twitch/{resource} and the
// METHOD picks the verb, so the route ledger, the read-scope policy and the
// wire contract see one pattern per method. The cost is a dispatch table
// below; the benefit is that a tool cannot be added to the console without the
// method and scope rule that already governs the route.
//
// Reads (GET) are read-token-denied: they carry the banned list, the unban
// appeals' text and the chatter names, which is chat content.

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// twitchLongToolTimeout bounds the tools that page through Helix or make one
// call per item (blocked-terms sync, the chatter list), which the Hub's 15s
// send timeout would cut short.
const twitchLongToolTimeout = 60 * time.Second

// twitchToolBody is every field any tool accepts. Unused fields are ignored.
type twitchToolBody struct {
	Message         string                `json:"message"`
	Color           string                `json:"color"`
	Login           string                `json:"login"`
	UserID          string                `json:"userId"`
	MessageID       string                `json:"messageId"`
	DurationSeconds int                   `json:"durationSeconds"`
	Active          *bool                 `json:"active"`
	Allow           *bool                 `json:"allow"`
	Reason          string                `json:"reason"`
	Text            string                `json:"text"`
	ID              string                `json:"id"`
	Status          string                `json:"status"`
	ResolutionText  string                `json:"resolutionText"`
	Role            string                `json:"role"`
	Settings        *chat.AutoModSettings `json:"settings"`

	// terms is filled by the server from the automod rules, never decoded.
	terms []string
}

func (b twitchToolBody) user() chat.UserRef { return chat.UserRef{ID: b.UserID, Login: b.Login} }

type toolFn func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error)

func (s *Server) handleTwitchToolGet(w http.ResponseWriter, r *http.Request) {
	s.serveTwitchTool(w, r, twitchReads, http.StatusBadGateway, false)
}
func (s *Server) handleTwitchToolPost(w http.ResponseWriter, r *http.Request) {
	s.serveTwitchTool(w, r, twitchPosts, http.StatusBadRequest, true)
}
func (s *Server) handleTwitchToolPut(w http.ResponseWriter, r *http.Request) {
	s.serveTwitchTool(w, r, twitchPuts, http.StatusBadRequest, true)
}
func (s *Server) handleTwitchToolPatch(w http.ResponseWriter, r *http.Request) {
	s.serveTwitchTool(w, r, twitchPatches, http.StatusBadRequest, true)
}
func (s *Server) handleTwitchToolDelete(w http.ResponseWriter, r *http.Request) {
	s.serveTwitchTool(w, r, twitchDeletes, http.StatusBadRequest, false)
}

func (s *Server) serveTwitchTool(w http.ResponseWriter, r *http.Request, table map[string]toolFn, failStatus int, hasBody bool) {
	if !s.requireChat(w) {
		return
	}
	fn, ok := table[chi.URLParam(r, "resource")]
	if !ok {
		writeError(w, http.StatusNotFound, "no such Twitch tool: "+chi.URLParam(r, "resource"))
		return
	}
	var b twitchToolBody
	if hasBody && !decodeJSON(w, r, &b) {
		return
	}
	// Query parameters fill what a body-less verb (GET, DELETE) cannot carry.
	q := r.URL.Query()
	for dst, key := range map[*string]string{&b.UserID: "userId", &b.Login: "login", &b.MessageID: "messageId",
		&b.ID: "id", &b.Role: "role", &b.Status: "status"} {
		if *dst == "" {
			*dst = strings.TrimSpace(q.Get(key))
		}
	}
	if chi.URLParam(r, "resource") == "blocked-terms-sync" {
		set, err := s.store.GetSettings()
		if err != nil {
			writeStoreError(w, err)
			return
		}
		b.terms = plainAutomodTerms(set.Automod.Rules)
	}
	var out any
	account := strings.TrimSpace(q.Get("account"))
	timeout := time.Duration(0)
	switch chi.URLParam(r, "resource") {
	case "blocked-terms-sync", "chatters":
		timeout = twitchLongToolTimeout
	}
	err := s.chat.WithTwitchTools(r.Context(), account, timeout,
		func(ctx2 context.Context, tt chat.TwitchTools) (err error) {
			out, err = fn(r.WithContext(ctx2), tt, b)
			return err
		})
	if err != nil {
		writeError(w, failStatus, err.Error())
		return
	}
	// An approved appeal lifts the ban on Twitch's side; polyemesis's own record
	// of it and the chat.moderation event go through the same bookkeeping as
	// DELETE /chat/bans.
	if ur, ok := out.(chat.UnbanRequest); ok && ur.Status == "approved" {
		s.chat.RecordUnbanFromAppeal(r.Context(), account, ur.UserID)
	}
	if out == nil {
		out = map[string]string{"status": "done"}
	}
	writeJSON(w, http.StatusOK, out)
}

var twitchReads = map[string]toolFn{
	"pin": func(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
		p, err := tt.GetPin(r.Context())
		return map[string]any{"pin": p}, err
	},
	"shield": func(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
		return tt.GetShield(r.Context())
	},
	"blocked-terms": func(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
		v, err := tt.ListBlockedTerms(r.Context())
		return map[string]any{"terms": nonNil(v)}, err
	},
	"chatters": func(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
		return tt.Chatters(r.Context(), r.URL.Query().Get("list") == "1")
	},
	"automod-settings": func(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
		return tt.GetAutoMod(r.Context())
	},
	"roles": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		v, err := tt.ListRole(r.Context(), b.Role)
		return map[string]any{"role": b.Role, "holders": nonNil(v)}, err
	},
	"banned": func(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
		v, err := tt.ListBanned(r.Context())
		return map[string]any{"banned": nonNil(v)}, err
	},
	"unban-requests": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		v, err := tt.ListUnbanRequests(r.Context(), b.Status)
		return map[string]any{"requests": nonNil(v)}, err
	},
	"shoutout-cooldowns": func(_ *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
		return tt.ShoutoutCooldowns(), nil
	},
}

var twitchPosts = map[string]toolFn{
	"blocked-terms-sync": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return tt.SyncBlockedTerms(r.Context(), b.terms)
	},
	"announcements": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.Announce(r.Context(), b.Message, b.Color)
	},
	"shoutouts": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return tt.Shoutout(r.Context(), b.user())
	},
	"warnings": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.Warn(r.Context(), b.user(), b.Reason)
	},
	"blocked-terms": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return tt.AddBlockedTerm(r.Context(), b.Text)
	},
	"held-messages": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		if b.Allow == nil {
			return nil, errNeed("allow (true to approve, false to deny)")
		}
		return nil, tt.ResolveHeldMessage(r.Context(), b.MessageID, *b.Allow)
	},
	"roles": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.AddRole(r.Context(), b.Role, b.user())
	},
	"suspicious": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.SetSuspicious(r.Context(), b.user(), b.Status)
	},
}

var twitchPuts = map[string]toolFn{
	"pin": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.PinMessage(r.Context(), b.MessageID, b.DurationSeconds)
	},
	"shield": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		if b.Active == nil {
			return nil, errNeed("active")
		}
		return tt.SetShield(r.Context(), *b.Active)
	},
	"automod-settings": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		if b.Settings == nil {
			return nil, errNeed("settings")
		}
		return tt.SetAutoMod(r.Context(), *b.Settings)
	},
}

var twitchPatches = map[string]toolFn{
	"pin": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.UpdatePin(r.Context(), b.MessageID, b.DurationSeconds)
	},
	"unban-requests": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return tt.ResolveUnbanRequest(r.Context(), b.ID, b.Status, b.ResolutionText)
	},
}

var twitchDeletes = map[string]toolFn{
	"pin": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.Unpin(r.Context(), b.MessageID)
	},
	"blocked-terms": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.DeleteBlockedTerm(r.Context(), b.ID)
	},
	"roles": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.RemoveRole(r.Context(), b.Role, b.user())
	},
	"suspicious": func(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
		return nil, tt.ClearSuspicious(r.Context(), b.user())
	},
}

type needErr string

func (e needErr) Error() string  { return string(e) + " is required" }
func errNeed(field string) error { return needErr(field) }

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// plainAutomodTerms returns the enabled automod rules whose pattern is a plain
// word or phrase: no regex metacharacter at all. QuoteMeta changing a pattern
// is the whole test, so a rule like "free\s+robux" or "spam.*" is left out
// rather than guessed at.
func plainAutomodTerms(rules []db.AutomodRule) []string {
	var out []string
	for _, p := range rules {
		t := strings.TrimSpace(p.Pattern)
		if p.Enabled && t != "" && regexp.QuoteMeta(t) == t {
			out = append(out, t)
		}
	}
	return out
}
