// Package activity is the one shape of "a viewer did something": a follow, a
// sub, a tip, a raid, and the platform's own reports about the stream. It holds
// TYPES ONLY -- no I/O, no goroutines, no policy -- so that every producer (a
// chat adapter, a platform event transport) and every consumer (the activity
// hub, the events stream, hooks, the overlay gate) can import it without
// importing each other.
//
// WHY A NEW NAME. "Alert" already means an operator alert in this codebase
// (internal/alerts: destination.down, disk.low, ...) and owns the `alert` topic,
// the `alert` event type and the Automation -> Alerts tab. Nothing viewer-facing
// is called an alert on any wire. This is ACTIVITY: SSE topic `activity`, SSE
// types `activity` and `activity.display`, bus type `activity`, hook trigger
// `activity`. The overlay a streamer puts on screen may be LABELLED "Alerts
// overlay" in the console; the wire name does not follow the label.
//
// IMPORT RULE. This package imports the standard library and nothing else.
// internal/db imports internal/hooks, and hooks carries an activity on its
// envelope, so importing db here (for db.Platform) would close the loop
// db -> hooks -> activity -> db. Platform is therefore a plain string holding a
// db.Platform's value; callers convert at the boundary.
//
// AMOUNTS ARE NEVER CONVERTED. A Twitch bit is a US cent, but a KICK has no
// published rate and exchange rates move daily: a table in a contract would be
// wrong somewhere. An amount is money in its own currency OR a platform unit,
// and a threshold is per currency or per unit (db.MinAmount).
package activity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Kind names what happened. These strings are on the wire, in stored settings
// and in hooks, so they are never renamed.
type Kind string

const (
	// KindFollow: somebody followed the channel.
	KindFollow Kind = "follow"
	// KindSubscribe: a first subscription or membership. A YouTube membership,
	// a Rumble paid sub and an X Premium subscription are all this, with Product
	// naming the platform's word.
	KindSubscribe Kind = "subscribe"
	// KindResub: a renewal or a milestone (Months says how far in).
	KindResub Kind = "resub"
	// KindGift: one gifter, Count gifts, optionally the Recipients.
	KindGift Kind = "gift"
	// KindTip: money or a platform unit sent with or without a message: Twitch
	// bits, a Super Chat or Sticker, KICKs, a Rumble rant. ONE kind, because an
	// overlay, a threshold and a hook all ask the same question -- who paid, how
	// much, what did they say -- and the platform's own name survives in
	// Source.Type.
	KindTip Kind = "tip"
	// KindRaid: another channel sent its viewers (Direction "in") or this
	// channel sent its own away ("out").
	KindRaid Kind = "raid"
	// KindRedemption: a channel-points (or equivalent) reward was redeemed.
	KindRedemption Kind = "redemption"
	// KindHypeTrain: a hype train began, moved or ended (Phase).
	KindHypeTrain Kind = "hypetrain"
	// KindPoll: a platform-native poll (Phase). Never on the overlay.
	KindPoll Kind = "poll"
	// KindPrediction: a platform-native prediction (Phase). Never on the overlay.
	KindPrediction Kind = "prediction"
	// KindStream: the PLATFORM's report that the stream went online, offline or
	// changed its title. Not broadcast.live, which is polyemesis moving a
	// broadcast on air. Never on the overlay.
	KindStream Kind = "stream"
	// KindShoutout: a shoutout given (Direction "out") or received ("in").
	KindShoutout Kind = "shoutout"
	// KindModeration: a moderation action taken OUTSIDE polyemesis (Action).
	// chat.moderation stays the record of polyemesis's own actions. Never on
	// the overlay.
	KindModeration Kind = "moderation"
	// KindReaction: an aggregate of reactions (Facebook), with no actor. Never
	// on the overlay.
	KindReaction Kind = "reaction"
)

// AllKinds is every Kind declared above, in declaration order. Kept in step
// with the const block by an AST guard (TestAllKindsIsTheWholeConstBlock)
// rather than by care, as events.AllTypes is: a list a human maintains is the
// shape that silently falls behind.
func AllKinds() []Kind {
	return []Kind{
		KindFollow, KindSubscribe, KindResub, KindGift, KindTip, KindRaid,
		KindRedemption, KindHypeTrain, KindPoll, KindPrediction, KindStream,
		KindShoutout, KindModeration, KindReaction,
	}
}

