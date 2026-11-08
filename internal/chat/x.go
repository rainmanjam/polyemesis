package chat

// X (Twitter) Livestream chat: the read, the send, delete, and mute/unmute.
//
// Everything here is behind the Livestream switch. internal/api attaches this
// adapter only when the oauth.X gate is open (POLYEMESIS_X_LIVESTREAM), so with
// the gate closed no X chat is ever read or posted.
//
// READ. X's chat history (GET /2/broadcasts/{id}/chat) returns messages only
// after a broadcast ENDS, so it cannot show live chat. Live chat is delivered ONLY
// to a registered webhook (docs.x.com/livestream-api/chat/accessing-live-chat):
// polyemesis registers its public address with X, subscribes the account's
// broadcast.chat and broadcast.chat.remove events to that webhook, and X POSTs
// each event to /api/v1/webhooks/x, where internal/api verifies the signature and
// hands the body to IngestWebhook. The old route, a subscription without a webhook
// read from GET /2/activity/stream, answered 503 on every attempt against live X
// and is gone. See x_webhook.go for the registration and the event path.
//
// The webhook calls take the app-only bearer (AppBearer), which is NOT the user's
// token. It is supplied separately, and nothing here mints it. Without it, or
// without a public HTTPS address, Run returns a Fatal error that names what is
// missing, because retrying cannot supply either.
//
// PROVISIONAL, against live X: the broadcast.chat payload is decoded in the shape
// docs.x.com/x-api/activity/event-payloads documents (message_id, broadcast_id,
// message, reply_to, author.data). A live delivery may differ.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// xBroadcastsPath is the X API v2 broadcasts path prefix.
const xBroadcastsPath = "/2/broadcasts/"

// XMaxMessage is X's chat limit. SendBroadcastChatRequest.text carries maxLength
// 140 in the served spec.
const XMaxMessage = 140

const xAPIBaseDefault = platform.XAPIHost

// xNumericID is the shape X uses for message and user ids: digits only, 1 to 19
// characters (the spec's ^[0-9]{1,19}$).
var xNumericID = regexp.MustCompile(`^[0-9]{1,19}$`)

// XConfig configures the X chat adapter.
type XConfig struct {
	// AccountRef is the numeric user id the connection is keyed by.
	AccountRef string
	// Channel is the handle, for the tab header.
	Channel string
	// Token supplies the user-context access token for the subscription, the
	// send, the delete and the mute calls.
	Token TokenFunc
	// AppBearer is the app-only bearer token the webhook and subscription-list
	// calls require. The operator supplies it, and nothing here obtains it. Empty
	// makes Run return a Fatal error.
	AppBearer string
	// WebhookURL is the public address X posts events to: https, no port, at most
	// 200 characters (X's rules). Empty makes Run return a Fatal error carrying
	// WebhookProblem.
	WebhookURL string
	// WebhookProblem says why WebhookURL is empty (no hostname, a self-signed
	// certificate, ...), so the operator is told what to change.
	WebhookProblem string
	// WebhookRecheck is how often a live Run asks X whether the webhook is still valid. Zero means 30 minutes.
	WebhookRecheck time.Duration
	// RefreshToken forces a refresh of a user token X refused before its stored expiry, given the refused token, and
	// returns the new one. Nil means a refused token is final. Only a token X itself refused (a 401, or a 403
	// "Unsupported Authentication") triggers it, once per call.
	RefreshToken func(ctx context.Context, rejected string) (string, error)
	// Wait pauses Run for a rate limit, returning early (with the context's error) when ctx ends. Nil sleeps on a
	// timer; a test replaces it.
	Wait func(ctx context.Context, d time.Duration) error
	// Log receives the warnings that are not errors (for instance a replaced subscription). Nil discards them.
	Log *slog.Logger
	// APIBase is the host. Empty means production.
	APIBase string
	// HTTP is the client. Nil means a default with a timeout.
	HTTP *http.Client
	// Now is the clock. Nil means time.Now. Mute durations are converted to an
	// absolute timestamp against it.
	Now func() time.Time
}

