package chat

// X (Twitter) Livestream chat: the read, the send, delete, and mute/unmute.
//
// Everything here is behind X's Livestream whitelist. internal/api attaches this
// adapter only when the oauth.X gate is open (POLYEMESIS_X_LIVESTREAM), so with
// the gate closed no X chat is ever read or posted.
//
// READ. X's chat history (GET /2/broadcasts/{id}/chat) returns messages only
// after a broadcast ENDS, so it cannot show live chat. Live chat arrives on the
// Activity API: a subscription for the broadcast.chat event is created with the
// user's token, and the events are read from GET /2/activity/stream, which X's
// spec secures with the app-only bearer scheme. That bearer is NOT the user's
// token. It is supplied separately (AppBearer), and nothing here mints it.
// Without it Run returns a Fatal error that names the missing token, because
// retrying cannot supply it.
//
// PROVISIONAL, against live X: the broadcast.chat payload has no entry in the
// spec's discriminator mapping, so it is decoded here as the BroadcastChatMessage
// shape (id, text, author_id, author_username, author_name, created_at_ms,
// broadcast_id, reply_to). A live stream may differ. The stream framing (one JSON
// document per line) is also PROVISIONAL.

import (
	"bufio"
	"context"
	"encoding/json"
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

// XMaxMessage is X's chat limit. SendBroadcastChatRequest.text carries maxLength
// 140 in the served spec.
var XMaxMessage = platform.Must(platform.X).Chat.SendMax

const xAPIBaseDefault = "https://api.x.com"

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
	// AppBearer is the app-only bearer token the activity stream requires. It is
	// PROVISIONAL: the operator supplies it, and nothing here obtains it. Empty
	// makes Run return a Fatal error.
	AppBearer string
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
	return &XAdapter{cfg: cfg}, nil
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
// PROVISIONAL: X's activity stream may also deliver our own message back. If it
// does, the echo and the platform copy will not dedupe against each other.
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
	if err := x.xCall(ctx, http.MethodPost, "/2/broadcasts/"+url.PathEscape(id)+"/chat", false,
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
	if err := x.xCall(ctx, http.MethodDelete, "/2/broadcasts/"+url.PathEscape(id)+"/chat/"+messageID,
		false, nil, &out); err != nil {
		return fmt.Errorf("X refused to remove the message: %s", xProblemMessage(err))
	}
	if !out.Data.Deleted {
		return errors.New("X did not confirm the message was removed")
	}
	return nil
}

// ---------------------------------------------------------------- ban / mute

// Ban mutes a viewer in the running broadcast's chat. X's mute is the nearest
// thing to a ban: a zero duration is an indefinite mute, and a positive one is a
// timeout.
//
// UNIT: X takes end_at_ms, an ABSOLUTE Unix timestamp in milliseconds, so the
// duration is added to now here, at the call site. The reason is not sent,
// because X's mute request has no field for one.
func (x *XAdapter) Ban(ctx context.Context, userID string, d time.Duration, _ string) error {
	userID = strings.TrimSpace(userID)
	if !xNumericID.MatchString(userID) {
		return fmt.Errorf("X user id %q is not digits, so this mute cannot be addressed", userID)
	}
	id, err := x.currentBroadcastID(ctx)
	if err != nil {
		return err
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
	if err := x.xCall(ctx, http.MethodPost, "/2/broadcasts/"+url.PathEscape(id)+"/chat/mutes",
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
	id, err := x.currentBroadcastID(ctx)
	if err != nil {
		return err
	}
	if err := x.xCall(ctx, http.MethodDelete, "/2/broadcasts/"+url.PathEscape(id)+"/chat/mutes/"+userID,
		false, nil, nil); err != nil {
		return fmt.Errorf("X refused the unmute: %s", xProblemMessage(err))
	}
	return nil
}

// ------------------------------------------------------------------- read

// Run reads live chat from the activity stream until ctx ends. It subscribes to
// broadcast.chat as the user, streams as the app, deliberately delivers only that
// event type, and removes the subscription on the way out.
func (x *XAdapter) Run(ctx context.Context, sink Sink) error {
	if strings.TrimSpace(x.cfg.AppBearer) == "" {
		return Fatal(errors.New("X live chat needs an app-only bearer token for the activity stream, " +
			"which polyemesis does not have. Set POLYEMESIS_X_APP_BEARER to the app's bearer token " +
			"from the X developer console. Until then the chat pane stays empty, and sending still works"))
	}

	var me struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodGet, "/2/users/me", false, nil, &me); err != nil {
		return fmt.Errorf("X would not say which account to read chat for: %s", xProblemMessage(err))
	}
	var sub struct {
		Data struct {
			Subscription struct {
				ID string `json:"subscription_id"`
			} `json:"subscription"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodPost, "/2/activity/subscriptions", false, map[string]any{
		"event_type": "broadcast.chat",
		"filter":     map[string]string{"user_id": me.Data.ID},
	}, &sub); err != nil {
		return fmt.Errorf("X refused the broadcast.chat subscription: %s", xProblemMessage(err))
	}
	subID := strings.TrimSpace(sub.Data.Subscription.ID)
	if subID == "" {
		// X created a subscription this run cannot name, so it can never be removed. Fatal, because a retry would
		// only create, and leak, another one.
		return Fatal(errors.New("X created the broadcast.chat subscription but returned no subscription_id, " +
			"so polyemesis could not remove it later; live chat is stopped rather than leaving subscriptions " +
			"running on X's side"))
	}
	defer x.removeSubscription(subID)

	return x.streamChat(ctx, sink, &xBroadcastOwner{x: x})
}

// removeSubscription deletes the activity subscription. It uses a fresh context
// because the run's context is already done by the time this runs.
func (x *XAdapter) removeSubscription(subID string) {
	if subID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = x.xCall(ctx, http.MethodDelete, "/2/activity/subscriptions/"+url.PathEscape(subID), false, nil, nil)
}

// streamClient is the HTTP client for the long-lived activity stream. The control client carries a whole-request
// Timeout, and Go applies that to reading the response body as well, so it would cut the stream every 30 seconds.
// The stream keeps the control client's transport and redirect and cookie handling, but has no overall deadline:
// the run's context ends it.
func (x *XAdapter) streamClient() *http.Client {
	return &http.Client{
		Transport:     x.cfg.HTTP.Transport,
		CheckRedirect: x.cfg.HTTP.CheckRedirect,
		Jar:           x.cfg.HTTP.Jar,
	}
}

// streamChat reads the app-only activity stream line by line. A broadcast.chat
// event becomes a Message; every other event is skipped.
func (x *XAdapter) streamChat(ctx context.Context, sink Sink, owner *xBroadcastOwner) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, x.cfg.APIBase+"/2/activity/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+x.cfg.AppBearer)
	req.Header.Set("Accept", "application/json")
	resp, err := x.streamClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("X refused the activity stream (%d)", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// The stream is the whole app's, so it also carries chat for every other
		// account's broadcasts. Only this account's running broadcast gets through.
		if m, broadcastID, ok := x.decodeChat([]byte(line)); ok && owner.owns(ctx, broadcastID) {
			sink.Deliver(m)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return sc.Err()
}

// xChatEvent is one activity-stream document.
type xChatEvent struct {
	Data struct {
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
	} `json:"data"`
}

// xChatPayload is the broadcast.chat payload in the documented shape: the text is in message, the message id is
// message_id, and the author sits under author.data. The documented payload has no created time, so the arrival time
// stands in for it. PROVISIONAL against live X: see the file header.
type xChatPayload struct {
	MessageID   string `json:"message_id"`
	BroadcastID string `json:"broadcast_id"`
	Message     string `json:"message"`
	ReplyTo     string `json:"reply_to"`
	Author      struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"data"`
	} `json:"author"`
}

// decodeChat turns one stream line into a Message. Anything that is not a
// broadcast.chat event, or that carries no text, is dropped.
func (x *XAdapter) decodeChat(line []byte) (Message, string, bool) {
	var ev xChatEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Data.EventType != "broadcast.chat" {
		return Message{}, "", false
	}
	var p xChatPayload
	if err := json.Unmarshal(ev.Data.Payload, &p); err != nil || strings.TrimSpace(p.Message) == "" {
		return Message{}, "", false
	}
	at := x.cfg.Now()
	name := p.Author.Data.Name
	if name == "" {
		name = p.Author.Data.Username
	}
	id := p.MessageID
	if id == "" {
		id = "syn-x-" + p.Author.Data.ID + "-" + strconv.FormatInt(at.UnixNano(), 10)
	}
	return Message{
		ID:        id,
		Platform:  db.PlatformX,
		Account:   x.cfg.AccountRef,
		Channel:   x.cfg.Channel,
		Author:    Author{ID: p.Author.Data.ID, Name: name},
		Text:      p.Message,
		At:        at,
		ReplyToID: p.ReplyTo,
	}, p.BroadcastID, true
}

// xOwnerRefreshEvery bounds how often a broadcast id already judged not this account's is checked again. Chat for
// another account's show arrives continually on the shared stream, so without a bound each of those lines would
// cost a request.
const xOwnerRefreshEvery = 15 * time.Second

// xOwnerRefreshFloor is the shortest gap between two checks of any kind. A broadcast id never seen before is checked
// as soon as the floor allows, so this account's own new show is recognised from its first lines rather than after
// another account's chat has used up the long window (review #44).
const xOwnerRefreshFloor = time.Second

// xOwnerRetryAfterFailure is how soon an id is asked about again after a broadcast list read that failed. Shorter
// than xOwnerRefreshEvery, because a failure judged nothing; longer than the floor, because X may be rate limiting.
const xOwnerRetryAfterFailure = 3 * time.Second

// xOwnerSeenMax bounds the memory of judged ids. Past it the memory starts again, which costs one check per id.
const xOwnerSeenMax = 1024

// xBroadcastOwner answers whether a chat event belongs to this account. The
// activity stream is the app's, not the account's: one subscription per user
// covers all of that user's broadcasts, and every other connected account's chat
// arrives on the same stream. An event is this account's only when its
// broadcast_id is the broadcast this account is running now.
type xBroadcastOwner struct {
	x       *XAdapter
	mu      sync.Mutex
	running string
	checked time.Time
	// seen records when each id was last judged not this account's. An id not in it has never been checked.
	seen map[string]time.Time
}

func (o *xBroadcastOwner) owns(ctx context.Context, broadcastID string) bool {
	if broadcastID == "" {
		return false // an event that names no broadcast cannot be placed, so it is not shown as ours
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if broadcastID == o.running {
		return true
	}
	if told := o.x.toldBroadcast(); told != "" && broadcastID == told {
		// The coordinator published this broadcast for this account, so no read is needed to place it.
		o.running = told
		return true
	}
	now := o.x.cfg.Now()
	if last, ok := o.seen[broadcastID]; ok && now.Sub(last) < xOwnerRefreshEvery {
		return false
	}
	if !o.checked.IsZero() && now.Sub(o.checked) < xOwnerRefreshFloor {
		return false
	}
	o.checked = now
	// "No running broadcast" is a judgement, and the id is remembered as checked below, so another account's chat
	// does not cost a request per line; it is checked again once the long window has passed. A read that failed is
	// no judgement: the id is held only for the short retry, so one transient error does not drop this account's
	// own chat for the whole window, and a rate-limited X is not asked again every second either.
	id, err := o.x.runningBroadcastID(ctx)
	if err != nil && !errors.Is(err, errXNoRunningBroadcast) {
		o.remember(broadcastID, now.Add(xOwnerRetryAfterFailure-xOwnerRefreshEvery))
		return false
	}
	if err == nil {
		o.running = id
		if broadcastID == id {
			return true
		}
	}
	o.remember(broadcastID, now)
	return false
}

// remember records when an id was last judged, bounded by xOwnerSeenMax. The caller holds o.mu.
func (o *xBroadcastOwner) remember(broadcastID string, at time.Time) {
	if o.seen == nil || len(o.seen) >= xOwnerSeenMax {
		o.seen = map[string]time.Time{}
	}
	o.seen[broadcastID] = at
}
