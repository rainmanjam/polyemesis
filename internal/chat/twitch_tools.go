package chat

// The Twitch channel tools: everything a broadcaster or moderator does to a
// ROOM or a ROLE rather than to one message -- announcements, shoutouts, pins,
// Shield Mode, warnings, blocked terms, the chatter list, AutoMod, roles, the
// banned list, unban requests and suspicious-user flags.
//
// All of it is Helix, all of it addressed with the same two ids the existing
// moderation calls use (see helixReady), and none of it has been exercised
// against a live account: it is built from Twitch's published reference and
// tested against fakes. The capability matrix says "unverified" for that
// reason. Doc: https://dev.twitch.tv/docs/api/reference/
//
// NEW SCOPES, one reconnect (oauth.Twitch.ScopeVersion 6). Every call below
// answers a 401 with the same instruction -- reconnect -- because an account
// connected before the bump holds a token without the scope.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TwitchTools is the optional capability the Twitch adapter has and the other
// three platforms do not. One interface rather than a dozen: they share one
// credential, one pair of ids and one error vocabulary, and a UI that offers
// "shoutout" without "warn" would be offering half a console.
type TwitchTools interface {
	Adapter
	Announce(ctx context.Context, message, color string) error
	Shoutout(ctx context.Context, u UserRef) (ShoutoutResult, error)
	ShoutoutCooldowns() ShoutoutResult
	GetPin(ctx context.Context) (*PinnedMessage, error)
	PinMessage(ctx context.Context, messageID string, durationSeconds int) error
	UpdatePin(ctx context.Context, messageID string, durationSeconds int) error
	Unpin(ctx context.Context, messageID string) error
	GetShield(ctx context.Context) (ShieldState, error)
	SetShield(ctx context.Context, active bool) (ShieldState, error)
	Warn(ctx context.Context, u UserRef, reason string) error
	ListBlockedTerms(ctx context.Context) ([]BlockedTerm, error)
	AddBlockedTerm(ctx context.Context, text string) (BlockedTerm, error)
	DeleteBlockedTerm(ctx context.Context, id string) error
	SyncBlockedTerms(ctx context.Context, terms []string) (BlockedTermSync, error)
	Chatters(ctx context.Context, withList bool) (ChatterList, error)
	GetAutoMod(ctx context.Context) (AutoModSettings, error)
	SetAutoMod(ctx context.Context, s AutoModSettings) (AutoModSettings, error)
	ResolveHeldMessage(ctx context.Context, messageID string, allow bool) error
	ListRole(ctx context.Context, role string) ([]RoleHolder, error)
	AddRole(ctx context.Context, role string, u UserRef) error
	RemoveRole(ctx context.Context, role string, u UserRef) error
	ListBanned(ctx context.Context) ([]BannedUser, error)
	ListUnbanRequests(ctx context.Context, status string) ([]UnbanRequest, error)
	ResolveUnbanRequest(ctx context.Context, id, status, text string) (UnbanRequest, error)
	SetSuspicious(ctx context.Context, u UserRef, status string) error
	ClearSuspicious(ctx context.Context, u UserRef) error
}

var _ TwitchTools = (*TwitchAdapter)(nil)

// UserRef names a viewer by numeric id or by login. An operator types a name;
// a chat message carries an id. Either is accepted and a login is resolved
// through Get Users.
type UserRef struct {
	ID    string `json:"userId,omitempty"`
	Login string `json:"login,omitempty"`
}

// ShoutoutResult is the cooldown state after a shoutout, or on its own.
//
// Helix returns no body for a shoutout, and the authoritative cooldowns arrive
// only on the EventSub channel.shoutout.create event, which this build does not
// subscribe to. These times are polyemesis's own count from the documented
// limits (one shoutout per 2 minutes, one per 60 minutes to the same channel),
// so a shoutout sent from Twitch's own UI is not seen here and the 429 from
// Twitch stays the final word.
type ShoutoutResult struct {
	// NextAnyAt is when a shoutout to anyone may be sent again; nil if now.
	NextAnyAt *time.Time `json:"nextAnyAt"`
	// Targets maps a target id to the time it may be shouted out again.
	Targets map[string]time.Time `json:"targets"`
}

// PinnedMessage is the one mod-pinned message a channel can hold.
type PinnedMessage struct {
	MessageID    string `json:"messageId"`
	SenderID     string `json:"senderId"`
	SenderLogin  string `json:"senderLogin"`
	SenderName   string `json:"senderName"`
	PinnedByName string `json:"pinnedByName"`
	Text         string `json:"text"`
}

// ShieldState is Shield Mode's activation status.
type ShieldState struct {
	Active          bool   `json:"active"`
	ModeratorLogin  string `json:"moderatorLogin,omitempty"`
	LastActivatedAt string `json:"lastActivatedAt,omitempty"`
}

// BlockedTerm is one word or phrase Twitch holds back.
type BlockedTerm struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// BlockedTermSync reports what a push of polyemesis automod words did.
type BlockedTermSync struct {
	Added   []string      `json:"added"`
	Present []string      `json:"present"`
	Skipped []SkippedTerm `json:"skipped"`
}

// SkippedTerm is a term that was not sent, with why.
type SkippedTerm struct {
	Term   string `json:"term"`
	Reason string `json:"reason"`
}

// Chatter is one person connected to the chat room.
type Chatter struct {
	UserID string `json:"userId"`
	Login  string `json:"login"`
	Name   string `json:"name"`
}

