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
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rivo/uniseg"

	"github.com/rainmanjam/polyemesis/internal/backoff"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// KickMaxMessage is Kick's published limit on chat content. The adapter
// enforces it in Send; the registry carries the same figure for everyone else,
// and TestRegistryAgreesWithTheChatAdapters holds the two together.
//
// Kick counts user-perceived characters (grapheme clusters), not runes, so a
// family emoji or a letter with a combining mark is one. It ALSO caps the
// content at KickMaxBytes of UTF-8 (docs.kick.com/apis/chat), and a run of
// emoji hits that long before the character limit. Send enforces both and
// refuses, rather than truncating, matching every other adapter's too-long path.
const KickMaxMessage = 500

// KickMaxBytes is the UTF-8 byte cap Kick puts on chat content.
const KickMaxBytes = 2048

const (
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
	// kickSubRecheck is how often a live adapter confirms its chat
	// subscription still exists. Kick auto-disables a subscription after a day
	// of failed deliveries, and switching webhooks off for the app drops every
	// subscription at once (Kick changelog, 12/12/2025 and 19/12/2025), so
	// "created at start" is not "still there an hour later".
	kickSubRecheck = 30 * time.Minute
	// kickSubRetry is how soon a check that FAILED is tried again: a network
	// blip at start must not leave chat unsubscribed for half an hour.
	kickSubRetry = 2 * time.Minute
	// The one event this adapter needs.
	kickChatEventName    = "chat.message.sent"
	kickChatEventVersion = 1
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
	// CallbackPath builds the path the receiver is mounted at from the secret.
	// Required, and owned by the HTTP layer that registers the route, so the
	// path the operator pastes into Kick cannot drift from the one served.
	CallbackPath func(secret string) string
	// Verify authenticates a delivery. Required: the handler refuses every POST
	// when it is nil rather than accepting unverified events.
	//
	// It stays a function rather than a concrete type so a test can sign with
	// its own key, and so the signature scheme lives in kick_verify.go where it
	// can be read against Kick's documentation side by side.
	Verify func(r *http.Request, body []byte) error

	// Tick is how often Run wakes to refresh health and decide whether the
	// subscription is due a check. Zero means kickHealthTick; tests shorten it.
	Tick time.Duration
	// Log receives the subscription outcome. It never receives the webhook URL.
	Log *slog.Logger

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

	// The chat subscription's last check: when, and what it found. subProblem
	// is empty when the subscription exists (or has not been checked yet).
	subCheckedAt time.Time
	subChecked   bool
	subProblem   string
}

