package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
)

// A term is rejected, and counted under a reason, rather than stored in a form
// that would match the wrong thing. The reasons are the only per-term output the
// generator produces.
const (
	rejectShort  = "too short (under 3 letters)"
	rejectDigits = "digits that are not leetspeak"
	rejectToks   = "more than 4 tokens"
	rejectSingle = "a single-letter token"
	rejectEmpty  = "empty after folding"
)

const (
	maxTokens  = 4 // phrases match as token n-grams of up to 4 tokens
	minLetters = 3
	maxStars   = 5 // `c*` doubles the variants; this caps 2^n
)

type stats struct {
	dsoEntries, ldLines int
	byCatTier           map[string]int
	rejected            map[string]int
	dupes               int
	crossHate           int // LDNOOBW terms already present as hate terms
	exceptKept          int
	exceptDropped       int
}

func (s stats) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "sources: dsojevic %d entries, LDNOOBW %d lines\n", s.dsoEntries, s.ldLines)
	keys := make([]string, 0, len(s.byCatTier))
	for k := range s.byCatTier {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-24s %d\n", k, s.byCatTier[k])
	}
	rk := make([]string, 0, len(s.rejected))
	for k := range s.rejected {
		rk = append(rk, k)
	}
	sort.Strings(rk)
	for _, k := range rk {
		fmt.Fprintf(&b, "  rejected: %-34s %d\n", k, s.rejected[k])
	}
	fmt.Fprintf(&b, "  duplicates merged %d, LDNOOBW already hate %d, exceptions kept %d dropped %d\n",
		s.dupes, s.crossHate, s.exceptKept, s.exceptDropped)
	return b.String()
}

type dsoEntry struct {
	ID         string   `json:"id"`
	Match      string   `json:"match"`
	Tags       []string `json:"tags"`
	Severity   int      `json:"severity"`
	Exceptions []string `json:"exceptions"`
}

type override struct {
	Tier   map[string]string   `yaml:"tier"`
	Drop   []string            `yaml:"drop"`
	Except map[string][]string `yaml:"except"`
	Add    []struct {
		Term     string   `yaml:"term"`
		Category string   `yaml:"category"`
		Tier     string   `yaml:"tier"`
		Tags     []string `yaml:"tags"`
	} `yaml:"add"`
}

// generate reads the committed source snapshots (genDir/sources) and the
// maintainers' overrides (packDir/overrides.yaml) and returns the bytes of
// en-v1.json.
func generate(genDir, packDir string) ([]byte, stats, error) {
	st := stats{byCatTier: map[string]int{}, rejected: map[string]int{}}
	p, err := loadPins(filepath.Join(genDir, "pins.yaml"))
	if err != nil {
		return nil, st, err
	}

	// Every snapshot must be what the pin says it is, so a hand-edited source
	// file can't pass for upstream.
	for _, s := range p.Sources {
		for _, f := range s.Files {
			b, err := os.ReadFile(filepath.Join(genDir, "sources", s.Dir, f.Path))
			if err != nil {
				return nil, st, err
			}
			if got := hashHex(b); got != f.SHA256 {
				return nil, st, fmt.Errorf("sources/%s/%s: sha256 %s does not match its pin %s", s.Dir, f.Path, got, f.SHA256)
			}
		}
	}

	var ov override
	ob, err := os.ReadFile(filepath.Join(packDir, "overrides.yaml"))
	if err != nil {
		return nil, st, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(ob))
	dec.KnownFields(true)
	if err := dec.Decode(&ov); err != nil {
		return nil, st, fmt.Errorf("overrides.yaml: %w", err)
	}

	raw, err := os.ReadFile(filepath.Join(genDir, "sources", "dsojevic-profanity-list", "en.json"))
	if err != nil {
		return nil, st, err
	}
	var dso []dsoEntry
	if err := json.Unmarshal(raw, &dso); err != nil {
		return nil, st, fmt.Errorf("dsojevic en.json: %w", err)
	}
	st.dsoEntries = len(dso)

	byTerm := map[string]*pack.Entry{}
	put := func(e pack.Entry) {
		if old, ok := byTerm[e.Term]; ok {
			st.dupes++
			// Hate wins over profanity; otherwise the first writer stays.
			if e.Category == pack.CategoryHate && old.Category != pack.CategoryHate {
				*old = e
			}
			return
		}
		cp := e
		byTerm[e.Term] = &cp
	}

	for _, d := range dso {
		cat, tier, tags := classify(d)
		alts := strings.Split(strings.ToLower(d.Match), "|")
		for _, alt := range alts {
			for _, v := range expandStars(alt) {
				term, why := foldTerm(v)
				if why != "" {
					st.rejected[why]++
					continue
				}
				e := pack.Entry{Term: term, Category: cat, Tier: tier, Tags: tags}
				// Exceptions are written for partial matching, with `*` standing
				// in for the matched word. Keep only the single whole tokens
				// that contain the term, which is all whole-token matching can
				// use.
				for _, x := range d.Exceptions {
					xf := pack.Fold(strings.ReplaceAll(strings.ToLower(x), "*", v))
					if xf == "" || xf == term || strings.ContainsRune(xf, ' ') || !strings.Contains(xf, term) {
						st.exceptDropped++
						continue
					}
					e.Except = append(e.Except, xf)
				}
				put(e)
			}
		}
	}

	ld, err := os.ReadFile(filepath.Join(genDir, "sources", "ldnoobw", "en"))
	if err != nil {
		return nil, st, err
	}
	for _, line := range strings.Split(string(ld), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\xef\xbb\xbf"))
		if line == "" {
			continue
		}
		st.ldLines++
		term, why := foldTerm(strings.ToLower(line))
		if why != "" {
			st.rejected[why]++
			continue
		}
		if old, ok := byTerm[term]; ok {
			if old.Category == pack.CategoryHate {
				st.crossHate++
			}
			st.dupes++
			continue
		}
		byTerm[term] = &pack.Entry{Term: term, Category: pack.CategoryProfanity, Tier: pack.TierMild, Tags: []string{pack.TagSwearing}}
	}

	// Maintainers' overrides last. An override that matches nothing is an error.
	for term, tier := range ov.Tier {
		e, ok := byTerm[term]
		if !ok {
			return nil, st, fmt.Errorf("overrides.yaml: tier: no term %q", term)
		}
		e.Tier = tier
	}
	for term, ex := range ov.Except {
		e, ok := byTerm[term]
		if !ok {
			return nil, st, fmt.Errorf("overrides.yaml: except: no term %q", term)
		}
		e.Except = append(e.Except, ex...)
	}
	for _, term := range ov.Drop {
		if _, ok := byTerm[term]; !ok {
			return nil, st, fmt.Errorf("overrides.yaml: drop: no term %q", term)
		}
		delete(byTerm, term)
	}
	for _, a := range ov.Add {
		term := pack.Fold(a.Term)
		if term == "" || term != a.Term {
			return nil, st, fmt.Errorf("overrides.yaml: add: a term must be in pack form")
		}
		if _, ok := byTerm[term]; ok {
			return nil, st, fmt.Errorf("overrides.yaml: add: %q is already in the pack", term)
		}
		byTerm[term] = &pack.Entry{Term: term, Category: a.Category, Tier: a.Tier, Tags: a.Tags}
	}

	out := pack.Pack{ID: "en", Version: 1}
	for _, s := range p.Sources {
		name := s.Name
		out.Sources = append(out.Sources, pack.Source{Name: name, URL: s.URL, Licence: s.Licence, SHA: s.SHA})
	}
	for _, e := range byTerm {
		e.Except = sortedUnique(e.Except)
		st.exceptKept += len(e.Except)
		if e.Tags == nil {
			e.Tags = []string{}
		}
		out.Entries = append(out.Entries, *e)
		st.byCatTier[e.Category+"/"+e.Tier]++
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		return a.Term < b.Term
	})
	if err := out.Validate(); err != nil {
		return nil, st, err
	}
	b, err := out.Marshal()
	return b, st, err
}

