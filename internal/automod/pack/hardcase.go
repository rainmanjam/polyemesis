package pack

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The hard-case signals (spec section 6.2).
//
// With the Jev scope set to "hard", the model is asked only about messages the
// free checkers could not settle AND that show at least one sign of the kind of
// message a list cannot judge: a reclaimable slur, a threat aimed at someone,
// obfuscation with no list hit, a self-harm phrase, a sexual remark aimed at a
// person, an insult aimed at a reply. Everything here is free: lexicon lookups
// on the pack form the checker has already built.
//
// The lexicons are small, written by polyemesis, embedded and versioned with
// the pack. They are SIGNALS, not verdicts: a signal only means "worth one paid
// question", and the model (or nobody) decides what the message is.

//go:embed hardcase-en.json
var hardcaseEN []byte

// Signal names a reason a message is a hard case. Closed set; the engine counts
// them and the explain route can show them.
type Signal string

const (
	// SignalContextualHate: a contextual-tier hate term (a reclaimable or
	// two-way word) is present.
	SignalContextualHate Signal = "contextual_hate"
	// SignalThreat: second-person address plus a violence phrase.
	SignalThreat Signal = "threat"
	// SignalObfuscation: the pack form differs from the text by two or more
	// substitutions, and no term matched.
	SignalObfuscation Signal = "obfuscation"
	// SignalSelfHarm: a self-harm phrase.
	SignalSelfHarm Signal = "self_harm"
	// SignalSexualAddressed: a sexual phrase aimed at a person.
	SignalSexualAddressed Signal = "sexual_addressed"
	// SignalInsultReply: an insult phrase in a reply or at an @name.
	SignalInsultReply Signal = "insult_reply"
)

// obfuscationMin is how many substitutions make "obfuscation residue".
const obfuscationMin = 2

// maxLexiconEntries bounds each lexicon: the spec's "at most 150".
const maxLexiconEntries = 150

type hardcaseFile struct {
	ID       string              `json:"id"`
	Version  int                 `json:"version"`
	Address  []string            `json:"address"`
	Lexicons map[string][]string `json:"lexicons"`
}

// lexicon matches single tokens by map and phrases by a padded substring.
type lexicon struct {
	words   map[string]struct{}
	phrases []string
}

func newLexicon(entries []string) (*lexicon, error) {
	l := &lexicon{words: map[string]struct{}{}}
	for _, e := range entries {
		f := Fold(e)
		if f == "" {
			return nil, fmt.Errorf("hardcase: entry %q has no letters", e)
		}
		if strings.Contains(f, " ") {
			l.phrases = append(l.phrases, " "+f+" ")
		} else {
			l.words[f] = struct{}{}
		}
	}
	return l, nil
}

// has reports whether the pack form contains an entry as whole tokens.
func (l *lexicon) has(f Forms, padded string) bool {
	for _, t := range f.Tokens {
		if _, ok := l.words[t]; ok {
			return true
		}
	}
	for _, p := range l.phrases {
		if strings.Contains(padded, p) {
			return true
		}
	}
	return false
}

// HardCase is the compiled hard-case lexicon pack.
type HardCase struct {
	// Version is the lexicon version, held in step with the term pack's.
	Version int
	address map[string]struct{}
	lex     map[string]*lexicon
}

var (
	hardcaseOnce sync.Once
	hardcaseVal  *HardCase
	hardcaseErr  error
)

// DefaultHardCase returns the embedded English hard-case lexicons. Parsed once.
func DefaultHardCase() (*HardCase, error) {
	hardcaseOnce.Do(func() { hardcaseVal, hardcaseErr = ParseHardCase(hardcaseEN) })
	return hardcaseVal, hardcaseErr
}

