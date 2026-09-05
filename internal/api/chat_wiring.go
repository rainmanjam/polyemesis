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
	"os"
	"strings"
	"time"

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

	// Rumble hangs off no account row, so it cannot come out of the loop above.
	// See attachRumble.
	if s.attachRumble(ctx) {
		attached++
	}
	return attached
}

// attachRumble connects Rumble chat when a key is present in the environment,
// and reports whether it did.
//
// SEPARATE FROM chatAdapter ON PURPOSE, because Rumble is the first platform
// here that does not fit the account model at all. The other four are rows in
// platform_accounts with an OAuth token behind them; Rumble's live-stream API
// has no sign-in to produce such a row, only a key the operator copies out of
// their own settings page. Threading a fake account row through the store so it
// could ride the loop above would put a credential in the database that nothing
// can refresh, revoke or expire -- so the key stays in the process environment
// and never touches disk.
//
// An absent key is SILENT and is not an error. Rumble chat is opt-in, and every
// install that has not set the variable would otherwise get a startup warning
// about a platform they have never heard of.
func (s *Server) attachRumble(ctx context.Context) bool {
	// os.Getenv is the ONLY path that touches this value. Never a flag: argv is
	// world-readable in ps, and a key every local user can read has leaked. See
	// chat.RumbleChatKeyEnv.
	key := strings.TrimSpace(os.Getenv(chat.RumbleChatKeyEnv))
	if key == "" {
		return false
	}

	adapter, err := chat.NewRumble(chat.RumbleConfig{
		// There is no account id to use, and the ref travels on every message
		// to keep two accounts on one platform distinguishable. Rumble can only
		// ever have one here, because there is only one environment variable.
		AccountRef: "rumble",
		Channel:    strings.TrimSpace(os.Getenv("RUMBLE_CHAT_CHANNEL")),
		Key:        key,
	})
	if err != nil {
		// The error names the variable, never its value -- chat.NewRumble is
		// written so it cannot do otherwise.
		s.log.Info("chat: not connecting Rumble", "reason", err)
		return false
	}
	if err := s.chat.Attach(ctx, adapter); err != nil {
		s.log.Warn("chat: could not attach", "platform", db.PlatformRumble, "err", err)
		return false
	}
	return true
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

// facebookLiveVideoID recovers the broadcast id for this account's chat.
//
// TWO ROUTES, AND THE SECOND IS #725. A key polyemesis fetched carries the
// live-video id inside it, so FacebookLiveVideoID reads it straight off and
// nothing has to be asked. A key PASTED from Live Producer does not -- a
// persistent key is `FB-<numbers>-<n>-<random>` -- so this returned empty and
// the chat pane said it had nothing to attach to.
//
// It can be asked. Going live with a persistent key creates a live video on the
// same target the connected account can see, and the provider's AdoptLiveVideo
// finds it -- refusing rather than guessing when the target carries more than
// one at the same status. Facebook's own limit makes that refusal rare on this
// path: a persistent key carries one live video at a time.
//
// ONLY WHEN THE KEY CARRIES NO ID. A fetched key is authoritative and costs no
// request; adopting over it would replace a fact with a lookup.
//
// FAILURE IS EMPTY, NOT AN ERROR. Empty is the state this function already had
// and the adapter already explains, so a target with nothing live, an expired
// token or an ambiguous pair leaves the chat pane exactly as it was rather than
// breaking the wiring for every other platform in the same loop.
func (s *Server) facebookLiveVideoID(accountID int64) string {
	dests, err := s.store.ListDestinations()
	if err != nil {
		return ""
	}
	pasted := false
	for _, d := range dests {
		if d.AccountID == nil || *d.AccountID != accountID {
			continue
		}
		if id := oauth.FacebookLiveVideoID(d.StreamKey); id != "" {
			return id
		}
		// Linked to this account and holding a key that is not an id: the
		// hand-pasted case, and the only one worth spending a request on.
		pasted = true
	}
	if !pasted {
		return ""
	}
	return s.adoptFacebookLiveVideo(accountID)
}

// adoptFacebookLiveVideo asks the platform which broadcast this account is
// running. Empty on any refusal, for the reason above.
func (s *Server) adoptFacebookLiveVideo(accountID int64) string {
	p, err := s.providers.Get(db.PlatformFacebook)
	if err != nil {
		return ""
	}
	fb, ok := p.(*oauth.Facebook)
	if !ok || fb == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acct, err := s.tokenFor(ctx, accountID)
	if err != nil {
		return ""
	}
	id, err := fb.AdoptLiveVideo(ctx, acct.AccessToken, acct.AccountRef)
	if err != nil {
		// AT DEBUG, not warn. "Nothing is live on this target" is the ordinary
		// state of an account between broadcasts, and a warning on every chat
		// rewire for an idle account is noise that teaches operators to skim.
		s.log.Debug("no Facebook broadcast to attach chat to",
			"account", accountID, "err", err)
		return ""
	}
	s.log.Info("adopted a Facebook broadcast for chat; this destination's key was "+
		"pasted rather than fetched, so its id came from the account rather than the key",
		"account", accountID, "liveVideo", id)
	return id
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
// it is compared in full, in constant time, before anything else happens.
//
// A mismatch answers 404 rather than 401, and the answer is BYTE-IDENTICAL to
// the one an unrouted /api/v1 path gets: same status, same Content-Type, same
// Cache-Control, same body. That equality is the claim, and it was FALSE until
// it was measured (#158). This used to call http.NotFound, which writes Go's
// own `404 page not found` as text/plain with no Cache-Control, while the
// router's own miss is web.Handler's `{"error":"no such endpoint"}` as JSON
// with Cache-Control: no-store. Two different 404s, trivially told apart by an
// anonymous caller, and the difference answered the one question this route's
// existence is meant to keep private: whether /api/v1/chat/kick/{secret} is
// mounted here at all. The comment asserting an attacker "learns nothing" was
// therefore a false statement of coverage, which is worse than none, because it
// is what the next reader relies on.
//
// The SECRET itself was never the weak part and is untouched: 128 bits derived
// from the data key, compared with subtle.ConstantTimeCompare before any other
// work happens, so there is no timing, length or partial-match oracle on the
// value. What leaked was the SHAPE of the reply, not the credential.
//
// See TestWrongKickSecretIsIndistinguishableFromAnUnroutedPath, which asserts
// the equality against the real router rather than against this comment.
func (s *Server) handleKickChatWebhook(w http.ResponseWriter, r *http.Request) {
	want := s.kickCallbackSecret()
	if want == "" || !secretEqual(chi.URLParam(r, "secret"), want) {
		writeNoSuchEndpoint(w)
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
