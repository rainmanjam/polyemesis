package pack

import (
	"strings"
	"unicode/utf8"
)

// maxGram is the longest term, in tokens, that the matcher looks up.
const maxGram = 4

type termRef struct {
	category string
	tier     string
	except   map[string]bool
}

// termIndex is the lookup structure built once from a pack plus the operator's
// additions and suppressions.
type termIndex struct {
	// exact maps a term in pack form to its entry. Multi-word terms are keyed
	// with single spaces, so an n-gram of Pack is a direct substring lookup.
	exact map[string]termRef
	// despaced maps a term with its spaces removed (and runs collapsed again),
	// for the deliberately letter-spaced form.
	despaced map[string]termRef
	// single maps a severe single-token term with every run of a repeated
	// letter collapsed to one letter, for tokens the sender stretched.
	single map[string]termRef
	// grams is the largest n-gram in exact, at most maxGram.
	grams int
}

// Entry additions: AddTerm is an operator-supplied term. It is folded here, so
// the operator types a word and the index holds its pack form.
type AddTerm struct {
	Term     string
	Category string
	Tier     string
}

func buildTermIndex(p *Pack, add []AddTerm, allow []string, categories map[string]bool) *termIndex {
	ix := &termIndex{
		exact:    map[string]termRef{},
		despaced: map[string]termRef{},
		single:   map[string]termRef{},
		grams:    1,
	}
	suppressed := map[string]bool{}
	for _, a := range allow {
		suppressed[Fold(a)] = true
	}
	put := func(term, category, tier string, except []string) {
		if term == "" || suppressed[term] || !categories[category] {
			return
		}
		ref := termRef{category: category, tier: tier}
		if len(except) > 0 {
			ref.except = make(map[string]bool, len(except))
			for _, x := range except {
				ref.except[x] = true
			}
		}
		if old, ok := ix.exact[term]; ok && !stronger(ref, old) {
			return
		}
		ix.exact[term] = ref
		if n := strings.Count(term, " ") + 1; n > ix.grams && n <= maxGram {
			ix.grams = n
		}
		flat := collapse(strings.ReplaceAll(term, " ", ""))
		if strings.ContainsRune(term, ' ') || utf8.RuneCountInString(flat) >= 4 {
			ix.despaced[flat] = ref
		}
		if category == CategoryHate && tier == TierSevere && !strings.ContainsRune(term, ' ') {
			if s := collapseToOne(term); utf8.RuneCountInString(s) >= 4 {
				ix.single[s] = ref
			}
		}
	}
	if p != nil {
		for _, e := range p.Entries {
			put(e.Term, e.Category, e.Tier, e.Except)
		}
	}
	for _, a := range add {
		tier := a.Tier
		if tier == "" {
			tier = TierSevere
		}
		cat := a.Category
		if cat == "" {
			cat = CategoryHate
		}
		put(Fold(a.Term), cat, tier, nil)
	}
	return ix
}

// stronger reports whether a outranks b: hate over profanity, severe over
// contextual.
func stronger(a, b termRef) bool {
	rank := func(r termRef) int {
		n := 0
		if r.category == CategoryHate {
			n += 2
		}
		if r.tier == TierSevere || r.tier == TierStrong {
			n++
		}
		return n
	}
	return rank(a) > rank(b)
}

// collapseToOne reduces every run of one letter to a single letter.
func collapseToOne(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	var last rune
	for _, r := range s {
		if r == last {
			continue
		}
		last = r
		b.WriteRune(r)
	}
	return b.String()
}

// termHit is what the matcher found.
type termHit struct {
	ref  termRef
	form string // pack, despaced or stretched
}

// lookup finds the strongest term in the message forms, or false.
//
// Order of evidence: a whole-token n-gram of the pack form; then a run of
// single-character tokens joined (the deliberately letter-spaced form); then,
// for severe single-token terms only, a stretched token collapsed to one
// letter. Each later form is a weaker claim, so each is tried only when the
// earlier ones found nothing.
func (ix *termIndex) lookup(f Forms) (termHit, bool) {
	var best termHit
	found := false
	consider := func(ref termRef, form string) {
		if !found || stronger(ref, best.ref) {
			best, found = termHit{ref: ref, form: form}, true
		}
	}

	pack := f.Pack
	pos := 0
	for i := range f.Tokens {
		end := pos
		for n := 1; n <= ix.grams && i+n <= len(f.Tokens); n++ {
			end = pos
			for k := 0; k < n; k++ {
				end += len(f.Tokens[i+k])
				if k > 0 {
					end++
				}
			}
			if ref, ok := ix.exact[pack[pos:end]]; ok {
				consider(ref, "pack")
			}
		}
		pos += len(f.Tokens[i]) + 1
	}
	if found {
		return best, true
	}

	if f.Despaced != "" {
		if ref, ok := ix.despaced[f.Despaced]; ok {
			return termHit{ref: ref, form: "despaced"}, true
		}
	}
	// A letter-spaced stretch inside a longer message: "look at this s l u r".
	for i := 0; i < len(f.Tokens); {
		if utf8.RuneCountInString(f.Tokens[i]) != 1 {
			i++
			continue
		}
		j := i
		var b strings.Builder
		for j < len(f.Tokens) && utf8.RuneCountInString(f.Tokens[j]) == 1 {
			b.WriteString(f.Tokens[j])
			j++
		}
		if j-i >= 3 {
			if ref, ok := ix.despaced[collapse(b.String())]; ok {
				return termHit{ref: ref, form: "despaced"}, true
			}
		}
		i = j
	}

	for i, t := range f.Tokens {
		if i >= len(f.Stretch) || !f.Stretch[i] {
			continue
		}
		if ref, ok := ix.single[collapseToOne(t)]; ok && !ref.except[t] {
			return termHit{ref: ref, form: "stretched"}, true
		}
	}
	return termHit{}, false
}
