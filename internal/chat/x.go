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
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rainmanjam/polyemesis/internal/db"
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
	return &XAdapter{cfg: cfg, seen: newXSeen(xSeenMax)}, nil
}

// Platform implements Adapter.
func (x *XAdapter) Platform() db.Platform { return db.PlatformX }

// Account implements Adapter.
func (x *XAdapter) Account() string { return x.cfg.AccountRef }

// ------------------------------------------------------------------ transport

// xCall is one authenticated JSON call. The token is the user token unless bearer
// is set. out nil discards the body.
func (x *XAdapter) xCall(ctx context.Context, method, path string, bearer bool, payload, out any) error {
	var tok string
	var err error
	if bearer {
		tok = x.cfg.AppBearer
	} else {
		tok, err = x.cfg.Token(ctx)
		if err != nil {
			return err
		}
	}
	return doJSON(ctx, x.cfg.HTTP, method, x.cfg.APIBase+path, StaticToken(tok), payload, out)
}

// xProblemMessage reads X's error objects out of a failed call, so the operator
// sees X's own words. The raw body is never included, because it may carry data
// the operator did not ask to see.
func xProblemMessage(err error) string {
	return reasonOf(err)
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
		return "", fmt.Errorf("X would not list broadcasts: %s", xProblemMessage(err))
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

// Send posts text to the running broadcast as the account. It returns a
// synthesised echo, because X answers with a timestamp and no message id.
//
// PROVISIONAL: X's webhook may also deliver our own message back. If it does,
// the echo and the platform copy will not dedupe against each other.
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
		return Message{}, fmt.Errorf("X refused the chat message: %s", xProblemMessage(err))
	}
	if !out.Data.Success {
		return Message{}, errors.New("X did not confirm the chat message was sent")
	}
	now := x.cfg.Now()
	m := Message{
		ID:       fmt.Sprintf("syn-x-%d", now.UnixNano()),
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
		return fmt.Errorf("X refused to remove the message: %s", xProblemMessage(err))
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
		return fmt.Errorf("X refused the mute: %s", xProblemMessage(err))
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
		return fmt.Errorf("X refused the unmute: %s", xProblemMessage(err))
	}
	return nil
}
