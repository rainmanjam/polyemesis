package api

// Turning connected accounts into running chat adapters.
//
// internal/chat knows how to talk to four platforms and internal/api knows how
// to answer questions about them; this file is the only place that knows which
// accounts exist. It runs once at startup and again whenever the operator
// connects or disconnects an account, and it is deliberately total: every
// account that cannot produce an adapter contributes a named reason rather than
// an aborted pass, because one misconfigured platform must not cost the
// operator the other three.
//
// Adapters are attached as soon as an account exists rather than when a
// broadcast starts. That is on purpose. Twitch IRC and Kick's webhook work
// whether or not anything is live, and the two that genuinely need a broadcast
// — YouTube and Facebook — already report "waiting for a live broadcast" as a
// health state with a sentence attached. Attaching late would mean the chat
// pane was empty and silent for the one platform an operator is most likely to
// be testing with.

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// kickCallbackLabel derives the Kick webhook's path secret from the data
// directory's encryption key.
//
// The alternative was a random secret per process, which regenerates on every
// restart and silently breaks the URL the operator pasted into their Kick app —
// a failure that presents as chat simply never arriving. Deriving it from the
// key file makes it stable for the life of the install without adding a column,
// and it rotates exactly when the key does, which is the moment every other
// stored credential is invalidated anyway.
const kickCallbackLabel = "kick-chat-callback-v1"

// kickCallbackSecret is the path segment, or empty when there is no key box to
// derive from (which only happens in tests that build a Server by hand).
func (s *Server) kickCallbackSecret() string {
	if s.box == nil {
		return ""
	}
	// Half the derived material is 128 bits of path secret, which is far more
	// than a URL needs to be unguessable and keeps the pasted URL readable.
	return hex.EncodeToString(s.box.Derive(kickCallbackLabel)[:16])
}

// publicBaseURL is the externally reachable origin, or empty when this server
// has no way to know it.
//
// Empty is a supported answer and not a warning: behind a tunnel or a reverse
// proxy the operator knows their public URL and polyemesis does not, and the
// Kick adapter renders "this server's public URL + <path>" rather than
// inventing a hostname. Guessing here would put a wrong URL in front of someone
// about to paste it into a dashboard.
func publicBaseURL(cfg config.Config) string {
	host := strings.TrimSpace(cfg.TLS.Hostname)
	if host == "" || !config.IsPublicFQDN(host) || !cfg.ServesTLS() {
		return ""
	}
	return "https://" + host
}

// StartChat attaches an adapter for every connected account. Failures are
// logged and skipped; the return value is the number attached, for the caller's
// startup line.
func (s *Server) StartChat(ctx context.Context) int {
	if s.chat == nil || s.store == nil {
		return 0
	}
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		s.log.Warn("chat: could not list connected accounts", "err", err)
		return 0
	}

	attached := 0
	for _, a := range accts {
		adapter, err := s.chatAdapter(ctx, a)
		if err != nil {
			// Info, not Warn: "you have not gone live yet" and "this platform
			// has no chat adapter" are both normal, and a warning for either
			// trains operators to ignore the ones that matter.
			s.log.Info("chat: not connecting a platform", "platform", a.Platform, "account", a.AccountName, "reason", err)
			continue
		}
		if err := s.chat.Attach(ctx, adapter); err != nil {
			s.log.Warn("chat: could not attach", "platform", a.Platform, "err", err)
			continue
		}
		attached++
	}
	return attached
}

// chatToken is the closure every adapter refreshes through. It goes back to the
// store on each call rather than capturing a token, so a refresh performed by
// RefreshLoop (or by any other handler) is picked up by a chat adapter that has
// been running for hours.
func (s *Server) chatToken(id int64) chat.TokenFunc {
	return func(ctx context.Context) (string, error) {
		acct, err := s.tokenFor(ctx, id)
		if err != nil {
			return "", err
		}
		return acct.AccessToken, nil
	}
}

