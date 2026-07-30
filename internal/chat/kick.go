package chat

// Kick chat: webhooks in, REST out.
//
// Kick does not offer a socket to read; it POSTs events to a URL you give it.
// That inverts the usual failure mode. Every other adapter here fails loudly —
// a socket closes, a poll returns 403 — but a webhook that Kick cannot reach
// fails as perfect silence, and silence is indistinguishable from a quiet
// chat. An operator would sit through an entire broadcast believing nobody was
// talking.
//
// So this adapter spends most of its effort on saying so: it refuses to
// pretend, it probes its own callback URL from the outside, it names the
// reasons a URL cannot work (no public URL configured, a loopback or private
// address, plain HTTP), and if nothing has arrived after a while it says that
// too, with the exact URL to paste into the Kick app dashboard. None of those
// checks block anything — an operator behind a tunnel we cannot see through
// still gets a working chat, just with a warning next to it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

const (
	// KickMaxMessage is Kick's published limit on chat content.
	KickMaxMessage = 500
	// kickSilenceAfter is how long a connected-and-reachable adapter waits
	// before it starts telling the operator that nothing has arrived. Long
	// enough that a genuinely quiet ten minutes is not called an error, short
	// enough to catch a missing subscription well inside one broadcast.
	kickSilenceAfter = 10 * time.Minute
	// kickHealthTick is how often the silence check runs.
	kickHealthTick = 30 * time.Second
	// kickProbeTimeout bounds the self-reachability probe. It is short: this
	// is a diagnostic, and a slow answer to it must not delay chat starting.
	kickProbeTimeout = 8 * time.Second
	// kickBodyLimit bounds a webhook body.
	kickBodyLimit = 1 << 20
)

// KickConfig configures the webhook adapter.
type KickConfig struct {
	AccountRef string
	// BroadcasterUserID is Kick's numeric id for the channel, required to send.
	BroadcasterUserID int
	// Channel is the slug, for the tab header.
	Channel string
	Token   TokenFunc

	// PublicURL is the externally reachable base URL of this polyemesis, e.g.
	// "https://stream.example.com". Empty is a supported state and produces a
	// clear explanation rather than a failure.
	PublicURL string
	// CallbackSecret makes the callback path unguessable. It is defence in
	// depth rather than the authentication — a secret in a URL leaks through
	// proxy logs, Referer headers and any plain-HTTP hop, which is what Verify
	// below is for. A blank one is generated.
	CallbackSecret string
	// Verify authenticates a delivery. Required: the handler refuses every POST
	// when it is nil rather than accepting unverified events.
	//
	// It stays a function rather than a concrete type so a test can sign with
	// its own key, and so the signature scheme lives in kick_verify.go where it
	// can be read against Kick's documentation side by side.
	Verify func(r *http.Request, body []byte) error

	// Probe replaces the reachability check, for tests.
	Probe   func(ctx context.Context, endpoint string) (string, error)
	APIBase string
	HTTP    *http.Client
	Now     func() time.Time
	Sleep   func(context.Context, time.Duration) bool
}

// KickAdapter receives Kick chat over webhooks and posts back over REST.
type KickAdapter struct {
	cfg KickConfig

	mu           sync.Mutex
	sink         Sink
	health       Health
	lastEventAt  time.Time
	startedAt    time.Time
	reachable    bool
	reachDetail  string
	delivered    int64
	unparseable  int64
	beforeAttach int64
}

// NewKick builds the adapter.
func NewKick(cfg KickConfig) (*KickAdapter, error) {
	if cfg.Token == nil {
		return nil, fmt.Errorf("kick chat is not configured: no access token, so connect the Kick account in Settings → Platforms")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.kick.com"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.CallbackSecret == "" {
		cfg.CallbackSecret = randomSecret()
	}
	if cfg.AccountRef == "" && cfg.BroadcasterUserID > 0 {
		cfg.AccountRef = strconv.Itoa(cfg.BroadcasterUserID)
	}
	return &KickAdapter{cfg: cfg, health: Health{State: StateConnecting}}, nil
}

func (k *KickAdapter) Platform() db.Platform { return db.PlatformKick }
func (k *KickAdapter) Account() string       { return k.cfg.AccountRef }

// CallbackPath is where the receiver must be mounted, and what the operator
// pastes into the Kick app dashboard's webhook field (prefixed by PublicURL).
// The secret is part of the path because that is what authenticates a
// delivery in the absence of a signature scheme we can verify.
func (k *KickAdapter) CallbackPath() string {
	return "/api/v1/chat/kick/" + k.cfg.CallbackSecret
}