// classify decides a dsojevic entry's category, tier and sub-tags.
//
// Hate is what the spec says: racial, lgbtq or religious at severity 3 or more.
// Its sub-tags reuse Twitch's AutoMod category names so the platform level
// one-click can say which Twitch category each pack tag matches. Everything
// else is profanity, which ships off.
func classify(d dsoEntry) (cat, tier string, tags []string) {
	hate := false
	seen := map[string]bool{}
	for _, t := range d.Tags {
		var m string
		switch t {
		case "racial", "religious":
			m = pack.TagRaceEthnicityReligion
		case "lgbtq":
			m = pack.TagSexualitySexGender
		case "sexual":
			m = pack.TagSexBased
		default: // general, shock
			m = pack.TagSwearing
		}
		if !seen[m] {
			seen[m] = true
			tags = append(tags, m)
		}
		if (t == "racial" || t == "religious" || t == "lgbtq") && d.Severity >= 3 {
			hate = true
		}
	}
	if len(tags) == 0 {
		tags = []string{pack.TagSwearing}
	}
	sort.Strings(tags)
	if !hate {
		tier = pack.TierMild
		if d.Severity >= 3 {
			tier = pack.TierStrong
		}
		return pack.CategoryProfanity, tier, tags
	}
	tier = pack.TierContextual
	if d.Severity >= 4 || (d.Severity >= 3 && hasTag(d.Tags, "racial")) {
		tier = pack.TierSevere
	}
	return pack.CategoryHate, tier, tags
}

func hasTag(tags []string, t string) bool {
	for _, x := range tags {
		if x == t {
			return true
		}
	}
	return false
}

// expandStars expands dsojevic's `c*` ("the previous character appears one or
// more times"). The pack form collapses runs to two, so one or more is exactly
// the two variants c and cc.
func expandStars(s string) []string {
	rs := []rune(s)
	out := []string{""}
	stars := 0
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '*' {
			continue // a star is consumed with the character before it
		}
		starred := i+1 < len(rs) && rs[i+1] == '*' && stars < maxStars
		if starred {
			stars++
			next := make([]string, 0, len(out)*2)
			for _, o := range out {
				next = append(next, o+string(r), o+string(r)+string(r))
			}
			out = next
			continue
		}
		for k := range out {
			out[k] += string(r)
		}
	}
	return out
}

// foldTerm folds a raw term to pack form, or says why it can't be stored.
func foldTerm(raw string) (string, string) {
	if strings.ContainsAny(raw, "2689") {
		return "", rejectDigits
	}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !strings.ContainsFunc(f, unicode.IsLetter) {
			return "", rejectDigits
		}
	}
	t := pack.Fold(raw)
	if t == "" {
		return "", rejectEmpty
	}
	toks := strings.Fields(t)
	if len(toks) > maxTokens {
		return "", rejectToks
	}
	letters := 0
	for _, k := range toks {
		n := utf8.RuneCountInString(k)
		if n == 1 {
			return "", rejectSingle
		}
		letters += n
	}
	if letters < minLetters {
		return "", rejectShort
	}
	return t, ""
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	sort.Strings(in)
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
