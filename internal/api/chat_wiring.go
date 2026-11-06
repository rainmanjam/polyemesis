package api

// Turning connected accounts into running chat adapters.
//
// internal/chat knows how to talk to four platforms and internal/api knows how
// to answer questions about them; this file is the only place that knows which
// accounts exist. StartChat runs once, at startup, and it is deliberately
// total: every account that cannot produce an adapter contributes a named
// reason rather than an aborted pass, because one misconfigured platform must
// not cost the operator the other three.
//
// CONNECTION AND REMOVAL ARE BOTH LIVE. Connecting an account, or connecting it
// again, starts its chat at once (attachAccountChat); disconnecting it or
// removing its platform's developer credentials ends it at once
// (detachAccountChat, detachPlatformChat). Until the connect half existed, a
// Twitch account connected in Settings → Platforms read no chat until the
// process restarted, and the reconnect a failed Twitch login asks for left the
// chip reading Failed. StartChat and a connect decide through the same
// function, connectChat, so a restart and a connect cannot disagree about an
// account; and all four run under chatWire, each reading the database again
// under it, so a connect and a disconnect that race leave the Hub agreeing with
// whichever wrote last.
//
// Adapters are attached as soon as an account exists rather than when a
// broadcast starts. That is on purpose. Twitch IRC and Kick's webhook work
// whether or not anything is live, and the two that genuinely need a broadcast
// — YouTube and Facebook — already report "waiting for a live broadcast" as a
// health state with a sentence attached. Attaching late would mean the chat
// pane was empty and silent for the one platform an operator is most likely to
// be testing with.

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
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
//
// ctx is the server's lifetime, and StartChat keeps it: every account connected
// after startup attaches under it too (see attachAccountChat).
func (s *Server) StartChat(ctx context.Context) int {
	if s.chat == nil || s.store == nil {
		return 0
	}
	s.chatWire.Lock()
	defer s.chatWire.Unlock()
	// Kept before anything below can fail, so an install whose startup could
	// not read its accounts still starts the chat of the next one connected.
	s.chatLife = ctx

	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		s.log.Warn("chat: could not list connected accounts", "err", err)
		return 0
	}
	withCreds, err := s.platformsWithCreds()
	if err != nil {
		s.log.Warn("chat: could not list platform credentials", "err", err)
		return 0
	}

	attached := 0
	for _, a := range accts {
		if s.connectChat(ctx, a, withCreds) {
			attached++
		}
	}

	// Rumble hangs off no account row, so it cannot come out of the loop above.
	// See attachRumble.
	if s.attachRumble(ctx) {
		attached++
	}
	return attached
}

// connectChat attaches one account's chat when the rules allow it, and reports
// whether it did. Caller holds chatWire.
//
// THE ONE RULE, shared by StartChat and attachAccountChat so a restart and a
// connect cannot come to different answers about the same account:
//   - no developer credentials, no chat (the rule detachPlatformChat applies
//     live);
//   - chatAdapter's refusals, the X Livestream gate among them;
//   - Hub.Attach's, which reads the capability matrix to decide whether the
//     platform can be offered as a send target.
func (s *Server) connectChat(ctx context.Context, a db.PlatformAccount, withCreds map[db.Platform]bool) bool {
	// THE SAME RULE detachPlatformChat applies live. Removing a platform's
	// developer credentials ends its chat and leaves its account rows in place,
	// so a startup that attached every account regardless would put back, on
	// the next restart, the very chat the removal took away.
	if !withCreds[a.Platform] {
		s.log.Info("chat: not connecting a platform", "platform", a.Platform, "account", a.AccountName,
			"reason", "no developer credentials are stored for it; add them in Settings → Platform credentials")
		return false
	}
	adapter, err := s.chatAdapter(ctx, a)
	if err != nil {
		// Info, not Warn: "you have not gone live yet" and "this platform has
		// no chat adapter" are both normal, and a warning for either trains
		// operators to ignore the ones that matter.
		s.log.Info("chat: not connecting a platform", "platform", a.Platform, "account", a.AccountName, "reason", err)
		return false
	}
	if err := s.chat.Attach(ctx, adapter); err != nil {
		s.log.Warn("chat: could not attach", "platform", a.Platform, "err", err)
		return false
	}
	return true
}