// CallbackURL is the full URL, or empty when polyemesis does not know its own
// public address.
func (k *KickAdapter) CallbackURL() string {
	base := strings.TrimRight(strings.TrimSpace(k.cfg.PublicURL), "/")
	if base == "" {
		return ""
	}
	return base + k.CallbackPath()
}

func (k *KickAdapter) Health() Health {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.health
}

func (k *KickAdapter) setHealth(state State, detail string) {
	k.mu.Lock()
	k.health = Health{State: state, Detail: detail}
	k.mu.Unlock()
}

// Run registers the sink, checks that the callback can actually be reached and
// then holds the registration open until ctx ends, keeping the health honest
// about how long it has been since anything arrived.
func (k *KickAdapter) Run(ctx context.Context, sink Sink) error {
	k.mu.Lock()
	k.sink = sink
	k.startedAt = k.cfg.Now()
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		k.sink = nil
		k.mu.Unlock()
	}()

	reachable, detail := k.checkReachable(ctx)
	k.mu.Lock()
	k.reachable, k.reachDetail = reachable, detail
	k.mu.Unlock()
	k.refreshHealth()

	tick := time.NewTicker(kickHealthTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			k.setHealth(StateStopped, "")
			return nil
		case <-tick.C:
			k.refreshHealth()
		}
	}
}

// refreshHealth recomputes the one sentence the operator sees.
func (k *KickAdapter) refreshHealth() {
	k.mu.Lock()
	var (
		reachable = k.reachable
		detail    = k.reachDetail
		last      = k.lastEventAt
		started   = k.startedAt
		count     = k.delivered
		bad       = k.unparseable
	)
	k.mu.Unlock()

	switch {
	case !reachable:
		k.setHealth(StateDegraded, detail)
	case count == 0 && k.cfg.Now().Sub(started) > kickSilenceAfter:
		// Reachable, running, and nothing has ever arrived. The overwhelmingly
		// likely cause is that no webhook subscription exists, which polyemesis
		// cannot create on the operator's behalf.
		k.setHealth(StateDegraded, fmt.Sprintf(
			"no Kick chat events have arrived in %s. Check that your Kick app has a webhook subscription "+
				"for chat messages pointing at %s. Sending still works.",
			k.cfg.Now().Sub(started).Round(time.Minute), k.displayCallback()))
	case bad > 0 && count == 0:
		k.setHealth(StateDegraded, fmt.Sprintf(
			"Kick is delivering events (%d so far) that polyemesis could not read as chat messages. "+
				"Chat is otherwise connected.", bad))
	case last.IsZero():
		k.setHealth(StateLive, "listening for Kick chat events at "+k.displayCallback())
	default:
		k.setHealth(StateLive, "")
	}
}

// displayCallback is the callback URL for a human, or a description of why
// there is not one. It contains the path secret, which is deliberate: the
// operator has to paste it into Kick, and it is not a credential anyone else's
// screen will show.
func (k *KickAdapter) displayCallback() string {
	if u := k.CallbackURL(); u != "" {
		return u
	}
	return "this server's public URL + " + k.CallbackPath()
}

// checkReachable establishes whether Kick has any chance of delivering here.
//
// Every negative answer is a warning, never a refusal: the adapter keeps
// listening in all of them. A tunnel, a reverse proxy or a NAT rule can make a
// URL work that looks unreachable from in here, and refusing to listen because
// our own guess said so would be exactly the restrictive-check mistake this
// codebase keeps paying for.
func (k *KickAdapter) checkReachable(ctx context.Context) (bool, string) {
	raw := strings.TrimSpace(k.cfg.PublicURL)
	if raw == "" {
		return false, "Kick chat needs a publicly reachable callback URL, and polyemesis does not know its own " +
			"public address. Set the public URL in Settings → Server, then add " + k.CallbackPath() +
			" as the webhook URL in your Kick app. Until then, sending to Kick works but nothing is received."
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false, fmt.Sprintf("the configured public URL %q is not a URL Kick can post to, so no chat will be received.", raw)
	}

	var warn []string
	if u.Scheme != "https" {
		warn = append(warn, "Kick delivers webhooks over HTTPS and this URL is "+u.Scheme)
	}
	if isPrivateHost(u.Hostname()) {
		warn = append(warn, "the host "+u.Hostname()+" is a private or loopback address, which the internet cannot reach")
	}

	probe := k.cfg.Probe
	if probe == nil {
		probe = k.httpProbe
	}
	nonce := randomSecret()
	pctx, cancel := context.WithTimeout(ctx, kickProbeTimeout)
	defer cancel()

	got, perr := probe(pctx, k.CallbackURL()+"?probe="+nonce)
	switch {
	case perr != nil:
		warn = append(warn, "polyemesis could not reach its own callback URL from the outside ("+perr.Error()+")")
	case got != nonce:
		warn = append(warn, "the callback URL answered, but something other than polyemesis did — check what is in front of this server")
	}

	if len(warn) == 0 {
		return true, ""
	}
	return false, "Kick chat may not be received: " + strings.Join(warn, "; ") +
		". The webhook URL to configure in your Kick app is " + k.displayCallback() +
		". polyemesis is listening anyway, in case it is reachable in a way it cannot see from here."
}

