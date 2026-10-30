package polls

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxAnnouncement is the longest message the poll sends into chat, in
// characters: YouTube's live chat limit, the strictest of the platforms that
// can send. Twitch and Kick allow 500, so one text fits everywhere and every
// platform's viewers read the same words.
const MaxAnnouncement = 200

// truncate cuts s to n characters, marking the cut.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}

func fits(s string) bool { return utf8.RuneCountInString(s) <= MaxAnnouncement }

func humanDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	if d >= time.Minute {
		return fmt.Sprintf("%d min %d s", int(d/time.Minute), int(d%time.Minute/time.Second))
	}
	return fmt.Sprintf("%d s", int(d/time.Second))
}

// shorten builds a message from a template, shortening option labels and then
// the question until it fits MaxAnnouncement.
func shorten(question string, labels []string, build func(q string, labels []string) string) string {
	for max := MaxOption; max >= 4; max-- {
		short := make([]string, len(labels))
		for i, l := range labels {
			short[i] = truncate(l, max)
		}
		if s := build(question, short); fits(s) {
			return s
		}
	}
	short := make([]string, len(labels))
	for i, l := range labels {
		short[i] = truncate(l, 4)
	}
	for q := utf8.RuneCountInString(question); q >= 1; q-- {
		if s := build(truncate(question, q), short); fits(s) {
			return s
		}
	}
	return truncate(build("", short), MaxAnnouncement)
}

// StartText is what the poll says in every chat when it opens. It never names
// a platform, so the same words are fine on all of them.
func StartText(s Spec) string {
	return shorten(s.Question, s.Options, func(q string, labels []string) string {
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

// EndText is what the poll says in every chat when it closes.
func EndText(r Result) string {
	labels := make([]string, len(r.Options))
	for i, o := range r.Options {
		labels[i] = o.Label
	}
	w := r.Winners()
	return shorten(r.Question, labels, func(q string, labels []string) string {
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
