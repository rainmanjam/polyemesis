package pack

// The labelled fixture corpus and its harness.
//
// This file loads testdata/corpus-en.jsonl, testdata/corpus-en.positive.jsonl and
// testdata/sequences.jsonl, validates their shape, and holds the scorer that the
// detector PR plugs its checker into (corpusUnderTest, corpusSeqUnderTest). It asserts nothing about
// detection results: the only "detector" it runs is a perfect oracle and two
// degenerate ones, to prove the scorer and the targets file are wired correctly.
//
// The positive file holds offensive language. Failure messages therefore report
// file, line number and group only, never a line's text.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"text/tabwriter"
)

// ---------------------------------------------------------------- data model

type corpusLine struct {
	Text     string `json:"text"`
	Category string `json:"category"`
	Group    string `json:"group,omitempty"`
	Expect   string `json:"expect"`
	Note     string `json:"note"`
	Slice    string `json:"slice"`
	Tier     string `json:"tier,omitempty"`
	Detector string `json:"detector,omitempty"`
	Obf      string `json:"obf,omitempty"`
	Signal   string `json:"signal,omitempty"`
	Jev      string `json:"jev,omitempty"`
	Author   string `json:"author,omitempty"`
	Platform string `json:"platform,omitempty"`
	Protects string `json:"protects,omitempty"`
	Lang     string `json:"lang,omitempty"`
	Lists    string `json:"lists,omitempty"`

	file string
	n    int
}

type seqMessage struct {
	Author   string `json:"author"`
	Platform string `json:"platform"`
	OffsetMs int    `json:"offsetMs"`
	Text     string `json:"text"`
	Role     string `json:"role,omitempty"`
}

type corpusSeq struct {
	ID       string       `json:"id"`
	Category string       `json:"category"`
	Group    string       `json:"group"`
	Expect   string       `json:"expect"`
	Kind     string       `json:"kind"`
	Note     string       `json:"note"`
	FireAt   *int         `json:"fireAt,omitempty"`
	Messages []seqMessage `json:"messages"`

	n int
}

type groupTarget struct {
	Category       string  `json:"category"`
	Kind           string  `json:"kind,omitempty"`
	PositiveExpect string  `json:"positiveExpect"`
	Precision      float64 `json:"precision"`
	Recall         float64 `json:"recall"`
	RecallScope    string  `json:"recallScope,omitempty"`
	MinPositives   int     `json:"minPositives"`
	MinScopePos    int     `json:"minScopePositives,omitempty"`
	Source         string  `json:"source"`
}

type corpusTargets struct {
	Version           int            `json:"version"`
	Spec              string         `json:"spec"`
	Files             []string       `json:"files"`
	ProtectedNames    string         `json:"protectedNames"`
	BenignHoldCeiling float64        `json:"benignHoldCeiling"`
	BenignSlices      []string       `json:"benignSlices"`
	ZeroHoldSlices    []string       `json:"zeroHoldSlices"`
	MinLines          int            `json:"minLines"`
	MinSlice          map[string]int `json:"minSlice"`
	MinCategory       map[string]int `json:"minCategoryPositives"`
	// HardCaseRecall is the least share of the lines Jev should flag that each
	// hard-case signal must catch; HardCaseBenignCeiling the most benign lines
	// that may show any signal.
	HardCaseRecall        map[string]float64     `json:"hardcaseRecall"`
	HardCaseBenignCeiling float64                `json:"hardcaseBenignCeiling"`
	Groups                map[string]groupTarget `json:"groups"`
	Sequences             struct {
		File            string `json:"file"`
		MinSequences    int    `json:"minSequences"`
		MinPassSequence int    `json:"minPassSequences"`
	} `json:"sequences"`
	Semantics map[string]any `json:"semantics"`
	Notes     []string       `json:"notes"`
}

var (
	validCategories = map[string]bool{"none": true, "hate_speech": true, "doxxing": true, "scam": true, "flood": true, "impersonation": true}
	validExpect     = map[string]bool{"hold": true, "flag": true, "pass": true}
	validSlices     = map[string]bool{"benign": true, "scunthorpe": true, "non_english": true, "number_trap": true, "hype_trap": true, "positive": true, "hardcase": true, "protected_name_trap": true}
	validSignals    = map[string]bool{"contextual_hate": true, "address_violence": true, "obfuscation_residue": true, "self_harm": true, "sexual_address": true, "insult_reply": true}
	validJev        = map[string]bool{"none": true, "hate_speech": true, "harassment": true, "threat": true, "sexual_content": true, "self_harm": true, "spam": true, "other": true}
	validSeqCats    = map[string]bool{"raid": true, "spam_near_duplicate": true}
)

