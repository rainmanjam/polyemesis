package pack

import (
	"bytes"
	// Blank import: the //go:embed directive below needs package embed linked in,
	// but the file itself never refers to it by name.
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
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
	// terms is every single-word term the cut test knows: the lexicons' words
	// and, for the default pack, its contextual-tier hate terms (which only the
	// model judges, and which nothing else can catch once cut up). minTerm and
	// maxTerm bound their lengths so most words are rejected without folding.
	terms            map[string]struct{}
	minTerm, maxTerm int
}

// addTerm registers one folded single-word term.
func (h *HardCase) addTerm(t string) {
	if t == "" || len(t) > maxCutTerm || strings.Contains(t, " ") {
		return
	}
	if h.terms == nil {
		h.terms = map[string]struct{}{}
	}
	h.terms[t] = struct{}{}
	if h.minTerm == 0 || len(t) < h.minTerm {
		h.minTerm = len(t)
	}
	if len(t) > h.maxTerm {
		h.maxTerm = len(t)
	}
}

// maxCutTerm bounds the stack buffer a candidate word is folded into.
const maxCutTerm = 32

var (
	hardcaseOnce sync.Once
	hardcaseVal  *HardCase
	hardcaseErr  error
)

// DefaultHardCase returns the embedded English hard-case lexicons. Parsed once.
func DefaultHardCase() (*HardCase, error) {
	hardcaseOnce.Do(func() {
		hardcaseVal, hardcaseErr = ParseHardCase(hardcaseEN)
		if hardcaseErr != nil {
			return
		}
		// A contextual term cut up with dots and slashes has no fast finding
		// and, without this, no signal: the model would never be asked.
		if pk, err := Default(); err == nil {
			for _, e := range pk.Entries {
				if e.Category == CategoryHate && e.Tier == TierContextual {
					hardcaseVal.addTerm(Fold(e.Term))
				}
			}
		}
	})
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
	for _, l := range h.lex {
		for w := range l.words {
			h.addTerm(w)
		}
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
	if !in.TermHit && h.substitutions(in.Raw) >= obfuscationMin {
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
//
// What is NOT a substitution, because chat is full of it: a URL; a word whose
// digits are all one run at its start or end ("30fps", "4Head", "10k", "gr8",
// "lvl50", "wave10", "season2024", "player123"), which is a number attached to
// a word and not a letter replaced; and a word with fewer than two letters
// ("1v1", "5v5"). Leet is a digit or symbol standing INSIDE a word ("k1ll",
// "y0u", "g4m3"). The count is still summed over the message, since "k1ll y0u"
// is two words with one substitution each.
func substitutions(raw string) int { return (*HardCase)(nil).substitutions(raw) }

// substitutions is substitutions with the lexicons in hand: a word whose
// stand-ins sit at its edge ("5uicide") normally reads as a number attached to a
// word, but counts when its leet-folded form is a lexicon term.
func (h *HardCase) substitutions(raw string) int {
	n := 0
	for _, field := range strings.Fields(raw) {
		// BEFORE the link test: "k.y/s", or a term cut on a fragment that is
		// also a TLD (".gg/", ".to/"), reads as an address and would be skipped.
		// A field that is a term once its dots and slashes are gone is
		// obfuscation, whatever else it looks like.
		if h.cutTerm(field) {
			n += obfuscationMin
			continue
		}
		if urlLike(field) {
			continue
		}
		n += h.fieldSubstitutions(field)
	}
	return n
}

// cutTerm reports whether a field containing '.' or '/' is a term once those
// are stripped and the rest is leet-folded. Allocation-free, and rejected by
// length before any folding.
func (h *HardCase) cutTerm(field string) bool {
	if h == nil || h.maxTerm == 0 || strings.IndexAny(field, "./") < 0 {
		return false
	}
	// Stripped length is at least len-separators, and at most len.
	if len(field) < h.minTerm {
		return false
	}
	var buf [maxCutTerm]byte
	n := 0
	for _, r := range field {
		if r == '.' || r == '/' {
			continue
		}
		b, ok := leetByte(r)
		if !ok {
			if r >= utf8.RuneSelf && unicode.Is(unicode.Cf, r) {
				continue
			}
			return false
		}
		if n == len(buf) {
			return false
		}
		buf[n] = b
		n++
	}
	if n < h.minTerm || n > h.maxTerm {
		return false
	}
	_, ok := h.terms[string(buf[:n])]
	return ok
}

// leetByte folds one rune to the lower-case ASCII letter it reads as, or
// reports that it is not one: the non-allocating counterpart of Fold for the
// ASCII spellings leet uses.
func leetByte(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r), true
	case r >= 'A' && r <= 'Z':
		return byte(r) + 'a' - 'A', true
	}
	switch r {
	case '0':
		return 'o', true
	case '1', '!', '|':
		return 'i', true
	case '3':
		return 'e', true
	case '4', '@':
		return 'a', true
	case '5', '$':
		return 's', true
	case '7':
		return 't', true
	}
	return 0, false
}

// urlLike reports whether a whitespace-delimited field is a link, whose digits
// and separators are an address and not spelling.
//
// A link is a scheme and a host that url.Parse accepts, or a host (with or
// without "www.") ending in a real top-level domain. Dots and a slash alone are NOT enough:
// "k1ll.y0u/x", or a lexicon term cut up with them, would otherwise be waved
// through as an address and hide the very residue the signal looks for.
func urlLike(f string) bool {
	l := strings.ToLower(strings.TrimRight(f, ".,;:!?)]}\"'"))
	if strings.Contains(l, "://") {
		u, err := url.Parse(l)
		return err == nil && u.Scheme != "" && u.Hostname() != ""
	}
	host := strings.TrimPrefix(l, "www.")
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndexByte(host, '.'); i > 0 && i < len(host)-1 {
		_, ok := linkTLDs[host[i+1:]]
		return ok
	}
	return false
}

// linkTLDs are the top-level domains that appear in chat links. A short list on
// purpose: a made-up one ("k.y") is not an address.
var linkTLDs = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, t := range strings.Fields(`com net org io tv gg me co us uk de fr nl ca au jp ly be to fm app dev xyz
		info biz edu gov ru br es it pl se no fi dk cc ws ai gd sh link live online site store shop club
		page tech cloud one top vip pro wtf lol gl gy ms ac eu ch at nz za in kr cn tk ml ga cf`) {
		m[t] = struct{}{}
	}
	return m
}()

