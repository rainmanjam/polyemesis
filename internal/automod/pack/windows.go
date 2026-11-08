package pack

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Windowed detectors: the two that need a memory of recent messages. Both are
// bounded (a raid is exactly when unbounded state becomes the denial of
// service) and both take the time as an argument, so they are testable without
// sleeping and the corpus harness can replay a sequence's own offsets.

// ----------------------------------------------------------- similarity

// tokenSet is the distinct pack tokens of a message.
type tokenSet map[string]struct{}

func setOf(toks []string) tokenSet {
	s := make(tokenSet, len(toks))
	for _, t := range toks {
		s[t] = struct{}{}
	}
	return s
}

// jaccard is |a and b| over |a or b|. Two empty sets are not similar: a message
// with no letters says nothing to compare.
func jaccard(a, b tokenSet) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, large := a, b
	if len(small) > len(large) {
		small, large = large, small
	}
	inter := 0
	for t := range small {
		if _, ok := large[t]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// stripDigits removes digits and collapses what is left to single spaces, so
// "buy now 1" and "buy now 2" are one message.
func stripDigits(s string) string {
	var b strings.Builder
	space := true
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			continue
		case unicode.IsSpace(r):
			if !space {
				b.WriteByte(' ')
				space = true
			}
		default:
			b.WriteRune(unicode.ToLower(r))
			space = false
		}
	}
	return strings.TrimSpace(b.String())
}

// ------------------------------------------------------------ near-duplicates

// NearDupConfig bounds the per-author near-duplicate detector.
type NearDupConfig struct {
	// Window is how far back repeats count; MaxRepeats the count above which it
	// fires (so MaxRepeats 3 fires on the fourth).
	Window     time.Duration
	MaxRepeats int
	// Similarity is the token Jaccard at or above which two messages are one.
	Similarity float64
	// MinChars is the shortest pack form that can repeat: "gg" is a reaction.
	MinChars int
	// Retain caps entries per author, MaxAuthors authors in all, IdleEviction
	// how long a quiet author is kept.
	Retain       int
	MaxAuthors   int
	IdleEviction time.Duration
}

// DefaultNearDupConfig mirrors the history checker's repeat limits.
func DefaultNearDupConfig() NearDupConfig {
	return NearDupConfig{
		Window: 30 * time.Second, MaxRepeats: 3, Similarity: 0.8, MinChars: 6,
		Retain: 24, MaxAuthors: 20000, IdleEviction: 10 * time.Minute,
	}
}

type ndEntry struct {
	at     time.Time
	set    tokenSet
	digits string // the digit-stripped message
	// gid names the group of near copies this entry belongs to. Groups are
	// merged as messages arrive, so counting one is a pass over the entries and
	// not a search over their pairs.
	gid int
}

type ndAuthor struct {
	entries []ndEntry
	last    time.Time
	nextGid int
}

// NearDup counts, per author, messages that are near copies of each other: token
// Jaccard at or above the threshold, or identical once digits are stripped
// ("giveaway entry 1", "giveaway entry 2"). Cross-platform identity is not
// linked, so the key is platform plus author id.
type NearDup struct {
	mu      sync.Mutex
	cfg     NearDupConfig
	authors map[string]*ndAuthor
	sweepAt time.Time
}

// NewNearDup returns an empty detector.
func NewNearDup(cfg NearDupConfig) *NearDup {
	d := DefaultNearDupConfig()
	if cfg.Window <= 0 {
		cfg.Window = d.Window
	}
	if cfg.MaxRepeats <= 0 {
		cfg.MaxRepeats = d.MaxRepeats
	}
	if cfg.Similarity <= 0 {
		cfg.Similarity = d.Similarity
	}
	if cfg.MinChars <= 0 {
		cfg.MinChars = d.MinChars
	}
	if cfg.Retain <= 0 {
		cfg.Retain = d.Retain
	}
	if cfg.MaxAuthors <= 0 {
		cfg.MaxAuthors = d.MaxAuthors
	}
	if cfg.IdleEviction <= 0 {
		cfg.IdleEviction = d.IdleEviction
	}
	return &NearDup{cfg: cfg, authors: map[string]*ndAuthor{}}
}

// Observe records a message and reports how many near copies of it the author
// sent inside the window (this one included), and whether that is over the
// limit. A message too short to repeat is recorded as nothing.
func (d *NearDup) Observe(at time.Time, author string, f Forms) (count int, over bool) {
	if d == nil || author == "" || len(f.Pack) < d.cfg.MinChars {
		return 0, false
	}
	set := setOf(f.Tokens)
	digits := stripDigits(f.NFKC)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked(at)

	a := d.authors[author]
	if a == nil {
		a = &ndAuthor{}
		d.authors[author] = a
	}
	cutoff := at.Add(-d.cfg.Window)
	live := a.entries[:0]
	for _, e := range a.entries {
		if e.at.After(cutoff) {
			live = append(live, e)
		}
	}
	a.nextGid++
	a.entries = append(live, ndEntry{at: at, set: set, digits: digits, gid: a.nextGid})
	if len(a.entries) > d.cfg.Retain {
		a.entries = a.entries[len(a.entries)-d.cfg.Retain:]
	}
	a.last = at
	d.capLocked()

	return d.groupLocked(a.entries)
}