// Valid reports whether k is a kind this build knows.
func (k Kind) Valid() bool { return slices.Contains(AllKinds(), k) }

// OverlayKinds are the kinds an alerts overlay may ever draw. Everything else
// is console, hooks and the events stream only: a moderation action, a stream
// state change, a poll or prediction and a reaction tally are not something to
// put on air, and a raid going OUT is filtered by direction (OverlayVisible).
func OverlayKinds() []Kind {
	return []Kind{
		KindFollow, KindSubscribe, KindResub, KindGift, KindTip, KindRaid,
		KindRedemption, KindHypeTrain, KindShoutout,
	}
}

// ParseKind returns the Kind named s, or false.
func ParseKind(s string) (Kind, bool) {
	k := Kind(s)
	return k, k.Valid()
}

// Phase and Action vocabularies. Closed, because they are on the wire and an
// overlay or a hook switches on them.
var (
	hypePhases       = []string{"begin", "progress", "end"}
	pollPhases       = []string{"begin", "progress", "end"}
	predictionPhases = []string{"begin", "progress", "lock", "end"}
	streamPhases     = []string{"online", "offline", "update"}
	moderationActs   = []string{"ban", "timeout", "unban", "automod_hold"}
	directions       = []string{"in", "out"}
)

// Actor is a person, as the platform names them.
type Actor struct {
	// ID is the platform's id. It is NEVER on the overlay projection.
	ID    string `json:"id,omitempty"`
	Login string `json:"login,omitempty"`
	// Name is what the platform shows. Empty only when Anonymous.
	Name      string `json:"name"`
	Anonymous bool   `json:"anonymous,omitempty"`
}

// Amount is EITHER money (Currency, Minor, Exponent) OR a platform unit (Unit,
// Units); Validate refuses both and refuses neither.
type Amount struct {
	Currency string `json:"currency,omitempty"` // ISO 4217
	Minor    int64  `json:"minor,omitempty"`    // 500 with Exponent 2 = 5.00
	// Exponent is carried so no consumer needs a currency table: JPY is 0, USD
	// 2, BHD 3.
	Exponent int    `json:"exponent,omitempty"`
	Unit     string `json:"unit,omitempty"` // "bits" | "kicks"
	Units    int64  `json:"units,omitempty"`
	// Display is the platform's own string where it sends one (YouTube's
	// amountDisplayString), otherwise built by Normalise.
	Display string `json:"display,omitempty"`
}

// Money reports whether the amount is money rather than a platform unit.
func (a Amount) Money() bool { return a.Currency != "" }

// Recipient is one person a gift went to.
type Recipient struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// Source says where an event came from, and is what dedupes it.
type Source struct {
	// Type is the platform's own name for it: "channel.cheer", "superChatEvent",
	// "kicks.gifted", "rant", or "polyemesis.test".
	Type string `json:"type"`
	// ID is the dedupe id within (platform, account); synthetic where the
	// platform sends none.
	ID string `json:"id"`
}

// TestSourceType is the Source.Type of a test alert.
const TestSourceType = "polyemesis.test"

// ProgressOption is one choice in a poll or prediction.
type ProgressOption struct {
	Label  string `json:"label"`
	Votes  int64  `json:"votes,omitempty"`
	Points int64  `json:"points,omitempty"`
}

// Progress is how far a hype train, poll or prediction has got.
type Progress struct {
	Total   int64            `json:"total,omitempty"`
	Goal    int64            `json:"goal,omitempty"`
	Options []ProgressOption `json:"options,omitempty"`
	EndsAt  *time.Time       `json:"endsAt,omitempty"`
}

