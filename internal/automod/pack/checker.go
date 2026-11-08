package pack

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// The pack checker: every detector of the safe-by-default pack behind one call.
//
// It lives in this package, and returns pack-local Hits, because the engine
// (internal/automod) imports pack and pack must never import it. The engine
// converts a Hit to a Finding at the boundary; the corpus harness reads the Hits
// directly. Both therefore measure the same code.
//
// A Hit names the detector and tier, never the matched value. The engine logs a
// finding's Reason, and the matched value of a doxxing hit is the very thing the
// pack exists to keep out of the log.

// Categories a Hit may carry. Strings, as in pack.go: automod's own Category
// type is on the other side of the import.
const (
	CategoryDoxxing       = "doxxing"
	CategoryScam          = "scam"
	CategoryFlood         = "flood"
	CategoryImpersonation = "impersonation"
	CategoryRaid          = "raid"
)

// Groups name a detector family. They are what targets.json scores, and what
// Config.Detectors and Config.Hold are keyed by.
const (
	GroupHateSevere      = "hate_speech/severe"
	GroupHateContextual  = "hate_speech/contextual"
	GroupProfanityMild   = "profanity/mild"
	GroupProfanityStrong = "profanity/strong"
	GroupPhone           = "doxxing/phone"
	GroupEmail           = "doxxing/email"
	GroupIPv4            = "doxxing/ipv4"
	GroupIPv6            = "doxxing/ipv6"
	GroupAddress         = "doxxing/address"
	GroupGiveaway        = "scam/giveaway"
	GroupBio             = "scam/bio"
	GroupSupport         = "scam/support"
	GroupShortener       = "scam/shortener"
	GroupLookalike       = "scam/lookalike"
	GroupZalgo           = "flood/zalgo"
	GroupChar            = "flood/char"
	GroupEmote           = "flood/emote"
	GroupMentions        = "flood/mentions"
	GroupLinks           = "flood/links"
	GroupCaps            = "flood/caps"
	GroupImpersonation   = "impersonation"
	GroupRaid            = "raid"
	GroupNearDuplicate   = "spam_near_duplicate"
)

// defaultHold is the pack's policy (spec sections 3.1 to 3.5): which groups hold
// a message off the overlay, and which only flag it.
var defaultHold = map[string]bool{
	GroupHateSevere: true, GroupProfanityMild: true, GroupProfanityStrong: true,
	GroupPhone: true, GroupEmail: true, GroupIPv4: true, GroupIPv6: true, GroupAddress: true,
	GroupGiveaway: true, GroupBio: true, GroupSupport: true, GroupShortener: true, GroupLookalike: true,
	GroupZalgo: true, GroupChar: true, GroupMentions: true, GroupLinks: true,
	GroupImpersonation: true, GroupRaid: true,
	// Flag only: shouting, emote walls and contextual hate are culture or
	// ambiguity, and a hold hides the room's energy. Near-duplicates likewise.
	GroupHateContextual: false, GroupEmote: false, GroupCaps: false, GroupNearDuplicate: false,
}

// defaultOff are groups that ship disabled. A street address is the one: the
// false-positive rate in English chat is too high to hold on. (Profanity ships
// off too, but as a category: see Config.Categories.)
var defaultOff = map[string]bool{GroupAddress: true}

// DefaultHold reports the pack's own hold policy for a group.
func DefaultHold(group string) bool { return defaultHold[group] }

// Groups lists every group, for the API to render and tests to range over.
func Groups() []string {
	return []string{
		GroupHateSevere, GroupHateContextual, GroupProfanityMild, GroupProfanityStrong,
		GroupPhone, GroupEmail, GroupIPv4, GroupIPv6, GroupAddress,
		GroupGiveaway, GroupBio, GroupSupport, GroupShortener, GroupLookalike,
		GroupZalgo, GroupChar, GroupEmote, GroupMentions, GroupLinks, GroupCaps,
		GroupImpersonation, GroupRaid, GroupNearDuplicate,
	}
}