// groupLocked joins the newest entry to every retained entry it is a near copy
// of, and sizes the group it ends up in. Two messages are near copies when most
// of their words match or they are the same once digits are removed. A spammer
// who varies one word at a time ("a great product", "a really great product",
// "a great product now") produces copies that each resemble the first but not
// always each other, and joining groups as they arrive counts that as the one
// message repeated.
//
// One comparison per retained entry, once, on arrival: the earlier version
// searched the pairs and cost 48 µs on a chain of 24 near copies, on the adapter
// goroutine. An entry that ages out of the window leaves its group's other
// members joined; that can over-count a chain whose middle has expired, and the
// finding is flag-only.
func (d *NearDup) groupLocked(entries []ndEntry) (count int, over bool) {
	n := len(entries)
	newest := &entries[n-1]
	for i := 0; i < n-1; i++ {
		e := &entries[i]
		if e.gid == newest.gid {
			continue
		}
		if (newest.digits != "" && newest.digits == e.digits) ||
			sizeCompatible(len(newest.set), len(e.set), d.cfg.Similarity) && jaccard(newest.set, e.set) >= d.cfg.Similarity {
			old := e.gid
			for k := range entries {
				if entries[k].gid == old {
					entries[k].gid = newest.gid
				}
			}
		}
	}
	for _, e := range entries {
		if e.gid == newest.gid {
			count++
		}
	}
	return count, count > d.cfg.MaxRepeats
}

// Tracked reports how many authors are held.
func (d *NearDup) Tracked() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.authors)
}

func (d *NearDup) sweepLocked(now time.Time) {
	if now.Before(d.sweepAt) {
		return
	}
	d.sweepAt = now.Add(30 * time.Second)
	cutoff := now.Add(-d.cfg.IdleEviction)
	for k, a := range d.authors {
		if a.last.Before(cutoff) {
			delete(d.authors, k)
		}
	}
}

func (d *NearDup) capLocked() {
	if len(d.authors) <= d.cfg.MaxAuthors {
		return
	}
	type aged struct {
		key  string
		last time.Time
	}
	all := make([]aged, 0, len(d.authors))
	for k, a := range d.authors {
		all = append(all, aged{k, a.last})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].last.Before(all[j].last) })
	for i := 0; i < len(all)-d.cfg.MaxAuthors; i++ {
		delete(d.authors, all[i].key)
	}
}

// ---------------------------------------------------------------------- raid

// RaidConfig is the cross-author copypasta detector (spec section 11).
type RaidConfig struct {
	// Authors is how many distinct authors posting matching text inside Window
	// is a raid. Window is the sliding window.
	Authors int
	Window  time.Duration
	// MinChars is the shortest folded message that can count. "gg", "W", "LUL"
	// and "!play" are crowd hype and command spam, not a raid.
	MinChars int
	// Similarity is the token Jaccard at or above which two messages match.
	Similarity float64
	// Allow lists texts that are never a raid: a chant the community uses.
	// Compared in pack form.
	Allow []string
	// MaxClusters bounds distinct texts tracked, MaxAuthorsPer the authors
	// remembered per text.
	MaxClusters   int
	MaxAuthorsPer int
}

// DefaultRaidConfig is spec section 11's forgiving default: five distinct
// authors in twenty seconds, at least twelve characters.
func DefaultRaidConfig() RaidConfig {
	return RaidConfig{
		Authors: 5, Window: 20 * time.Second, MinChars: 12, Similarity: 0.8,
		MaxClusters: 512, MaxAuthorsPer: 64,
	}
}

type raidSeen struct {
	at     time.Time
	author string
}

type raidCluster struct {
	set     tokenSet
	seen    []raidSeen
	last    time.Time
	firedAt time.Time // zero until it fires
}

// RaidHit describes a raid in progress.
type RaidHit struct {
	// Authors is how many distinct authors have posted this text in the window
	// (frozen at the count that fired, for the messages held afterwards).
	Authors int
	// Names lists up to five participating author ids, for the console flag.
	Names []string
	// Continuing is true for a message held because its text matches a raid
	// already flagged, without being counted again.
	Continuing bool
}

// Raid counts DISTINCT authors, across platforms, posting matching text. It
// keys on a similarity fingerprint (token sets), not an exact string, because a
// raid varies punctuation, casing and look-alike letters.
type Raid struct {
	mu       sync.Mutex
	cfg      RaidConfig
	clusters []*raidCluster
	allow    map[string]bool
}