// attachAccountChat starts the chat of an account that has just been
// connected, or restarts it for one connected again. The OAuth callback and the
// device-code poll call it once the row is stored. Nothing here fails the
// connect, which has already succeeded: a chat that cannot start says why in
// the log, exactly as it would at startup.
//
// REPLACE, NOT SKIP. Connecting over an attached account swaps its adapter for
// a new one rather than leaving the old one running. The old one may be why the
// operator is connecting again: a Twitch adapter whose login was refused stops
// for good and says "reconnect the Twitch account in Settings → Platforms", and
// a reconnect that found it attached and left it alone would leave the chip
// reading Failed until a restart. The Hub refuses a second adapter for one
// account, so this is also what keeps a reconnect at exactly one.
//
// THE ROW IS READ AGAIN UNDER chatWire, never taken from the caller. A
// disconnect can land between the caller's write and this lock, and attaching
// from the caller's copy would start a chat for an account that no longer
// exists, with nothing left to end it. detachAccountChat reads again under the
// same lock, so whichever of the two runs second sees what the other did.
//
// UNDER THE SERVER'S LIFETIME, not the request's. The callback's context ends
// with its redirect, and an adapter started under it would stop at once and
// stay in the Hub as a chip over a chat that never arrives.
func (s *Server) attachAccountChat(p db.Platform, accountRef string) {
	// Rumble's chat hangs off an environment variable, not an account (see
	// attachRumble), and no connect has any claim on it.
	if s.chat == nil || s.store == nil || p == db.PlatformRumble {
		return
	}
	if s.beforeChatAttach != nil {
		s.beforeChatAttach()
	}
	s.chatWire.Lock()
	defer s.chatWire.Unlock()
	if s.chatLife == nil {
		// StartChat has not run, so there is no lifetime to attach under. When
		// it runs it lists every account, this one included.
		s.log.Info("chat: not connecting a platform yet", "platform", p,
			"reason", "chat has not started; it attaches every connected account when it does")
		return
	}
	// A READ THAT FAILS CHANGES NOTHING. With the row unknown there is nothing
	// to build from, and detaching anyway would trade a chat that may well be
	// working for none at all over a read that may only have hiccupped. The
	// next connect, or a restart, puts it right.
	a, found, err := s.accountByKey(p, accountRef)
	if err != nil {
		s.log.Warn("chat: could not read the account just connected; its chat is left as it was until it is connected again or polyemesis restarts",
			"platform", p, "err", err)
		return
	}
	withCreds, err := s.platformsWithCreds()
	if err != nil {
		s.log.Warn("chat: could not list platform credentials; the account's chat is left as it was until it is connected again or polyemesis restarts",
			"platform", p, "err", err)
		return
	}
	if s.whileChatAttach != nil {
		s.whileChatAttach()
	}
	// The old adapter goes whatever happens next, so the Hub ends up holding
	// what a restart would build for this row: one fresh adapter, or none.
	s.detachChat(p, accountRef)
	if !found {
		return
	}
	s.connectChat(s.chatLife, a, withCreds)
}

// accountByKey finds the account the Hub knows as (platform, account_ref), the
// pair platform_accounts holds UNIQUE. Read through ListPlatformAccounts for
// the reason accountChatKey gives -- it decrypts nothing -- and because it is
// the shape StartChat builds every adapter from.
func (s *Server) accountByKey(p db.Platform, accountRef string) (db.PlatformAccount, bool, error) {
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		return db.PlatformAccount{}, false, err
	}
	for _, a := range accts {
		if a.Platform == p && a.AccountRef == accountRef {
			return a, true, nil
		}
	}
	return db.PlatformAccount{}, false, nil
}

// platformsWithCreds is the set of platforms that have a developer app stored.
// Read through ListPlatformCreds, which decrypts nothing: the question is
// whether a row exists, and a secret that fails to open is the refresh path's
// problem to report, not a reason to skip chat here.
func (s *Server) platformsWithCreds() (map[db.Platform]bool, error) {
	creds, err := s.store.ListPlatformCreds()
	if err != nil {
		return nil, err
	}
	out := make(map[db.Platform]bool, len(creds))
	for _, c := range creds {
		out[c.Platform] = true
	}
	return out, nil
}

// accountChatKey is the (platform, account_ref) the Hub knows one account by.
//
// Read through ListPlatformAccounts rather than GetPlatformAccount because the
// latter decrypts the tokens, and a token that will not open is no reason to
// refuse a disconnect -- it is a reason to want one. An id with no row is
// db.ErrNotFound, the answer DeletePlatformAccount would have given.
func (s *Server) accountChatKey(id int64) (db.Platform, string, error) {
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		return "", "", err
	}
	for _, a := range accts {
		if a.ID == id {
			return a.Platform, a.AccountRef, nil
		}
	}
	return "", "", db.ErrNotFound
}