// XAdapter is the X chat connection.
type XAdapter struct {
	cfg XConfig

	// told is the broadcast the lifecycle coordinator last said this account published, or "" when it said the
	// broadcast ended or has said nothing. It is kept on the adapter, not on a Run, so a tell that arrives before
	// Run starts or between two reconnects is not lost.
	toldMu sync.Mutex
	told   string

	// live is the webhook receiving side, present only while Run is registered and subscribed.
	liveMu    sync.RWMutex
	liveUser  string
	liveInbox chan xWebhookEvent
	// seen holds the event_uuids already delivered, so a retry or replay from X is not shown twice. It lives on the
	// adapter, not on a Run, so a replay right after a reconnect is still recognised.
	seen *xSeen
	// queued holds the event_uuids accepted into the inbox, so a retry of one that is queued but not yet delivered is
	// a duplicate rather than a refusal (see IngestWebhook).
	queued *xSeen
	// fatal is set when Run ended with a Fatal error: no live adapter will come until a reconnect or a restart.
	// notLiveSince is when the adapter last stopped being registered (or was built), for Unavailable.
	fatal        bool
	notLiveSince time.Time

	// ready is true once Run has registered the webhook and the subscriptions (the inbox opens earlier, before the
	// subscriptions exist, so nothing X delivers is lost). notes are the operator-visible remarks Health reports.
	ready        bool
	replacedNote string
	invalidNote  string
}

// SetBroadcast tells the adapter which broadcast this account is running now. The lifecycle coordinator calls it
// after a PUBLISH, so the first chat lines of a new show are recognised at once instead of after the next poll,
// and with "" after an END. The 15-second poll stays as the fallback for a broadcast published outside polyemesis.
func (x *XAdapter) SetBroadcast(id string) {
	x.toldMu.Lock()
	x.told = strings.TrimSpace(id)
	x.toldMu.Unlock()
}

// toldBroadcast is the id SetBroadcast last recorded.
func (x *XAdapter) toldBroadcast() string {
	x.toldMu.Lock()
	defer x.toldMu.Unlock()
	return x.told
}

// currentBroadcastID is the broadcast Send, Delete and the mutes address: the told one when there is one, which
// saves a list read per call, and otherwise whatever X lists as RUNNING.
func (x *XAdapter) currentBroadcastID(ctx context.Context) (string, error) {
	if id := x.toldBroadcast(); id != "" {
		return id, nil
	}
	return x.runningBroadcastID(ctx)
}