// Event is one thing a viewer or the platform did. The wire shape is the
// contract: docs/ACTIVITY.md and docs/EVENTS.md describe it, and the field
// names here are on every surface.
type Event struct {
	// ID is opaque and per-process ("act_..."), minted by Normalise.
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Platform is a db.Platform's value ("twitch", "kick", ...).
	Platform string `json:"platform"`
	// Account is platform_accounts.account_ref, as chat.Message.Account.
	Account string    `json:"account,omitempty"`
	At      time.Time `json:"at"` // the platform's timestamp if it sent one, else receipt
	Actor   *Actor    `json:"actor,omitempty"`
	Amount  *Amount   `json:"amount,omitempty"`
	// Message is VIEWER-WRITTEN. It is treated as chat everywhere: redacted on
	// the way into the events stream, and held back from the overlay until
	// automod has passed it.
	Message string `json:"message,omitempty"`
	// Tier is as the platform sent it: "1000"/"2000"/"3000", a level name.
	Tier string `json:"tier,omitempty"`
	// Product is sub | membership | premium | paid_sub: the platform's word for
	// a subscribe or resub.
	Product    string         `json:"product,omitempty"`
	Months     int            `json:"months,omitempty"`
	Streak     int            `json:"streak,omitempty"`
	Count      int            `json:"count,omitempty"`
	Recipients []Recipient    `json:"recipients,omitempty"`
	Direction  string         `json:"direction,omitempty"` // in | out
	Viewers    int            `json:"viewers,omitempty"`
	Other      *Actor         `json:"other,omitempty"`
	Title      string         `json:"title,omitempty"`
	Status     string         `json:"status,omitempty"`
	Phase      string         `json:"phase,omitempty"`
	Action     string         `json:"action,omitempty"`
	Level      int            `json:"level,omitempty"`
	Progress   *Progress      `json:"progress,omitempty"`
	Breakdown  map[string]int `json:"breakdown,omitempty"`
	Source     Source         `json:"source"`
	// Test marks an event raised by "Send test alert" rather than by anything
	// that happened, so a hook can refuse to act on it.
	Test bool `json:"test,omitempty"`
}

// Field bounds. Viewer-written text is clamped here, once, so no consumer has
// to remember to.
const (
	MaxNameRunes    = 100
	MaxMessageRunes = 500
	MaxTitleRunes   = 200
	MaxShortRunes   = 64
	MaxRecipients   = 25
	MaxOptions      = 10
	MaxBreakdown    = 12
	MaxCount        = 1_000_000
)

// KindState is whether one kind is flowing from one source.
type KindState string

const (
	// KindOn: subscribed or being decoded.
	KindOn KindState = "on"
	// KindOff: switched off in settings.
	KindOff KindState = "off"
	// KindUnavailable: the platform will not send this to this account (not an
	// Affiliate, not monetised). NOT a failure: the source stays live.
	KindUnavailable KindState = "unavailable"
	// KindReconnect: the account's token predates the scope this kind needs.
	KindReconnect KindState = "reconnect"
	// KindUnknown: the kind exists but has not been seen yet, on a platform that
	// gives no way to ask.
	KindUnknown KindState = "unknown"
)

// KindStatus is one kind's state on one source, with the reason in the
// operator's words when it is not on. Detail never carries a token.
type KindStatus struct {
	Kind   Kind      `json:"kind"`
	State  KindState `json:"state"`
	Detail string    `json:"detail,omitempty"`
}

// Valid reports whether the state is one of the five words.
func (s KindState) Valid() bool {
	switch s {
	case KindOn, KindOff, KindUnavailable, KindReconnect, KindUnknown:
		return true
	}
	return false
}