// NewRaid returns an empty detector. Create it once and keep it: a settings
// save in the middle of a raid must not reset what it has seen.
func NewRaid(cfg RaidConfig) *Raid {
	d := DefaultRaidConfig()
	if cfg.Authors <= 0 {
		cfg.Authors = d.Authors
	}
	if cfg.Window <= 0 {
		cfg.Window = d.Window
	}
	if cfg.MinChars <= 0 {
		cfg.MinChars = d.MinChars
	}
	if cfg.Similarity <= 0 {
		cfg.Similarity = d.Similarity
	}
	if cfg.MaxClusters <= 0 {
		cfg.MaxClusters = d.MaxClusters
	}
	if cfg.MaxAuthorsPer <= 0 {
		cfg.MaxAuthorsPer = d.MaxAuthorsPer
	}
	r := &Raid{cfg: cfg, allow: map[string]bool{}}
	for _, a := range cfg.Allow {
		if f := Fold(a); f != "" {
			r.allow[f] = true
		}
	}
	return r
}

// Observe records a message from author (a platform-qualified id) and reports a
// raid when enough distinct authors have posted matching text. text and
// platformEmotes tell it whether the message is only emotes; a command ("!play")
// or an emote-only line never counts.
func (r *Raid) Observe(at time.Time, author string, f Forms, platformEmotes int) (RaidHit, bool) {
	if r == nil || author == "" || len(f.Pack) < r.cfg.MinChars || f.NFKC == "" {
		return RaidHit{}, false
	}
	if isCommand(f.NFKC) || emoteOnly(f.NFKC, platformEmotes) || r.allow[f.Pack] {
		return RaidHit{}, false
	}
	set := setOf(f.Tokens)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(at)

	var cl *raidCluster
	for i := len(r.clusters) - 1; i >= 0; i-- {
		c := r.clusters[i]
		if sizeCompatible(len(set), len(c.set), r.cfg.Similarity) && jaccard(set, c.set) >= r.cfg.Similarity {
			cl = c
			break
		}
	}
	if cl == nil {
		cl = &raidCluster{set: set, last: at}
		r.clusters = append(r.clusters, cl)
		if len(r.clusters) > r.cfg.MaxClusters {
			r.evictLocked()
		}
	}
	cl.last = at

	// Already flagged and still inside its window: hold without recounting.
	if !cl.firedAt.IsZero() && at.Sub(cl.firedAt) <= r.cfg.Window {
		return RaidHit{Authors: distinct(cl.seen), Names: names(cl.seen), Continuing: true}, true
	}
	cl.firedAt = time.Time{}

	cl.seen = append(cl.seen, raidSeen{at: at, author: author})
	cutoff := at.Add(-r.cfg.Window)
	live := cl.seen[:0]
	for _, s := range cl.seen {
		if s.at.After(cutoff) {
			live = append(live, s)
		}
	}
	cl.seen = live
	if len(cl.seen) > r.cfg.MaxAuthorsPer {
		cl.seen = cl.seen[len(cl.seen)-r.cfg.MaxAuthorsPer:]
	}
	if n := distinct(cl.seen); n >= r.cfg.Authors {
		cl.firedAt = at
		return RaidHit{Authors: n, Names: names(cl.seen)}, true
	}
	return RaidHit{}, false
}

// Tracked reports how many distinct texts are held.
func (r *Raid) Tracked() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.clusters)
}

// sizeCompatible is the cheap precheck for Jaccard: sets whose sizes differ by
// more than the threshold allows cannot match.
func sizeCompatible(a, b int, sim float64) bool {
	if a == 0 || b == 0 {
		return false
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	return float64(lo)/float64(hi) >= sim
}

func (r *Raid) pruneLocked(now time.Time) {
	live := r.clusters[:0]
	for _, c := range r.clusters {
		if now.Sub(c.last) <= r.cfg.Window {
			live = append(live, c)
		}
	}
	for i := len(live); i < len(r.clusters); i++ {
		r.clusters[i] = nil
	}
	r.clusters = live
}

// evictLocked drops the least recently seen cluster, the one nearest expiry.
func (r *Raid) evictLocked() {
	oldest := 0
	for i, c := range r.clusters {
		if c.last.Before(r.clusters[oldest].last) {
			oldest = i
		}
	}
	r.clusters = append(r.clusters[:oldest], r.clusters[oldest+1:]...)
}

func distinct(seen []raidSeen) int {
	m := make(map[string]struct{}, len(seen))
	for _, s := range seen {
		m[s.author] = struct{}{}
	}
	return len(m)
}

func names(seen []raidSeen) []string {
	m := map[string]bool{}
	var out []string
	for _, s := range seen {
		if !m[s.author] && len(out) < 5 {
			m[s.author] = true
			out = append(out, s.author)
		}
	}
	return out
}

// Reason is the operator-facing sentence for a raid hit: the count and the
// window, never the text.
func (h RaidHit) Reason(window time.Duration) string {
	return fmt.Sprintf("the same text from %d accounts in %s", h.Authors, window)
}
