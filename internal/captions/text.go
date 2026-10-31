package captions

import (
	"strings"
	"unicode"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// TextPolicy is what an operator asks the caption text to be before it leaves
// the captioner: the spellings of their own names and terms, and the words they
// do not want on screen. It is applied once, at the emit boundary, so every
// output (the WebSocket, the WebVTT sidecar, the SEI carriage and the exports)
// shows the same words.
type TextPolicy struct {
	// Vocabulary maps a term to its preferred spelling. The match is on whole
	// words and ignores case, so "polyemesis" in any capitalisation becomes the
	// spelling the operator chose.
	Vocabulary []string
	// Blocklist names words to mask. A masked word keeps its first letter and
	// its length, so "damn" reads as "d***", which is what a reader expects.
	Blocklist []string
}

// Apply returns text with the vocabulary spellings applied and the blocked words
// masked. Text with neither returns unchanged.
func (p TextPolicy) Apply(text string) string {
	if len(p.Vocabulary) == 0 && len(p.Blocklist) == 0 {
		return text
	}
	fixes := make(map[string]string, len(p.Vocabulary))
	for _, term := range p.Vocabulary {
		if t := strings.TrimSpace(term); t != "" {
			fixes[strings.ToLower(t)] = t
		}
	}
	blocked := make(map[string]bool, len(p.Blocklist))
	for _, w := range p.Blocklist {
		if t := strings.ToLower(strings.TrimSpace(w)); t != "" {
			blocked[t] = true
		}
	}
	return rewriteWords(text, func(w string) string {
		key := strings.ToLower(w)
		if blocked[key] {
			return mask(w)
		}
		if spelled, ok := fixes[key]; ok {
			return spelled
		}
		return w
	})
}

// Segments returns a copy of segs with the policy applied to each cue's text.
// The timestamps, the track and the speaker are left alone, so a cue still
// lands where it was spoken.
func (p TextPolicy) Segments(segs []transcribe.Segment) []transcribe.Segment {
	out := make([]transcribe.Segment, len(segs))
	for i, s := range segs {
		s.Text = p.Apply(s.Text)
		out[i] = s
	}
	return out
}

// rewriteWords calls fn on every maximal run of letters and digits, and leaves
// everything between those runs (spaces, punctuation) exactly as it was. Working
// on whole runs is what stops "class" from matching "ass" and "polyemesistic"
// from matching "polyemesis".
func rewriteWords(text string, fn func(string) string) string {
	var b strings.Builder
	var run []rune
	flush := func() {
		if len(run) > 0 {
			b.WriteString(fn(string(run)))
			run = run[:0]
		}
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			run = append(run, r)
			continue
		}
		flush()
		b.WriteRune(r)
	}
	flush()
	return b.String()
}

// mask keeps the first letter of a word and replaces the rest with asterisks,
// one per character, so the length of the word on screen is unchanged.
func mask(w string) string {
	rs := []rune(w)
	for i := 1; i < len(rs); i++ {
		rs[i] = '*'
	}
	return string(rs)
}