// ParseHardCase decodes and validates a lexicon file.
func ParseHardCase(b []byte) (*HardCase, error) {
	var f hardcaseFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("hardcase: %w", err)
	}
	if f.ID == "" || f.Version < 1 {
		return nil, fmt.Errorf("hardcase: needs an id and a version of at least 1")
	}
	h := &HardCase{Version: f.Version, address: map[string]struct{}{}, lex: map[string]*lexicon{}}
	for _, a := range f.Address {
		t := Fold(a)
		if t == "" || strings.Contains(t, " ") {
			return nil, fmt.Errorf("hardcase: address entry %q must be one word", a)
		}
		h.address[t] = struct{}{}
	}
	for _, name := range []string{"violence", "self_harm", "sexual", "insult"} {
		entries := f.Lexicons[name]
		if len(entries) == 0 || len(entries) > maxLexiconEntries {
			return nil, fmt.Errorf("hardcase: lexicon %s needs 1-%d entries", name, maxLexiconEntries)
		}
		l, err := newLexicon(entries)
		if err != nil {
			return nil, err
		}
		h.lex[name] = l
	}
	if len(f.Lexicons) != len(h.lex) {
		return nil, fmt.Errorf("hardcase: unknown lexicon name")
	}
	return h, nil
}

// SignalInput is what the engine knows beyond the text.
type SignalInput struct {
	// Raw is the message as sent, for counting substitutions.
	Raw string
	// Reply is true when the message replies to another.
	Reply bool
	// ContextualHate is true when the pack found a contextual-tier hate term.
	ContextualHate bool
	// TermHit is true when the pack found any hate or profanity term.
	TermHit bool
}

// Signals lists the hard-case signals a message shows, built from forms the
// caller already has. Empty means the message is not worth a paid question.
func (h *HardCase) Signals(f Forms, in SignalInput) []Signal {
	if h == nil {
		return nil
	}
	var out []Signal
	if in.ContextualHate {
		out = append(out, SignalContextualHate)
	}
	if f.Pack == "" {
		// Nothing but symbols and digits: no word to be read, no signal.
		return out
	}
	padded := " " + f.Pack + " "
	at := strings.IndexByte(f.NFKC, '@') >= 0
	addressed := in.Reply || at
	if !addressed {
		for _, t := range f.Tokens {
			if _, ok := h.address[t]; ok {
				addressed = true
				break
			}
		}
	}
	if addressed && h.lex["violence"].has(f, padded) {
		out = append(out, SignalThreat)
	}
	if !in.TermHit && substitutions(in.Raw) >= obfuscationMin {
		out = append(out, SignalObfuscation)
	}
	if h.lex["self_harm"].has(f, padded) {
		out = append(out, SignalSelfHarm)
	}
	if addressed && h.lex["sexual"].has(f, padded) {
		out = append(out, SignalSexualAddressed)
	}
	if (in.Reply || at) && h.lex["insult"].has(f, padded) {
		out = append(out, SignalInsultReply)
	}
	return out
}

// substitutions counts the characters the pack form had to rewrite to read the
// text: format characters removed, look-alike letters folded to ASCII, and
// digits or symbols standing in for letters inside a word. Plain text, however
// loud, scores zero.
func substitutions(raw string) int {
	n := 0
	// Walk word by word: a word is a run of letters, digits, marks and the
	// symbols leet uses, plus any format characters inside it.
	rs := []rune(raw)
	isCf := func(r rune) bool { return r >= utf8.RuneSelf && unicode.Is(unicode.Cf, r) }
	for i := 0; i < len(rs); {
		if !isWordRune(rs, i) {
			i++
			continue
		}
		j := i
		for j < len(rs) && (isWordRune(rs, j) || isCf(rs[j])) {
			j++
		}
		// Letters, not just "has a letter": "10k", "4k", "1v1" and "5v5" are
		// ordinary chat, and a word that is mostly digits is a score or a
		// count. Leet needs at least three letters to read as spelling.
		letters, ascii := 0, false
		for _, c := range rs[i:j] {
			if unicode.IsLetter(c) {
				letters++
				if c < utf8.RuneSelf {
					ascii = true
				}
			}
		}
		for _, c := range rs[i:j] {
			switch {
			case isCf(c):
				// Inside a word only: a zero-width joiner between emoji is not
				// a word, and is how every family emoji is built.
				n++
			case c >= utf8.RuneSelf:
				// A look-alike counts only in a word that ALSO has an ASCII
				// letter: mixed script is the tell. A plain Russian, Ukrainian
				// or Greek sentence maps nearly every letter to a Latin one and
				// is ordinary chat.
				if _, ok := skeletonOf(c); ok && ascii {
					n++
				}
			case letters >= 3 && !unicode.IsLetter(c) && leet(c) != c:
				n++
			}
		}
		i = j
	}
	return n
}
