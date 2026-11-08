package activityhub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/redact"
	"github.com/rainmanjam/polyemesis/internal/twitch/eventsub"
)

// TWITCH: EventSub OVER WEBSOCKET, AS AN activityhub.Source (design 3.1).
//
// One socket per connected Twitch account, opened with the account's user token
// (no public callback, which suits a self-hosted install). On the welcome
// message it creates a subscription for every type the account can have; the
// subscriptions live and die with the session, so a restart is a fresh session
// and Twitch replays nothing. Notifications become activity.Events with
// Source{Type: subscription type, ID: metadata.message_id}: Twitch delivers at
// least once and resends under the same id, so the hub's key (and this
// source's own seen-set, which outlives a reconnect) drops the repeat.
//
// WHAT THE SOURCE DOES NOT DO, ON PURPOSE. It never writes a text frame
// (Twitch closes with 4001). It never turns a tier-gated type into an error: a
// channel that is not an Affiliate or Partner has those kinds reported
// "unavailable" and the source stays live. It never keeps or logs a payload:
// they carry viewer text, so a bad one is counted, with its type and size.

// TwitchConfig is what the wiring (internal/api/activity_wiring.go) supplies.
type TwitchConfig struct {
	// AccountRef is the channel owner's Twitch user id: the broadcaster in
	// every condition, and the moderator in the two that name one.
	AccountRef string
	ClientID   string
	// Token returns the user access token; Refresh swaps one Twitch refused.
	Token   func(ctx context.Context) (string, error)
	Refresh func(ctx context.Context, rejected string) (string, error)
	// Scopes returns the scopes the account's token holds NOW. A scope bump
	// without a reconnect leaves a token without the new scopes, so the source
	// asks the account, never the ScopeVersion.
	Scopes func() []string
	// Mirrors lists the polls polyemesis mirrored, to skip them (D9).
	Mirrors *MirrorLedger
	Log     *slog.Logger

	// Overridable for tests and for a proxy; empty means Twitch's.
	WSURL     string
	HelixBase string
	HTTP      *http.Client
	Dialer    *websocket.Dialer
	// AllowReconnectHost replaces the "must be under twitch.tv" rule. Tests only.
	AllowReconnectHost func(rawurl string) error
	// KeepaliveGrace is added to Twitch's keepalive timeout before the silence
	// counts as a dead socket. Default 5 s.
	KeepaliveGrace time.Duration
	// TierRecheck is how often the channel's account type is read again.
	// Default 24 h.
	TierRecheck time.Duration
	// PollHold is how long a poll notification waits for an in-flight mirror
	// create to finish. Default 2 s.
	PollHold time.Duration
	// WelcomeWait bounds the wait for the first message. Default 15 s.
	WelcomeWait time.Duration
	Now         func() time.Time
}

// TwitchStats are the source's counters, the way Kick's are (kick.go): what
// arrived, what could not be read, what was a repeat, what was set aside.
type TwitchStats struct {
	Received    int64 `json:"received"`
	Emitted     int64 `json:"emitted"`
	Unparseable int64 `json:"unparseable"`
	Deduped     int64 `json:"deduped"`
	Ignored     int64 `json:"ignored"`
	Reconnects  int64 `json:"reconnects"`
}

// Twitch is the EventSub source for one account.
type Twitch struct {
	cfg TwitchConfig
	api *eventsub.API
	log *slog.Logger
	now func() time.Time

	received, emitted, unparseable, deduped, ignored, reconnects atomic.Int64

	seen *seenSet

	mu     sync.Mutex
	state  chat.State
	detail string
	kinds  map[activity.Kind]activity.KindStatus
}

// Compile-time contract.
var (
	_ Source       = (*Twitch)(nil)
	_ Healther     = (*Twitch)(nil)
	_ KindReporter = (*Twitch)(nil)
)

