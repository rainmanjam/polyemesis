package pack

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The shape detectors: properties of one message that need no history and no
// term list. They are pure functions of the text so that the history checker
// (internal/automod/history.go) and the pack checker count the same way.

// ShapeLimits are the thresholds for the per-message shape detectors. A zero
// value switches that detector off, which is how the engine hands the three
// the history checker already owns (mentions, links, caps) back to it.
type ShapeLimits struct {
	// MaxMentions is the most @-mentions in one message before it is spam.
	MaxMentions int
	// MaxLinks is the most links in one message.
	MaxLinks int
	// MinCapsLetters is the letter count below which a message cannot be
	// shouting; MaxCapsRatio the upper-case share above which it is.
	MinCapsLetters int
	MaxCapsRatio   float64
	// EmoteWall is the emote count at which a message may be an emote wall,
	// and EmoteShare the share of its tokens the emotes must make up.
	EmoteWall  int
	EmoteShare float64
	// CharRun is how many of one non-space rune in a row is a character flood,
	// and CharSpamRunes / CharSpamDistinct the length and distinct-rune count
	// of a long message made of almost nothing.
	CharRun          int
	CharSpamRunes    int
	CharSpamDistinct int
	// ZalgoMinMarks is the least combining marks, and ZalgoMarkRatio how many
	// times the base letters they must outnumber, in one cluster run.
	ZalgoMinMarks  int
	ZalgoMarkRatio int
}

// DefaultShapeLimits are the spec's numbers (section 3.4) and the history
// checker's own defaults for mentions, links and caps.
func DefaultShapeLimits() ShapeLimits {
	return ShapeLimits{
		MaxMentions: 5, MaxLinks: 3, MinCapsLetters: 12, MaxCapsRatio: 0.8,
		EmoteWall: 12, EmoteShare: 0.8,
		CharRun: 30, CharSpamRunes: 300, CharSpamDistinct: 4,
		ZalgoMinMarks: 8, ZalgoMarkRatio: 2,
	}
}

// Mentions counts @ signs, which is how the history checker has always counted
// mention spam.
func Mentions(text string) int { return strings.Count(text, "@") }

// Links counts links in text: explicit http(s) schemes, and failing that bare
// "www." hosts, because dropping the scheme is the obvious way past a filter
// that only counts "http".
func Links(text string) int {
	n := strings.Count(text, "http://") + strings.Count(text, "https://")
	if n == 0 {
		n = strings.Count(text, "www.")
	}
	return n
}

// CapsRatio reports the share of letters that are upper case, and whether the
// message has at least minLetters letters for the question to mean anything.
func CapsRatio(s string, minLetters int) (float64, bool) {
	var upper, letters int
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			upper++
		}
	}
	if letters < minLetters {
		return 0, false
	}
	return float64(upper) / float64(letters), true
}

// CharFlood reports whether s is one non-space rune run long enough to break an
// overlay's layout, or a long message made of almost nothing.
func CharFlood(s string, lim ShapeLimits) bool {
	if lim.CharRun <= 0 && lim.CharSpamRunes <= 0 {
		return false
	}
	var (
		last   rune
		run    int
		total  int
		seen   [8]rune
		nseen  int
		overfl bool
	)
	for _, r := range s {
		total++
		if r == last && !unicode.IsSpace(r) {
			run++
			if lim.CharRun > 0 && run >= lim.CharRun {
				return true
			}
		} else {
			last, run = r, 1
		}
		if overfl || lim.CharSpamRunes <= 0 {
			continue
		}
		found := false
		for i := 0; i < nseen; i++ {
			if seen[i] == r {
				found = true
				break
			}
		}
		if !found {
			if nseen < len(seen) {
				seen[nseen] = r
				nseen++
			}
			if nseen >= lim.CharSpamDistinct {
				overfl = true
			}
		}
	}
	return lim.CharSpamRunes > 0 && total >= lim.CharSpamRunes && nseen < lim.CharSpamDistinct
}