// NewX builds the adapter. The Token is required, because every call except the
// stream needs it.
func NewX(cfg XConfig) (*XAdapter, error) {
	if cfg.Token == nil {
		return nil, errors.New("X chat is not configured: no access token, so connect the X account in Settings → Platforms")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = xAPIBaseDefault
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &XAdapter{cfg: cfg, seen: newXSeen(xSeenMax), queued: newXSeen(xSeenMax), notLiveSince: cfg.Now()}, nil
}

// Platform implements Adapter.
func (x *XAdapter) Platform() db.Platform { return db.PlatformX }

// Account implements Adapter.
func (x *XAdapter) Account() string { return x.cfg.AccountRef }

// ------------------------------------------------------------------ transport

// xCall is one authenticated JSON call. The token is the user token unless bearer
// is set. out nil discards the body.
//
// Failures come back typed, so a caller (and Run) can tell them apart without reading prose: a 429 is an
// *xChatRateLimited, a token X refused is an *xCredentialError, and anything else is the *apiError.
func (x *XAdapter) xCall(ctx context.Context, method, path string, bearer bool, payload, out any) error {
	key := x.holdKey(method, path, bearer)
	if until, held := xChatHolds.until(key, x.cfg.Now()); held {
		return &xChatRateLimited{Until: until, Held: true}
	}
	var tok string
	if bearer {
		tok = x.cfg.AppBearer
	} else {
		var err error
		tok, err = x.cfg.Token(ctx)
		if err != nil {
			if errors.Is(err, oauth.ErrReconnectRequired) {
				// The refresh grant is gone: nothing but the operator can fix it.
				return &xCredentialError{kind: xCredUser, err: err}
			}
			return err
		}
	}
	err := doJSON(ctx, x.cfg.HTTP, method, x.cfg.APIBase+path, StaticToken(tok), payload, out)
	if err != nil && !bearer && x.cfg.RefreshToken != nil && x.userTokenRefused(err) {
		// X refused a token the store still thinks is good (revoked on X's side, or rotated by another install).
		// One forced refresh and one retry; if that fails too, the account needs reconnecting.
		fresh, rerr := x.cfg.RefreshToken(ctx, tok)
		switch {
		case rerr != nil && errors.Is(rerr, oauth.ErrReconnectRequired):
			return &xCredentialError{kind: xCredUser, err: err}
		case rerr != nil:
			return fmt.Errorf("X refused the account's token and refreshing it failed: %w", rerr)
		case fresh != "" && fresh != tok:
			err = doJSON(ctx, x.cfg.HTTP, method, x.cfg.APIBase+path, StaticToken(fresh), payload, out)
		}
	}
	if err == nil {
		return nil
	}
	return x.classify(err, key, bearer)
}

// userTokenRefused reports an answer that says the user token itself was not accepted.
func (x *XAdapter) userTokenRefused(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && oauth.XCredentialRejectedProblem(ae.Status, ae.Problem+" "+ae.Types)
}

// classify turns a failed call into the typed error its status calls for. Anything it does not recognise passes
// through as the *apiError.
func (x *XAdapter) classify(err error, key string, bearer bool) error {
	var ae *apiError
	if !errors.As(err, &ae) {
		return err
	}
	now := x.cfg.Now()
	if until, ok := oauth.XRateHoldFrom(ae.Status, ae.Header, now); ok {
		// Jitter, so the accounts that were held by the same reset do not all come back in the same second.
		until = until.Add(time.Duration(rand.Int64N(int64(xChatJitter))))
		xChatHolds.hold(key, until, now)
		if ae.Status == http.StatusTooManyRequests {
			return &xChatRateLimited{Until: until, Problem: ae.Problem}
		}
	}
	problem := ae.Problem + " " + ae.Types
	switch {
	case oauth.XCredentialRejectedProblem(ae.Status, problem):
		if bearer {
			return &xCredentialError{kind: xCredApp, err: err}
		}
		return &xCredentialError{kind: xCredUser, err: err}
	case !bearer && ae.Status == http.StatusForbidden && strings.Contains(problem, "missing-scopes"):
		return &xCredentialError{kind: xCredScope, err: err}
	}
	return err
}

// xChatJitter bounds the random extra wait added to a rate-limit reset.
const xChatJitter = 3 * time.Second

// holdKey names one rate-limit window. X limits per endpoint, and the app-bearer routes (webhooks, subscriptions) are
// limited per APP, so every account on the app shares their key; user-token routes are per account. The host is part
// of the key so a test server never inherits another's hold.
func (x *XAdapter) holdKey(method, path string, bearer bool) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	who := "user:" + x.cfg.AccountRef
	if bearer {
		who = "app"
	}
	return x.cfg.APIBase + "|" + who + "|" + method + " " + path
}

// xChatHolds is the rate-limit holds, shared by every X adapter in the process.
var xChatHolds = &xHoldBook{m: map[string]time.Time{}}

type xHoldBook struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// xHoldBookMax bounds the book; expired holds are dropped on every write.
const xHoldBookMax = 128

func (b *xHoldBook) until(key string, now time.Time) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	u, ok := b.m[key]
	if !ok || !u.After(now) {
		delete(b.m, key)
		return time.Time{}, false
	}
	return u, true
}

func (b *xHoldBook) hold(key string, until, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, u := range b.m {
		if !u.After(now) {
			delete(b.m, k)
		}
	}
	for len(b.m) >= xHoldBookMax {
		for k := range b.m {
			delete(b.m, k)
			break
		}
	}
	if cur, ok := b.m[key]; !ok || until.After(cur) {
		b.m[key] = until
	}
}

// xChatRateLimited is X (or an earlier answer from X) asking for a pause.
type xChatRateLimited struct {
	Until   time.Time
	Held    bool
	Problem string
}

func (e *xChatRateLimited) Error() string {
	msg := "X is rate-limiting these chat calls; try again after " + e.Until.UTC().Format("15:04:05") + " UTC"
	if e.Problem != "" && !e.Held {
		msg += " (X said: " + e.Problem + ")"
	}
	return msg
}