// httpProbe fetches the callback URL and returns whatever nonce came back.
func (k *KickAdapter) httpProbe(ctx context.Context, endpoint string) (string, error) {
	hc := k.cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: kickProbeTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("callback URL returned %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(body)), nil
}

// isPrivateHost reports whether an address is one the public internet cannot
// route to. A hostname that is not a literal IP returns false — resolving it
// here would be a guess about somebody else's DNS.
func isPrivateHost(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return strings.EqualFold(host, "localhost")
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// ------------------------------------------------------------------ receiver

// Handler is the webhook endpoint, for the API layer to mount at
// CallbackPath(). It answers a GET probe with the nonce it was given, which is
// how checkReachable proves that the thing answering that URL is this process
// and not a parked domain returning 200 for everything.
func (k *KickAdapter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, r.URL.Query().Get("probe"))
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, kickBodyLimit))
		if err != nil {
			http.Error(w, "could not read the request body", http.StatusBadRequest)
			return
		}
		// Fail closed. A nil Verify used to mean "skip the check", which is how
		// this adapter shipped with signature verification silently switched
		// off: the hook existed, the nil guard existed, and no construction
		// site ever assigned it. An unconfigured verifier is now a refused
		// delivery, so the same omission would surface as chat that does not
		// arrive rather than as chat that arrives unauthenticated.
		if k.cfg.Verify == nil {
			http.Error(w, "webhook verification is not configured", http.StatusServiceUnavailable)
			return
		}
		if err := k.cfg.Verify(r, body); err != nil {
			// 401 and nothing else. Which part of a forgery failed is not
			// information the sender is owed.
			http.Error(w, "signature rejected", http.StatusUnauthorized)
			return
		}

		k.ingest(r.Header.Get("Kick-Event-Type"), body)
		// Always 200 once the body is in hand. A webhook that gets an error
		// gets retried, and a retry storm of chat messages helps nobody: a
		// message we could not parse is a message, not an outage.
		w.WriteHeader(http.StatusOK)
	})
}

// ingest turns one delivery into a message, or counts it as unreadable.
func (k *KickAdapter) ingest(eventType string, body []byte) {
	k.mu.Lock()
	k.lastEventAt = k.cfg.Now()
	sink := k.sink
	k.mu.Unlock()

	m, ok := k.messageFrom(eventType, body)
	if !ok {
		k.mu.Lock()
		k.unparseable++
		k.mu.Unlock()
		return
	}
	if sink == nil {
		// A delivery that arrived while nothing was attached. Counted rather
		// than dropped silently, so "Kick is posting to us but chat is off"
		// has a number behind it.
		k.mu.Lock()
		k.beforeAttach++
		k.mu.Unlock()
		return
	}
	k.mu.Lock()
	k.delivered++
	k.mu.Unlock()
	sink.Deliver(m)
}