// ChatterList is the connected-chatter count and, when asked, the people.
type ChatterList struct {
	// Total is Twitch's own count of everyone connected, viewers included.
	Total int `json:"total"`
	// Chatters is empty unless the list was asked for.
	Chatters  []Chatter `json:"chatters,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
}

// AutoModSettings are Twitch's own AutoMod levels (0 off to 4 strictest).
//
// THE PUT REPLACES, IT DOES NOT PATCH. Twitch zeroes every category a request
// does not name, so a single-category change would silently switch the other
// seven off. Set() sends either the overall level alone or all seven
// categories, never a mix; see Validate.
type AutoModSettings struct {
	OverallLevel          *int `json:"overallLevel"`
	Aggression            int  `json:"aggression"`
	Bullying              int  `json:"bullying"`
	Disability            int  `json:"disability"`
	Misogyny              int  `json:"misogyny"`
	RaceEthnicityReligion int  `json:"raceEthnicityOrReligion"`
	SexBasedTerms         int  `json:"sexBasedTerms"`
	SexualitySexGender    int  `json:"sexualitySexOrGender"`
	Swearing              int  `json:"swearing"`
}

// RoleHolder is a moderator or VIP.
type RoleHolder struct {
	UserID string `json:"userId"`
	Login  string `json:"login"`
	Name   string `json:"name"`
}

// BannedUser is a ban or an unexpired timeout.
type BannedUser struct {
	UserID    string `json:"userId"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// UnbanRequest is a banned viewer's appeal.
type UnbanRequest struct {
	ID             string `json:"id"`
	UserID         string `json:"userId"`
	Login          string `json:"login"`
	Name           string `json:"name"`
	Text           string `json:"text"`
	Status         string `json:"status"`
	CreatedAt      string `json:"createdAt,omitempty"`
	ResolvedAt     string `json:"resolvedAt,omitempty"`
	ResolutionText string `json:"resolutionText,omitempty"`
}

// Twitch's published limits, enforced here so the operator is told in words
// rather than by a 400 that names a field.
const (
	twitchMaxAnnouncement  = 500
	twitchMaxWarning       = 500
	twitchMinTerm          = 2
	twitchMaxTerm          = 500
	twitchMinPin           = 30
	twitchMaxPin           = 1800
	twitchMaxResolution    = 500
	twitchShoutoutGap      = 2 * time.Minute
	twitchShoutoutSameGap  = 60 * time.Minute
	twitchChatterPageLimit = 5
	twitchPageCap          = 10
)

var announceColors = map[string]bool{"primary": true, "blue": true, "green": true, "orange": true, "purple": true}

// toolState is the adapter's in-memory shoutout ledger. Its zero value works.
type toolState struct {
	mu       sync.Mutex
	lastAny  time.Time
	lastWhom map[string]time.Time
	now      func() time.Time
}

func (s *toolState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ------------------------------------------------------------------ plumbing

func (t *TwitchAdapter) helix(ctx context.Context, method, path string, q url.Values, body, out any) error {
	endpoint := t.helixBase() + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	return doJSONHeaders(ctx, t.cfg.HTTP, method, endpoint, t.cfg.HelixToken,
		map[string]string{twitchClientIDHeader: t.cfg.ClientID}, body, out)
}

// modQuery is the pair of ids most moderation endpoints want.
func (t *TwitchAdapter) modQuery() url.Values {
	return url.Values{"broadcaster_id": {t.cfg.BroadcasterID}, "moderator_id": {t.cfg.ModeratorID}}
}

// broadcasterOnly refuses a call Twitch only answers for the channel owner.
// The role endpoints and the banned list require broadcaster_id to equal the
// token's user; refusing here beats a 401 that reads like a missing scope.
func (t *TwitchAdapter) broadcasterOnly(what string) error {
	if err := t.helixReady(); err != nil {
		return err
	}
	if t.cfg.BroadcasterID != t.cfg.ModeratorID {
		return fmt.Errorf("Twitch only lets the channel's owner %s. The connected account is a "+
			"moderator of %s, not its broadcaster", what, firstNonEmpty(t.cfg.Channel, "this channel"))
	}
	return nil
}

// toolError turns a Helix refusal into the thing to do about it. hints maps a
// status to a call-specific sentence; the defaults cover the three statuses
// every call shares. Twitch's own message is always appended.
func (t *TwitchAdapter) toolError(err error, verb string, hints map[int]string) error {
	if err == nil {
		return nil
	}
	st := statusOf(err)
	if h, ok := hints[st]; ok {
		return fmt.Errorf("Twitch would not %s. %s: %w", verb, h, err)
	}
	switch st {
	case http.StatusUnauthorized:
		return fmt.Errorf("Twitch refused to %s. This account was connected before that permission "+
			"was requested — disconnect and reconnect it in Settings → Platforms: %w", verb, err)
	case http.StatusForbidden:
		return fmt.Errorf("Twitch says this account does not moderate %s, so it may not %s: %w",
			firstNonEmpty(t.cfg.Channel, "that channel"), verb, err)
	case http.StatusTooManyRequests:
		return fmt.Errorf("Twitch is rate-limiting this action. Wait a little and try again: %w", err)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, 425:
		return fmt.Errorf("Twitch would not %s: %w", verb, err)
	}
	return err
}

// resolveUser returns a numeric id for a UserRef, looking a login up if needed.
func (t *TwitchAdapter) resolveUser(ctx context.Context, u UserRef) (string, error) {
	if id := strings.TrimSpace(u.ID); id != "" {
		return id, nil
	}
	login := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(u.Login), "@"))
	if login == "" {
		return "", fmt.Errorf("name a user, by login or by id")
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := t.helix(ctx, http.MethodGet, "/users", url.Values{"login": {login}}, nil, &out); err != nil {
		return "", t.toolError(err, "look up "+login, nil)
	}
	if len(out.Data) == 0 || out.Data[0].ID == "" {
		return "", fmt.Errorf("Twitch has no user named %q", login)
	}
	return out.Data[0].ID, nil
}

// pageAll follows a Helix cursor up to twitchPageCap pages. more reports that
// the cap stopped it.
func (t *TwitchAdapter) pageAll(ctx context.Context, path string, q url.Values, cap int,
	each func(raw []byte) (n int, cursor string, err error)) (more bool, err error) {
	after := ""
	for i := 0; i < cap; i++ {
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		if after != "" {
			qq.Set("after", after)
		}
		var raw rawJSON
		if err := t.helix(ctx, http.MethodGet, path, qq, nil, &raw); err != nil {
			return false, err
		}
		if len(raw) == 0 {
			return false, nil
		}
		_, next, err := each(raw)
		if err != nil {
			return false, err
		}
		if next == "" {
			return false, nil
		}
		after = next
	}
	return true, nil
}

// rawJSON captures a response body so pageAll's callback can decode its own
// shape.
type rawJSON []byte

func (r *rawJSON) UnmarshalJSON(b []byte) error { *r = append((*r)[:0], b...); return nil }

// ------------------------------------------------------------- announcements

// Announce posts a highlighted announcement.
// POST /helix/chat/announcements, scope moderator:manage:announcements.
func (t *TwitchAdapter) Announce(ctx context.Context, message, color string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("nothing to announce")
	}
	if n := len([]rune(message)); n > twitchMaxAnnouncement {
		return fmt.Errorf("Twitch truncates announcements at %d characters and this one is %d", twitchMaxAnnouncement, n)
	}
	color = strings.ToLower(strings.TrimSpace(color))
	if color == "" {
		color = "primary"
	}
	if !announceColors[color] {
		return fmt.Errorf("announcement colour %q is not one of primary, blue, green, orange, purple", color)
	}
	if err := t.helixReady(); err != nil {
		return err
	}
	err := t.helix(ctx, http.MethodPost, "/chat/announcements", t.modQuery(),
		map[string]string{"message": message, "color": color}, nil)
	return t.toolError(err, "send that announcement", map[int]string{
		http.StatusTooManyRequests: "Twitch limits how many announcements one moderator may send to a channel in a window; wait and try again",
	})
}

