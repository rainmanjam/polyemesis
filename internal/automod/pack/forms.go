// Package pack is the data and the text normalisation behind the safe-by-default
// automod pack: an embedded, versioned term list and the forms a message is
// folded into before it is compared against that list.
//
// It deliberately imports nothing from internal/automod. The pack checker that
// consumes it lives in this package's future checker.go, and the engine must be
// able to hold that checker without an import cycle.
//
// THE OPERATOR'S REGEX RULES ARE NOT TOUCHED. automod.Normalise and its small
// homoglyph fold stay exactly as they are, so every example in docs/AUTOMOD.md
// keeps meaning what it says. The forms here are separate and are used only for
// pack and name matching, where the comparison is whole-token against an
// English list and a full UTS #39 skeleton is safe in a way it is not for a
// regex that may be run against any of the product's 15 locales.
package pack

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// maxRun is how many times one letter may repeat in the pack form. Two, not
// one, so that "good" and "book" survive while "fuuuuck" folds to "fuuck". Terms
// are stored through the same fold, so both sides agree.
const maxRun = 2

// Forms is every shared form of one message, built once and read by all pack
// detectors. Building it per detector would repeat the NFKC and skeleton work.
type Forms struct {
	// NFKC is the message in NFKC with format characters (Cf: zero-width,
	// bidi controls, BOM) removed. Digits stay digits here, which is what the
	// doxxing detectors and URL/host parsing need.
	NFKC string
	// Pack is the folded message: tokens joined by single spaces.
	Pack string
	// Tokens are the pack tokens, as substrings of Pack. Valid while Pack is.
	Tokens []string
	// Despaced is the tokens joined, collapsed again, when the message looks
	// deliberately letter-spaced ("b a d w o r d"). Empty otherwise.
	Despaced string
	// Stretch is aligned with Tokens: true where the sender repeated a letter
	// more than twice in a row ("fuuuuck"). The pack form keeps two, so a term
	// stored with one letter needs this to be tried against the collapsed token.
	Stretch []bool
}

// NewForms builds every form of text.
func NewForms(text string) Forms {
	n := NFKC(text)
	p, toks, stretch := fold(n)
	f := Forms{NFKC: n, Pack: p, Tokens: toks, Stretch: stretch}
	if deliberatelySpaced(toks) {
		f.Despaced = collapse(strings.Join(toks, ""))
	}
	return f
}

// Fold returns the pack form of s as a single string, tokens separated by one
// space. It is how a term is stored: the drift test requires that
// Fold(term) == term for every entry, so a term can never silently stop
// matching the text it was written to catch.
func Fold(s string) string {
	p, _, _ := fold(NFKC(s))
	return p
}

// NFKC normalises s to NFKC and removes format (Cf) characters.
func NFKC(s string) string {
	s = norm.NFKC.String(s)
	for i := 0; i < len(s); i++ {
		if s[i] < utf8.RuneSelf {
			continue
		}
		// There is a non-ASCII byte. Only now pay for the rune walk.
		return stripFormat(s)
	}
	return s
}

