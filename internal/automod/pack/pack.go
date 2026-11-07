package pack

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// The term list is embedded and has no network path: an update ships in a
// release, so what a given binary matches is fixed and reviewable.
//
//go:embed en-v1.json
var enV1 []byte

// licences travel with the data they cover, following internal/ffmpeg/fonts.go,
// which embeds LICENSE-Inter.txt beside the font. NOTICE.txt is the CC-BY-4.0
// credit and the statement that the list was modified.
//
//go:embed LICENSES
var licenceFS embed.FS

// Categories a pack entry belongs to. They are strings here, not automod
// Categories, because this package must not import internal/automod. The
// checker converts at the boundary.
const (
	CategoryHate      = "hate_speech"
	CategoryProfanity = "profanity"
)

// Tiers. Hate terms are severe (meaning does not depend on context; holds
// alone) or contextual (reclaimable, or ordinary in some uses; needs a second
// opinion). Profanity is mild or strong, taken from the source's own severity.
const (
	TierSevere     = "severe"
	TierContextual = "contextual"
	TierMild       = "mild"
	TierStrong     = "strong"
)

// Sub-tags. They reuse Twitch AutoMod's category names so that the one-click
// platform level can say which Twitch category each pack tag matches.
const (
	TagRaceEthnicityReligion = "race_ethnicity_religion"
	TagSexualitySexGender    = "sexuality_sex_gender"
	TagSexBased              = "sex_based_terms"
	TagSwearing              = "swearing"
)

// Source records where part of the pack came from, to the exact upstream commit
// so an update can be diffed against what was seeded.
type Source struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Licence string `json:"licence"`
	SHA     string `json:"sha"`
}

// Entry is one term. Term is stored already in pack form (Fold(Term) == Term);
// Except lists whole tokens that contain the term and are benign.
type Entry struct {
	Term     string   `json:"term"`
	Category string   `json:"category"`
	Tier     string   `json:"tier"`
	Tags     []string `json:"tags"`
	Except   []string `json:"except"`
}

// Pack is a versioned term list.
type Pack struct {
	ID      string   `json:"id"`
	Version int      `json:"version"`
	Sources []Source `json:"sources"`
	Entries []Entry  `json:"entries"`
}

var (
	defaultOnce sync.Once
	defaultPack *Pack
	defaultErr  error
)

// Default returns the embedded English pack. It is parsed once.
func Default() (*Pack, error) {
	defaultOnce.Do(func() { defaultPack, defaultErr = Parse(enV1) })
	return defaultPack, defaultErr
}

// Parse decodes and validates a pack.
func Parse(b []byte) (*Pack, error) {
	var p Pack
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks that the pack is well formed: a term is in pack form, appears
// once, and has a known category and a tier that belongs to it. A pack that
// fails this would silently not match what it was written to catch.
func (p *Pack) Validate() error {
	if p.ID == "" || p.Version < 1 {
		return fmt.Errorf("pack: needs an id and a version of at least 1")
	}
	if len(p.Sources) == 0 {
		return fmt.Errorf("pack %s: needs at least one source", p.ID)
	}
	seen := make(map[string]bool, len(p.Entries))
	for i, e := range p.Entries {
		if e.Term == "" || Fold(e.Term) != e.Term {
			return fmt.Errorf("pack %s: entry %d is not in pack form", p.ID, i)
		}
		if seen[e.Term] {
			return fmt.Errorf("pack %s: entry %d repeats a term", p.ID, i)
		}
		seen[e.Term] = true
		switch e.Category {
		case CategoryHate:
			if e.Tier != TierSevere && e.Tier != TierContextual {
				return fmt.Errorf("pack %s: entry %d: hate tier %q", p.ID, i, e.Tier)
			}
		case CategoryProfanity:
			if e.Tier != TierMild && e.Tier != TierStrong {
				return fmt.Errorf("pack %s: entry %d: profanity tier %q", p.ID, i, e.Tier)
			}
		default:
			return fmt.Errorf("pack %s: entry %d: category %q", p.ID, i, e.Category)
		}
		for _, x := range e.Except {
			if Fold(x) != x || strings.ContainsRune(x, ' ') || !strings.Contains(x, e.Term) {
				return fmt.Errorf("pack %s: entry %d: an exception is not a whole token containing the term", p.ID, i)
			}
		}
	}
	return nil
}

// Marshal writes the pack in the committed layout: the header pretty-printed
// and one entry per line, so a term change is one changed line in review.
// Output is deterministic and ends in a newline.
func (p *Pack) Marshal() ([]byte, error) {
	var b bytes.Buffer
	enc := func(v any) ([]byte, error) {
		var o bytes.Buffer
		e := json.NewEncoder(&o)
		e.SetEscapeHTML(false)
		if err := e.Encode(v); err != nil {
			return nil, err
		}
		return bytes.TrimRight(o.Bytes(), "\n"), nil
	}
	id, _ := enc(p.ID)
	fmt.Fprintf(&b, "{\n  \"id\": %s,\n  \"version\": %d,\n  \"sources\": [\n", id, p.Version)
	for i, s := range p.Sources {
		j, err := enc(s)
		if err != nil {
			return nil, err
		}
		sep := ","
		if i == len(p.Sources)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    %s%s\n", j, sep)
	}
	b.WriteString("  ],\n  \"entries\": [\n")
	for i, e := range p.Entries {
		if e.Tags == nil {
			e.Tags = []string{}
		}
		if e.Except == nil {
			e.Except = []string{}
		}
		j, err := enc(e)
		if err != nil {
			return nil, err
		}
		sep := ","
		if i == len(p.Entries)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    %s%s\n", j, sep)
	}
	b.WriteString("  ]\n}\n")
	return b.Bytes(), nil
}

// Licences returns the embedded licence and notice files, by file name.
func Licences() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(licenceFS, "LICENSES", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := licenceFS.ReadFile(path)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(path, "LICENSES/")] = b
		return nil
	})
	return out, err
}

// Count returns the number of entries per "category/tier", for stats and tests.
func (p *Pack) Count() map[string]int {
	m := map[string]int{}
	for _, e := range p.Entries {
		m[e.Category+"/"+e.Tier]++
	}
	return m
}

// Terms returns the terms of one category in stored order.
func (p *Pack) Terms(category string) []string {
	var out []string
	for _, e := range p.Entries {
		if e.Category == category {
			out = append(out, e.Term)
		}
	}
	sort.Strings(out)
	return out
}