// detachAccountChat ends the chat of an account that has just been
// disconnected. The Hub keys adapters by (platform, account_ref), which
// platform_accounts holds UNIQUE, so this ends exactly that account's chat and
// no other.
//
// Without it the adapter outlived its row. A Facebook adapter with no live
// video never calls its token, so it never noticed; it went on reporting
// "waiting for a broadcast", and the Dashboard went on drawing its chip, until
// the process restarted.
//
// A RECONNECT MAY HAVE PUT THE ROW BACK between the delete and chatWire -- the
// same platform and account_ref under a new id -- and attached its chat.
// Ending that chat would leave a connected account silent until a restart, so
// a row found here is left to the connect that wrote it. A read that fails
// detaches anyway: the delete this follows did happen.
func (s *Server) detachAccountChat(p db.Platform, accountRef string) {
	if s.chat == nil {
		return
	}
	s.chatWire.Lock()
	defer s.chatWire.Unlock()
	_, found, err := s.accountByKey(p, accountRef)
	if err == nil && found {
		return
	}
	if err != nil {
		s.log.Warn("chat: could not check whether the account was connected again; ending its chat, since the disconnect went through",
			"platform", p, "account", accountRef, "err", err)
	}
	s.detachChat(p, accountRef)
}

// chatDetachWait bounds how long a delete handler waits for an adapter to
// return after it is cancelled. Every adapter honours its context, so this is
// normally milliseconds; the bound is for the one that does not, which would
// otherwise hold the operator's request open with the delete already done.
const chatDetachWait = 5 * time.Second

// detachChat is the one place a handler ends a chat -- a delete, or a connect
// replacing the adapter it had: unlisted and cancelled at once, with the wait
// bounded (see chat.Hub.DetachWithin). Caller holds chatWire.
func (s *Server) detachChat(p db.Platform, account string) {
	if !s.chat.DetachWithin(p, account, chatDetachWait) {
		s.log.Warn("chat adapter is slow to stop; it is detached and will finish in the background",
			"platform", p, "account", account, "waited", chatDetachWait)
	}
}