// Normalise trims, clamps and fills what a producer may leave out: the id, the
// time, a built Display for an amount. It returns a copy; the receiver's slices
// and maps are not shared with it.
func (e Event) Normalise(now func() time.Time) Event {
	if now == nil {
		now = time.Now
	}
	e.Kind = Kind(strings.ToLower(strings.TrimSpace(string(e.Kind))))
	e.Platform = strings.ToLower(strings.TrimSpace(e.Platform))
	e.Account = clip(strings.TrimSpace(e.Account), MaxShortRunes*2)
	if e.ID == "" {
		e.ID = newID()
	}
	if e.At.IsZero() {
		e.At = now()
	}
	e.At = e.At.UTC()
	e.Message = clip(strings.TrimSpace(e.Message), MaxMessageRunes)
	e.Tier = clip(strings.TrimSpace(e.Tier), MaxShortRunes)
	e.Product = clip(strings.TrimSpace(e.Product), MaxShortRunes)
	e.Title = clip(strings.TrimSpace(e.Title), MaxTitleRunes)
	e.Status = clip(strings.TrimSpace(e.Status), MaxShortRunes)
	e.Phase = strings.ToLower(strings.TrimSpace(e.Phase))
	e.Action = strings.ToLower(strings.TrimSpace(e.Action))
	e.Direction = strings.ToLower(strings.TrimSpace(e.Direction))
	e.Source.Type = clip(strings.TrimSpace(e.Source.Type), MaxShortRunes)
	e.Source.ID = clip(strings.TrimSpace(e.Source.ID), MaxTitleRunes)
	e.Actor = normaliseActor(e.Actor)
	e.Other = normaliseActor(e.Other)
	e.Months = clampInt(e.Months, 0, 10_000)
	e.Streak = clampInt(e.Streak, 0, 10_000)
	e.Count = clampInt(e.Count, 0, MaxCount)
	e.Viewers = clampInt(e.Viewers, 0, 100_000_000)
	e.Level = clampInt(e.Level, 0, 1000)

	if e.Amount != nil {
		a := *e.Amount
		a.Currency = strings.ToUpper(strings.TrimSpace(a.Currency))
		a.Unit = strings.ToLower(strings.TrimSpace(a.Unit))
		a.Display = clip(strings.TrimSpace(a.Display), MaxShortRunes)
		if a.Display == "" {
			a.Display = a.build()
		}
		e.Amount = &a
	}

	if len(e.Recipients) > 0 {
		rs := make([]Recipient, 0, min(len(e.Recipients), MaxRecipients))
		for _, r := range e.Recipients {
			if len(rs) == MaxRecipients {
				break
			}
			r.ID = clip(strings.TrimSpace(r.ID), MaxShortRunes*2)
			r.Name = clip(strings.TrimSpace(r.Name), MaxNameRunes)
			if r.Name != "" {
				rs = append(rs, r)
			}
		}
		e.Recipients = rs
		if len(rs) == 0 {
			e.Recipients = nil
		}
		// A gift of one with a named recipient is a gift of one.
		if e.Kind == KindGift && e.Count < len(e.Recipients) {
			e.Count = len(e.Recipients)
		}
	}

	if e.Progress != nil {
		p := *e.Progress
		if len(p.Options) > MaxOptions {
			p.Options = p.Options[:MaxOptions]
		}
		opts := make([]ProgressOption, len(p.Options))
		for i, o := range p.Options {
			o.Label = clip(strings.TrimSpace(o.Label), MaxShortRunes*2)
			opts[i] = o
		}
		p.Options = opts
		if len(opts) == 0 {
			p.Options = nil
		}
		e.Progress = &p
	}

	if len(e.Breakdown) > 0 {
		b := make(map[string]int, min(len(e.Breakdown), MaxBreakdown))
		keys := make([]string, 0, len(e.Breakdown))
		for k := range e.Breakdown {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if len(b) == MaxBreakdown {
				break
			}
			b[clip(strings.TrimSpace(k), MaxShortRunes)] = clampInt(e.Breakdown[k], 0, MaxCount)
		}
		e.Breakdown = b
	}
	return e
}

func normaliseActor(a *Actor) *Actor {
	if a == nil {
		return nil
	}
	c := *a
	c.ID = clip(strings.TrimSpace(c.ID), MaxShortRunes*2)
	c.Login = clip(strings.TrimSpace(c.Login), MaxNameRunes)
	c.Name = clip(strings.TrimSpace(c.Name), MaxNameRunes)
	if c.Name == "" && !c.Anonymous {
		// A platform that sends a login and no display name means the login.
		c.Name = c.Login
	}
	return &c
}

// build is the Display a platform did not supply. It runs inside Normalise,
// before Validate refuses an out-of-range exponent, so it must not trust one:
// the loop and the %0*d padding below are both as long as the exponent.
func (a Amount) build() string {
	if a.Money() {
		if a.Exponent < 0 || a.Exponent > MaxExponent {
			return ""
		}
		if a.Exponent == 0 {
			return fmt.Sprintf("%d %s", a.Minor, a.Currency)
		}
		div := int64(1)
		for i := 0; i < a.Exponent; i++ {
			div *= 10
		}
		neg := ""
		m := a.Minor
		if m < 0 {
			neg, m = "-", -m
		}
		return fmt.Sprintf("%s%d.%0*d %s", neg, m/div, a.Exponent, m%div, a.Currency)
	}
	return strconv.FormatInt(a.Units, 10) + " " + a.Unit
}