func stripFormat(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= utf8.RuneSelf && unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Name is the form used to compare account names and link hosts:
// skeleton(lowercase(NFKC(s))) with every character that is not a letter or
// digit removed.
func Name(s string) string {
	s = strings.ToLower(NFKC(s))
	s = Skeleton(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Skeleton replaces characters that look like an ASCII letter or digit with
// that letter or digit, per the generated UTS #39 table. Pure-ASCII input is
// returned untouched without allocating.
func Skeleton(s string) string {
	i := 0
	for ; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			break
		}
	}
	if i == len(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(s[:i])
	for _, r := range s[i:] {
		if to, ok := skeletonOf(r); ok {
			b.WriteString(to)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// skeletonOf looks r up in the generated table. Binary search: the table is
// sorted by rune and a few thousand entries is twelve comparisons.
func skeletonOf(r rune) (string, bool) {
	if r < utf8.RuneSelf {
		return "", false
	}
	lo, hi := 0, len(skeletonTable)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		switch e := &skeletonTable[mid]; {
		case e.r == r:
			return e.to, true
		case e.r < r:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return "", false
}

// leet maps the digits that stand in for letters. Applied only inside a token
// that already contains a letter, so "2024" and "1-0" stay digits.
func leet(r rune) rune {
	switch r {
	case '0':
		return 'o'
	case '1':
		return 'i'
	case '3':
		return 'e'
	case '4':
		return 'a'
	case '5':
		return 's'
	case '7':
		return 't'
	case '@':
		return 'a'
	case '$':
		return 's'
	case '!', '|':
		return 'i'
	}
	return r
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// isLetterish is a letter, or a mark that belongs to one. Marks are kept so a
// Devanagari, Thai or Vietnamese word stays one token; zalgo is a density
// question answered on the raw text, never by stripping here.
func isLetterish(r rune) bool { return unicode.IsLetter(r) || unicode.IsMark(r) }

// fold builds the pack form from an NFKC string.
//
// Steps: skeleton -> lowercase -> leet (inside tokens with a letter) -> collapse
// runs of one letter to two -> split on non-letters.
func fold(s string) (string, []string, []bool) {
	if s == "" {
		return "", nil, nil
	}
	rs := foldRunes(s)
	fb := foldBuilder{tok: -1}
	fb.b.Grow(len(rs))

	for i := 0; i < len(rs); {
		// A word is a maximal run of word characters: letters, digits, marks,
		// and the symbols that stand in for a letter when they sit inside one.
		if !isWordRune(rs, i) {
			fb.flush()
			i++
			continue
		}
		j := i
		for j < len(rs) && isWordRune(rs, j) {
			j++
		}
		fb.word(rs[i:j])
		i = j
	}

	out := fb.b.String()
	if len(fb.offs) == 0 {
		return "", nil, nil
	}
	toks, stretch := tokensOf(out, fb.offs)
	return out, toks, stretch
}

// tokensOf slices the tokens of out at offs, and reports which are stretched.
func tokensOf(out string, offs [][3]int) ([]string, []bool) {
	toks := make([]string, len(offs))
	stretch := make([]bool, len(offs))
	for k, o := range offs {
		toks[k] = out[o[0]:o[1]]
		stretch[k] = o[2] == 1
	}
	return toks, stretch
}

// foldRunes lower-cases s and expands its look-alike letters to their ASCII
// skeletons, one rune per letter.
func foldRunes(s string) []rune {
	rs := make([]rune, 0, len(s))
	for _, r := range s {
		if r < utf8.RuneSelf {
			if 'A' <= r && r <= 'Z' {
				r += 'a' - 'A'
			}
			rs = append(rs, r)
			continue
		}
		if to, ok := skeletonOf(r); ok {
			for i := 0; i < len(to); i++ {
				rs = append(rs, rune(to[i]))
			}
			continue
		}
		rs = append(rs, unicode.ToLower(r))
	}
	return rs
}

// word writes one word of rs. Its stand-in symbols read as letters when the
// word has a letter, and a symbol that is not a letter ends the token.
func (f *foldBuilder) word(word []rune) {
	hasLetter := wordHasLetter(word)
	for _, r := range word {
		if hasLetter {
			r = leet(r)
		}
		if !isLetterish(r) {
			f.flush()
			continue
		}
		f.write(r)
	}
	f.flush()
}

// wordHasLetter reports whether a word holds a letter, which is what makes its
// stand-in symbols leet rather than literal.
func wordHasLetter(word []rune) bool {
	for _, r := range word {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// foldBuilder is the state fold carries across the runes of one string: the
// pack form written so far, the offsets of each token in it, and the run of the
// letter being written.
type foldBuilder struct {
	b    strings.Builder
	offs [][3]int // start, end, stretched
	last rune
	run  int
	tok  int  // start offset of the token being written, or -1
	long bool // the token being written had a letter repeated past maxRun
}

// flush ends the token being written, if it has any text.
func (f *foldBuilder) flush() {
	if f.tok >= 0 && f.b.Len() > f.tok {
		st := 0
		if f.long {
			st = 1
		}
		f.offs = append(f.offs, [3]int{f.tok, f.b.Len(), st})
	}
	f.tok = -1
	f.long = false
	f.last, f.run = 0, 0
}

// write appends one letter to the current token. A run of one letter is written
// at most maxRun times; a token that runs past it is marked stretched.
func (f *foldBuilder) write(r rune) {
	if f.tok < 0 {
		// Separate tokens with one space; offsets exclude it.
		if f.b.Len() > 0 {
			f.b.WriteByte(' ')
		}
		f.tok = f.b.Len()
	}
	if r == f.last {
		f.run++
		if f.run > maxRun {
			f.long = true
			return
		}
	} else {
		f.last, f.run = r, 1
	}
	f.b.WriteRune(r)
}

// isWordRune reports whether rs[i] is part of a word. Symbols that can stand in
// for a letter count only where they cannot be punctuation: '@', '!' and '|'
// must have an alphanumeric on both sides ("n!gger" yes, "@name" and "hello!"
// no), and '$' needs an alphanumeric or another '$' beside it ("$hit", "a$$").
func isWordRune(rs []rune, i int) bool {
	r := rs[i]
	switch r {
	case '@', '!', '|':
		return i > 0 && i+1 < len(rs) && isAlnum(rs[i-1]) && isAlnum(rs[i+1])
	case '$':
		return (i > 0 && (isAlnum(rs[i-1]) || rs[i-1] == '$')) ||
			(i+1 < len(rs) && (isAlnum(rs[i+1]) || rs[i+1] == '$'))
	}
	return isAlnum(r) || unicode.IsMark(r)
}

// collapse re-applies the run limit to a joined string.
func collapse(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	var last rune
	run := 0
	for _, r := range s {
		if r == last {
			run++
			if run > maxRun {
				continue
			}
		} else {
			last, run = r, 1
		}
		b.WriteRune(r)
	}
	return b.String()
}

// deliberatelySpaced reports whether the tokens look letter-spaced to evade a
// filter ("b a d w o r d") rather than merely containing short words. It is the
// same heuristic as automod.deliberatelySpaced (rules.go), restated here because
// that function is unexported and importing internal/automod would put a cycle
// in the way of the checker: several tokens, most of them a single character.
// Stripping spaces unconditionally would turn "a bad wordsmith" into a match
// for "badword".
func deliberatelySpaced(toks []string) bool {
	if len(toks) < 4 {
		return false
	}
	single := 0
	for _, t := range toks {
		if utf8.RuneCountInString(t) == 1 {
			single++
		}
	}
	return single*2 > len(toks)
}