const (
	twitchSeenCap = 4096
	// staleAfter is the replay-protection rule Twitch gives for webhooks and
	// that holds here too: a message stamped more than ten minutes ago is not
	// something that just happened.
	twitchStaleAfter = 10 * time.Minute
	// twitchSubscribeParallel is how many subscriptions are created at once; the
	// whole set must be in before Twitch's welcome window closes.
	twitchSubscribeParallel = 8
)

// NewTwitch builds the source. It does not connect.
func NewTwitch(cfg TwitchConfig) *Twitch {
	t := &Twitch{cfg: cfg, log: cfg.Log, now: cfg.Now, seen: newSeenSet(twitchSeenCap)}
	if t.log == nil {
		t.log = slog.Default()
	}
	if t.now == nil {
		t.now = time.Now
	}
	t.api = &eventsub.API{Base: cfg.HelixBase, ClientID: cfg.ClientID, HTTP: cfg.HTTP, Token: cfg.Token, Refresh: cfg.Refresh}
	t.kinds = t.initialKinds()
	return t
}

// Platform and Account identify the source.
func (t *Twitch) Platform() string { return "twitch" }
func (t *Twitch) Account() string  { return t.cfg.AccountRef }

// Stats returns the counters.
func (t *Twitch) Stats() TwitchStats {
	return TwitchStats{
		Received: t.received.Load(), Emitted: t.emitted.Load(), Unparseable: t.unparseable.Load(),
		Deduped: t.deduped.Load(), Ignored: t.ignored.Load(), Reconnects: t.reconnects.Load(),
	}
}

// Health is the operator's view of the socket. It is empty once Run has
// returned, so the supervisor's own "trying again in ..." line shows.
func (t *Twitch) Health() chat.Health {
	t.mu.Lock()
	defer t.mu.Unlock()
	return chat.Health{State: t.state, Detail: t.detail}
}

func (t *Twitch) setHealth(st chat.State, detail string) {
	t.mu.Lock()
	t.state, t.detail = st, detail
	t.mu.Unlock()
}

// Kinds reports every kind this source covers, in display order.
func (t *Twitch) Kinds() []activity.KindStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]activity.KindStatus, 0, len(t.kinds))
	for _, k := range eventsub.Kinds() {
		out = append(out, t.kinds[k])
	}
	return out
}

func (t *Twitch) setKinds(m map[activity.Kind]activity.KindStatus) {
	t.mu.Lock()
	t.kinds = m
	t.mu.Unlock()
}

func (t *Twitch) setKind(k activity.Kind, st activity.KindStatus) {
	t.mu.Lock()
	if t.kinds == nil {
		t.kinds = map[activity.Kind]activity.KindStatus{}
	}
	t.kinds[k] = st
	t.mu.Unlock()
}

func (t *Twitch) grantedScopes() []string {
	if t.cfg.Scopes == nil {
		return nil
	}
	return t.cfg.Scopes()
}

// kindLabel is the operator's word for a kind, for the details below.
func kindLabel(k activity.Kind) string {
	switch k {
	case activity.KindFollow:
		return "follows"
	case activity.KindSubscribe:
		return "subscriptions"
	case activity.KindResub:
		return "resub messages"
	case activity.KindGift:
		return "gifted subs"
	case activity.KindTip:
		return "bits"
	case activity.KindRaid:
		return "raids"
	case activity.KindRedemption:
		return "channel points"
	case activity.KindHypeTrain:
		return "hype trains"
	case activity.KindPoll:
		return "polls"
	case activity.KindPrediction:
		return "predictions"
	case activity.KindStream:
		return "stream status"
	case activity.KindShoutout:
		return "shoutouts"
	}
	return string(k)
}

const (
	detailNeedsTier  = "Twitch only sends this to Affiliates and Partners."
	detailReconnect  = "Reconnect the Twitch account in Settings → Platforms to allow this."
	detailStarting   = "Waiting for the Twitch connection."
	detailRevoked    = "Twitch says access to this was revoked; reconnect the Twitch account."
	detailGone       = "Twitch no longer offers this event."
	detailRefused    = "Twitch would not turn this on for this channel; it is normally an Affiliate or Partner feature."
	detailLimit      = "Twitch's subscription limit was reached, so this is off for now."
	detailAnotherSes = "Twitch still has this on for another connection; it will recover on the next reconnect."
	detailTransient  = "Twitch could not turn this on just now; it is tried again on the next reconnect."
)