type xCredKind int

const (
	// xCredUser: X refused the connected account's token. Reconnecting fixes it.
	xCredUser xCredKind = iota
	// xCredApp: X refused the app-only bearer token. Fixing POLYEMESIS_X_APP_BEARER fixes it.
	xCredApp
	// xCredScope: the token is good but lacks a permission. Reconnecting grants it.
	xCredScope
)

// xCredentialError is a refusal retrying cannot fix. Its text says what to change; Run ends such a failure Fatal.
type xCredentialError struct {
	kind xCredKind
	err  error
}

func (e *xCredentialError) Unwrap() error { return e.err }

func (e *xCredentialError) Error() string {
	var said string
	if p := xProblemMessage(e.err); p != "" {
		said = " (X said: " + p + ")"
	}
	switch e.kind {
	case xCredApp:
		return "X refused the app bearer token, so check POLYEMESIS_X_APP_BEARER (it may be mistyped or regenerated)" + said
	case xCredScope:
		return "the connected X account's token lacks a permission chat needs, so reconnect the X account in Settings → Platforms to grant it" + said
	}
	return "X refused the connected X account's token (revoked or expired), so reconnect the X account in Settings → Platforms" + said
}

// xFailure is a prefix and X's own reason over the error that caused them, so the operator reads the sentence and a
// caller can still errors.As the cause.
type xFailure struct {
	msg string
	err error
}

func (e *xFailure) Error() string { return e.msg }
func (e *xFailure) Unwrap() error { return e.err }

// xFail is "<what X would not do>: <X's reason>" over err.
func xFail(what string, err error) error {
	return &xFailure{msg: what + ": " + xWhy(err), err: err}
}

// xProblemMessage reads X's own words out of a failed call, so the operator sees them: RFC 7807 title and detail,
// {errors:[...]}, and {error_type, message}. The raw body is never included, because it may carry data the operator
// did not ask to see, and neither is any URL.
func xProblemMessage(err error) string {
	var cred *xCredentialError
	if errors.As(err, &cred) {
		return cred.Error()
	}
	var rl *xChatRateLimited
	if errors.As(err, &rl) {
		return rl.Error()
	}
	var ae *apiError
	if errors.As(err, &ae) {
		if ae.Problem != "" {
			return ae.Problem
		}
		return ae.Reason
	}
	return ""
}

// runningBroadcastID finds the RUNNING broadcast on the account. X lists
// broadcasts newest first and an account can hold several, so the state is what
// decides, not the position.
func (x *XAdapter) runningBroadcastID(ctx context.Context) (string, error) {
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			BroadcastID string `json:"broadcast_id"`
			State       string `json:"state"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodGet, "/2/broadcasts?"+url.Values{
		"broadcast.fields": {"id,broadcast_id,state"},
	}.Encode(), false, nil, &out); err != nil {
		return "", xFail("X would not list broadcasts", err)
	}
	for _, b := range out.Data {
		if b.State != "RUNNING" {
			continue
		}
		if b.ID != "" {
			return b.ID, nil
		}
		return b.BroadcastID, nil
	}
	return "", errXNoRunningBroadcast
}

// errXNoRunningBroadcast is X's own answer that the account is not live, as against a list read that failed. The
// chat owner check treats the two differently: the first is a judgement, the second is no answer at all.
var errXNoRunningBroadcast = errors.New("X has no running broadcast on this account, so chat has nowhere to go; go live first")

// --------------------------------------------------------------------- send

// Send posts text to the running broadcast as the account. It returns an echo
// whose id is the answer's data.timestamp, the server time in nanoseconds as a
// string: X documents that value as the message id, the one Delete takes as
// message_id and a reply names as reply_to (docs.x.com/livestream-api/chat/
// send-chat-message). A synthesised id stands in only when X sent no digits.
//
// PROVISIONAL: the webhook's broadcast.chat event carries message_id, and
// that it equals this timestamp is read from the docs, not seen live. If it
// does, our own message arriving back dedupes against this echo.
func (x *XAdapter) Send(ctx context.Context, text string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, errors.New("nothing to send")
	}
	if n := utf8.RuneCountInString(text); n > XMaxMessage {
		return Message{}, fmt.Errorf("X chat messages stop at %d characters and this one is %d", XMaxMessage, n)
	}
	id, err := x.currentBroadcastID(ctx)
	if err != nil {
		return Message{}, err
	}
	var out struct {
		Data struct {
			Success   bool   `json:"success"`
			Timestamp string `json:"timestamp"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodPost, xBroadcastsPath+url.PathEscape(id)+"/chat", false,
		map[string]string{"text": text}, &out); err != nil {
		return Message{}, xFail("X refused the chat message", err)
	}
	if !out.Data.Success {
		return Message{}, errors.New("X did not confirm the chat message was sent")
	}
	now := x.cfg.Now()
	msgID := strings.TrimSpace(out.Data.Timestamp)
	if !xNumericID.MatchString(msgID) {
		msgID = fmt.Sprintf("syn-x-%d", now.UnixNano())
	}
	m := Message{
		ID:       msgID,
		Platform: db.PlatformX,
		Account:  x.cfg.AccountRef,
		Channel:  x.cfg.Channel,
		Author:   Author{ID: x.cfg.AccountRef, Name: x.cfg.Channel},
		Text:     text,
		At:       now,
		Echo:     true,
	}
	return m, nil
}