// Validate refuses an event the rest of the system could not make sense of:
// a kind it does not know, an amount that is money AND a unit, a raid with no
// other channel. Normalise first.
func (e Event) Validate() error {
	if !e.Kind.Valid() {
		return fmt.Errorf("activity: unknown kind %q", e.Kind)
	}
	if e.Platform == "" {
		return fmt.Errorf("activity: %s has no platform", e.Kind)
	}
	if e.Source.Type == "" {
		return fmt.Errorf("activity: %s has no source type", e.Kind)
	}
	if e.Source.ID == "" {
		return fmt.Errorf("activity: %s has no source id to dedupe on", e.Kind)
	}
	if e.Direction != "" && !slices.Contains(directions, e.Direction) {
		return fmt.Errorf("activity: direction %q is not in or out", e.Direction)
	}
	if e.Amount != nil {
		if err := e.Amount.Validate(); err != nil {
			return err
		}
	}
	if e.Actor != nil && e.Actor.Name == "" && !e.Actor.Anonymous {
		return fmt.Errorf("activity: %s has an actor with no name", e.Kind)
	}

	needActor := func() error {
		if e.Actor == nil {
			return fmt.Errorf("activity: %s needs an actor", e.Kind)
		}
		return nil
	}
	needOther := func() error {
		if e.Other == nil || e.Other.Name == "" {
			return fmt.Errorf("activity: %s needs the other channel", e.Kind)
		}
		return nil
	}
	needPhase := func(allowed []string) error {
		if !slices.Contains(allowed, e.Phase) {
			return fmt.Errorf("activity: %s phase %q is not one of %s", e.Kind, e.Phase, strings.Join(allowed, ", "))
		}
		return nil
	}

	switch e.Kind {
	case KindFollow:
		return needActor()
	case KindSubscribe:
		return needActor()
	case KindResub:
		if err := needActor(); err != nil {
			return err
		}
		if e.Months < 1 {
			return fmt.Errorf("activity: resub needs the number of months")
		}
	case KindGift:
		if err := needActor(); err != nil {
			return err
		}
		if e.Count < 1 {
			return fmt.Errorf("activity: gift needs a count of at least 1")
		}
	case KindTip:
		if err := needActor(); err != nil {
			return err
		}
		if e.Amount == nil {
			return fmt.Errorf("activity: tip needs an amount")
		}
	case KindRaid, KindShoutout:
		if err := needOther(); err != nil {
			return err
		}
		if e.Direction == "" {
			return fmt.Errorf("activity: %s needs a direction", e.Kind)
		}
	case KindRedemption:
		if err := needActor(); err != nil {
			return err
		}
		if e.Title == "" {
			return fmt.Errorf("activity: redemption needs the reward title")
		}
	case KindHypeTrain:
		return needPhase(hypePhases)
	case KindPoll:
		if err := needPhase(pollPhases); err != nil {
			return err
		}
		if e.Title == "" {
			return fmt.Errorf("activity: poll needs a title")
		}
	case KindPrediction:
		if err := needPhase(predictionPhases); err != nil {
			return err
		}
		if e.Title == "" {
			return fmt.Errorf("activity: prediction needs a title")
		}
	case KindStream:
		return needPhase(streamPhases)
	case KindModeration:
		if !slices.Contains(moderationActs, e.Action) {
			return fmt.Errorf("activity: moderation action %q is not one of %s", e.Action, strings.Join(moderationActs, ", "))
		}
		return needOther()
	case KindReaction:
		if e.Count < 1 {
			return fmt.Errorf("activity: reaction needs a count of at least 1")
		}
	}
	return nil
}

// MaxExponent is the largest minor-unit exponent an amount may carry.
const MaxExponent = 6

