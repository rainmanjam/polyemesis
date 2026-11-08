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
	if a == b {
		return false
	}
	ra, rb := []rune(a), []rune(b)
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
		return string(ra[i+1:]) == string(rb[i+1:])
	}
	return string(ra[i+1:]) == string(rb[i:])
}

// protectedName is one account name that an impostor might copy.
type protectedName struct {
	raw    string // lower-cased NFKC, for the "is it the same account" test
	folded string
}

func newProtectedName(name string) (protectedName, bool) {
	raw := strings.ToLower(NFKC(strings.TrimSpace(name)))
	f := foldName(name)
	if utf8.RuneCountInString(f) < 3 {
		return protectedName{}, false
	}
	return protectedName{raw: raw, folded: f}, true
}

// NameSet is a bounded, concurrency-safe set of protected names, with the
// operator's own list kept apart from the names learned at runtime.
//
// Learned names (anyone seen with the broadcaster or moderator flag) live in
// memory only and are capped. They are deliberately not written to settings: a
// runtime read-modify-write of the settings blob would race the operator's
// own PUT /settings.
type NameSet struct {
	mu      sync.RWMutex
	static  []protectedName
	learned map[string]protectedName
	order   []string // insertion order, for eviction
	max     int
}

// DefaultMaxLearnedNames bounds the learned set.
const DefaultMaxLearnedNames = 500

// NewNameSet returns a set holding the operator's own protected names.
func NewNameSet(static []string) *NameSet {
	ns := &NameSet{learned: map[string]protectedName{}, max: DefaultMaxLearnedNames}
	ns.SetStatic(static)
	return ns
}

// SetStatic replaces the operator's own list. Safe to call while checking.
func (n *NameSet) SetStatic(names []string) {
	var out []protectedName
	for _, s := range names {
		if p, ok := newProtectedName(s); ok {
			out = append(out, p)
		}
	}
	n.mu.Lock()
	n.static = out
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
		delete(n.learned, n.order[0])
		n.order = n.order[1:]
	}
	n.learned[p.raw] = p
	n.order = append(n.order, p.raw)
}

// Len reports how many names are protected.
func (n *NameSet) Len() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.static) + len(n.learned)
}

// match finds a protected name the author's name imitates. The second arm,
// edit distance one on names of six or more characters, needs a signal
// (a link, a handle or a scam hit) because a fan's name is often one letter off.
func (n *NameSet) match(author string, signal func() bool) (detector, who string, ok bool) {
	raw := strings.ToLower(NFKC(strings.TrimSpace(author)))
	f := foldName(author)
	if raw == "" || utf8.RuneCountInString(f) < 3 {
		return "", "", false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	edit := false
	var editWho string
	check := func(p protectedName) bool {
		if p.raw == raw {
			return false // the real account, or the same name on another platform
		}
		if p.folded == f {
			detector, who = "fold", p.raw
			return true
		}
		if utf8.RuneCountInString(f) >= 6 && utf8.RuneCountInString(p.folded) >= 6 && editDistanceOne(p.folded, f) {
			edit, editWho = true, p.raw
		}
		return false
	}
	for _, p := range n.static {
		if check(p) {
			return detector, who, true
		}
	}
	for _, p := range n.learned {
		if check(p) {
			return detector, who, true
		}
	}
	if edit && signal() {
		return "edit1", editWho, true
	}
	return "", "", false
}