// initialKinds is the report before the first session: a scope the token lacks
// is "reconnect" already, everything else is not yet known.
func (t *Twitch) initialKinds() map[activity.Kind]activity.KindStatus {
	granted := t.grantedScopes()
	m := map[activity.Kind]activity.KindStatus{}
	for _, s := range eventsub.Catalogue() {
		m[s.Kind] = worse(m[s.Kind], activity.KindStatus{Kind: s.Kind, State: activity.KindUnknown, Detail: detailStarting})
		if !s.Holds(granted) {
			m[s.Kind] = worse(m[s.Kind], activity.KindStatus{Kind: s.Kind, State: activity.KindReconnect, Detail: detailReconnect})
		}
	}
	return m
}

// severity orders the states for the roll-up of several subscription types
// into one kind: the worst one is reported.
func severity(s activity.KindState) int {
	switch s {
	case activity.KindOn:
		return 1
	case activity.KindUnknown:
		return 2
	case activity.KindUnavailable:
		return 3
	case activity.KindReconnect:
		return 4
	}
	return 0
}

func worse(cur, next activity.KindStatus) activity.KindStatus {
	if cur.Kind == "" || severity(next.State) > severity(cur.State) {
		return next
	}
	return cur
}

// ---------------------------------------------------------------------------
// Run

// Run connects and serves one Twitch account until ctx ends or the socket is
// lost. It returns an error for anything the supervisor should retry.
func (t *Twitch) Run(ctx context.Context, emit func(activity.Event)) error {
	t.setHealth(chat.StateConnecting, "Connecting to Twitch alerts.")
	defer t.setHealth("", "")
	err := t.run(ctx, emit)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// errDeadGrant marks a token Twitch (or the store) says is gone for good.
func deadGrant(err error) bool {
	return errors.Is(err, chat.ErrReconnectRequired) || chat.IsFatal(err) || eventsub.IsStatus(err, http.StatusUnauthorized)
}

// parkUntilReconnected is the answer to a dead grant: the supervisor has no
// notion of a fatal error, so left alone it would ask Twitch again every two
// minutes with a token that cannot work. Say so, and wait to be stopped; the
// operator reconnecting the account detaches and re-attaches this source.
func (t *Twitch) parkUntilReconnected(ctx context.Context) error {
	for k, st := range t.kindsSnapshot() {
		if st.State == activity.KindOn || st.State == activity.KindUnknown {
			t.setKind(k, activity.KindStatus{Kind: k, State: activity.KindReconnect, Detail: detailReconnect})
		}
	}
	t.setHealth(chat.StateFailed, "Twitch alerts stopped: reconnect the Twitch account in Settings → Platforms.")
	<-ctx.Done()
	return ctx.Err()
}

func (t *Twitch) kindsSnapshot() map[activity.Kind]activity.KindStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[activity.Kind]activity.KindStatus, len(t.kinds))
	for k, v := range t.kinds {
		out[k] = v
	}
	return out
}