// kickUser is a broadcaster or a sender on a Kick event.
type kickUser struct {
	UserID      int    `json:"user_id"`
	Username    string `json:"username"`
	ChannelSlug string `json:"channel_slug"`
	Identity    struct {
		UsernameColor string `json:"username_color"`
		Badges        []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"badges"`
	} `json:"identity"`
}

// kickChatEvent decodes a chat delivery leniently.
//
// Alternate spellings are accepted throughout because Kick's event payloads
// are documented less precisely than its REST endpoints, and a field name that
// turns out to be "message" rather than "content" must cost a fallback, not
// the whole feature. Unknown fields are ignored by construction.
type kickChatEvent struct {
	MessageID string `json:"message_id"`
	ID        string `json:"id"`
	Content   string `json:"content"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
	Timestamp string `json:"timestamp"`

	Broadcaster kickUser `json:"broadcaster"`
	Sender      kickUser `json:"sender"`

	Emotes []struct {
		EmoteID   string `json:"emote_id"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Positions []struct {
			S int `json:"s"`
			E int `json:"e"`
		} `json:"positions"`
	} `json:"emotes"`

	RepliesTo struct {
		MessageID string   `json:"message_id"`
		Content   string   `json:"content"`
		Sender    kickUser `json:"sender"`
	} `json:"replies_to"`
}

// messageFrom decodes a delivery. It reports false for anything that is not
// recognisably a chat message — Kick sends livestream and moderation events
// down the same webhook, and those are not errors, they are simply not ours.
func (k *KickAdapter) messageFrom(eventType string, body []byte) (Message, bool) {
	if eventType != "" && !strings.Contains(strings.ToLower(eventType), "chat") {
		return Message{}, false
	}
	var ev kickChatEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return Message{}, false
	}

	text := firstNonEmpty(ev.Content, ev.Message)
	if strings.TrimSpace(text) == "" {
		return Message{}, false
	}

	at, err := time.Parse(time.RFC3339, firstNonEmpty(ev.CreatedAt, ev.Timestamp))
	if err != nil {
		at = k.cfg.Now()
	}

	badges := make([]Badge, 0, len(ev.Sender.Identity.Badges))
	for _, b := range ev.Sender.Identity.Badges {
		badges = append(badges, Badge{ID: strings.ToLower(firstNonEmpty(b.Type, b.Text)), Label: b.Text})
	}
	// Kick's own event does not flag the broadcaster; comparing ids does, and
	// it is the one role a chat pane must always get right.
	if ev.Sender.UserID != 0 && ev.Sender.UserID == ev.Broadcaster.UserID {
		badges = append(badges, Badge{ID: "broadcaster", Label: "Host"})
	}

	channel := firstNonEmpty(ev.Broadcaster.ChannelSlug, k.cfg.Channel)
	return Message{
		ID:       firstNonEmpty(ev.MessageID, ev.ID),
		Platform: db.PlatformKick,
		Account:  k.cfg.AccountRef,
		Channel:  channel,
		Text:     text,
		At:       at,
		Author: Author{
			ID:     strconv.Itoa(ev.Sender.UserID),
			Name:   ev.Sender.Username,
			Color:  ev.Sender.Identity.UsernameColor,
			Badges: badges,
		},
		Emotes:    kickEmotes(ev),
		ReplyToID: ev.RepliesTo.MessageID,
		ReplyTo:   ev.RepliesTo.Sender.Username,
	}, true
}

// kickEmotes converts Kick's position pairs.
//
// The end is treated as inclusive, matching the IRC convention Kick's chat
// inherits. If that is wrong the range is one character long in the wrong
// direction, and Normalise drops anything that no longer fits the text — a
// cosmetic loss rather than a lost message, which is why it is safe to hold
// this convention without having verified it.
func kickEmotes(ev kickChatEvent) []Emote {
	var out []Emote
	for _, e := range ev.Emotes {
		id := firstNonEmpty(e.EmoteID, e.ID)
		for _, p := range e.Positions {
			out = append(out, Emote{ID: id, Name: e.Name, Start: p.S, End: p.E + 1})
		}
	}
	return out
}

// -------------------------------------------------------------------- output

// Send posts to Kick chat as the connected user.
func (k *KickAdapter) Send(ctx context.Context, text string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, fmt.Errorf("nothing to send")
	}
	if n := len([]rune(text)); n > KickMaxMessage {
		return Message{}, fmt.Errorf("Kick accepts %d characters and this message is %d", KickMaxMessage, n)
	}
	if k.cfg.BroadcasterUserID <= 0 {
		return Message{}, fmt.Errorf("Kick chat has no broadcaster id recorded; reconnect the Kick account in Settings → Platforms")
	}

	payload := map[string]any{
		"content": text,
		// "user" posts as the connected account rather than as a bot identity,
		// which is what an operator typing into the unified box expects.
		"type":                "user",
		"broadcaster_user_id": k.cfg.BroadcasterUserID,
	}
	var out struct {
		Data struct {
			IsSent    bool   `json:"is_sent"`
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	err := doJSON(ctx, k.cfg.HTTP, http.MethodPost, k.cfg.APIBase+"/public/v1/chat", k.cfg.Token, payload, &out)
	if err != nil {
		if statusOf(err) == http.StatusUnauthorized {
			return Message{}, Fatal(fmt.Errorf("kick rejected the access token; reconnect the Kick account in Settings → Platforms"))
		}
		return Message{}, err
	}

	// Returned with Kick's own message id so the webhook copy of this message,
	// which will arrive shortly, is deduplicated against it.
	return Message{
		ID:       out.Data.MessageID,
		Platform: db.PlatformKick,
		Account:  k.cfg.AccountRef,
		Channel:  k.cfg.Channel,
		Author: Author{
			ID:          strconv.Itoa(k.cfg.BroadcasterUserID),
			Name:        firstNonEmpty(k.cfg.Channel, "you"),
			Broadcaster: true,
		},
		Text: text,
		At:   k.cfg.Now(),
	}, nil
}

// Delete removes a message. It needs the moderation:chat_message:manage scope,
// and a token granted before chat shipped will not have it — the 401 says so
// in Kick's words, and this turns that into the instruction that fixes it.
func (k *KickAdapter) Delete(ctx context.Context, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return fmt.Errorf("no message id to delete")
	}
	err := doJSON(ctx, k.cfg.HTTP, http.MethodDelete,
		k.cfg.APIBase+"/public/v1/chat/"+url.PathEscape(messageID), k.cfg.Token, nil, nil)
	if err != nil && (statusOf(err) == http.StatusUnauthorized || statusOf(err) == http.StatusForbidden) {
		return fmt.Errorf("Kick refused the deletion. If this account was connected before unified chat existed "+
			"it never granted moderation:chat_message:manage — disconnect and reconnect it in Settings → Platforms. "+
			"Kick said: %s", err)
	}
	return err
}

// KickMaxTimeout is Kick's documented ceiling: 10080 minutes, which is seven
// days. Beyond it the API rejects the request outright, so a longer timeout is
// converted to a permanent ban only where the caller asked for one — never
// silently.
const KickMaxTimeout = 10080 * time.Minute

// Ban removes a user from the chat, permanently or for a timeout.
//
// KICK COUNTS IN MINUTES. YouTube and Twitch count in seconds. This is the
// conversion that makes a unified "600" mean ten minutes everywhere instead of
// seven days here, and it is the entire reason the interface takes a Duration.
func (k *KickAdapter) Ban(ctx context.Context, userID string, d time.Duration, reason string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("no user id to ban")
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		// Kick's ids are integers in JSON, not strings. Sending a quoted id
		// fails as a validation error that names neither field.
		return fmt.Errorf("kick user id %q is not numeric, so this ban cannot be addressed", userID)
	}

	body := map[string]any{
		"broadcaster_user_id": k.cfg.BroadcasterUserID,
		"user_id":             uid,
	}
	if d > 0 {
		if d > KickMaxTimeout {
			return fmt.Errorf("kick timeouts stop at 7 days and this one is %s; "+
				"ask for a permanent ban explicitly if that is what you mean", d)
		}
		// Rounded UP: a 30-second timeout must not truncate to zero minutes,
		// because zero here means PERMANENT.
		mins := int64((d + time.Minute - 1) / time.Minute)
		body["duration"] = mins
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		if len([]rune(reason)) > 100 {
			// Kick's documented maximum. A long reason must not cost the ban.
			reason = string([]rune(reason)[:100])
		}
		body["reason"] = reason
	}

	err = doJSON(ctx, k.cfg.HTTP, http.MethodPost,
		k.cfg.APIBase+"/public/v1/moderation/bans", k.cfg.Token, body, nil)
	if err != nil && (statusOf(err) == http.StatusUnauthorized || statusOf(err) == http.StatusForbidden) {
		return fmt.Errorf("Kick refused the ban. If this account was connected before banning existed "+
			"it never granted moderation:ban — disconnect and reconnect it in Settings → Platforms. "+
			"Kick said: %w", err)
	}
	return err
}

// Unban lifts a ban or an unexpired timeout.
func (k *KickAdapter) Unban(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("no user id to unban")
	}
	uid, cerr := strconv.Atoi(userID)
	if cerr != nil {
		return fmt.Errorf("kick user id %q is not numeric, so this cannot be addressed", userID)
	}
	err := doJSON(ctx, k.cfg.HTTP, http.MethodDelete,
		k.cfg.APIBase+"/public/v1/moderation/bans", k.cfg.Token,
		map[string]any{
			"broadcaster_user_id": k.cfg.BroadcasterUserID,
			"user_id":             uid,
		}, nil)
	if err != nil && (statusOf(err) == http.StatusUnauthorized || statusOf(err) == http.StatusForbidden) {
		return fmt.Errorf("Kick refused the unban; the account may lack moderation:ban. "+
			"Reconnect it in Settings → Platforms. Kick said: %w", err)
	}
	return err
}

// randomSecret is an unguessable path segment. crypto/rand only: a predictable
// callback path is an open door for anyone who can guess it to inject chat.
func randomSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not survivable for something whose whole job
		// is being unguessable, and a fallback to a weak source would be worse
		// than the panic: it would look like it worked.
		panic("chat: no entropy available for a webhook secret: " + err.Error())
	}
	return hex.EncodeToString(b)
}