// NewKick builds the adapter.
func NewKick(cfg KickConfig) (*KickAdapter, error) {
	if cfg.Token == nil {
		return nil, fmt.Errorf("kick chat is not configured: no access token, so connect the Kick account in Settings → Platforms")
	}
	if cfg.CallbackPath == nil {
		return nil, fmt.Errorf("kick chat is not configured: no callback path builder")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = platform.KickAPIHost
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Tick <= 0 {
		cfg.Tick = kickHealthTick
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Sleep == nil {
		cfg.Sleep = backoff.SleepCtx
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

// BroadcasterUserID is the Kick channel this adapter listens for.
func (k *KickAdapter) BroadcasterUserID() int { return k.cfg.BroadcasterUserID }

// VerifyDelivery checks a delivery's signature with this adapter's verifier.
// Kick signs every channel's deliveries with one key, so any adapter can vouch
// for a delivery that belongs to none of them. A missing verifier fails closed.
func (k *KickAdapter) VerifyDelivery(r *http.Request, body []byte) error {
	if k.cfg.Verify == nil {
		return fmt.Errorf("webhook verification is not configured")
	}
	return k.cfg.Verify(r, body)
}

// KickDeliveryBroadcaster names the channel a webhook body is about, or 0 when
// the body does not say. Kick has one callback URL per app, so every connected
// channel's chat arrives together and this is what tells them apart. The body
// is not yet verified here: it only picks which adapter will verify it.
func KickDeliveryBroadcaster(body []byte) int {
	var ev struct {
		Broadcaster struct {
			UserID int `json:"user_id"`
		} `json:"broadcaster"`
	}
	if json.Unmarshal(body, &ev) != nil {
		return 0
	}
	return ev.Broadcaster.UserID
}

// CallbackPath is where the receiver must be mounted, and what the operator
// pastes into the Kick app dashboard's webhook field (prefixed by PublicURL).
// The secret is part of the path because that is what authenticates a
// delivery in the absence of a signature scheme we can verify.
func (k *KickAdapter) CallbackPath() string {
	return k.cfg.CallbackPath(k.cfg.CallbackSecret)
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

// Run registers the sink, checks that the callback can actually be reached,
// makes sure Kick is subscribed to deliver chat to it, and then holds the
// registration open until ctx ends, keeping the health honest about how long
// it has been since anything arrived and re-confirming the subscription.
//
// Nothing is unsubscribed when ctx ends. Kick subscriptions persist per app and
// broadcaster, so deleting on every shutdown would drop every chat message Kick
// delivers while polyemesis restarts, and the next Run would only have to
// create it again. A subscription for a channel that has been disconnected is
// harmless: the delivery is verified, matches no adapter, and is acknowledged.
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
	k.ensureSubscription(ctx)
	k.refreshHealth()

	tick := time.NewTicker(k.cfg.Tick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			k.setHealth(StateStopped, "")
			return nil
		case <-tick.C:
			if k.subscriptionDue() {
				k.ensureSubscription(ctx)
			}
			k.refreshHealth()
		}
	}
}

// subscriptionDue reports whether it is time to look at the subscription again:
// every kickSubRecheck while it is fine, kickSubRetry after a failure.
func (k *KickAdapter) subscriptionDue() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.subChecked {
		return true
	}
	wait := kickSubRecheck
	if k.subProblem != "" {
		wait = kickSubRetry
	}
	return k.cfg.Now().Sub(k.subCheckedAt) >= wait
}

// ensureSubscription makes sure Kick has a chat.message.sent webhook
// subscription for this channel, creating it only when missing, and records
// the outcome for the health line and the log.
//
// Idempotent by construction: GET the subscriptions first and POST only on
// absence, because a duplicate subscription is a second delivery of every chat
// message.
func (k *KickAdapter) ensureSubscription(ctx context.Context) {
	problem := k.syncSubscription(ctx)
	if ctx.Err() != nil {
		// Shutting down: the failure is the cancellation, not Kick.
		return
	}
	k.mu.Lock()
	changed := !k.subChecked || k.subProblem != problem
	k.subChecked, k.subCheckedAt, k.subProblem = true, k.cfg.Now(), problem
	k.mu.Unlock()
	// Logged when the outcome changes, not every thirty minutes. The message
	// never carries the callback URL: its path is a secret.
	if changed {
		if problem != "" {
			k.cfg.Log.Warn("kick chat: chat subscription is not in place", "account", k.cfg.AccountRef, "problem", problem)
		} else {
			k.cfg.Log.Info("kick chat: subscribed to chat messages", "account", k.cfg.AccountRef)
		}
	}
}

// kickWebhookHint is what to do when Kick will not take the subscription.
const kickWebhookHint = "Enable webhooks for your Kick app (Kick → Settings → Developer → your app → Enable webhooks, " +
	"with the webhook URL shown under Settings → Platform credentials → Kick), and reconnect the Kick account if it " +
	"was connected before the events:subscribe permission existed."

// syncSubscription returns "" when the chat subscription exists afterwards and
// a sentence for the operator when it does not.
func (k *KickAdapter) syncSubscription(ctx context.Context) string {
	if k.cfg.BroadcasterUserID <= 0 {
		return "Kick chat has no broadcaster id recorded, so it cannot subscribe to chat messages; reconnect the Kick account in Settings → Platforms."
	}
	endpoint := k.cfg.APIBase + "/public/v1/events/subscriptions"

	var existing struct {
		Data []struct {
			Event   string `json:"event"`
			Version int    `json:"version"`
			Method  string `json:"method"`
		} `json:"data"`
	}
	q := url.Values{"broadcaster_user_id": {strconv.Itoa(k.cfg.BroadcasterUserID)}}
	if err := doJSON(ctx, k.cfg.HTTP, http.MethodGet, endpoint+"?"+q.Encode(), k.cfg.Token, nil, &existing); err != nil {
		return k.subscriptionProblem("could not read its Kick event subscriptions", err)
	}
	for _, s := range existing.Data {
		if s.Event == kickChatEventName && s.Version == kickChatEventVersion &&
			(s.Method == "" || s.Method == "webhook") {
			return ""
		}
	}

	body := map[string]any{
		"events": []map[string]any{{"name": kickChatEventName, "version": kickChatEventVersion}},
		"method": "webhook",
		// Ignored for a user token (Kick infers the broadcaster from it), sent
		// anyway so the request says what it means.
		"broadcaster_user_id": k.cfg.BroadcasterUserID,
	}
	var created struct {
		Data []struct {
			Name  string `json:"name"`
			Error string `json:"error"`
		} `json:"data"`
	}
	if err := doJSON(ctx, k.cfg.HTTP, http.MethodPost, endpoint, k.cfg.Token, body, &created); err != nil {
		return k.subscriptionProblem("could not subscribe to Kick chat messages", err)
	}
	// A 200 can still carry a per-event refusal.
	for _, d := range created.Data {
		if strings.TrimSpace(d.Error) != "" {
			return "Kick refused the chat subscription: " + d.Error + ". " + kickWebhookHint
		}
	}
	return ""
}

// subscriptionProblem turns a failed call into the sentence the operator sees.
func (k *KickAdapter) subscriptionProblem(what string, err error) string {
	switch statusOf(err) {
	case http.StatusUnauthorized:
		return "Kick rejected the access token while checking the chat subscription; reconnect the Kick account in Settings → Platforms."
	case http.StatusForbidden, http.StatusBadRequest:
		return "Kick webhook not enabled for your app: " + what + " (Kick said " + kickSaid(err) + "). " + kickWebhookHint
	}
	return what + ": " + kickSaid(err) + ". polyemesis will retry shortly."
}

// kickSaid is the platform's own words for a failed call, without the URL.
func kickSaid(err error) string {
	if ae, ok := err.(*apiError); ok {
		return fmt.Sprintf("%d %s", ae.Status, strings.TrimSpace(ae.Body))
	}
	return err.Error()
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

	k.mu.Lock()
	subProblem := k.subProblem
	k.mu.Unlock()

	switch {
	case !reachable:
		if subProblem != "" {
			detail += " Also: " + subProblem
		}
		k.setHealth(StateDegraded, detail)
	case subProblem != "":
		// Outranks the silence message below: while the subscription is not in
		// place, silence is the expected result and the subscription is the cause.
		k.setHealth(StateDegraded, subProblem+" Sending still works.")
	case count == 0 && k.cfg.Now().Sub(started) > kickSilenceAfter:
		// Reachable, subscribed, and nothing has ever arrived. polyemesis
		// created the subscription itself, so what is left is Kick not reaching
		// the URL in the app's webhook field.
		k.setHealth(StateDegraded, fmt.Sprintf(
			"no Kick chat events have arrived in %s. polyemesis is subscribed to chat messages, so check that "+
				"your Kick app has webhooks enabled and its webhook URL is the one shown under Settings → Platform "+
				"credentials → Kick. Sending still works.",
			k.cfg.Now().Sub(started).Round(time.Minute)))
	case bad > 0 && count == 0:
		k.setHealth(StateDegraded, fmt.Sprintf(
			"Kick is delivering events (%d so far) that polyemesis could not read as chat messages. "+
				"Chat is otherwise connected.", bad))
	case last.IsZero():
		k.setHealth(StateLive, "subscribed to Kick chat messages; waiting for the first one")
	default:
		k.setHealth(StateLive, "")
	}
}

// callbackWhere is how a health or warning sentence refers to the webhook URL.
//
// It NEVER contains the URL. The path carries the secret that authenticates a
// delivery, health text is broadcast to every chat pane and written to logs,
// and the URL has one proper home: Settings → Platform credentials → Kick,
// behind the operator's session (Server.KickChatCallbackURL).
const callbackWhere = "the webhook URL shown under Settings → Platform credentials → Kick"

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
			"public address. Set a public HTTPS hostname in Settings → Server, then add " + callbackWhere +
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
		". The webhook URL to configure in your Kick app is " + callbackWhere +
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
	if n := uniseg.GraphemeClusterCount(text); n > KickMaxMessage {
		return Message{}, fmt.Errorf("Kick accepts %d characters and this message is %d", KickMaxMessage, n)
	}
	if n := len(text); n > KickMaxBytes {
		return Message{}, fmt.Errorf("Kick accepts %d bytes of UTF-8 and this message is %d (emoji are several bytes each)", KickMaxBytes, n)
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
// EffectiveTimeout is how long Kick actually enforces a timeout of d: Kick
// counts whole minutes, and polyemesis rounds UP so a short timeout never
// becomes zero, which Kick reads as permanent. See TimeoutRounder.
func (k *KickAdapter) EffectiveTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return (d + time.Minute - 1) / time.Minute * time.Minute
}

var _ TimeoutRounder = (*KickAdapter)(nil)

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
		// because zero here means PERMANENT. Through EffectiveTimeout so the
		// Hub's sanction record and this request cannot disagree.
		mins := int64(k.EffectiveTimeout(d) / time.Minute)
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
