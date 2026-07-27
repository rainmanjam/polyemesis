package chat

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// MaxTextRunes bounds a stored message. It is far above every platform's own
// limit (Kick's 500 is the smallest published one) and exists only so a
// malformed frame cannot make one message cost a megabyte of history. A
// platform that starts accepting longer messages will be truncated here rather
// than refused, which is the failure mode worth having.
const MaxTextRunes = 2000

// Badge is one badge the platform put next to a name. The shape is
// deliberately loose: Twitch has id/version pairs, Kick has typed badges with
// a label, YouTube has boolean roles, and the renderer wants all three to be
// the same list.
type Badge struct {
	ID string `json:"id"`
	// Version distinguishes "subscriber/12" from "subscriber/1". Empty for a
	// platform that does not version its badges.
	Version string `json:"version,omitempty"`
	// Label is the badge's human name when the platform supplies one, for a
	// tooltip and for accessibility.
	Label string `json:"label,omitempty"`
}

// Emote is one inline image, located by rune offsets into Text.
//
// Rune offsets, not bytes: Twitch's emote tag indexes code points, and a
// message containing an emoji before an emote would misplace every subsequent
// range if these were byte offsets. Every adapter converts into this
// convention before the message leaves it.
type Emote struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Start is inclusive, End exclusive — Go's slicing convention, not
	// Twitch's inclusive-end one, because every consumer of this is Go or
	// JavaScript and both slice the same way.
	Start int `json:"start"`
	End   int `json:"end"`
	// URL is the image, when the platform gives one without a second request.
	URL string `json:"url,omitempty"`
}

// Author is who said it.
type Author struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	// Color is "#rrggbb" lowercase, or empty when the platform sent none or
	// sent something unparseable. Empty means "the UI picks one" — a name with
	// no colour is normal on every platform.
	Color  string  `json:"color,omitempty"`
	Badges []Badge `json:"badges,omitempty"`
	// Moderator, Subscriber and Broadcaster are the three roles that exist
	// everywhere in some form and that a chat pane actually renders
	// differently. Anything more specific stays in Badges.
	Moderator   bool `json:"moderator,omitempty"`
	Subscriber  bool `json:"subscriber,omitempty"`
	Broadcaster bool `json:"broadcaster,omitempty"`
}

// Message is one chat message from any platform, in the shape the rest of
// polyemesis speaks.
type Message struct {
	// ID is the platform's own message id where there is one. Normalise
	// synthesises a stable "syn-" id when a platform sends none, because
	// dedupe, deletion and reply-to all key on it.
	ID       string      `json:"id"`
	Platform db.Platform `json:"platform"`
	// Account is the platform_accounts.account_ref this arrived on. Two Twitch
	// channels connected at once are two accounts, and merging their chat into
	// one undifferentiated stream is a bug, not a feature.
	Account string `json:"account,omitempty"`
	// Channel is the platform's name for where this was said, for the
	// per-platform tab header.
	Channel string    `json:"channel,omitempty"`
	Author  Author    `json:"author"`
	Text    string    `json:"text"`
	Emotes  []Emote   `json:"emotes,omitempty"`
	At      time.Time `json:"at"`
	// Action marks an emote/me message, which every platform renders in the
	// author's colour without a colon.
	Action bool `json:"action,omitempty"`
	// ReplyToID and ReplyTo describe a threaded reply. Twitch and Kick both
	// have one; YouTube and Facebook live comments do not.
	ReplyToID string `json:"replyToId,omitempty"`
	ReplyTo   string `json:"replyTo,omitempty"`
	// Echo marks a message polyemesis sent itself. It survives dedupe against
	// the platform's own copy of the same message, so the local render and the
	// platform's echo do not both appear.
	Echo bool `json:"echo,omitempty"`
}

// Key identifies a message for deduplication. Platform and account are part of
// it because message ids are only unique within a platform, and two platforms
// will eventually both call something "1".
func (m Message) Key() string {
	return string(m.Platform) + "\x00" + m.Account + "\x00" + m.ID
}

// Zero reports whether this is the empty Message, which is what a Sender
// returns when the platform will echo the sent message back through the normal
// feed and a locally synthesised copy would duplicate it.
func (m Message) Zero() bool {
	return m.ID == "" && m.Text == "" && m.Platform == ""
}