// detachPlatformChat ends every chat on a platform whose developer credentials
// have just been removed.
//
// Every token on the platform was issued to the app being removed, and none of
// them can be refreshed without it -- YouTube's reads are already refused the
// moment its client goes (ytQuotaShared.Unbind). Leaving the chat attached
// would keep the platform on the Dashboard until a token expired, and then show
// it failing for a reason the operator chose. StartChat applies the same rule
// at startup, so a restart agrees with this.
//
// RUMBLE IS EXEMPT. Its chat hangs off an environment variable rather than an
// app or an account (see attachRumble), so no credentials route has any claim
// on it, and a DELETE that names it -- which deletes nothing -- must not end it.
func (s *Server) detachPlatformChat(p db.Platform) {
	if s.chat == nil || p == db.PlatformRumble {
		return
	}
	// Under chatWire, so a connect that read the credentials just before they
	// went cannot attach after this has looked.
	s.chatWire.Lock()
	defer s.chatWire.Unlock()
	for _, st := range s.chat.Statuses() {
		if st.Platform == p {
			s.detachChat(st.Platform, st.Account)
		}
	}
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
	// The key comes from the environment only, read once at startup
	// (startupenv). Never a flag: argv is world-readable in ps, and a key every
	// local user can read has leaked. See chat.RumbleChatKeyEnv.
	key := s.env.RumbleChatKey
	if key == "" {
		return false
	}

	adapter, err := chat.NewRumble(chat.RumbleConfig{
		// There is no account id to use, and the ref travels on every message
		// to keep two accounts on one platform distinguishable. Rumble can only
		// ever have one here, because there is only one environment variable.
		AccountRef: "rumble",
		Channel:    s.env.RumbleChatChannel,
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
		// THE ALLOWANCE IS THE DENOMINATOR OF EVERY PACING DECISION, so it has
		// to come from the operator rather than from a constant. #732: the
		// field existed and nothing set it, so an install granted a larger
		// quota after a YouTube API Services audit was paced against the
		// default ten thousand -- polling roughly a hundred times slower than
		// it was entitled to, with nothing saying so.
		//
		// This is the BUILD half of the answer only. chatAdapter runs when an
		// adapter is built -- at startup, and when an account is connected --
		// so a value read here and held would be a value a settings save could
		// not change until the next build. ApplyYouTubeQuota is the other
		// half, pushing a saved allowance into the adapter this built;
		// settingsReload records the pair as ClassLive on the strength of it.
		units, reserve := youtubeQuotaPair(s.youtubeQuota())
		return chat.NewYouTube(chat.YouTubeConfig{
			AccountRef:   a.AccountRef,
			Channel:      a.AccountName,
			Token:        s.chatToken(a.ID),
			QuotaUnits:   units,
			QuotaReserve: reserve,
			// The Google client's ONE allowance, shared with every other
			// YouTube channel on it and every other spender (PR #950 review).
			Quota: s.ytQuota(),
		})

	case db.PlatformTwitch:
		// The token is read here so a missing one is a "not configured" state
		// at attach. It is NOT what reconnects use: the runner reconnects by
		// calling Run again on the adapter this returns, not through this
		// function, so PassToken supplies a fresh one for every PASS line.
		acct, err := s.tokenFor(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		// Moderation is Helix, not IRC, and needs three things IRC never
		// carries: a Client-Id, the channel's numeric id, and a token that is
		// still fresh an hour after connect.
		//
		// connectChat has already checked that a credentials ROW exists; this
		// reads it for the client id. A read that still fails here -- a secret
		// that will not open -- is NOT an error: IRC has the token it needs,
		// and refusing to open the pane because a moderation call might one
		// day fail would trade a working feature for one that is not being
		// used yet. Delete says so plainly if it is ever called.
		var clientID string
		if creds, cerr := s.store.GetPlatformCreds(s.box, db.PlatformTwitch); cerr == nil {
			clientID = creds.ClientID
		}
		return chat.NewTwitch(chat.TwitchConfig{
			Nick:       a.AccountName,
			Channel:    a.AccountName,
			AccountRef: a.AccountRef,
			Token:      acct.AccessToken,
			PassToken:  s.chatToken(a.ID),
			HelixToken: s.chatToken(a.ID),
			ClientID:   clientID,
			// Channel is set from AccountName just above, so the account IS the
			// broadcaster and the two ids are the same. They are separate fields
			// because that stops being true the moment someone reads a channel
			// they merely moderate, and a moderation call addressed at the wrong
			// channel is worse than one that refuses.
			BroadcasterID: a.AccountRef,
			ModeratorID:   a.AccountRef,
			// Nil in production. The tests' fake IRC server.
			Dial: s.twitchChatDial,
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

	case db.PlatformX:
		// X chat is part of the Livestream family, so it is attached only while
		// the operator's Livestream gate is open (POLYEMESIS_X_LIVESTREAM). With
		// the gate closed the account stays connected for sign-in and nothing
		// reads or posts chat for it.
		if s.livestreamGate(db.PlatformX) != "open" {
			return nil, fmt.Errorf("X chat needs X Livestream access (app must be whitelisted by X)")
		}
		return chat.NewX(chat.XConfig{
			AccountRef: a.AccountRef,
			Channel:    a.AccountName,
			Token:      s.chatToken(a.ID),
			// PROVISIONAL: the activity stream needs an app-only bearer token,
			// which the operator supplies from the X developer console. Nothing
			// here mints it. Empty makes the adapter's Run refuse with a Fatal
			// error that says so.
			AppBearer: s.env.XAppBearer,
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

// youtubeQuota is the operator's stated YouTube allowance, falling back to the
// package defaults when settings cannot be read. #732.
//
// FALLING BACK RATHER THAN FAILING, and only for an unreadable store: a chat
// adapter that refused to start because the settings table hiccupped would take
// chat down for a reason unrelated to chat. The defaults are the same numbers
// NewYouTube would have used anyway, so this degrades to the old behaviour
// rather than to something new.
//
// A stored ZERO cannot arrive here -- ChatSettings.problems refuses it on the
// way in, for the reason on the field: too high kills chat mid-broadcast, so a
// value that arrived by accident is refused where it is entered. The guard
// below is for a row written before this field existed.
func (s *Server) youtubeQuota() db.ChatSettings {
	cs := db.DefaultSettings().Chat
	set, err := s.store.GetSettings()
	if err != nil {
		return cs
	}
	if set.Chat.YouTubeQuotaUnits > 0 {
		cs.YouTubeQuotaUnits = set.Chat.YouTubeQuotaUnits
		cs.YouTubeQuotaReserve = set.Chat.YouTubeQuotaReserve
	}
	return cs
}

// youtubeQuotaPair is the one place the stored allowance stops being two
// interchangeable ints and becomes the two distinct types the chat package
// takes.
//
// poka-yoke: gives the compiler the only thing it needs to reject a swapped
// YouTubeConfig literal [control]
//
// db.ChatSettings holds both numbers as int, because it is a settings row and
// every other field in it is a plain scalar too. chat.YouTubeConfig holds them
// as chat.QuotaUnits and chat.QuotaReserve, because there the two roles are
// worth telling apart -- a 10,000-unit allowance with a 200 reserve written the
// wrong way round becomes a 200-unit allowance, the clamp then drops the
// reserve, and the adapter paces a hundred times too slowly with nothing
// reporting a problem. That is #732 restored in silence.
//
// One conversion has to exist somewhere between an int pair and a typed pair,
// and this is deliberately it: a single named line whose two halves read
// Units-to-Units and Reserve-to-Reserve, rather than a conversion at every
// construction site. It is not itself compiler-proof, which is why
// TestYouTubeQuotaPairKeepsTheAllowanceAndTheReserveApart asserts it directly.
//
// Carrying the two types all the way back into db.ChatSettings would close even
// that line, and is the right next step; it is not taken here because it lands
// in internal/db and internal/api/handlers.go, which this change does not own.
func youtubeQuotaPair(cs db.ChatSettings) (chat.QuotaUnits, chat.QuotaReserve) {
	return chat.QuotaUnits(cs.YouTubeQuotaUnits), chat.QuotaReserve(cs.YouTubeQuotaReserve)
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

// setXBroadcast is the lifecycle coordinator's pointChat: it points the running
// X chat adapter of one account at the broadcast the coordinator just published,
// or clears it with "" after an end. The adapter's own 15-second poll stays as
// the fallback for a broadcast published outside polyemesis. Every miss -- chat
// off, the account gone, no adapter because the Livestream gate is closed -- is
// a quiet no-op, because it runs on the coordinator's sweep.
func (s *Server) setXBroadcast(accountID int64, broadcastID string) {
	if s.chat == nil || s.store == nil {
		return
	}
	acct, err := s.store.GetPlatformAccount(s.box, accountID)
	if err != nil || acct.Platform != db.PlatformX {
		return
	}
	a, ok := s.chat.Adapter(db.PlatformX, acct.AccountRef)
	if !ok {
		return
	}
	if xa, ok := a.(*chat.XAdapter); ok {
		xa.SetBroadcast(broadcastID)
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
	h, ok := s.kickChatHandler(r)
	if !ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	h.ServeHTTP(w, r)
}

// kickChatHandler finds the receiver of the Kick adapter a delivery belongs to.
//
// Kick posts every connected channel to one callback URL and the adapter stamps
// its own account on what it ingests, so a delivery has to reach the adapter for
// the broadcaster it names. A delivery for a channel with no adapter here is
// dropped after its signature is checked; one that names no channel at all goes to the
// first adapter, which counts it as unreadable. GET probes carry no body and go
// to the first adapter, as before.
func (s *Server) kickChatHandler(r *http.Request) (http.Handler, bool) {
	var kicks []*chat.KickAdapter
	for _, st := range s.chat.Statuses() {
		if st.Platform != db.PlatformKick {
			continue
		}
		a, ok := s.chat.Adapter(db.PlatformKick, st.Account)
		if !ok {
			continue
		}
		if k, ok := a.(*chat.KickAdapter); ok {
			kicks = append(kicks, k)
		}
	}
	if len(kicks) == 0 {
		return nil, false
	}
	if r.Method != http.MethodPost {
		return kicks[0].Handler(), true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return kicks[0].Handler(), true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	id := chat.KickDeliveryBroadcaster(body)
	if id == 0 {
		return kicks[0].Handler(), true
	}
	for _, k := range kicks {
		if k.BroadcasterUserID() == id {
			return k.Handler(), true
		}
	}
	// Nobody here listens to that channel. It is still checked, so a forged
	// delivery earns the same 401 it would for a connected one.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := kicks[0].VerifyDelivery(r, body); err != nil {
			http.Error(w, "signature rejected", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), true
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