func (t *Twitch) run(ctx context.Context, emit func(activity.Event)) error {
	tier, err := t.api.BroadcasterType(ctx, t.cfg.AccountRef)
	if err != nil {
		if deadGrant(err) {
			return t.parkUntilReconnected(ctx)
		}
		return err
	}

	wsURL := t.cfg.WSURL
	if wsURL == "" {
		wsURL = eventsub.DefaultWSURL
	}
	conn, err := eventsub.Dial(ctx, fmt.Sprintf("%s?keepalive_timeout_seconds=%d", wsURL, eventsub.KeepaliveSeconds), t.cfg.Dialer)
	if err != nil {
		return err
	}
	// cur and pending are closed by name at return: the reconnect swaps them.
	var pending *eventsub.Conn
	defer func() {
		conn.Close()
		if pending != nil {
			pending.Close()
		}
	}()

	welcome, err := t.awaitWelcome(ctx, conn)
	if err != nil {
		return err
	}
	keepalive := t.keepaliveFor(welcome)

	ok, err := t.subscribeAll(ctx, welcome.ID, tier)
	if err != nil {
		if deadGrant(err) {
			return t.parkUntilReconnected(ctx)
		}
		return err
	}
	if !ok {
		// Nothing could be turned on, so Twitch would close the socket as
		// unused (4003) after its welcome window. Health is cleared when Run
		// returns, so the reason travels in the error, which the supervisor
		// shows as the source's detail.
		return fmt.Errorf("none of the alert types could be turned on (%s)", strings.TrimSuffix(t.summary(tier), "."))
	}
	t.setHealth(chat.StateLive, t.summary(tier))

	timer := time.NewTimer(keepalive)
	defer timer.Stop()
	recheck := t.cfg.TierRecheck
	if recheck <= 0 {
		recheck = 24 * time.Hour
	}
	tierTick := time.NewTicker(recheck)
	defer tierTick.Stop()

	var graceTimer *time.Timer // the 30 s Twitch gives for a reconnect
	var graceC <-chan time.Time
	defer func() {
		if graceTimer != nil {
			graceTimer.Stop()
		}
	}()
	rearm := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(keepalive)
	}

	curC := conn.Frames()
	var pendingC <-chan eventsub.Frame
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-timer.C:
			return fmt.Errorf("Twitch went quiet for %s; reconnecting", keepalive.Round(time.Second))

		case <-graceC:
			// session_reconnect was answered too late or not at all.
			return errors.New("the Twitch reconnect did not finish in time")

		case <-tierTick.C:
			now, terr := t.api.BroadcasterType(ctx, t.cfg.AccountRef)
			if terr != nil {
				if deadGrant(terr) {
					return t.parkUntilReconnected(ctx)
				}
				continue // read again at the next tick; the socket is fine
			}
			if now != tier {
				return errors.New("the channel's Twitch account type changed; subscribing again")
			}

		case f, open := <-pendingC:
			if !open || f.Err != nil {
				// The new socket never came up. The old one is still good until
				// Twitch closes it (4004 after the grace), which ends the run.
				pending.Close()
				pending, pendingC = nil, nil
				continue
			}
			rearm()
			if f.Bad {
				t.unparseable.Add(1)
				continue
			}
			switch f.Msg.Type() {
			case eventsub.TypeWelcome:
				// Subscriptions carried over: nothing is created here.
				sess, serr := f.Msg.Session()
				if serr != nil {
					t.unparseable.Add(1)
					continue
				}
				old := conn
				conn, curC = pending, pending.Frames()
				pending, pendingC = nil, nil
				old.Close()
				keepalive = t.keepaliveFor(sess)
				rearm()
				if graceTimer != nil {
					graceTimer.Stop()
					graceC = nil
				}
				t.reconnects.Add(1)
			case eventsub.TypeNotification:
				t.notification(ctx, f.Msg, emit)
			}

		case f, open := <-curC:
			if !open || f.Err != nil {
				if pending != nil {
					// The old socket ended while the new one is coming up; carry on with the new.
					curC = nil
					continue
				}
				return t.closeError(f.Err)
			}
			rearm()
			if f.Bad {
				t.unparseable.Add(1)
				continue
			}
			switch f.Msg.Type() {
			case eventsub.TypeKeepalive, eventsub.TypeWelcome:
			case eventsub.TypeNotification:
				t.notification(ctx, f.Msg, emit)
			case eventsub.TypeRevocation:
				t.revocation(f.Msg)
			case eventsub.TypeReconnect:
				if pending != nil {
					continue
				}
				np, rerr := t.startReconnect(ctx, f.Msg)
				if rerr != nil {
					return rerr
				}
				pending, pendingC = np, np.Frames()
				graceTimer = time.NewTimer(30 * time.Second)
				graceC = graceTimer.C
			default:
				t.ignored.Add(1)
			}
		}
	}
}