// Config configures a Checker. The zero value is the pack's defaults, except
// Shape: a zero ShapeLimits switches every shape detector off, so a caller that
// wants them passes DefaultShapeLimits() (automod.NewProtection does).
type Config struct {
	// Pack is the term list. Nil means the embedded English pack.
	Pack *Pack
	// Categories switches whole term categories: hate_speech (on) and
	// profanity (off). Absent means the default.
	Categories map[string]bool
	// Detectors switches one group on or off. Absent means the default (on,
	// except the street address).
	Detectors map[string]bool
	// Hold overrides the hold policy for one group. Absent means the default.
	Hold map[string]bool
	// AddTerms and AllowTerms are the operator's diff over the pack.
	AddTerms   []AddTerm
	AllowTerms []string
	// AllowDomains are never a shortener, lookalike or scam link.
	AllowDomains []string
	// Shape sets the per-message limits. A zero field disables its detector.
	Shape ShapeLimits
	// Reserved is the address-range policy for IP detection. The zero value is
	// production's: documentation ranges are rejected.
	Reserved ReservedPolicy
	// PhoneMinSeps is how many separators a bare 10 or 11 digit run needs (2 by
	// default, 3 for channels whose game posts scores).
	PhoneMinSeps int
	// Names are the protected account names. Nil disables impersonation.
	Names *NameSet
	// AllowIDs are author ids ("twitch:123") that are never an impostor.
	AllowIDs map[string]bool
	// Raid and NearDup are the windowed detectors. Nil disables each. They are
	// passed in, not built here, so one set of them outlives the Checker: a
	// settings save in the middle of a raid builds a new Checker and must not
	// forget what the old one had seen.
	Raid    *Raid
	NearDup *NearDup
}

// Message is one chat message as the checker sees it.
type Message struct {
	// Platform and AuthorID identify the sender; windowed detectors key on both.
	Platform string
	AuthorID string
	// AuthorName is the display name, for impersonation.
	AuthorName string
	Text       string
	// EmoteCount is how many emotes the platform marked; 0 falls back to a
	// vocabulary of common emote names.
	EmoteCount int
	// At is the message time for the windowed detectors; zero means now.
	At time.Time
}

// Hit is one detector's opinion about one message.
type Hit struct {
	// Category is the closed-set reason: hate_speech, profanity, doxxing, scam,
	// flood, impersonation or raid.
	Category string
	// Group is the detector family, e.g. "doxxing/phone".
	Group string
	// Detector is the part of Group after the slash, or the group itself.
	Detector string
	// Tier is the term tier for hate and profanity hits.
	Tier string
	// Form is the form of the message the evidence was found on: raw, nfkc,
	// pack, despaced or stretched.
	Form string
	// Reason names the detector, never the matched value.
	Reason string
	// Hold asks that the message be kept off the overlay.
	Hold bool
	// Authors lists participating authors of a raid, for the console flag.
	Authors []string
}

// Checker runs the pack. It is safe for concurrent use.
type Checker struct {
	cfg   Config
	terms *termIndex
	on    map[string]bool
	hold  map[string]bool
}

// New builds a checker. A bad pack is an error; everything else has a default.
func New(cfg Config) (*Checker, error) {
	p := cfg.Pack
	if p == nil {
		var err error
		if p, err = Default(); err != nil {
			return nil, err
		}
	}
	cats := map[string]bool{CategoryHate: true, CategoryProfanity: false}
	for k, v := range cfg.Categories {
		cats[k] = v
	}
	if cfg.PhoneMinSeps < 2 {
		cfg.PhoneMinSeps = 2
	}
	c := &Checker{cfg: cfg, on: map[string]bool{}, hold: map[string]bool{}}
	for _, g := range Groups() {
		on := !defaultOff[g]
		if cat, _, _ := strings.Cut(g, "/"); cat == CategoryProfanity {
			// Profanity is a term category, switched by Categories (default
			// off) rather than detector by detector.
			on = cats[CategoryProfanity]
		}
		if v, ok := cfg.Detectors[g]; ok {
			on = v
		}
		c.on[g] = on
		h := defaultHold[g]
		if v, ok := cfg.Hold[g]; ok {
			h = v
		}
		c.hold[g] = h
	}
	c.terms = buildTermIndex(p, cfg.AddTerms, cfg.AllowTerms, cats)
	return c, nil
}

// HoldFor reports whether group would hold, under this checker's policy.
func (c *Checker) HoldFor(group string) bool { return c.hold[group] }

// Enabled reports whether group is switched on.
func (c *Checker) Enabled(group string) bool { return c.on[group] }