// ------------------------------------------------------------------- delete

// Delete removes one message from the running broadcast. X offers no hide, so
// this is the only message removal there is.
func (x *XAdapter) Delete(ctx context.Context, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if !xNumericID.MatchString(messageID) {
		return fmt.Errorf("X message id %q is not digits, so it cannot be addressed", messageID)
	}
	id, err := x.currentBroadcastID(ctx)
	if err != nil {
		return err
	}
	var out struct {
		Data struct {
			Deleted bool `json:"deleted"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodDelete, xBroadcastsPath+url.PathEscape(id)+"/chat/"+messageID,
		false, nil, &out); err != nil {
		return xFail("X refused to remove the message", err)
	}
	if !out.Data.Deleted {
		return errors.New("X did not confirm the message was removed")
	}
	return nil
}

// ---------------------------------------------------------------- ban / mute

// Ban mutes a viewer in the broadcaster's chat. X's mute is the nearest thing to
// a ban: a zero duration is an indefinite mute, and a positive one is a timeout.
// Mutes belong to the broadcaster and apply to every broadcast they host, so the
// route is POST /2/broadcasts/chat/mutes with no broadcast id and needs no live
// broadcast (docs.x.com/livestream-api/chat/mute-user). broadcast_id is for a
// moderator acting in another host's chats, which this adapter never does.
//
// UNIT: X takes end_at_ms, an ABSOLUTE Unix timestamp in milliseconds, so the
// duration is added to now here, at the call site. The reason is not sent,
// because X's mute request has no field for one.
func (x *XAdapter) Ban(ctx context.Context, userID string, d time.Duration, _ string) error {
	userID = strings.TrimSpace(userID)
	if !xNumericID.MatchString(userID) {
		return fmt.Errorf("X user id %q is not digits, so this mute cannot be addressed", userID)
	}
	body := map[string]string{"user_id": userID}
	if d > 0 {
		body["end_at_ms"] = strconv.FormatInt(x.cfg.Now().Add(d).UnixMilli(), 10)
	}
	var out struct {
		Data struct {
			Muted bool `json:"muted"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodPost, xBroadcastsPath+"chat/mutes",
		false, body, &out); err != nil {
		return xFail("X refused the mute", err)
	}
	if !out.Data.Muted {
		return errors.New("X did not confirm the viewer was muted")
	}
	return nil
}

// Unban lifts a mute.
func (x *XAdapter) Unban(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if !xNumericID.MatchString(userID) {
		return fmt.Errorf("X user id %q is not digits, so this unmute cannot be addressed", userID)
	}
	if err := x.xCall(ctx, http.MethodDelete, xBroadcastsPath+"chat/mutes/"+userID,
		false, nil, nil); err != nil {
		return xFail("X refused the unmute", err)
	}
	return nil
}