// Normalise returns the message as it should be stored and rendered.
//
// It never rejects. Everything questionable — an emote range past the end of
// the text, a colour in a spelling we do not recognise, a timestamp the
// platform omitted — is repaired or dropped in favour of keeping the message,
// because a chat pane missing one person's line because their badge JSON was
// odd is a worse outcome than a line rendered without its badge.
//
// now supplies the clock so tests are not timing-dependent; a nil now means
// time.Now.
func (m Message) Normalise(now func() time.Time) Message {
	if now == nil {
		now = time.Now
	}
	if m.At.IsZero() {
		m.At = now()
	}
	// UTC throughout: messages are merged from four platforms in four
	// timezone spellings and sorted against each other.
	m.At = m.At.UTC()

	m.Text = cleanText(m.Text)
	m.Author.Name = strings.TrimSpace(m.Author.Name)
	if m.Author.Name == "" {
		// A nameless author is renderable; a nameless author labelled "" is
		// not. The id is a poor name and still better than a blank gap.
		m.Author.Name = firstNonEmpty(m.Author.ID, "unknown")
	}
	m.Author.Color = normaliseColor(m.Author.Color)
	m.Author.Badges = cleanBadges(m.Author.Badges)
	applyBadgeRoles(&m.Author)

	m.Emotes = cleanEmotes(m.Emotes, len([]rune(m.Text)))
	m.ReplyTo = strings.TrimSpace(m.ReplyTo)
	m.ReplyToID = strings.TrimSpace(m.ReplyToID)

	if m.ID == "" {
		m.ID = synthID(m)
	}
	return m
}

// cleanText makes a message safe to render on one line and bounded in size.
// Control characters are dropped rather than escaped: no platform's chat has a
// legitimate use for them, and a stray \r that survives into a log line or a
// terminal is how a chat message ends up rewriting somebody's screen.
func cleanText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	runes := 0
	for _, r := range s {
		if runes >= MaxTextRunes {
			break
		}
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(' ')
		case r == 0xFFFD:
			// Invalid UTF-8 already decoded to the replacement character.
			// Keeping it is more honest than dropping the whole message.
			b.WriteRune(r)
		case unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
		runes++
	}
	return strings.TrimSpace(b.String())
}

// normaliseColor accepts the two spellings platforms actually send and refuses
// to guess at anything else. An unrecognised colour becomes empty, which the UI
// already handles — it is the state every chatter who never picked a colour is
// in.
func normaliseColor(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return ""
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return "#" + s
}

func cleanBadges(in []Badge) []Badge {
	if len(in) == 0 {
		return nil
	}
	out := make([]Badge, 0, len(in))
	for _, b := range in {
		b.ID = strings.TrimSpace(b.ID)
		b.Version = strings.TrimSpace(b.Version)
		b.Label = strings.TrimSpace(b.Label)
		if b.ID == "" && b.Label == "" {
			continue
		}
		if b.ID == "" {
			b.ID = strings.ToLower(b.Label)
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyBadgeRoles fills the three role flags from badges when the platform
// expressed the role only that way.
//
// It only ever sets flags. A platform that said "not a moderator" while
// sending a moderator badge is telling us something we cannot resolve, and
// promoting the badge is the direction that renders the chat correctly.
func applyBadgeRoles(a *Author) {
	for _, b := range a.Badges {
		switch strings.ToLower(b.ID) {
		case "broadcaster", "owner", "host":
			a.Broadcaster = true
		case "moderator", "mod", "staff", "global_mod", "admin":
			a.Moderator = true
		case "subscriber", "founder", "member", "sponsor", "subgifter":
			a.Subscriber = true
		}
	}
	// The broadcaster can always moderate their own channel, on every platform
	// here, and only Twitch bothers to send the mod flag alongside the
	// broadcaster badge. Deriving it means the moderation affordances in the UI
	// do not have to special-case the person who owns the channel.
	if a.Broadcaster {
		a.Moderator = true
	}
}

// cleanEmotes drops ranges that do not fit the text and sorts what is left.
//
// Out of range means the adapter and the platform disagreed about indexing,
// and rendering that range would corrupt the message — but the message itself
// is fine, so it survives without its emote. Overlaps are left alone: a
// renderer walking sorted ranges can skip an overlap, and discarding one of
// two legitimate emotes because they touch would be worse.
func cleanEmotes(in []Emote, textRunes int) []Emote {
	if len(in) == 0 {
		return nil
	}
	out := make([]Emote, 0, len(in))
	for _, e := range in {
		if e.Start < 0 || e.End <= e.Start || e.End > textRunes {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// synthID makes a stable id for a platform that sends none.
//
// Stable is the whole point: the same message seen twice — a webhook retry, a
// poll page that overlaps the previous one — must produce the same id so
// dedupe catches it. It is derived from the content rather than from a counter
// for exactly that reason.
func synthID(m Message) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d",
		m.Platform, m.Account, m.Author.ID, m.Text, m.At.UnixMilli())
	return fmt.Sprintf("syn-%016x", h.Sum64())
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
