package polls

import (
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/platform"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxAnnouncement is the longest message the poll sends into chat, in
// characters: YouTube's live chat limit (200), which was the strictest of the
// platforms that can send when this was written. Twitch and Kick allow 500, so
// one text fits everywhere and every platform's viewers read the same words.
//
// X stops at 140, so its chat is sent a shortened text instead (AnnounceMax);
// every other chat keeps this one.
const MaxAnnouncement = platform.YouTubeChatMax

// truncate cuts s to n characters, marking the cut. The cut never lands inside
// a grapheme: a ZWJ sequence (family, profession emoji), a flag, an emoji with a
// skin tone or variation selector, or a base letter with its combining marks is
// kept whole or dropped whole.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	r := []rune(s)
	cut := n - 1
	for cut > 0 && splitsGrapheme(r, cut) {
		cut--
	}
	return strings.TrimRight(string(r[:cut]), " ") + "…"
}

// splitsGrapheme reports whether cutting r before index i would separate
// runes that render as one character.
func splitsGrapheme(r []rune, i int) bool {
	if i <= 0 || i >= len(r) {
		return false
	}
	prev, next := r[i-1], r[i]
	switch {
	case prev == 0x200D, next == 0x200D: // zero-width joiner on either side
		return true
	case next >= 0xFE00 && next <= 0xFE0F: // variation selector
		return true
	case next >= 0x1F3FB && next <= 0x1F3FF: // emoji skin tone
		return true
	case next == 0x20E3: // keycap
		return true
	case unicode.Is(unicode.Mn, next) || unicode.Is(unicode.Me, next) || unicode.Is(unicode.Mc, next):
		return true
	case isRegionalIndicator(prev) && isRegionalIndicator(next):
		// A flag is a pair: cutting between two indicators splits it only when
		// the run before the cut has odd length.
		n := 0
		for j := i - 1; j >= 0 && isRegionalIndicator(r[j]); j-- {
			n++
		}
		return n%2 == 1
	}
	return false
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func fits(s string, max int) bool { return utf8.RuneCountInString(s) <= max }

// AnnounceMax is the longest announcement one platform's chat takes: the poll's
// own cap, or the platform's send limit from the registry when that is shorter.
// X stops at 140, so its viewers get a shortened text rather than a refused one.
func AnnounceMax(p db.Platform) int {
	if row, ok := platform.ByEnum(string(p)); ok && row.Chat.SendMax > 0 && row.Chat.SendMax < MaxAnnouncement {
		return row.Chat.SendMax
	}
	return MaxAnnouncement
}

func humanDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	if d >= time.Minute {
		return fmt.Sprintf("%d min %d s", int(d/time.Minute), int(d%time.Minute/time.Second))
	}
	return fmt.Sprintf("%d s", int(d/time.Second))
}

// shorten builds a message from a template until it fits max characters. The
// question gives way first, down to a readable floor, so the options stay legible;
// only then are the labels shortened, and only after them is the question cut
// further.
func shorten(max int, question string, labels []string, build func(q string, labels []string) string) string {
	full := utf8.RuneCountInString(question)
	floor := minQuestion
	if full < floor {
		floor = full
	}
	for q := full; q >= floor; q-- {
		if s := build(truncate(question, q), labels); fits(s, max) {
			return s
		}
	}
	q := truncate(question, floor)
	for ml := MaxOption; ml >= 4; ml-- {
		short := make([]string, len(labels))
		for i, l := range labels {
			short[i] = truncate(l, ml)
		}
		if s := build(q, short); fits(s, max) {
			return s
		}
	}
	short := make([]string, len(labels))
	for i, l := range labels {
		short[i] = truncate(l, 4)
	}
	for q := floor - 1; q >= 1; q-- {
		if s := build(truncate(question, q), short); fits(s, max) {
			return s
		}
	}
	return truncate(build("", short), max)
}

// minQuestion is how much of the question is kept before any option label is
// shortened to make room.
const minQuestion = 24

// StartText is what the poll says in every chat when it opens. It never names
// a platform, so the same words are fine on all of them.
func StartText(s Spec) string { return StartTextMax(s, MaxAnnouncement) }

// StartTextMax is StartText shortened to at most max characters, with an
// ellipsis wherever a label or the question was cut. The voting instructions
// are kept whole; the question and labels give way.
func StartTextMax(s Spec, max int) string {
	return shorten(max, s.Question, s.Options, func(q string, labels []string) string {
		var b strings.Builder
		b.WriteString("POLL: ")
		b.WriteString(q)
		b.WriteString(" —")
		for i, l := range labels {
			fmt.Fprintf(&b, " %d) %s", i+1, l)
		}
		fmt.Fprintf(&b, " — type 1-%d or !vote N to vote. %s to vote.", len(labels), humanDuration(s.Duration()))
		return b.String()
	})
}

// NativeStartText is what the poll says in a chat where the platform's own
// poll is running: vote THERE. It names only the platform it is posted on,
// which is the one platform its viewers are already watching.
func NativeStartText(s Spec, p db.Platform) string {
	max := AnnounceMax(p)
	where := "the poll in this chat"
	switch p {
	case db.PlatformTwitch:
		where = "the Twitch poll above chat"
	case db.PlatformYouTube:
		where = "the poll in the live chat"
	}
	q := s.Question
	for n := utf8.RuneCountInString(q); n >= 1; n-- {
		text := fmt.Sprintf("POLL: %s — vote in %s. %s to vote.", truncate(q, n), where, humanDuration(s.Duration()))
		if fits(text, max) {
			return text
		}
	}
	return truncate("POLL — vote in "+where+".", max)
}

// EndText is what the poll says in every chat when it closes.
func EndText(r Result) string { return EndTextMax(r, MaxAnnouncement) }

// EndTextMax is EndText shortened to at most max characters.
func EndTextMax(r Result, max int) string {
	labels := make([]string, len(r.Options))
	for i, o := range r.Options {
		labels[i] = o.Label
	}
	w := r.Winners()
	return shorten(max, r.Question, labels, func(q string, labels []string) string {
		switch {
		case len(w) == 0:
			return fmt.Sprintf("POLL CLOSED: %s — no votes.", q)
		case len(w) == 1:
			v := r.Options[w[0]].Votes
			return fmt.Sprintf("POLL CLOSED: %s — %s wins with %d%% (%d of %d votes).",
				q, labels[w[0]], v*100/r.Total, v, r.Total)
		default:
			names := make([]string, len(w))
			for i, k := range w {
				names[i] = labels[k]
			}
			return fmt.Sprintf("POLL CLOSED: %s — a tie between %s, %d votes each (%d in all).",
				q, strings.Join(names, ", "), r.Options[w[0]].Votes, r.Total)
		}
	})
}