func (t *Twitch) keepaliveFor(s eventsub.SessionInfo) time.Duration {
	secs := eventsub.KeepaliveSeconds
	if s.KeepaliveTimeoutSeconds != nil && *s.KeepaliveTimeoutSeconds > 0 {
		secs = *s.KeepaliveTimeoutSeconds
	}
	grace := t.cfg.KeepaliveGrace
	if grace <= 0 {
		grace = 5 * time.Second
	}
	return time.Duration(secs)*time.Second + grace
}

// awaitWelcome reads the first message of a fresh connection.
func (t *Twitch) awaitWelcome(ctx context.Context, c *eventsub.Conn) (eventsub.SessionInfo, error) {
	wait := t.cfg.WelcomeWait
	if wait <= 0 {
		wait = 15 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return eventsub.SessionInfo{}, ctx.Err()
		case <-timer.C:
			return eventsub.SessionInfo{}, errors.New("Twitch did not welcome the connection")
		case f, open := <-c.Frames():
			if !open || f.Err != nil {
				return eventsub.SessionInfo{}, t.closeError(f.Err)
			}
			if f.Bad || f.Msg.Type() != eventsub.TypeWelcome {
				continue
			}
			return f.Msg.Session()
		}
	}
}

// startReconnect dials the reconnect_url exactly as given, after checking that
// it is Twitch's.
func (t *Twitch) startReconnect(ctx context.Context, m eventsub.Message) (*eventsub.Conn, error) {
	sess, err := m.Session()
	if err != nil || sess.ReconnectURL == nil || *sess.ReconnectURL == "" {
		return nil, errors.New("Twitch asked to reconnect without saying where")
	}
	check := eventsub.ValidReconnectURL
	if t.cfg.AllowReconnectHost != nil {
		check = t.cfg.AllowReconnectHost
	}
	if err := check(*sess.ReconnectURL); err != nil {
		return nil, err
	}
	c, err := eventsub.Dial(ctx, *sess.ReconnectURL, t.cfg.Dialer)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// closeError turns the end of a socket into the error the supervisor logs, and
// for 4003 into the health line that names the cause.
func (t *Twitch) closeError(err error) error {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case eventsub.CloseUnused:
			return fmt.Errorf("Twitch closed the connection as unused (%s)", strings.TrimSuffix(t.summary(""), "."))
		case eventsub.CloseReconnectGrace, eventsub.CloseBadReconnect:
			return errors.New("Twitch asked for a new connection")
		case eventsub.CloseInternal, eventsub.CloseNetworkTimeout, eventsub.CloseNetworkError:
			return fmt.Errorf("Twitch dropped the connection (%d)", ce.Code)
		}
		return fmt.Errorf("Twitch closed the connection (%d)", ce.Code)
	}
	return errors.New("the Twitch connection was lost")
}

// ---------------------------------------------------------------------------
// Subscribing

type subOutcome struct {
	spec  eventsub.Spec
	state activity.KindState
	why   string
	fatal error
}

// subscribeAll creates every subscription the account can have and records
// each kind's state. ok is false when not one was created.
func (t *Twitch) subscribeAll(ctx context.Context, sessionID, tier string) (bool, error) {
	specs := eventsub.Catalogue()
	granted := t.grantedScopes()
	out := make([]subOutcome, len(specs))

	sem := make(chan struct{}, twitchSubscribeParallel)
	var wg sync.WaitGroup
	for i, s := range specs {
		o := subOutcome{spec: s}
		switch {
		case !s.Holds(granted):
			o.state, o.why = activity.KindReconnect, detailReconnect
		case s.NeedsTier && tier == eventsub.TierNone:
			o.state, o.why = activity.KindUnavailable, detailNeedsTier
		default:
			out[i] = o
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, s eventsub.Spec) {
				defer wg.Done()
				defer func() { <-sem }()
				out[i] = t.createOne(ctx, sessionID, s)
			}(i, s)
			continue
		}
		out[i] = o
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return false, err
	}

	kinds := map[activity.Kind]activity.KindStatus{}
	created := 0
	for _, o := range out {
		if o.fatal != nil {
			return false, o.fatal
		}
		if o.state == activity.KindOn {
			created++
		}
		detail := o.why
		kinds[o.spec.Kind] = worse(kinds[o.spec.Kind], activity.KindStatus{Kind: o.spec.Kind, State: o.state, Detail: detail})
	}
	t.setKinds(kinds)
	return created > 0, nil
}

