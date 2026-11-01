package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CaptionPolicy is one programme's caption text policy (#8): the spellings of
// the operator's own names and terms, and the words masked on screen. The
// captioner applies it once, at its emit boundary (captions.TextPolicy), so the
// dashboard, the WebVTT track, the SEI and POST carriage and the exports all
// show the same words.
//
// Both lists hold single words, because the captioner matches whole words: a
// phrase could never match, and an entry that cannot match is a setting that
// looks as if it works and does nothing.
type CaptionPolicy struct {
	// Vocabulary are terms in the spelling the operator wants. A recognised
	// word that matches one, ignoring case, is written in this spelling. It
	// corrects case and accents; it cannot make the recogniser hear a word it
	// did not hear.
	Vocabulary []string `json:"vocabulary,omitempty"`
	// Blocklist are words masked on screen: the first letter is kept and the
	// rest become asterisks, so "darn" reads as "d***".
	Blocklist []string `json:"blocklist,omitempty"`
}

const (
	// MaxCaptionPolicyEntries bounds each list. Every caption is checked against
	// both, on the captioner's emit path, so the bound is also a cost bound.
	MaxCaptionPolicyEntries = 200
	// MaxCaptionPolicyWordLength bounds one entry, in characters.
	MaxCaptionPolicyWordLength = 48
)

// normalized returns the policy with each entry trimmed, blanks dropped and a
// repeat that differs only in case dropped, keeping the first spelling given.
// It refuses an entry the captioner could never match, and a list or an entry
// past its bound.
func (p CaptionPolicy) normalized() (CaptionPolicy, error) {
	voc, err := normalizeCaptionWords("custom vocabulary", p.Vocabulary)
	if err != nil {
		return CaptionPolicy{}, err
	}
	blk, err := normalizeCaptionWords("masked words", p.Blocklist)
	if err != nil {
		return CaptionPolicy{}, err
	}
	return CaptionPolicy{Vocabulary: voc, Blocklist: blk}, nil
}

func normalizeCaptionWords(what string, in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		w := strings.TrimSpace(raw)
		if w == "" {
			continue
		}
		if n := utf8.RuneCountInString(w); n > MaxCaptionPolicyWordLength {
			return nil, fmt.Errorf("%s: %.20q… is %d characters, the most is %d", what, w, n, MaxCaptionPolicyWordLength)
		}
		if !captionWord(w) {
			return nil, fmt.Errorf("%s: %q is not one word; captions are matched word by word, so use letters, "+
				"digits and apostrophes only, with no spaces or hyphens", what, w)
		}
		key := strings.ToLower(w)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, w)
	}
	if len(out) > MaxCaptionPolicyEntries {
		return nil, fmt.Errorf("%s: %d entries, the most is %d", what, len(out), MaxCaptionPolicyEntries)
	}
	return out, nil
}

// captionWord reports whether w is one word in the sense the captioner matches:
// letters and digits, with apostrophes only inside it ("don't"). It mirrors
// captions.rewriteWords, which trims apostrophes at either end of a word.
func captionWord(w string) bool {
	rs := []rune(w)
	for i, r := range rs {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
		case r == '\'' && i > 0 && i < len(rs)-1:
		default:
			return false
		}
	}
	return len(rs) > 0
}

// captionPolicyStored is the value written to sources.caption_policy: the empty string for no
// policy, its JSON otherwise.
func captionPolicyStored(p CaptionPolicy) (string, error) {
	if len(p.Vocabulary) == 0 && len(p.Blocklist) == 0 {
		return "", nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// captionPolicyFromStored reads sources.caption_policy. A blob that will not
// parse reads as no policy, the same fallback the ingest blob takes, so one bad
// row cannot take the sources page down.
func captionPolicyFromStored(s string) CaptionPolicy {
	var p CaptionPolicy
	if s == "" {
		return p
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return CaptionPolicy{}
	}
	return p
}

// CaptionBurn is where a rendition draws the live captions into its picture.
type CaptionBurn string

const (
	// CaptionBurnOff draws nothing. It is the zero value.
	CaptionBurnOff CaptionBurn = ""
	// CaptionBurnBottom draws them at the foot of the picture, where viewers
	// look for captions.
	CaptionBurnBottom CaptionBurn = "bottom"
	// CaptionBurnTop draws them at the top, for a picture whose lower third is
	// already taken.
	CaptionBurnTop CaptionBurn = "top"
)

// valid reports whether b is one of the three positions.
func (b CaptionBurn) valid() bool {
	switch b {
	case CaptionBurnOff, CaptionBurnBottom, CaptionBurnTop:
		return true
	}
	return false
}