// ---------------------------------------------------------------- shoutouts

// Shoutout sends a Shoutout to another broadcaster.
// POST /helix/chat/shoutouts, scope moderator:manage:shoutouts.
func (t *TwitchAdapter) Shoutout(ctx context.Context, u UserRef) (ShoutoutResult, error) {
	if err := t.helixReady(); err != nil {
		return ShoutoutResult{}, err
	}
	target, err := t.resolveUser(ctx, u)
	if err != nil {
		return ShoutoutResult{}, err
	}
	if target == t.cfg.BroadcasterID {
		return ShoutoutResult{}, fmt.Errorf("a broadcaster cannot give themselves a shoutout")
	}
	// The local count only ever refuses what Twitch would refuse anyway.
	if wait := t.tools.shoutoutWait(target); wait > 0 {
		return t.ShoutoutCooldowns(), fmt.Errorf("shoutouts are rate-limited: wait %s before shouting out %s",
			wait.Round(time.Second), firstNonEmpty(u.Login, target))
	}
	q := url.Values{
		"from_broadcaster_id": {t.cfg.BroadcasterID},
		"to_broadcaster_id":   {target},
		"moderator_id":        {t.cfg.ModeratorID},
	}
	if err := t.helix(ctx, http.MethodPost, "/chat/shoutouts", q, nil, nil); err != nil {
		return t.ShoutoutCooldowns(), t.toolError(err, "send that shoutout", map[int]string{
			http.StatusBadRequest:      "The channel has to be live with at least one viewer, and you cannot shout out yourself",
			http.StatusForbidden:       "This account has to moderate the channel, and Twitch may refuse that particular target",
			http.StatusTooManyRequests: "Twitch allows one shoutout every 2 minutes and one to the same channel every 60 minutes; shoutouts sent from Twitch's own site count too and are not visible here",
		})
	}
	t.tools.recordShoutout(target)
	return t.ShoutoutCooldowns(), nil
}

// ShoutoutCooldowns reports the windows polyemesis has counted.
func (t *TwitchAdapter) ShoutoutCooldowns() ShoutoutResult {
	s := &t.tools
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	out := ShoutoutResult{Targets: map[string]time.Time{}}
	if end := s.lastAny.Add(twitchShoutoutGap); end.After(now) {
		out.NextAnyAt = &end
	}
	for id, at := range s.lastWhom {
		if end := at.Add(twitchShoutoutSameGap); end.After(now) {
			out.Targets[id] = end
		}
	}
	return out
}

func (s *toolState) shoutoutWait(target string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	var wait time.Duration
	if end := s.lastAny.Add(twitchShoutoutGap); end.After(now) {
		wait = end.Sub(now)
	}
	if at, ok := s.lastWhom[target]; ok {
		if end := at.Add(twitchShoutoutSameGap); end.After(now) && end.Sub(now) > wait {
			wait = end.Sub(now)
		}
	}
	return wait
}

func (s *toolState) recordShoutout(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	s.lastAny = now
	if s.lastWhom == nil {
		s.lastWhom = map[string]time.Time{}
	}
	s.lastWhom[target] = now
}

// -------------------------------------------------------------------- pins

func pinDuration(sec int) error {
	if sec != 0 && (sec < twitchMinPin || sec > twitchMaxPin) {
		return fmt.Errorf("a pin lasts %d to %d seconds, or until the stream ends (0)", twitchMinPin, twitchMaxPin)
	}
	return nil
}