// createOne makes one subscription, retrying what is transient and turning
// everything Twitch says about the account (tier, scope, cost) into a kind
// state rather than an error. Only a dead grant is fatal.
func (t *Twitch) createOne(ctx context.Context, sessionID string, s eventsub.Spec) subOutcome {
	o := subOutcome{spec: s}
	for attempt := 0; attempt < 3; attempt++ {
		res, err := t.api.Subscribe(ctx, sessionID, s, t.cfg.AccountRef)
		if err == nil {
			if res.Cost > 0 {
				t.log.Info("twitch eventsub: a subscription has a cost",
					"type", s.Type, "cost", res.Cost, "total", res.TotalCost, "max", res.MaxTotalCost)
			}
			o.state = activity.KindOn
			return o
		}
		var ae *eventsub.APIError
		if !errors.As(err, &ae) {
			// A transport failure: try again briefly, then give up on this one.
			if !sleep(ctx, 500*time.Millisecond) {
				return o
			}
			continue
		}
		switch {
		case ae.Status == http.StatusUnauthorized, errors.Is(err, chat.ErrReconnectRequired), chat.IsFatal(err):
			o.fatal = err
			return o
		case ae.Status == http.StatusForbidden:
			// The scope is held (checked above), so this is the tier or the
			// account: not a failure.
			o.state, o.why = activity.KindUnavailable, detailRefused
			return o
		case ae.Status == http.StatusGone:
			o.state, o.why = activity.KindUnavailable, detailGone
			return o
		case ae.Status == http.StatusConflict:
			// Another session of this account still holds the type; it frees
			// itself when that socket closes. Retry shortly, then say so.
			o.state, o.why = activity.KindUnknown, detailAnotherSes
			if !sleep(ctx, time.Second) {
				return o
			}
			continue
		case ae.Status == http.StatusTooManyRequests:
			// Either Helix's rate limit (Ratelimit-Reset says when) or the
			// subscription cost/count limit. A reset within the window is waited
			// out once; otherwise the kind is unavailable, not the source failed.
			if wait := time.Until(ae.RetryAt); attempt == 0 && wait > 0 && wait <= 5*time.Second {
				if !sleep(ctx, wait) {
					return o
				}
				continue
			}
			o.state, o.why = activity.KindUnavailable, detailLimit
			return o
		case ae.Status >= 500:
			if !sleep(ctx, 500*time.Millisecond) {
				return o
			}
			continue
		default: // 400 and the rest: Twitch will not take it as asked
			t.log.Warn("twitch eventsub: a subscription was refused", "type", s.Type, "status", ae.Status)
			o.state, o.why = activity.KindUnavailable, detailRefused
			return o
		}
	}
	if o.state == "" {
		o.state, o.why = activity.KindUnknown, detailTransient
	}
	return o
}

func sleep(ctx context.Context, d time.Duration) bool {
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-tm.C:
		return true
	}
}