// standardExponent lists the currencies whose minor unit is not the usual
// hundredth, and the common ones that are, so an amount whose exponent
// contradicts its currency (a yen with exponent 2 is a hundredfold error in
// every threshold) is refused rather than believed. A currency not listed is
// accepted at any exponent in 0..MaxExponent: this is a guard against the
// frequent mistakes, not a copy of ISO 4217.
var standardExponent = map[string]int{
	"JPY": 0, "KRW": 0, "VND": 0, "CLP": 0, "ISK": 0, "UGX": 0, "XAF": 0, "XOF": 0, "PYG": 0, "RWF": 0,
	"BHD": 3, "KWD": 3, "JOD": 3, "OMR": 3, "TND": 3, "IQD": 3, "LYD": 3,
	"USD": 2, "EUR": 2, "GBP": 2, "CAD": 2, "AUD": 2, "NZD": 2, "CHF": 2, "SEK": 2, "NOK": 2, "DKK": 2,
	"PLN": 2, "CZK": 2, "MXN": 2, "BRL": 2, "INR": 2, "SGD": 2, "HKD": 2, "ZAR": 2, "TRY": 2, "RUB": 2,
}

// ExponentFor returns the standard minor-unit exponent of a currency, and false
// for one this build does not list.
func ExponentFor(currency string) (int, bool) {
	e, ok := standardExponent[strings.ToUpper(currency)]
	return e, ok
}

// Validate refuses an amount that is both money and a unit, or neither, an
// exponent outside 0..MaxExponent, or one that contradicts a known currency.
func (a Amount) Validate() error {
	if a.Exponent < 0 || a.Exponent > MaxExponent {
		return fmt.Errorf("activity: exponent %d is not between 0 and %d", a.Exponent, MaxExponent)
	}
	if want, known := ExponentFor(a.Currency); known && a.Exponent != want {
		return fmt.Errorf("activity: %s has %d minor-unit digits, not %d", a.Currency, want, a.Exponent)
	}
	if a.Currency == "" && a.Exponent != 0 {
		return fmt.Errorf("activity: a platform unit carries no exponent")
	}
	money := a.Currency != ""
	unit := a.Unit != "" || a.Units != 0
	switch {
	case money && unit:
		return fmt.Errorf("activity: an amount is money or a platform unit, not both")
	case !money && !unit:
		return fmt.Errorf("activity: an amount needs a currency or a unit")
	case money && len(a.Currency) != 3:
		return fmt.Errorf("activity: currency %q is not an ISO 4217 code", a.Currency)
	case money && a.Minor < 0, !money && a.Units < 0:
		return fmt.Errorf("activity: an amount cannot be negative")
	case !money && a.Unit == "":
		return fmt.Errorf("activity: a unit amount needs the unit's name")
	}
	return nil
}

// Key is the dedupe identity: the same fact reported twice has the same key.
// Shaped like chat.Message.Key, and for the same reason -- platform ids are
// only unique within a platform and an account.
func (e Event) Key() string {
	return e.Platform + "\x00" + e.Account + "\x00" + e.Source.Type + "\x00" + e.Source.ID
}

// OverlayVisible reports whether this event may be drawn on an alerts overlay
// at all, whatever the settings say: the kind must be an overlay kind, and a
// raid or shoutout going OUT is the streamer's own act, not an alert. Test
// events obey the same rule, so a style preview shows what a real one would.
func (e Event) OverlayVisible() bool {
	if !slices.Contains(OverlayKinds(), e.Kind) {
		return false
	}
	// A raid or shoutout going OUT is the streamer's own act, not an alert: the
	// overlay would otherwise announce "Shoutout from X" for one they gave.
	if (e.Kind == KindRaid || e.Kind == KindShoutout) && e.Direction == "out" {
		return false
	}
	return true
}

// DisplayName is the name an overlay would draw, before any name check:
// "Someone" is the overlay's job (it is a translated word), so an anonymous
// actor is "" here.
func (e Event) DisplayName() string {
	switch {
	case e.Kind == KindRaid || e.Kind == KindShoutout:
		if e.Other != nil {
			return e.Other.Name
		}
	case e.Actor != nil && !e.Actor.Anonymous:
		return e.Actor.Name
	}
	return ""
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not survivable for anything else either; a
		// time-derived id still keeps this event distinct in the ring.
		return "act_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "act_" + hex.EncodeToString(b[:])
}

func clip(s string, runes int) string {
	if runes <= 0 {
		return s
	}
	n := 0
	for i := range s {
		if n == runes {
			return strings.ToValidUTF8(s[:i], "")
		}
		n++
	}
	return strings.ToValidUTF8(s, "")
}

func clampInt(v, lo, hi int) int {
	return min(max(v, lo), hi)
}