func (t *TwitchAdapter) pinQuery(messageID string, sec int) (url.Values, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil, fmt.Errorf("no message id to pin")
	}
	if err := pinDuration(sec); err != nil {
		return nil, err
	}
	if err := t.helixReady(); err != nil {
		return nil, err
	}
	q := t.modQuery()
	q.Set("message_id", messageID)
	if sec > 0 {
		q.Set("duration_seconds", fmt.Sprint(sec))
	}
	return q, nil
}

var pinHints = map[int]string{
	http.StatusNotFound: "It no longer has that message, or nothing is pinned",
	http.StatusConflict: "That message is already pinned",
}

// PinMessage pins a chat message. PUT /helix/chat/pins, scope
// moderator:manage:chat_messages, which this account already holds.
func (t *TwitchAdapter) PinMessage(ctx context.Context, messageID string, sec int) error {
	q, err := t.pinQuery(messageID, sec)
	if err != nil {
		return err
	}
	return t.toolError(t.helix(ctx, http.MethodPut, "/chat/pins", q, nil, nil), "pin that message", pinHints)
}

// UpdatePin changes how long the pinned message stays. PATCH /helix/chat/pins.
func (t *TwitchAdapter) UpdatePin(ctx context.Context, messageID string, sec int) error {
	q, err := t.pinQuery(messageID, sec)
	if err != nil {
		return err
	}
	return t.toolError(t.helix(ctx, http.MethodPatch, "/chat/pins", q, nil, nil), "change that pin", pinHints)
}

// Unpin removes a pin. DELETE /helix/chat/pins.
func (t *TwitchAdapter) Unpin(ctx context.Context, messageID string) error {
	q, err := t.pinQuery(messageID, 0)
	if err != nil {
		return err
	}
	return t.toolError(t.helix(ctx, http.MethodDelete, "/chat/pins", q, nil, nil), "unpin that message", pinHints)
}

// GetPin reads the pinned message, nil if none. GET /helix/chat/pins.
func (t *TwitchAdapter) GetPin(ctx context.Context) (*PinnedMessage, error) {
	if err := t.helixReady(); err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			MessageID    string `json:"message_id"`
			SenderUserID string `json:"sender_user_id"`
			SenderLogin  string `json:"sender_user_login"`
			SenderName   string `json:"sender_user_name"`
			PinnedByName string `json:"pinned_by_user_name"`
			Message      struct {
				Text string `json:"text"`
			} `json:"message"`
		} `json:"data"`
	}
	if err := t.helix(ctx, http.MethodGet, "/chat/pins", t.modQuery(), nil, &out); err != nil {
		return nil, t.toolError(err, "read the pinned message", nil)
	}
	if len(out.Data) == 0 {
		return nil, nil
	}
	d := out.Data[0]
	return &PinnedMessage{MessageID: d.MessageID, SenderID: d.SenderUserID, SenderLogin: d.SenderLogin,
		SenderName: d.SenderName, PinnedByName: d.PinnedByName, Text: d.Message.Text}, nil
}

// ------------------------------------------------------------- Shield Mode

type shieldWire struct {
	Data []struct {
		IsActive        bool   `json:"is_active"`
		ModeratorLogin  string `json:"moderator_login"`
		LastActivatedAt string `json:"last_activated_at"`
	} `json:"data"`
}

func (w shieldWire) state() ShieldState {
	if len(w.Data) == 0 {
		return ShieldState{}
	}
	d := w.Data[0]
	return ShieldState{Active: d.IsActive, ModeratorLogin: d.ModeratorLogin, LastActivatedAt: d.LastActivatedAt}
}

// GetShield reads Shield Mode. GET /helix/moderation/shield_mode, scope
// moderator:manage:shield_mode (moderator:read:shield_mode also works).
func (t *TwitchAdapter) GetShield(ctx context.Context) (ShieldState, error) {
	if err := t.helixReady(); err != nil {
		return ShieldState{}, err
	}
	var out shieldWire
	if err := t.helix(ctx, http.MethodGet, "/moderation/shield_mode", t.modQuery(), nil, &out); err != nil {
		return ShieldState{}, t.toolError(err, "read Shield Mode", nil)
	}
	return out.state(), nil
}

// SetShield switches Shield Mode. PUT /helix/moderation/shield_mode.
func (t *TwitchAdapter) SetShield(ctx context.Context, active bool) (ShieldState, error) {
	if err := t.helixReady(); err != nil {
		return ShieldState{}, err
	}
	var out shieldWire
	err := t.helix(ctx, http.MethodPut, "/moderation/shield_mode", t.modQuery(),
		map[string]bool{"is_active": active}, &out)
	if err != nil {
		return ShieldState{}, t.toolError(err, "change Shield Mode", nil)
	}
	st := out.state()
	// The PUT echoes the new state; fall back to what was asked if it did not.
	if len(out.Data) == 0 {
		st.Active = active
	}
	return st, nil
}

// ---------------------------------------------------------------- warnings

// Warn warns a chat user; they must acknowledge before they can chat again.
// POST /helix/moderation/warnings, scope moderator:manage:warnings.
func (t *TwitchAdapter) Warn(ctx context.Context, u UserRef, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("a warning needs a reason; Twitch shows it to the viewer")
	}
	if n := len([]rune(reason)); n > twitchMaxWarning {
		return fmt.Errorf("a warning reason is at most %d characters and this one is %d", twitchMaxWarning, n)
	}
	if err := t.helixReady(); err != nil {
		return err
	}
	id, err := t.resolveUser(ctx, u)
	if err != nil {
		return err
	}
	body := map[string]any{"data": map[string]string{"user_id": id, "reason": reason}}
	err = t.helix(ctx, http.MethodPost, "/moderation/warnings", t.modQuery(), body, nil)
	return t.toolError(err, "warn that user", map[int]string{
		http.StatusConflict: "Someone else is changing that user's warning right now, or they are acknowledging an earlier one; try again in a moment",
	})
}