// Zalgo reports whether any whitespace-separated run of s carries combining
// marks far out of proportion to its base letters. A density test, not "any
// mark": Devanagari, Thai and Vietnamese put marks on most words and must pass.
func Zalgo(s string, lim ShapeLimits) bool {
	if lim.ZalgoMinMarks <= 0 {
		return false
	}
	ratio := lim.ZalgoMarkRatio
	if ratio < 1 {
		ratio = 1
	}
	var marks, bases int
	flush := func() bool {
		need := ratio * bases
		if need < lim.ZalgoMinMarks {
			need = lim.ZalgoMinMarks
		}
		hit := marks >= need
		marks, bases = 0, 0
		return hit
	}
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			if flush() {
				return true
			}
		case unicode.Is(unicode.Mn, r) && r >= 0x0300:
			// Mn is the class zalgo is made of. Mc (a spacing vowel sign) is
			// not counted: it advances the cursor and cannot stack.
			marks++
		case unicode.IsMark(r):
		default:
			bases++
		}
	}
	return flush()
}

// commonEmotes is the fallback vocabulary for a message whose platform did not
// count its emotes (Message.Emotes is empty): the names that fill walls on
// Twitch, 7TV, BTTV and FFZ. A platform's own count always wins. Matching is
// case-sensitive and whole-token, as the platforms' own emote names are.
var commonEmotes = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`Kappa KappaPride KappaRoss KappaClaus Kreygasm PogChamp Pog PogU Poggers
		LUL LULW KEKW OMEGALUL OMEGALULiguess monkaS monkaW monkaGIGA PepeHands Pepega PepeLaugh Sadge Copium
		Clap EZ EZ4ENCE 4Head 3Head 5Head BibleThump ResidentSleeper TriHard WutFace SeemsGood FailFish
		DansGame NotLikeThis PJSalt SwiftRage BrokeBack Jebaited Kippa MonkaS peepoHappy peepoSad catJAM
		widepeepoHappy widepeepoSad forsenE gachiGASM Hmm PauseChamp HYPERS HeyGuys VoHiYo Lurk
		LetsGo GIGACHAD Aware Clueless Susge COPIUM OkayChamp YEP NOPE peepoClap peepoHappy FeelsGoodMan
		FeelsBadMan FeelsStrongMan CoolStoryBob SourPls PogBones EleGiggle Squid1 Squid2 Squid3 Squid4 TwitchLit
		PartyHat GlitchLit DatSheffy BloodTrail StinkyCheese ThankEgg ratJAM Madge Weirdge Okayge pepeLaugh`) {
		commonEmotes[e] = true
	}
}

// EmoteCount returns how many emotes text holds, and how many whitespace
// separated tokens it has. When the platform counted them, that count wins and
// the vocabulary is not consulted.
func EmoteCount(text string, platformCount int) (emotes, tokens int) {
	for _, f := range strings.Fields(text) {
		tokens++
		if platformCount == 0 && commonEmotes[f] {
			emotes++
		}
	}
	if platformCount > 0 {
		emotes = platformCount
	}
	return emotes, tokens
}

// EmoteWall reports an emote wall: enough emotes, and emotes make up most of
// the message's tokens.
func EmoteWall(text string, platformCount int, lim ShapeLimits) bool {
	if lim.EmoteWall <= 0 {
		return false
	}
	// A wall needs EmoteWall tokens, so at least that many minus one spaces:
	// the common line is cleared by one scan, without splitting it.
	if platformCount < lim.EmoteWall && spaceRunes(text) < lim.EmoteWall-1 {
		return false
	}
	e, t := EmoteCount(text, platformCount)
	if e < lim.EmoteWall || t == 0 {
		return false
	}
	return float64(e) >= lim.EmoteShare*float64(t)
}

// spaceRunes counts Unicode whitespace runes, the same notion of a separator
// strings.Fields uses, so the EmoteWall precheck cannot disagree with the
// tokeniser (a wall split by tabs, U+3000 or NBSP has no ASCII spaces).
func spaceRunes(text string) int {
	n := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// emoteOnly reports a message made of nothing but emotes.
func emoteOnly(text string, platformCount int) bool {
	e, t := EmoteCount(text, platformCount)
	if t == 0 {
		return false
	}
	if platformCount > 0 {
		// Platform-marked emotes do not say how many tokens they covered; treat
		// a message with as many emotes as tokens as emote-only.
		return e >= t
	}
	return e == t
}

// isCommand reports a chat command or bot trigger such as "!play".
func isCommand(text string) bool {
	t := strings.TrimLeftFunc(text, unicode.IsSpace)
	if t == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(t)
	return r == '!' || r == '/'
}