// isTerm reports whether a word, leet-folded, is a known term. No allocation:
// a length check first, then a stack-buffer fold.
func (h *HardCase) isTerm(word []rune) bool {
	if h == nil || h.maxTerm == 0 || len(word) < h.minTerm || len(word) > h.maxTerm {
		return false
	}
	var buf [maxCutTerm]byte
	for i, r := range word {
		b, ok := leetByte(r)
		if !ok {
			return false
		}
		buf[i] = b
	}
	_, ok := h.terms[string(buf[:len(word)])]
	return ok
}

func (h *HardCase) fieldSubstitutions(field string) int {
	n := 0
	// Walk word by word: a word is a run of letters, digits, marks and the
	// symbols leet uses, plus any format characters inside it.
	rs := []rune(field)
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
		word := rs[i:j]
		letters, ascii := 0, false
		for _, c := range word {
			if unicode.IsLetter(c) {
				letters++
				if c < utf8.RuneSelf {
					ascii = true
				}
			}
		}
		leets := leetRuns(word)
		for _, c := range word {
			switch {
			case isCf(c):
				// Inside a word only: a zero-width joiner between emoji is not
				// a word, and is how every family emoji is built.
				n++
			case c >= utf8.RuneSelf:
				// A look-alike counts only in a word that ALSO has an ASCII
				// letter: mixed script is the tell. A plain Russian, Ukrainian
				// or Greek sentence maps nearly every letter to a Latin one and
				// is ordinary chat. A Latin letter with a mark (Turkish dotless
				// i, Polish s with an acute, or l with a stroke) is spelling, not
				// a disguise; a Latin-script CONFUSABLE (U+0251 for a, U+0261
				// for g) is exactly the disguise.
				if _, ok := skeletonOf(c); ok && ascii && !alphabetLetter(c) {
					n++
				}
			}
		}
		if letters >= 2 && (leets.inside() || (leets.count > 0 && h.isTerm(word))) {
			n += leets.count
		}
		i = j
	}
	return n
}

// alphabetLetter reports whether a non-ASCII letter is part of a real Latin
// alphabet: an ASCII letter carrying marks (s with an acute, o with a
// diaeresis), or one of the few letters that do not decompose (Turkish dotless
// i, the Polish and Nordic strokes and the eth).
func alphabetLetter(r rune) bool {
	switch r {
	case 'æ', 'Æ', 'œ', 'Œ', 'ı', 'ł', 'Ł', 'ø', 'Ø', 'đ', 'Đ', 'ð', 'Ð', 'ß':
		return true
	}
	d := norm.NFD.String(string(r))
	rs := []rune(d)
	if len(rs) < 2 || rs[0] >= utf8.RuneSelf || !unicode.IsLetter(rs[0]) {
		return false
	}
	for _, m := range rs[1:] {
		if !unicode.IsMark(m) {
			return false
		}
	}
	return true
}

// leetShape describes where a word's digit and symbol stand-ins are.
type leetShape struct {
	count int
	runs  int
	// atEdge is true when the stand-ins are one run touching the start or end.
	atEdge bool
}

// inside reports whether the stand-ins read as spelling: some run is not a
// plain number attached to the front or back of the word.
func (s leetShape) inside() bool { return s.count > 0 && !(s.runs == 1 && s.atEdge) }

func leetRuns(word []rune) leetShape {
	var s leetShape
	prev := false
	for k, c := range word {
		// A run is every non-letter in a row, so "2024" is one run; only the
		// characters leet actually uses are counted.
		is := !unicode.IsLetter(c) && !unicode.IsMark(c)
		if is {
			if leet(c) != c {
				s.count++
			}
			if !prev {
				s.runs++
				if k == 0 {
					s.atEdge = true
				}
			}
			if k == len(word)-1 {
				s.atEdge = true
			}
		}
		prev = is
	}
	return s
}