// ----------------------------------------------------------- blocked terms

// ListBlockedTerms reads the channel's blocked terms. Private terms (the ones
// AutoMod keeps to itself) are not returned by Twitch.
// GET /helix/moderation/blocked_terms, scope moderator:manage:blocked_terms.
func (t *TwitchAdapter) ListBlockedTerms(ctx context.Context) ([]BlockedTerm, error) {
	if err := t.helixReady(); err != nil {
		return nil, err
	}
	q := t.modQuery()
	q.Set("first", "100")
	var out []BlockedTerm
	_, err := t.pageAll(ctx, "/moderation/blocked_terms", q, twitchPageCap, func(raw []byte) (int, string, error) {
		var page struct {
			Data []struct {
				ID        string `json:"id"`
				Text      string `json:"text"`
				CreatedAt string `json:"created_at"`
			} `json:"data"`
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
		}
		if err := jsonUnmarshal(raw, &page); err != nil {
			return 0, "", err
		}
		for _, d := range page.Data {
			out = append(out, BlockedTerm{ID: d.ID, Text: d.Text, CreatedAt: d.CreatedAt})
		}
		return len(page.Data), page.Pagination.Cursor, nil
	})
	if err != nil {
		return nil, t.toolError(err, "list blocked terms", nil)
	}
	return out, nil
}

// termProblem says why a term cannot be sent, or "".
func termProblem(term string) string {
	n := len([]rune(term))
	switch {
	case n < twitchMinTerm:
		return fmt.Sprintf("shorter than Twitch's %d-character minimum", twitchMinTerm)
	case n > twitchMaxTerm:
		return fmt.Sprintf("longer than Twitch's %d-character maximum", twitchMaxTerm)
	}
	return ""
}