// Inspect runs every stateless detector. It records nothing, so it is safe for
// a test box that must leave no trace.
func (c *Checker) Inspect(m Message) []Hit {
	return c.inspect(m, NewForms(m.Text))
}

// Observe runs Inspect and then the windowed detectors, which record the
// message. Windowed hits come last.
func (c *Checker) Observe(m Message) []Hit {
	f := NewForms(m.Text)
	hits := c.inspect(m, f)
	return append(hits, c.windowed(m, f)...)
}

func (c *Checker) add(hits []Hit, group, form, reason string) []Hit {
	cat, det, _ := strings.Cut(group, "/")
	if det == "" {
		det = group
	}
	if group == GroupNearDuplicate {
		cat, det = CategoryFlood, "near_duplicate"
	}
	return append(hits, Hit{
		Category: cat, Group: group, Detector: det, Form: form, Reason: reason, Hold: c.hold[group],
	})
}

func (c *Checker) inspect(m Message, f Forms) []Hit {
	var hits []Hit
	if f.NFKC == "" {
		return nil
	}

	// Terms: the strongest tier only, so a severe hit does not also score as a
	// contextual one.
	if hit, ok := c.terms.lookup(f); ok {
		group := hit.ref.category + "/" + hit.ref.tier
		if c.on[group] {
			cat := hit.ref.category
			reason := "hate term"
			switch group {
			case GroupHateContextual:
				reason = "possible hate term (needs context)"
			case GroupProfanityMild, GroupProfanityStrong:
				reason = "profanity"
			}
			hits = append(hits, Hit{Category: cat, Group: group, Detector: hit.ref.tier, Tier: hit.ref.tier,
				Form: hit.form, Reason: reason, Hold: c.hold[group]})
		}
	}

	hits = c.doxx(hits, f)
	hits = c.scam(hits, f)
	hits = c.shape(hits, m, f)
	hits = c.impersonation(hits, m, f)
	return hits
}

func (c *Checker) doxx(hits []Hit, f Forms) []Hit {
	s := f.NFKC
	digits, hasAt, colons, dots, brackets := 0, false, 0, 0, false
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digits++
		case r == '@':
			hasAt = true
		case r == ':':
			colons++
		case r == '.':
			dots++
		case r == ']' || r == ')':
			brackets = true
		}
	}
	// Prechecks, so a plain line costs a pass over its runes and nothing more:
	// an address needs digits and three dots, a v6 address two colons ("https:"
	// has one), and a spelled-out email a closing bracket.
	if c.on[GroupPhone] && digits >= 8 && hasPhone(s, c.cfg.PhoneMinSeps) {
		hits = c.add(hits, GroupPhone, "nfkc", "phone number")
	}
	if c.on[GroupEmail] && (hasAt || brackets && strings.Contains(strings.ToLower(s), "at")) {
		for _, d := range emailDomains(s) {
			if !domainAllowed(d, c.cfg.AllowDomains) {
				hits = c.add(hits, GroupEmail, "nfkc", "email address")
				break
			}
		}
	}
	if c.on[GroupIPv4] && digits >= 4 && dots >= 3 && hasIPv4(s, c.cfg.Reserved) {
		hits = c.add(hits, GroupIPv4, "nfkc", "IP address")
	}
	if c.on[GroupIPv6] && colons >= 2 && hasIPv6(s, c.cfg.Reserved) {
		hits = c.add(hits, GroupIPv6, "nfkc", "IP address")
	}
	if c.on[GroupAddress] && digits >= 1 && hasAddress(s) {
		hits = c.add(hits, GroupAddress, "nfkc", "street address")
	}
	return hits
}

func (c *Checker) scam(hits []Hit, f Forms) []Hit {
	if c.on[GroupGiveaway] && baitHit(f) {
		hits = c.add(hits, GroupGiveaway, "pack", "giveaway or crypto lure")
	}
	if c.on[GroupBio] && bioHit(f) {
		hits = c.add(hits, GroupBio, "pack", "check-my-bio pitch")
	}
	if c.on[GroupSupport] && supportHit(f) {
		hits = c.add(hits, GroupSupport, "pack", "fake platform support")
	}
	if c.on[GroupShortener] || c.on[GroupLookalike] {
		hosts := hostsOf(f.NFKC)
		if c.on[GroupShortener] && shortenerHit(hosts, c.cfg.AllowDomains) {
			hits = c.add(hits, GroupShortener, "nfkc", "URL shortener link")
		}
		if c.on[GroupLookalike] && lookalikeHit(hosts, c.cfg.AllowDomains) {
			hits = c.add(hits, GroupLookalike, "nfkc", "lookalike link")
		}
	}
	return hits
}

