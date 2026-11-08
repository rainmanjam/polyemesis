package pack

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// Impersonation (spec section 3.5): an author whose name folds onto a protected
// name (the broadcaster's, a moderator's) while the raw names differ.

// impostorWords are decorations an impersonator adds to a name.
var impostorWords = []string{"official", "real", "support", "live", "tv"}

// foldName reduces an account name to what it is compared on: the confusables
// skeleton of its lower-cased NFKC form with separators removed (Name), then
// the decorations impersonators add stripped from either end, and any trailing
// digits ("streamer_2").
func foldName(s string) string {
	n := Name(s)
	for {
		before := n
		n = strings.TrimRight(n, "0123456789")
		for _, w := range impostorWords {
			if len(n) > len(w)+2 {
				n = strings.TrimPrefix(n, w)
				n = strings.TrimSuffix(n, w)
			}
		}
		if n == before {
			return n
		}
	}
}

// editDistanceOne reports a and b differ by exactly one insertion, deletion or
// substitution.
func editDistanceOne(a, b string) bool {
	return editDistanceOneRunes([]rune(a), []rune(b))
}

// editDistanceOneRunes is editDistanceOne on rune slices that are already
// converted, so a scan over many names converts each name once, not once per
// comparison. It allocates nothing.
func editDistanceOneRunes(ra, rb []rune) bool {
	if d := len(ra) - len(rb); d > 1 || d < -1 {
		return false
	}
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	i := 0
	for i < len(rb) && ra[i] == rb[i] {
		i++
	}
	if len(ra) == len(rb) {
		if i == len(rb) {
			return false // identical
		}
		return runesEqual(ra[i+1:], rb[i+1:])
	}
	return runesEqual(ra[i+1:], rb[i:])
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// protectedName is one account name that an impostor might copy.
type protectedName struct {
	raw    string // lower-cased NFKC, for the "is it the same account" test
	folded string
	runes  []rune // folded, converted once
}

func newProtectedName(name string) (protectedName, bool) {
	raw := strings.ToLower(NFKC(strings.TrimSpace(name)))
	f := foldName(name)
	if utf8.RuneCountInString(f) < 3 {
		return protectedName{}, false
	}
	return protectedName{raw: raw, folded: f, runes: []rune(f)}, true
}

// nameIndex finds protected names by what a match needs, so checking an author
// does not walk every name: byFold answers the exact-fold arm in one lookup, and
// byLen holds the names long enough for the edit-distance arm keyed by folded
// length (a name one edit away is one rune shorter, equal or one longer).
type nameIndex struct {
	byFold map[string][]protectedName
	byLen  map[int][]protectedName
}

func newNameIndex() nameIndex {
	return nameIndex{byFold: map[string][]protectedName{}, byLen: map[int][]protectedName{}}
}

func (x *nameIndex) add(p protectedName) {
	x.byFold[p.folded] = append(x.byFold[p.folded], p)
	if n := len(p.runes); n >= minEditRunes {
		x.byLen[n] = append(x.byLen[n], p)
	}
}

func (x *nameIndex) remove(p protectedName) {
	x.byFold[p.folded] = dropRaw(x.byFold[p.folded], p.raw)
	if len(x.byFold[p.folded]) == 0 {
		delete(x.byFold, p.folded)
	}
	n := len(p.runes)
	x.byLen[n] = dropRaw(x.byLen[n], p.raw)
	if len(x.byLen[n]) == 0 {
		delete(x.byLen, n)
	}
}

func dropRaw(s []protectedName, raw string) []protectedName {
	out := s[:0:0]
	for _, p := range s {
		if p.raw != raw {
			out = append(out, p)
		}
	}
	return out
}

// minEditRunes is the shortest folded name the edit-distance arm compares: a
// fan's name is often one letter off a short one.
const minEditRunes = 6

// NameSet is a bounded, concurrency-safe set of protected names, with the
// operator's own list kept apart from the names learned at runtime.
//
// Learned names (anyone seen with the broadcaster or moderator flag) live in
// memory only and are capped. They are deliberately not written to settings: a
// runtime read-modify-write of the settings blob would race the operator's
// own PUT /settings.
type NameSet struct {
	mu      sync.RWMutex
	static  nameIndex
	nstatic int
	learned map[string]protectedName
	lidx    nameIndex
	order   []string // insertion order, for eviction
	max     int
}

// DefaultMaxLearnedNames bounds the learned set.
const DefaultMaxLearnedNames = 500

// NewNameSet returns a set holding the operator's own protected names.
func NewNameSet(static []string) *NameSet {
	ns := &NameSet{learned: map[string]protectedName{}, lidx: newNameIndex(), max: DefaultMaxLearnedNames}
	ns.SetStatic(static)
	return ns
}

// SetStatic replaces the operator's own list. Safe to call while checking.
func (n *NameSet) SetStatic(names []string) {
	idx, count := newNameIndex(), 0
	for _, s := range names {
		if p, ok := newProtectedName(s); ok {
			idx.add(p)
			count++
		}
	}
	n.mu.Lock()
	n.static, n.nstatic = idx, count
	n.mu.Unlock()
}

// Learn adds a name seen on a broadcaster or moderator. The oldest learned name
// is dropped at the cap.
func (n *NameSet) Learn(name string) {
	p, ok := newProtectedName(name)
	if !ok {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.learned[p.raw]; ok {
		return
	}
	if n.max > 0 && len(n.order) >= n.max {
		oldest := n.order[0]
		n.lidx.remove(n.learned[oldest])
		delete(n.learned, oldest)
		n.order = n.order[1:]
	}
	n.learned[p.raw] = p
	n.lidx.add(p)
	n.order = append(n.order, p.raw)
}

// Len reports how many names are protected.
func (n *NameSet) Len() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.nstatic + len(n.learned)
}

// match finds a protected name the author's name imitates. The second arm,
// edit distance one on names of six or more characters, needs a signal
// (a link, a handle or a scam hit) because a fan's name is often one letter off.
//
// Cost does not grow with the number of names: the exact arm is a map lookup
// and the edit arm looks only at names of a length one edit could reach.
func (n *NameSet) match(author string, signal func() bool) (detector, who string, ok bool) {
	raw := strings.ToLower(NFKC(strings.TrimSpace(author)))
	f := foldName(author)
	fr := []rune(f)
	if raw == "" || len(fr) < 3 {
		return "", "", false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	// The operator's own names first, then the learned ones, as before.
	for _, idx := range []*nameIndex{&n.static, &n.lidx} {
		for _, p := range idx.byFold[f] {
			if p.raw != raw { // the real account, or the same name on another platform
				return "fold", p.raw, true
			}
		}
	}
	if len(fr) < minEditRunes {
		return "", "", false
	}
	editWho := ""
	for _, idx := range []*nameIndex{&n.static, &n.lidx} {
		for l := len(fr) - 1; l <= len(fr)+1; l++ {
			for _, p := range idx.byLen[l] {
				if p.raw != raw && editDistanceOneRunes(p.runes, fr) {
					editWho = p.raw
				}
			}
		}
	}
	if editWho != "" && signal() {
		return "edit1", editWho, true
	}
	return "", "", false
}