// AddBlockedTerm blocks a word or phrase. Twitch returns the existing term if
// it is already there. A leading or trailing * is Twitch's wildcard.
// POST /helix/moderation/blocked_terms.
func (t *TwitchAdapter) AddBlockedTerm(ctx context.Context, text string) (BlockedTerm, error) {
	text = strings.TrimSpace(text)
	if p := termProblem(text); p != "" {
		return BlockedTerm{}, fmt.Errorf("%q is %s", text, p)
	}
	if err := t.helixReady(); err != nil {
		return BlockedTerm{}, err
	}
	var out struct {
		Data []struct {
			ID        string `json:"id"`
			Text      string `json:"text"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	err := t.helix(ctx, http.MethodPost, "/moderation/blocked_terms", t.modQuery(),
		map[string]string{"text": text}, &out)
	if err != nil {
		return BlockedTerm{}, t.toolError(err, "block that term", nil)
	}
	if len(out.Data) == 0 {
		return BlockedTerm{Text: text}, nil
	}
	d := out.Data[0]
	return BlockedTerm{ID: d.ID, Text: d.Text, CreatedAt: d.CreatedAt}, nil
}

// DeleteBlockedTerm removes a term by id. Twitch answers 204 even for an id it
// does not have. DELETE /helix/moderation/blocked_terms.
func (t *TwitchAdapter) DeleteBlockedTerm(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("no blocked term id to remove")
	}
	if err := t.helixReady(); err != nil {
		return err
	}
	q := t.modQuery()
	q.Set("id", id)
	return t.toolError(t.helix(ctx, http.MethodDelete, "/moderation/blocked_terms", q, nil, nil), "remove that term", nil)
}

// SyncBlockedTerms pushes terms to Twitch, ADDING ONLY.
//
// It never removes a Twitch term: the operator, a moderator or AutoMod may have
// added it, and polyemesis cannot tell which. It reads the current list first
// so a term already there is reported as present rather than re-sent. Private
// terms are invisible to that read, which is harmless -- Twitch answers a
// duplicate add with the existing term.
func (t *TwitchAdapter) SyncBlockedTerms(ctx context.Context, terms []string) (BlockedTermSync, error) {
	res := BlockedTermSync{Added: []string{}, Present: []string{}, Skipped: []SkippedTerm{}}
	existing, err := t.ListBlockedTerms(ctx)
	if err != nil {
		return res, err
	}
	have := make(map[string]bool, len(existing))
	for _, e := range existing {
		have[strings.ToLower(e.Text)] = true
	}
	seen := map[string]bool{}
	for _, raw := range terms {
		term := strings.TrimSpace(raw)
		key := strings.ToLower(term)
		switch {
		case seen[key]:
			continue
		case strings.Contains(term, "*"):
			// A * is Twitch's wildcard, valid only at an end of a word. A plain
			// term from polyemesis that contains one is not that.
			res.Skipped = append(res.Skipped, SkippedTerm{term, "contains *, which Twitch reads as a wildcard"})
		case termProblem(term) != "":
			res.Skipped = append(res.Skipped, SkippedTerm{term, termProblem(term)})
		case have[key]:
			res.Present = append(res.Present, term)
		default:
			if _, err := t.AddBlockedTerm(ctx, term); err != nil {
				return res, fmt.Errorf("stopped after adding %d of %d: %w", len(res.Added), len(terms), err)
			}
			res.Added = append(res.Added, term)
		}
		seen[key] = true
	}
	return res, nil
}

// ---------------------------------------------------------------- chatters

// Chatters reads who is connected to chat. GET /helix/chat/chatters, scope
// moderator:read:chatters. The count is Twitch's own total and includes
// viewers who never type.
//
// A SEPARATE CALL, NOT PART OF STATS: an account still on the old scope version
// gets a 401 here, and that must never take its viewer count down with it.
func (t *TwitchAdapter) Chatters(ctx context.Context, withList bool) (ChatterList, error) {
	if err := t.helixReady(); err != nil {
		return ChatterList{}, err
	}
	q := t.modQuery()
	q.Set("first", "1")
	if withList {
		q.Set("first", "1000")
	}
	var res ChatterList
	pages := 1
	if withList {
		pages = twitchChatterPageLimit
	}
	more, err := t.pageAll(ctx, "/chat/chatters", q, pages, func(raw []byte) (int, string, error) {
		var page struct {
			Data []struct {
				UserID string `json:"user_id"`
				Login  string `json:"user_login"`
				Name   string `json:"user_name"`
			} `json:"data"`
			Total      int `json:"total"`
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
		}
		if err := jsonUnmarshal(raw, &page); err != nil {
			return 0, "", err
		}
		res.Total = page.Total
		if withList {
			for _, d := range page.Data {
				res.Chatters = append(res.Chatters, Chatter{d.UserID, d.Login, d.Name})
			}
		}
		if !withList {
			return len(page.Data), "", nil
		}
		return len(page.Data), page.Pagination.Cursor, nil
	})
	if err != nil {
		return ChatterList{}, t.toolError(err, "list the chatters", nil)
	}
	res.Truncated = withList && more
	return res, nil
}

// ------------------------------------------------------------------ AutoMod

type autoModWire struct {
	OverallLevel *int `json:"overall_level,omitempty"`
	Aggression   *int `json:"aggression,omitempty"`
	Bullying     *int `json:"bullying,omitempty"`
	Disability   *int `json:"disability,omitempty"`
	Misogyny     *int `json:"misogyny,omitempty"`
	Race         *int `json:"race_ethnicity_or_religion,omitempty"`
	SexBased     *int `json:"sex_based_terms,omitempty"`
	Sexuality    *int `json:"sexuality_sex_or_gender,omitempty"`
	Swearing     *int `json:"swearing,omitempty"`
}

func autoModLevel(n int) error {
	if n < 0 || n > 4 {
		return fmt.Errorf("an AutoMod level is 0 (off) to 4 (strictest), not %d", n)
	}
	return nil
}

// Validate checks the levels. OverallLevel set means "this one level, nothing
// else sent"; unset means all seven categories go.
func (s AutoModSettings) Validate() error {
	if s.OverallLevel != nil {
		return autoModLevel(*s.OverallLevel)
	}
	for _, n := range s.categories() {
		if err := autoModLevel(n); err != nil {
			return err
		}
	}
	return nil
}

func (s AutoModSettings) categories() []int {
	return []int{s.Aggression, s.Bullying, s.Disability, s.Misogyny, s.RaceEthnicityReligion,
		s.SexBasedTerms, s.SexualitySexGender, s.Swearing}
}

// wire builds the PUT body: the overall level alone, or every category. Never
// both, which Twitch refuses, and never a subset, which Twitch zeroes.
func (s AutoModSettings) wire() autoModWire {
	if s.OverallLevel != nil {
		return autoModWire{OverallLevel: s.OverallLevel}
	}
	p := func(n int) *int { return &n }
	return autoModWire{
		Aggression: p(s.Aggression), Bullying: p(s.Bullying), Disability: p(s.Disability),
		Misogyny: p(s.Misogyny), Race: p(s.RaceEthnicityReligion), SexBased: p(s.SexBasedTerms),
		Sexuality: p(s.SexualitySexGender), Swearing: p(s.Swearing),
	}
}

func (w autoModWire) settings() AutoModSettings {
	v := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	return AutoModSettings{OverallLevel: w.OverallLevel, Aggression: v(w.Aggression), Bullying: v(w.Bullying),
		Disability: v(w.Disability), Misogyny: v(w.Misogyny), RaceEthnicityReligion: v(w.Race),
		SexBasedTerms: v(w.SexBased), SexualitySexGender: v(w.Sexuality), Swearing: v(w.Swearing)}
}

// GetAutoMod reads Twitch's AutoMod levels.
// GET /helix/moderation/automod/settings, scope moderator:manage:automod_settings
// (moderator:read:automod_settings also works).
func (t *TwitchAdapter) GetAutoMod(ctx context.Context) (AutoModSettings, error) {
	if err := t.helixReady(); err != nil {
		return AutoModSettings{}, err
	}
	var out struct {
		Data []autoModWire `json:"data"`
	}
	if err := t.helix(ctx, http.MethodGet, "/moderation/automod/settings", t.modQuery(), nil, &out); err != nil {
		return AutoModSettings{}, t.toolError(err, "read the AutoMod settings", nil)
	}
	if len(out.Data) == 0 {
		return AutoModSettings{}, fmt.Errorf("Twitch returned no AutoMod settings for this channel")
	}
	return out.Data[0].settings(), nil
}

// SetAutoMod replaces Twitch's AutoMod levels.
// PUT /helix/moderation/automod/settings, scope moderator:manage:automod_settings.
func (t *TwitchAdapter) SetAutoMod(ctx context.Context, s AutoModSettings) (AutoModSettings, error) {
	if err := s.Validate(); err != nil {
		return AutoModSettings{}, err
	}
	if err := t.helixReady(); err != nil {
		return AutoModSettings{}, err
	}
	var out struct {
		Data []autoModWire `json:"data"`
	}
	if err := t.helix(ctx, http.MethodPut, "/moderation/automod/settings", t.modQuery(), s.wire(), &out); err != nil {
		return AutoModSettings{}, t.toolError(err, "change the AutoMod settings", nil)
	}
	if len(out.Data) == 0 {
		return s, nil
	}
	return out.Data[0].settings(), nil
}

// ResolveHeldMessage approves or denies a message AutoMod is holding.
// POST /helix/moderation/automod/message, scope moderator:manage:automod.
//
// THE RECEIVING HALF IS NOT BUILT. Knowing that Twitch is holding a message
// takes EventSub (automod.message.hold), which this build does not subscribe
// to; the call is here so the EventSub wave only has to supply the id.
func (t *TwitchAdapter) ResolveHeldMessage(ctx context.Context, messageID string, allow bool) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return fmt.Errorf("no held message id")
	}
	if err := t.helixReady(); err != nil {
		return err
	}
	action := "DENY"
	if allow {
		action = "ALLOW"
	}
	err := t.helix(ctx, http.MethodPost, "/moderation/automod/message", nil,
		map[string]string{"user_id": t.cfg.ModeratorID, "msg_id": messageID, "action": action}, nil)
	return t.toolError(err, strings.ToLower(action)+" that held message", map[int]string{
		http.StatusNotFound: "It no longer holds that message; a held message expires, or another moderator already decided it",
	})
}

// ------------------------------------------------------------------- roles

func roleSpec(role string) (listPath, scopeNote string, err error) {
	switch role {
	case "moderator":
		return "/moderation/moderators", "channel:manage:moderators", nil
	case "vip":
		return "/channels/vips", "channel:manage:vips", nil
	}
	return "", "", fmt.Errorf("role must be moderator or vip, not %q", role)
}

var roleHints = map[int]string{
	http.StatusUnprocessableEntity: "A person cannot be a moderator and a VIP at once — remove the other role first, or they already hold this one",
	http.StatusConflict:            "The channel has no free VIP slot",
	425:                            "The channel has not finished Twitch's Build a Community requirement, which Twitch requires before VIPs can be assigned",
	http.StatusBadRequest:          "The user may be banned from the channel, or already hold the role",
}

// ListRole lists moderators or VIPs. GET /helix/moderation/moderators or
// GET /helix/channels/vips.
func (t *TwitchAdapter) ListRole(ctx context.Context, role string) ([]RoleHolder, error) {
	path, _, err := roleSpec(role)
	if err != nil {
		return nil, err
	}
	if err := t.broadcasterOnly("list its " + role + "s"); err != nil {
		return nil, err
	}
	q := url.Values{"broadcaster_id": {t.cfg.BroadcasterID}, "first": {"100"}}
	var out []RoleHolder
	_, err = t.pageAll(ctx, path, q, twitchPageCap, func(raw []byte) (int, string, error) {
		var page struct {
			Data []struct {
				UserID string `json:"user_id"`
				Login  string `json:"user_login"`
				Name   string `json:"user_name"`
			} `json:"data"`
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
		}
		if err := jsonUnmarshal(raw, &page); err != nil {
			return 0, "", err
		}
		for _, d := range page.Data {
			out = append(out, RoleHolder{d.UserID, d.Login, d.Name})
		}
		return len(page.Data), page.Pagination.Cursor, nil
	})
	if err != nil {
		return nil, t.toolError(err, "list the "+role+"s", nil)
	}
	return out, nil
}

func (t *TwitchAdapter) roleCall(ctx context.Context, method, role string, u UserRef, verb string) error {
	path, _, err := roleSpec(role)
	if err != nil {
		return err
	}
	if err := t.broadcasterOnly(verb + " a " + role); err != nil {
		return err
	}
	id, err := t.resolveUser(ctx, u)
	if err != nil {
		return err
	}
	q := url.Values{"broadcaster_id": {t.cfg.BroadcasterID}, "user_id": {id}}
	return t.toolError(t.helix(ctx, method, path, q, nil, nil), verb+" that "+role, roleHints)
}

// AddRole makes a user a moderator or VIP. POST /helix/moderation/moderators,
// scope channel:manage:moderators; POST /helix/channels/vips, scope
// channel:manage:vips.
func (t *TwitchAdapter) AddRole(ctx context.Context, role string, u UserRef) error {
	return t.roleCall(ctx, http.MethodPost, role, u, "add")
}

// RemoveRole removes a moderator or VIP. DELETE on the same two paths.
func (t *TwitchAdapter) RemoveRole(ctx context.Context, role string, u UserRef) error {
	return t.roleCall(ctx, http.MethodDelete, role, u, "remove")
}

// ------------------------------------------------- banned and unban requests

// ListBanned lists bans and unexpired timeouts. GET /helix/moderation/banned,
// scope moderator:manage:banned_users (moderation:read also works). Twitch
// answers it only for the channel owner.
func (t *TwitchAdapter) ListBanned(ctx context.Context) ([]BannedUser, error) {
	if err := t.broadcasterOnly("list its banned users"); err != nil {
		return nil, err
	}
	q := url.Values{"broadcaster_id": {t.cfg.BroadcasterID}, "first": {"100"}}
	var out []BannedUser
	_, err := t.pageAll(ctx, "/moderation/banned", q, twitchPageCap, func(raw []byte) (int, string, error) {
		var page struct {
			Data []struct {
				UserID    string `json:"user_id"`
				Login     string `json:"user_login"`
				Name      string `json:"user_name"`
				Reason    string `json:"reason"`
				CreatedAt string `json:"created_at"`
				ExpiresAt string `json:"expires_at"`
			} `json:"data"`
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
		}
		if err := jsonUnmarshal(raw, &page); err != nil {
			return 0, "", err
		}
		for _, d := range page.Data {
			out = append(out, BannedUser{d.UserID, d.Login, d.Name, d.Reason, d.CreatedAt, d.ExpiresAt})
		}
		return len(page.Data), page.Pagination.Cursor, nil
	})
	if err != nil {
		return nil, t.toolError(err, "list the banned users", nil)
	}
	return out, nil
}

type unbanWire struct {
	ID             string `json:"id"`
	UserID         string `json:"user_id"`
	Login          string `json:"user_login"`
	Name           string `json:"user_name"`
	Text           string `json:"text"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	ResolvedAt     string `json:"resolved_at"`
	ResolutionText string `json:"resolution_text"`
}

func (w unbanWire) req() UnbanRequest {
	return UnbanRequest{w.ID, w.UserID, w.Login, w.Name, w.Text, w.Status, w.CreatedAt, w.ResolvedAt, w.ResolutionText}
}

var unbanHints = map[int]string{
	http.StatusBadRequest: "The channel may not be receiving unban requests (it is a channel setting), or the status is not valid",
	http.StatusNotFound:   "It has no such unban request",
}

// ListUnbanRequests lists appeals with a status (pending, approved, denied,
// acknowledged, canceled). GET /helix/moderation/unban_requests, scope
// moderator:manage:unban_requests.
func (t *TwitchAdapter) ListUnbanRequests(ctx context.Context, status string) ([]UnbanRequest, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = "pending"
	}
	switch status {
	case "pending", "approved", "denied", "acknowledged", "canceled":
	default:
		return nil, fmt.Errorf("unban request status %q is not pending, approved, denied, acknowledged or canceled", status)
	}
	if err := t.helixReady(); err != nil {
		return nil, err
	}
	q := t.modQuery()
	q.Set("status", status)
	q.Set("first", "100")
	var out []UnbanRequest
	_, err := t.pageAll(ctx, "/moderation/unban_requests", q, twitchPageCap, func(raw []byte) (int, string, error) {
		var page struct {
			Data       []unbanWire `json:"data"`
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
		}
		if err := jsonUnmarshal(raw, &page); err != nil {
			return 0, "", err
		}
		for _, d := range page.Data {
			out = append(out, d.req())
		}
		return len(page.Data), page.Pagination.Cursor, nil
	})
	if err != nil {
		return nil, t.toolError(err, "list the unban requests", unbanHints)
	}
	return out, nil
}

// ResolveUnbanRequest approves or denies an appeal.
// PATCH /helix/moderation/unban_requests.
func (t *TwitchAdapter) ResolveUnbanRequest(ctx context.Context, id, status, text string) (UnbanRequest, error) {
	id = strings.TrimSpace(id)
	status = strings.ToLower(strings.TrimSpace(status))
	if id == "" {
		return UnbanRequest{}, fmt.Errorf("no unban request id")
	}
	if status != "approved" && status != "denied" {
		return UnbanRequest{}, fmt.Errorf("an unban request is resolved as approved or denied, not %q", status)
	}
	if n := len([]rune(text)); n > twitchMaxResolution {
		return UnbanRequest{}, fmt.Errorf("a resolution message is at most %d characters and this one is %d", twitchMaxResolution, n)
	}
	if err := t.helixReady(); err != nil {
		return UnbanRequest{}, err
	}
	q := t.modQuery()
	q.Set("unban_request_id", id)
	q.Set("status", status)
	if text = strings.TrimSpace(text); text != "" {
		q.Set("resolution_text", text)
	}
	var out struct {
		Data []unbanWire `json:"data"`
	}
	if err := t.helix(ctx, http.MethodPatch, "/moderation/unban_requests", q, nil, &out); err != nil {
		return UnbanRequest{}, t.toolError(err, status[:len(status)-1]+" that unban request", unbanHints)
	}
	if len(out.Data) == 0 {
		return UnbanRequest{ID: id, Status: status}, nil
	}
	return out.Data[0].req(), nil
}

// ------------------------------------------------------------- suspicious

// SetSuspicious flags a chatter ACTIVE_MONITORING (their messages are shown to
// moderators) or RESTRICTED (they cannot be seen by viewers). Helix publishes
// no LIST for these flags, so polyemesis can set and clear but not enumerate.
// POST /helix/moderation/suspicious_users, scope moderator:manage:suspicious_users.
func (t *TwitchAdapter) SetSuspicious(ctx context.Context, u UserRef, status string) error {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status != "ACTIVE_MONITORING" && status != "RESTRICTED" {
		return fmt.Errorf("a suspicious-user status is ACTIVE_MONITORING or RESTRICTED, not %q", status)
	}
	if err := t.helixReady(); err != nil {
		return err
	}
	id, err := t.resolveUser(ctx, u)
	if err != nil {
		return err
	}
	err = t.helix(ctx, http.MethodPost, "/moderation/suspicious_users", t.modQuery(),
		map[string]string{"user_id": id, "status": status}, nil)
	return t.toolError(err, "flag that user", map[int]string{
		http.StatusBadRequest: "The status change is not allowed for this user",
	})
}

// ClearSuspicious removes the flag. DELETE /helix/moderation/suspicious_users.
func (t *TwitchAdapter) ClearSuspicious(ctx context.Context, u UserRef) error {
	if err := t.helixReady(); err != nil {
		return err
	}
	id, err := t.resolveUser(ctx, u)
	if err != nil {
		return err
	}
	q := t.modQuery()
	q.Set("user_id", id)
	return t.toolError(t.helix(ctx, http.MethodDelete, "/moderation/suspicious_users", q, nil, nil), "clear that flag", nil)
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