func (c *Checker) shape(hits []Hit, m Message, f Forms) []Hit {
	lim := c.cfg.Shape
	if c.on[GroupZalgo] && Zalgo(m.Text, lim) {
		hits = c.add(hits, GroupZalgo, "raw", "zalgo text")
	}
	if c.on[GroupChar] && CharFlood(m.Text, lim) {
		hits = c.add(hits, GroupChar, "raw", "character flood")
	}
	if c.on[GroupEmote] && EmoteWall(f.NFKC, m.EmoteCount, lim) {
		hits = c.add(hits, GroupEmote, "raw", "emote wall")
	}
	if c.on[GroupMentions] && lim.MaxMentions > 0 && Mentions(m.Text) > lim.MaxMentions {
		hits = c.add(hits, GroupMentions, "raw", "mass mentions")
	}
	if c.on[GroupLinks] && lim.MaxLinks > 0 && Links(m.Text) > lim.MaxLinks {
		hits = c.add(hits, GroupLinks, "raw", "too many links")
	}
	// Shouting is not reported on a character flood (the flood is the finding)
	// or on a message of nothing but emotes, whose names are capitalised by
	// nature.
	charHit := false
	for _, h := range hits {
		charHit = charHit || h.Group == GroupChar
	}
	if c.on[GroupCaps] && lim.MaxCapsRatio > 0 && !charHit {
		if r, ok := CapsRatio(m.Text, lim.MinCapsLetters); ok && r > lim.MaxCapsRatio && !emoteOnly(f.NFKC, m.EmoteCount) {
			hits = c.add(hits, GroupCaps, "raw", "shouting")
		}
	}
	return hits
}

func (c *Checker) impersonation(hits []Hit, m Message, f Forms) []Hit {
	if !c.on[GroupImpersonation] || c.cfg.Names == nil || m.AuthorName == "" {
		return hits
	}
	if c.cfg.AllowIDs[m.Platform+":"+m.AuthorID] {
		return hits
	}
	det, who, ok := c.cfg.Names.match(m.AuthorName, func() bool {
		if Links(m.Text) > 0 || strings.Contains(m.Text, "@") || strings.Contains(f.NFKC, "://") {
			return true
		}
		hosts := hostsOf(f.NFKC)
		return scamSignal(f, hosts, c.cfg.AllowDomains)
	})
	if !ok {
		return hits
	}
	h := Hit{Category: CategoryImpersonation, Group: GroupImpersonation, Detector: det, Form: "name",
		Reason: fmt.Sprintf("name looks like %s", who), Hold: c.hold[GroupImpersonation]}
	return append(hits, h)
}

// windowed runs the detectors that remember: near-duplicates for one author and
// raids across authors.
func (c *Checker) windowed(m Message, f Forms) []Hit {
	at := m.At
	if at.IsZero() {
		at = time.Now()
	}
	var hits []Hit
	key := m.Platform + ":" + m.AuthorID
	if m.AuthorID != "" && c.on[GroupNearDuplicate] && c.cfg.NearDup != nil {
		if n, over := c.cfg.NearDup.Observe(at, key, f); over {
			hits = c.add(hits, GroupNearDuplicate, "pack",
				fmt.Sprintf("near-identical message %d times in %s", n, c.cfg.NearDup.cfg.Window))
		}
	}
	if m.AuthorID != "" && c.on[GroupRaid] && c.cfg.Raid != nil {
		if rh, ok := c.cfg.Raid.Observe(at, key, f, m.EmoteCount); ok {
			h := Hit{Category: CategoryRaid, Group: GroupRaid, Detector: "raid", Form: "pack",
				Reason: rh.Reason(c.cfg.Raid.cfg.Window), Hold: c.hold[GroupRaid], Authors: rh.Names}
			hits = append(hits, h)
		}
	}
	return hits
}