// chatAdapter builds the adapter for one account, or explains why it cannot.
func (s *Server) chatAdapter(ctx context.Context, a db.PlatformAccount) (chat.Adapter, error) {
	switch a.Platform {
	case db.PlatformYouTube:
		return chat.NewYouTube(chat.YouTubeConfig{
			AccountRef: a.AccountRef,
			Channel:    a.AccountName,
			Token:      s.chatToken(a.ID),
		})

	case db.PlatformTwitch:
		// IRC wants the token itself rather than a supplier: the PASS line is
		// written once, at connect, and the runner reconnects through this
		// whole function when it expires.
		acct, err := s.tokenFor(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		// Moderation is Helix, not IRC, and needs three things IRC never
		// carries: a Client-Id, the channel's numeric id, and a token that is
		// still fresh an hour after connect.
		//
		// Missing developer credentials are NOT an error here. Chat itself
		// works without them -- IRC has the token it needs -- and refusing to
		// open the pane because a moderation call might one day fail would
		// trade a working feature for one that is not being used yet. Delete
		// says so plainly if it is ever called.
		var clientID string
		if creds, cerr := s.store.GetPlatformCreds(s.box, db.PlatformTwitch); cerr == nil {
			clientID = creds.ClientID
		}
		return chat.NewTwitch(chat.TwitchConfig{
			Nick:       a.AccountName,
			Channel:    a.AccountName,
			AccountRef: a.AccountRef,
			Token:      acct.AccessToken,
			HelixToken: s.chatToken(a.ID),
			ClientID:   clientID,
			// Channel is set from AccountName just above, so the account IS the
			// broadcaster and the two ids are the same. They are separate fields
			// because that stops being true the moment someone reads a channel
			// they merely moderate, and a moderation call addressed at the wrong
			// channel is worse than one that refuses.
			BroadcasterID: a.AccountRef,
			ModeratorID:   a.AccountRef,
		})

	case db.PlatformKick:
		id, err := kickBroadcasterID(a.AccountRef)
		if err != nil {
			return nil, err
		}
		return chat.NewKick(chat.KickConfig{
			AccountRef:        a.AccountRef,
			BroadcasterUserID: id,
			Channel:           a.AccountName,
			Token:             s.chatToken(a.ID),
			PublicURL:         publicBaseURL(s.cfg),
			CallbackSecret:    s.kickCallbackSecret(),
			// The signature check. Without this line the adapter refuses every
			// delivery — which is the point: the previous version of this
			// construction site simply omitted it, and the handler's nil guard
			// turned that omission into unauthenticated chat injection rather
			// than into a visible failure.
			Verify: chat.KickVerifier(s.kickKeys),
		})

	case db.PlatformFacebook:
		// Facebook comments hang off one live video, and the only place its id
		// survives a restart is the destination's stream key — see
		// oauth.FacebookLiveVideoID. An empty id is a supported state the
		// adapter explains for itself, so it is passed through rather than
		// refused here.
		return chat.NewFacebook(chat.FacebookConfig{
			AccountRef:  a.AccountRef,
			Channel:     a.AccountName,
			Token:       s.chatToken(a.ID),
			LiveVideoID: s.facebookLiveVideoID(a.ID),
		})
	}
	return nil, fmt.Errorf("polyemesis has no chat adapter for %s", a.Platform)
}

// kickBroadcasterID turns the stored account ref back into the numeric id Kick
// needs to post chat. Kick's Account() writes the id into the ref, so a ref that
// is not numeric means the row predates the Kick provider.
func kickBroadcasterID(ref string) (int, error) {
	id := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(ref), "%d", &id); err != nil || id <= 0 {
		return 0, fmt.Errorf("this Kick account has no broadcaster id stored; disconnect and reconnect it")
	}
	return id, nil
}

// facebookLiveVideoID recovers the broadcast id from a destination linked to
// this account. Empty when nothing is linked or the key does not carry one.
func (s *Server) facebookLiveVideoID(accountID int64) string {
	dests, err := s.store.ListDestinations()
	if err != nil {
		return ""
	}
	for _, d := range dests {
		if d.AccountID == nil || *d.AccountID != accountID {
			continue
		}
		if id := oauth.FacebookLiveVideoID(d.StreamKey); id != "" {
			return id
		}
	}
	return ""
}

// SetFacebookBroadcast points the running Facebook adapter at a broadcast. The
// key-refresh path calls it after creating a live video, so comments start
// arriving without waiting for a restart.
func (s *Server) setFacebookBroadcast(accountRef, liveVideoID string) {
	if s.chat == nil || liveVideoID == "" {
		return
	}
	a, ok := s.chat.Adapter(db.PlatformFacebook, accountRef)
	if !ok {
		return
	}
	if fb, ok := a.(*chat.FacebookAdapter); ok {
		fb.SetLiveVideoID(liveVideoID)
	}
}

// handleKickChatWebhook receives Kick's event deliveries.
//
// It is mounted outside the session middleware because Kick posts here
// unauthenticated; the unguessable path segment is the credential, which is why
// it is compared in full before anything else happens. A mismatch answers 404
// rather than 401: an attacker probing for the path learns nothing from a
// "wrong secret" that they would not learn from "no such route".
func (s *Server) handleKickChatWebhook(w http.ResponseWriter, r *http.Request) {
	want := s.kickCallbackSecret()
	if want == "" || !secretEqual(chi.URLParam(r, "secret"), want) {
		http.NotFound(w, r)
		return
	}
	if s.chat == nil {
		// 200 on purpose. Kick retries a non-2xx, and a server with chat
		// switched off would collect an ever-growing retry storm for events it
		// is never going to want.
		w.WriteHeader(http.StatusOK)
		return
	}
	h, ok := s.kickChatHandler()
	if !ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	h.ServeHTTP(w, r)
}

// kickChatHandler finds the attached Kick adapter's receiver.
func (s *Server) kickChatHandler() (http.Handler, bool) {
	for _, st := range s.chat.Statuses() {
		if st.Platform != db.PlatformKick {
			continue
		}
		a, ok := s.chat.Adapter(db.PlatformKick, st.Account)
		if !ok {
			continue
		}
		if k, ok := a.(*chat.KickAdapter); ok {
			return k.Handler(), true
		}
	}
	return nil, false
}

// KickChatCallbackURL is what the operator pastes into their Kick app. Empty
// when this server does not know its own public URL, which the setup page says
// in words rather than showing a URL that would not work.
func (s *Server) KickChatCallbackURL() string {
	base := publicBaseURL(s.cfg)
	secret := s.kickCallbackSecret()
	if base == "" || secret == "" {
		return ""
	}
	return base + "/api/v1/chat/kick/" + secret
}