// ------------------------------------------------------------------- loading

func loadTargets(t testing.TB) corpusTargets {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "targets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tg corpusTargets
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tg); err != nil {
		t.Fatalf("targets.json: %v", err)
	}
	return tg
}

// readJSONL calls fn for every non-header line. A header record, `{"_header": ...}`,
// is accepted on line 1 only. Errors name the file and line, never the text.
func readJSONL(t testing.TB, name string, fn func(n int, raw []byte)) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		raw := sc.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			t.Errorf("%s:%d: blank line", name, n)
			continue
		}
		if bytes.HasPrefix(raw, []byte(`{"_header"`)) {
			if n != 1 {
				t.Errorf("%s:%d: header record is only allowed on line 1", name, n)
			}
			continue
		}
		fn(n, raw)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if n == 0 {
		t.Fatalf("%s: empty", name)
	}
}

func strictDecode(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func loadCorpus(t testing.TB) []corpusLine {
	t.Helper()
	tg := loadTargets(t)
	var out []corpusLine
	for _, name := range tg.Files {
		readJSONL(t, name, func(n int, raw []byte) {
			var l corpusLine
			if err := strictDecode(raw, &l); err != nil {
				// the decoder error can quote the offending key, never the text value
				t.Errorf("%s:%d: %v", name, n, err)
				return
			}
			l.file, l.n = name, n
			out = append(out, l)
		})
	}
	return out
}

func loadSequences(t testing.TB) []corpusSeq {
	t.Helper()
	tg := loadTargets(t)
	var out []corpusSeq
	readJSONL(t, tg.Sequences.File, func(n int, raw []byte) {
		var s corpusSeq
		if err := strictDecode(raw, &s); err != nil {
			t.Errorf("%s:%d: %v", tg.Sequences.File, n, err)
			return
		}
		s.n = n
		out = append(out, s)
	})
	return out
}

func where(l corpusLine) string { return fmt.Sprintf("%s:%d (group %q)", l.file, l.n, l.Group) }

// ----------------------------------------------------------------- pack terms

type packTerms struct {
	byTier map[string][]string // hate_speech tier -> terms
	all    []string            // every hate_speech term
	except map[string]bool
}

func hateTerms(t testing.TB) packTerms {
	t.Helper()
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	pt := packTerms{byTier: map[string][]string{}, except: map[string]bool{}}
	for _, e := range p.Entries {
		for _, x := range e.Except {
			pt.except[x] = true
		}
		if e.Category != "hate_speech" {
			continue
		}
		pt.byTier[e.Tier] = append(pt.byTier[e.Tier], e.Term)
		pt.all = append(pt.all, e.Term)
	}
	return pt
}

// hasTerm reports whether any of terms is a whole-token n-gram of f.Pack, or
// equals f.Despaced (the deliberately letter-spaced form).
func hasTerm(f Forms, terms []string) bool {
	padded := " " + f.Pack + " "
	for _, term := range terms {
		if strings.Contains(padded, " "+term+" ") {
			return true
		}
		if f.Despaced != "" && f.Despaced == term {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------- shape

func TestCorpusShape(t *testing.T) {
	tg := loadTargets(t)
	lines := loadCorpus(t)
	seen := map[[3]string]string{}
	groupPos := map[string]int{}
	groupScope := map[string]int{}
	slice := map[string]int{}
	catPos := map[string]int{}
	for _, l := range lines {
		w := where(l)
		if l.Text == "" || l.Note == "" {
			t.Errorf("%s: empty text or note", w)
		}
		if strings.TrimSpace(l.Text) != l.Text {
			t.Errorf("%s: text has leading or trailing space", w)
		}
		if !validCategories[l.Category] {
			t.Errorf("%s: unknown category %q", w, l.Category)
		}
		if !validExpect[l.Expect] {
			t.Errorf("%s: unknown expect %q", w, l.Expect)
		}
		if !validSlices[l.Slice] {
			t.Errorf("%s: unknown slice %q", w, l.Slice)
		}
		key := [3]string{l.Text, l.Author, l.Platform}
		if prev, dup := seen[key]; dup {
			t.Errorf("%s: duplicate of %s", w, prev)
		}
		seen[key] = fmt.Sprintf("%s:%d", l.file, l.n)
		slice[l.Slice]++

		if l.Group == "" {
			if l.Category != "none" || l.Expect != "pass" {
				t.Errorf("%s: a line with no group must be category none, expect pass", w)
			}
		} else {
			g, ok := tg.Groups[l.Group]
			switch {
			case !ok:
				t.Errorf("%s: group is not in targets.json", w)
			case g.Category != l.Category && !(g.Category == "flood" && l.Category == "flood"):
				t.Errorf("%s: group belongs to category %q, line says %q", w, g.Category, l.Category)
			case g.Kind == "sequence":
				t.Errorf("%s: sequence group on a single-line record", w)
			}
		}
		if l.Expect != "pass" {
			g := tg.Groups[l.Group]
			if l.Expect != g.PositiveExpect {
				t.Errorf("%s: expect %q, but the group's spec default is %q", w, l.Expect, g.PositiveExpect)
			}
			groupPos[l.Group]++
			catPos[l.Category]++
			if g.RecallScope == "obfuscated" && l.Obf != "" {
				groupScope[l.Group]++
			}
		}
		if l.Slice == "positive" && l.Expect == "pass" {
			t.Errorf("%s: positive slice with expect pass", w)
		}
		if l.Slice != "positive" && l.Slice != "hardcase" && l.Expect != "pass" && l.Group != "flood/emote" && l.Group != "flood/caps" {
			t.Errorf("%s: only positives, hardcases and flag-only flood traps may expect %q (slice %q)", w, l.Expect, l.Slice)
		}
		if l.Slice == "hardcase" {
			if !validSignals[l.Signal] || !validJev[l.Jev] {
				t.Errorf("%s: hardcase needs a valid signal and jev (%q, %q)", w, l.Signal, l.Jev)
			}
			if l.Signal != "contextual_hate" && l.Expect != "pass" {
				t.Errorf("%s: only contextual_hate hard cases are caught by the pack alone", w)
			}
		} else if l.Signal != "" || l.Jev != "" {
			t.Errorf("%s: signal/jev outside the hardcase slice", w)
		}
		if l.Category == "impersonation" {
			if l.Author == "" || l.Platform == "" {
				t.Errorf("%s: impersonation lines need author and platform", w)
			}
		}
		if l.Group == "hate_speech/severe" && l.Slice == "positive" && l.Tier != "severe" {
			t.Errorf("%s: severe positive without tier", w)
		}
		if l.Group == "hate_speech/contextual" && l.Expect != "pass" && l.Tier != "contextual" {
			t.Errorf("%s: contextual positive without tier", w)
		}
	}
	// positive file holds only hate_speech; the main file holds none of it
	for _, l := range lines {
		isPosFile := l.file == "corpus-en.positive.jsonl"
		isHate := strings.HasPrefix(l.Group, "hate_speech/") && l.Expect != "pass"
		if isPosFile != isHate {
			t.Errorf("%s: hate positives belong in corpus-en.positive.jsonl, and only they", where(l))
		}
	}
	if len(lines) < tg.MinLines {
		t.Errorf("corpus has %d lines, want at least %d", len(lines), tg.MinLines)
	}
	for s, min := range tg.MinSlice {
		if slice[s] < min {
			t.Errorf("slice %q has %d lines, want at least %d", s, slice[s], min)
		}
	}
	for c, min := range tg.MinCategory {
		if catPos[c] < min {
			t.Errorf("category %q has %d positives, want at least %d", c, catPos[c], min)
		}
	}
	for name, g := range tg.Groups {
		if g.Kind == "sequence" {
			continue
		}
		if groupPos[name] < g.MinPositives {
			t.Errorf("group %q has %d positives, want at least %d", name, groupPos[name], g.MinPositives)
		}
		if g.RecallScope == "obfuscated" && groupScope[name] < g.MinScopePos {
			t.Errorf("group %q has %d obfuscated positives, want at least %d", name, groupScope[name], g.MinScopePos)
		}
		if g.PositiveExpect != "hold" && g.PositiveExpect != "flag" {
			t.Errorf("group %q: positiveExpect %q", name, g.PositiveExpect)
		}
		if g.Precision <= 0 || g.Precision > 1 || g.Recall <= 0 || g.Recall > 1 {
			t.Errorf("group %q: precision/recall out of (0,1]", name)
		}
	}
	if tg.BenignHoldCeiling <= 0 || tg.BenignHoldCeiling > 0.002 {
		t.Errorf("benignHoldCeiling %v: the spec ceiling is 0.002 and may only be tightened", tg.BenignHoldCeiling)
	}
	for _, s := range append(append([]string{}, tg.BenignSlices...), tg.ZeroHoldSlices...) {
		if !validSlices[s] {
			t.Errorf("targets.json names unknown slice %q", s)
		}
	}
}

func TestCorpusSlurFileHygiene(t *testing.T) {
	pt := hateTerms(t)
	lines := loadCorpus(t)
	for _, l := range lines {
		f := NewForms(l.Text)
		hit := hasTerm(f, pt.all)
		switch {
		case l.file != "corpus-en.positive.jsonl" && hit:
			t.Errorf("%s: a hate_speech pack term appears in the main file; it belongs in the positive file", where(l))
		case l.file == "corpus-en.positive.jsonl" && !hit:
			t.Errorf("%s: no pack hate term found in the line's pack form; regenerate the corpus", where(l))
		}
	}
	b, err := os.ReadFile(filepath.Join("testdata", "corpus-en.positive.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := bytes.Cut(b, []byte("\n"))
	if !bytes.Contains(first, []byte("WARNING")) || !bytes.HasPrefix(first, []byte(`{"_header"`)) {
		t.Error("corpus-en.positive.jsonl line 1 must be the _header record carrying the content warning")
	}
}

// Each obfuscated or plain hate positive must reduce, under the real forms, to a
// term of its own tier. This checks the corpus (and that the pack still agrees
// with it), not any detector.
func TestCorpusHatePositivesNormaliseToTheirTier(t *testing.T) {
	pt := hateTerms(t)
	for _, l := range loadCorpus(t) {
		if l.Expect == "pass" || !strings.HasPrefix(l.Group, "hate_speech/") {
			continue
		}
		if !hasTerm(NewForms(l.Text), pt.byTier[l.Tier]) {
			t.Errorf("%s: pack form holds no %s-tier term (obf %q)", where(l), l.Tier, l.Obf)
		}
	}
}

// The spec asks the Scunthorpe slice to carry every dsojevic exception entry.
func TestCorpusCoversEveryPackException(t *testing.T) {
	pt := hateTerms(t)
	have := map[string]bool{}
	for _, l := range loadCorpus(t) {
		if l.Slice != "scunthorpe" {
			continue
		}
		for _, tok := range NewForms(l.Text).Tokens {
			have[tok] = true
		}
	}
	isTerm := map[string]bool{}
	for _, term := range pt.all {
		isTerm[term] = true
	}
	var missing int
	for x := range pt.except {
		if isTerm[Fold(x)] {
			continue // an exception token that is itself a pack term cannot be a benign fixture
		}
		if !have[Fold(x)] {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d pack exception tokens are not in the scunthorpe slice; regenerate the corpus", missing)
	}
}

// ------------------------------------------------------------ reserved data

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@([A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+)`)
	reSpell = regexp.MustCompile(`(?i)(\S+)\s*[\[(]at[\])]\s*(\S+)\s*[\[(]dot[\])]\s*([A-Za-z]+)`)
	reIPv4  = regexp.MustCompile(`(^|[^0-9A-Za-z.\-])(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})($|[^0-9.]|\.[^0-9])`)
	rePhone = regexp.MustCompile(`555\D{0,2}01\d\d`)
	reV6    = regexp.MustCompile(`[0-9A-Fa-f]{1,4}(?::[0-9A-Fa-f]{0,4}){4,7}`)
)

func reservedDomain(d string) bool {
	d = strings.ToLower(d)
	for _, s := range []string{"example.com", "example.org", "example.net"} {
		if d == s || strings.HasSuffix(d, "."+s) {
			return true
		}
	}
	return strings.HasSuffix(d, ".example") || strings.HasSuffix(d, ".test") || d == "example" || d == "test"
}

func docOrPrivateV4(a netip.Addr) bool {
	for _, p := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16"} {
		if netip.MustParsePrefix(p).Contains(a) {
			return true
		}
	}
	return false
}

// No real person's data: phones end in 555-01xx, emails sit on reserved
// domains, IPs sit on documentation or private ranges.
func TestCorpusUsesReservedDataOnly(t *testing.T) {
	doc6 := netip.MustParsePrefix("2001:db8::/32")
	check := func(l corpusLine, text string) {
		w := where(l)
		for _, m := range reEmail.FindAllStringSubmatch(text, -1) {
			if !reservedDomain(m[1]) {
				t.Errorf("%s: email on a non-reserved domain", w)
			}
		}
		for _, m := range reSpell.FindAllStringSubmatch(text, -1) {
			if !reservedDomain(m[2] + "." + m[3]) {
				t.Errorf("%s: spelled-out email on a non-reserved domain", w)
			}
		}
		for _, m := range reIPv4.FindAllStringSubmatch(text, -1) {
			a, err := netip.ParseAddr(strings.Join(m[2:6], "."))
			if err != nil {
				continue // octet over 255 or leading zero: not an address
			}
			if !docOrPrivateV4(a) && l.Slice != "number_trap" {
				t.Errorf("%s: public-looking IPv4 outside the number-trap slice", w)
			}
		}
		for _, m := range reV6.FindAllString(text, -1) {
			if a, err := netip.ParseAddr(m); err == nil && a.Is6() && !doc6.Contains(a) && l.Slice != "number_trap" {
				t.Errorf("%s: IPv6 outside 2001:db8::/32", w)
			}
		}
	}
	for _, l := range loadCorpus(t) {
		check(l, l.Text)
		if l.Group == "doxxing/phone" && l.Expect != "pass" && !rePhone.MatchString(l.Text) {
			t.Errorf("%s: phone positive is not in the 555-01xx range", where(l))
		}
		if l.Group == "doxxing/ipv6" && l.Expect != "pass" && !strings.Contains(l.Text, "2001:db8") {
			t.Errorf("%s: IPv6 positive is not on 2001:db8::/32", where(l))
		}
		if l.Group == "doxxing/ipv4" && l.Expect != "pass" {
			ok := false
			for _, m := range reIPv4.FindAllStringSubmatch(l.Text, -1) {
				if a, err := netip.ParseAddr(strings.Join(m[2:6], ".")); err == nil && docOrPrivateV4(a) && !strings.HasPrefix(a.String(), "10.") && !strings.HasPrefix(a.String(), "192.168.") {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s: IPv4 positive is not on a documentation range", where(l))
			}
		}
	}
	for _, s := range loadSequences(t) {
		for i, m := range s.Messages {
			check(corpusLine{Text: m.Text, file: "sequences.jsonl", n: s.n, Group: s.Group, Slice: "sequence"}, m.Text)
			_ = i
		}
	}
}

func TestProtectedNamesFixture(t *testing.T) {
	tg := loadTargets(t)
	b, err := os.ReadFile(filepath.Join("testdata", tg.ProtectedNames))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Note  string `json:"_note"`
		Names []struct {
			Platform string `json:"platform"`
			Name     string `json:"name"`
			Role     string `json:"role"`
		} `json:"names"`
	}
	if err := strictDecode(b, &f); err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, n := range f.Names {
		if n.Name == "" || n.Platform == "" {
			t.Errorf("%s: entry missing name or platform", tg.ProtectedNames)
		}
		set[n.Name] = true
	}
	for _, l := range loadCorpus(t) {
		if l.Protects != "" && !set[l.Protects] {
			t.Errorf("%s: protects a name that is not in %s", where(l), tg.ProtectedNames)
		}
		if l.Category == "impersonation" && l.Slice == "positive" && l.Protects == "" {
			t.Errorf("%s: impersonation positive without protects", where(l))
		}
	}
}

// ---------------------------------------------------------------- sequences

func TestSequencesShape(t *testing.T) {
	tg := loadTargets(t)
	seqs := loadSequences(t)
	ids := map[string]bool{}
	var pass int
	groupPos := map[string]int{}
	pt := hateTerms(t)
	for _, s := range seqs {
		w := fmt.Sprintf("%s:%d (group %q)", tg.Sequences.File, s.n, s.Group)
		if s.ID == "" || ids[s.ID] {
			t.Errorf("%s: missing or duplicate id", w)
		}
		ids[s.ID] = true
		if !validSeqCats[s.Category] || s.Group != s.Category {
			t.Errorf("%s: category %q / group %q", w, s.Category, s.Group)
		}
		if g, ok := tg.Groups[s.Group]; !ok || g.Kind != "sequence" {
			t.Errorf("%s: group is not a sequence group in targets.json", w)
		} else if s.Expect != "pass" && s.Expect != g.PositiveExpect {
			t.Errorf("%s: expect %q, but the group's spec default is %q", w, s.Expect, g.PositiveExpect)
		}
		if !validExpect[s.Expect] || s.Note == "" || s.Kind == "" {
			t.Errorf("%s: bad expect, note or kind", w)
		}
		if len(s.Messages) < 2 {
			t.Errorf("%s: a sequence needs at least 2 messages", w)
			continue
		}
		if s.Messages[0].OffsetMs != 0 {
			t.Errorf("%s: first offsetMs must be 0", w)
		}
		authors := map[string]bool{}
		prev := -1
		for i, m := range s.Messages {
			if m.Author == "" || m.Platform == "" || m.Text == "" {
				t.Errorf("%s: message %d missing author, platform or text", w, i)
			}
			if m.OffsetMs < prev {
				t.Errorf("%s: message %d goes back in time", w, i)
			}
			prev = m.OffsetMs
			if hasTerm(NewForms(m.Text), pt.all) {
				t.Errorf("%s: message %d holds a hate term", w, i)
			}
			if i <= fireIndex(s) {
				authors[m.Author] = true
			}
		}
		if s.Expect == "pass" {
			pass++
			if s.FireAt != nil {
				t.Errorf("%s: pass sequence with fireAt", w)
			}
			continue
		}
		groupPos[s.Group]++
		if s.FireAt == nil || *s.FireAt < 0 || *s.FireAt >= len(s.Messages) {
			t.Errorf("%s: fireAt missing or out of range", w)
			continue
		}
		if s.Group == "raid" {
			if len(authors) < 5 {
				t.Errorf("%s: raid positive has %d distinct authors by fireAt, want 5 or more", w, len(authors))
			}
			if s.Messages[*s.FireAt].OffsetMs > 20000 {
				t.Errorf("%s: raid positive fires after 20 s", w)
			}
		} else {
			a := s.Messages[0].Author
			for _, m := range s.Messages {
				if m.Author != a {
					t.Errorf("%s: near-duplicate positive must be one author", w)
					break
				}
			}
			if s.Messages[*s.FireAt].OffsetMs > 30000 {
				t.Errorf("%s: near-duplicate positive fires after 30 s", w)
			}
		}
	}
	if len(seqs) < tg.Sequences.MinSequences || pass < tg.Sequences.MinPassSequence {
		t.Errorf("%d sequences (%d pass), want at least %d (%d pass)", len(seqs), pass, tg.Sequences.MinSequences, tg.Sequences.MinPassSequence)
	}
	for name, g := range tg.Groups {
		if g.Kind == "sequence" && groupPos[name] < g.MinPositives {
			t.Errorf("sequence group %q has %d positives, want at least %d", name, groupPos[name], g.MinPositives)
		}
	}
}

func fireIndex(s corpusSeq) int {
	if s.FireAt == nil {
		return -1
	}
	return *s.FireAt
}

// ------------------------------------------------------------------- scoring

// corpusFinding is what a checker reports for one message.
type corpusFinding struct {
	Group string
	Hold  bool
}

// corpusChecker judges one line. PR 4 assigns corpusUnderTest to the pack
// checker; the line's Author and Platform are set for impersonation lines.
type corpusChecker func(l corpusLine, f Forms) []corpusFinding

// corpusSeqChecker judges a whole sequence and returns the findings for each
// message, in order.
type corpusSeqChecker func(s corpusSeq) [][]corpusFinding

var (
	corpusUnderTest    corpusChecker
	corpusSeqUnderTest corpusSeqChecker
)

type groupStat struct {
	tp, fp, fn          int // precision uses tp and fp; recall uses scopeTP over scopeN
	scopeN, scopeTP     int
	overHold, underHold int
}

func (g groupStat) precision() float64 {
	if g.tp+g.fp == 0 {
		return 1
	}
	return float64(g.tp) / float64(g.tp+g.fp)
}

func (g groupStat) recall() float64 {
	if g.scopeN == 0 {
		return 1
	}
	return float64(g.scopeTP) / float64(g.scopeN)
}

type corpusReport struct {
	groups     map[string]*groupStat
	benignN    int
	benignHeld int
	zeroHolds  map[string]int
}

func newReport(tg corpusTargets) *corpusReport {
	r := &corpusReport{groups: map[string]*groupStat{}, zeroHolds: map[string]int{}}
	for name := range tg.Groups {
		r.groups[name] = &groupStat{}
	}
	return r
}

func hits(fs []corpusFinding) map[string]bool { // group -> held
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Group] = m[f.Group] || f.Hold
	}
	return m
}

func (r *corpusReport) scoreLines(tg corpusTargets, lines []corpusLine, chk corpusChecker) {
	benign := map[string]bool{}
	for _, s := range tg.BenignSlices {
		benign[s] = true
	}
	zero := map[string]bool{}
	for _, s := range tg.ZeroHoldSlices {
		zero[s] = true
	}
	for _, l := range lines {
		fs := chk(l, NewForms(l.Text))
		h := hits(fs)
		anyHold := false
		for _, f := range fs {
			anyHold = anyHold || f.Hold
		}
		if benign[l.Slice] && l.Expect == "pass" {
			r.benignN++
			if anyHold {
				r.benignHeld++
			}
		}
		if zero[l.Slice] && anyHold {
			r.zeroHolds[l.Slice]++
		}
		for name, g := range tg.Groups {
			if g.Kind == "sequence" {
				continue
			}
			st := r.groups[name]
			held, hit := h[name]
			positive := l.Group == name && l.Expect != "pass"
			if hit {
				if positive {
					st.tp++
				} else {
					st.fp++
				}
			}
			if !positive {
				continue
			}
			ok := hit && (l.Expect != "hold" || held)
			if hit && l.Expect == "hold" && !held {
				st.underHold++
			}
			if hit && l.Expect == "flag" && held {
				st.overHold++
			}
			if g.RecallScope != "obfuscated" || l.Obf != "" {
				st.scopeN++
				if ok {
					st.scopeTP++
				}
			}
		}
	}
}

func (r *corpusReport) scoreSeqs(tg corpusTargets, seqs []corpusSeq, chk corpusSeqChecker) {
	for _, s := range seqs {
		per := chk(s)
		for name, g := range tg.Groups {
			if g.Kind != "sequence" {
				continue
			}
			st := r.groups[name]
			first, heldAt := -1, -1
			for i, fs := range per {
				if held, ok := hits(fs)[name]; ok {
					if first < 0 {
						first = i
					}
					if held && heldAt < 0 {
						heldAt = i
					}
				}
			}
			positive := s.Group == name && s.Expect != "pass"
			if first >= 0 {
				if positive {
					st.tp++
				} else {
					st.fp++
				}
			}
			if !positive {
				continue
			}
			st.scopeN++
			at := fireIndex(s)
			ok := first >= 0 && first <= at && (s.Expect != "hold" || (heldAt >= 0 && heldAt <= at))
			if s.Expect == "flag" && heldAt >= 0 {
				st.overHold++
			}
			if ok {
				st.scopeTP++
			}
		}
	}
}

// violations lists every target the report misses.
func (r *corpusReport) violations(tg corpusTargets) []string {
	var out []string
	names := make([]string, 0, len(tg.Groups))
	for n := range tg.Groups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		g, st := tg.Groups[n], r.groups[n]
		if st.precision() < g.Precision {
			out = append(out, fmt.Sprintf("%s: precision %.3f < %.2f", n, st.precision(), g.Precision))
		}
		if st.recall() < g.Recall {
			out = append(out, fmt.Sprintf("%s: recall %.3f < %.2f", n, st.recall(), g.Recall))
		}
		if st.overHold > 0 {
			out = append(out, fmt.Sprintf("%s: %d flag-only lines were held", n, st.overHold))
		}
	}
	if r.benignN > 0 {
		if rate := float64(r.benignHeld) / float64(r.benignN); rate > tg.BenignHoldCeiling {
			out = append(out, fmt.Sprintf("benign hold rate %.4f > %.4f (%d of %d)", rate, tg.BenignHoldCeiling, r.benignHeld, r.benignN))
		}
	}
	zs := make([]string, 0, len(r.zeroHolds))
	for s := range r.zeroHolds {
		zs = append(zs, s)
	}
	sort.Strings(zs)
	for _, s := range zs {
		out = append(out, fmt.Sprintf("slice %s: %d holds, want zero", s, r.zeroHolds[s]))
	}
	return out
}

func (r *corpusReport) table() string {
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "group\ttp\tfp\tprecision\trecall\tunderHold\toverHold")
	names := make([]string, 0, len(r.groups))
	for n := range r.groups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		g := r.groups[n]
		fmt.Fprintf(w, "%s\t%d\t%d\t%.3f\t%.3f\t%d\t%d\n", n, g.tp, g.fp, g.precision(), g.recall(), g.underHold, g.overHold)
	}
	fmt.Fprintf(w, "benign held\t%d of %d\n", r.benignHeld, r.benignN)
	w.Flush()
	return b.String()
}

// TestCorpusDetection is the gate PR 4 turns on by assigning corpusUnderTest
// (and corpusSeqUnderTest). Until then there is no detector to measure, which
// the log says plainly; the scoring machinery itself is covered below.
func TestCorpusDetection(t *testing.T) {
	tg := loadTargets(t)
	rep := newReport(tg)
	if corpusUnderTest == nil && corpusSeqUnderTest == nil {
		t.Log("no checker wired to the corpus yet; the detector PR assigns corpusUnderTest")
		return
	}
	if corpusUnderTest != nil {
		rep.scoreLines(tg, loadCorpus(t), corpusUnderTest)
	}
	if corpusSeqUnderTest != nil {
		rep.scoreSeqs(tg, loadSequences(t), corpusSeqUnderTest)
	}
	t.Log("\n" + rep.table())
	for _, v := range rep.violations(tg) {
		t.Error(v)
	}
}

// oracle answers every line exactly as labelled.
func oracleLines(l corpusLine, _ Forms) []corpusFinding {
	if l.Expect == "pass" {
		return nil
	}
	return []corpusFinding{{Group: l.Group, Hold: l.Expect == "hold"}}
}

func oracleSeq(s corpusSeq) [][]corpusFinding {
	out := make([][]corpusFinding, len(s.Messages))
	if s.Expect != "pass" {
		out[fireIndex(s)] = []corpusFinding{{Group: s.Group, Hold: s.Expect == "hold"}}
	}
	return out
}

func TestScorerOracleMeetsEveryTarget(t *testing.T) {
	tg := loadTargets(t)
	rep := newReport(tg)
	rep.scoreLines(tg, loadCorpus(t), oracleLines)
	rep.scoreSeqs(tg, loadSequences(t), oracleSeq)
	if v := rep.violations(tg); len(v) > 0 {
		t.Fatalf("the oracle must meet every target; got %v\n%s", v, rep.table())
	}
}

func TestScorerCatchesDegenerateCheckers(t *testing.T) {
	tg := loadTargets(t)
	lines, seqs := loadCorpus(t), loadSequences(t)

	holdAll := func(l corpusLine, _ Forms) []corpusFinding {
		return []corpusFinding{{Group: "hate_speech/severe", Hold: true}}
	}
	rep := newReport(tg)
	rep.scoreLines(tg, lines, holdAll)
	v := strings.Join(rep.violations(tg), "\n")
	if !strings.Contains(v, "benign hold rate") || !strings.Contains(v, "want zero") {
		t.Errorf("hold-everything must break the benign ceiling and the zero-hold slices:\n%s", v)
	}

	rep = newReport(tg)
	rep.scoreLines(tg, lines, func(corpusLine, Forms) []corpusFinding { return nil })
	rep.scoreSeqs(tg, seqs, func(s corpusSeq) [][]corpusFinding { return make([][]corpusFinding, len(s.Messages)) })
	v = strings.Join(rep.violations(tg), "\n")
	for name := range tg.Groups {
		if !strings.Contains(v, name+": recall") {
			t.Errorf("flag-nothing must miss recall for %s", name)
		}
	}

	// holding a flag-only line is a violation even when it was detected
	rep = newReport(tg)
	rep.scoreLines(tg, lines, func(l corpusLine, f Forms) []corpusFinding {
		fs := oracleLines(l, f)
		for i := range fs {
			fs[i].Hold = true
		}
		return fs
	})
	if v := strings.Join(rep.violations(tg), "\n"); !strings.Contains(v, "flag-only lines were held") {
		t.Errorf("holding flag-only lines must be reported:\n%s", v)
	}
}