// summary is the live line: how many of the kinds are on, and why the rest are not.
func (t *Twitch) summary(tier string) string {
	snap := t.kindsSnapshot()
	on, total := 0, 0
	var needTier, reconnect []string
	for _, k := range eventsub.Kinds() {
		total++
		st := snap[k]
		switch st.State {
		case activity.KindOn:
			on++
		case activity.KindUnavailable:
			needTier = append(needTier, kindLabel(k))
		case activity.KindReconnect:
			reconnect = append(reconnect, kindLabel(k))
		}
	}
	s := fmt.Sprintf("Twitch alerts: %d of %d kinds on", on, total)
	if len(needTier) > 0 {
		if tier == eventsub.TierNone {
			s += "; " + joinWords(needTier) + " need Affiliate or Partner"
		} else {
			s += "; " + joinWords(needTier) + " are not available to this channel"
		}
	}
	if len(reconnect) > 0 {
		s += "; reconnect Twitch for " + joinWords(reconnect)
	}
	return s + "."
}

func joinWords(w []string) string {
	switch len(w) {
	case 0:
		return ""
	case 1:
		return w[0]
	}
	return strings.Join(w[:len(w)-1], ", ") + " and " + w[len(w)-1]
}

// ---------------------------------------------------------------------------
// Notifications

func (t *Twitch) notification(ctx context.Context, m eventsub.Message, emit func(activity.Event)) {
	t.received.Add(1)
	typ := m.Metadata.SubscriptionType
	if !t.seen.add(m.Metadata.MessageID) {
		// Twitch resends under the same message id, and the reconnect overlap
		// can deliver one message on both sockets.
		t.deduped.Add(1)
		return
	}
	at := m.Metadata.Time()
	if !at.IsZero() && t.now().Sub(at) > twitchStaleAfter {
		t.ignored.Add(1)
		return
	}
	raw, err := m.Event()
	if err != nil {
		t.unparseable.Add(1)
		t.log.Debug("twitch eventsub: a notification had no event", "type", typ)
		return
	}
	mp, oc := mapTwitch(typ, raw, at, t.cfg.AccountRef)
	switch oc {
	case outcomeIgnored:
		t.ignored.Add(1)
		return
	case outcomeBad:
		t.unparseable.Add(1)
		// Type and size only: the body is viewer text.
		t.log.Debug("twitch eventsub: a notification could not be read", "type", typ, "bytes", len(raw))
		return
	}
	if mp.pollID != "" {
		hold := t.cfg.PollHold
		if hold <= 0 {
			hold = 2 * time.Second
		}
		if t.cfg.Mirrors.Mirrored(ctx, mp.pollID, hold) {
			t.ignored.Add(1)
			return
		}
	}
	mp.ev.Source.ID = m.Metadata.MessageID
	t.emitted.Add(1)
	emit(mp.ev)
}

// revocation records what Twitch took away. The socket stays open: the other
// subscriptions are untouched.
func (t *Twitch) revocation(m eventsub.Message) {
	sub, err := m.Revocation()
	if err != nil {
		t.unparseable.Add(1)
		return
	}
	for _, s := range eventsub.Catalogue() {
		if s.Type != sub.Type {
			continue
		}
		st := activity.KindStatus{Kind: s.Kind}
		switch sub.Status {
		case eventsub.RevokedAuthorization:
			st.State, st.Detail = activity.KindReconnect, detailRevoked
		default: // user_removed, version_removed
			st.State, st.Detail = activity.KindUnavailable, detailGone
		}
		t.setKind(s.Kind, st)
		t.log.Info("twitch eventsub: a subscription was revoked", "type", redact.Redact(sub.Type), "status", sub.Status)
		return
	}
}

// ---------------------------------------------------------------------------
// seen-set

// seenSet is a bounded set of message ids: a copy of the hub's markSeen shape,
// kept here because it must outlive a reconnect of THIS source.
type seenSet struct {
	mu    sync.Mutex
	cap   int
	set   map[string]struct{}
	order []string
}

func newSeenSet(cap int) *seenSet { return &seenSet{cap: cap, set: map[string]struct{}{}} }

// add reports whether id was new.
func (s *seenSet) add(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.set[id]; dup {
		return false
	}
	s.set[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > s.cap {
		delete(s.set, s.order[0])
		s.order = s.order[1:]
	}
	return true
}
