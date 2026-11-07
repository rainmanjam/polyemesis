package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
)

const (
	packFile = "../../internal/automod/pack/en-v1.json"
)

// TestEmbeddedPackMatchesGeneratorOutput is the drift test. The pack is
// committed so review shows the term diff, which is only trustworthy if the
// committed file is exactly what the pinned sources and overrides produce. It
// reads the committed source snapshots, so it needs no network.
func TestEmbeddedPackMatchesGeneratorOutput(t *testing.T) {
	want, _, err := generate(".")
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile(packFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(have, want) {
		t.Fatal("internal/automod/pack/en-v1.json differs from the generator output: run `go run ./scripts/gen-pack` and commit the result")
	}
}

func TestGenerationIsDeterministic(t *testing.T) {
	a, _, err := generate(".")
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := generate(".")
	if !bytes.Equal(a, b) {
		t.Fatal("two runs produced different bytes")
	}
}

func TestSnapshotsMatchTheirPins(t *testing.T) {
	p, err := loadPins("pins.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Sources {
		for _, f := range s.Files {
			b, err := os.ReadFile(filepath.Join("sources", s.Dir, f.Path))
			if err != nil {
				t.Fatal(err)
			}
			if got := hashHex(b); got != f.SHA256 {
				t.Errorf("%s/%s: sha256 %s, pinned %s", s.Dir, f.Path, got, f.SHA256)
			}
		}
	}
}

// The header of the shipped pack must say what pins.yaml says, so a moved pin
// can't leave a stale commit id in the pack.
func TestPackHeaderCarriesThePins(t *testing.T) {
	p, err := loadPins("pins.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := pack.Default()
	if err != nil {
		t.Fatal(err)
	}
	var want []pack.Source
	for _, s := range p.Sources {
		want = append(want, pack.Source{Name: s.Name, URL: s.URL, Licence: s.Licence, SHA: s.SHA})
	}
	if !reflect.DeepEqual(got.Sources, want) {
		t.Fatalf("pack header sources %+v differ from pins %+v", got.Sources, want)
	}
}

func TestSnapshotLicencesAreTheEmbeddedOnes(t *testing.T) {
	l, err := pack.Licences()
	if err != nil {
		t.Fatal(err)
	}
	for dir, name := range map[string]string{
		"dsojevic-profanity-list": "LICENSE-dsojevic-profanity-list.txt",
		"ldnoobw":                 "LICENSE-ldnoobw-CC-BY-4.0.txt",
	} {
		src, err := os.ReadFile(filepath.Join("sources", dir, "LICENSE"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(src, l[name]) {
			t.Errorf("sources/%s/LICENSE differs from the embedded %s", dir, name)
		}
	}
}

func TestExpandStars(t *testing.T) {
	got := expandStars("lo*ng")
	sort.Strings(got)
	if want := []string{"long", "loong"}; !reflect.DeepEqual(got, want) {
		t.Errorf("expandStars = %v, want %v", got, want)
	}
	if got := expandStars("plain"); !reflect.DeepEqual(got, []string{"plain"}) {
		t.Errorf("expandStars(plain) = %v", got)
	}
	if n := len(expandStars("a*b*c*d*e*f*g*h*")); n != 1<<maxStars {
		t.Errorf("star expansion is not capped: %d variants", n)
	}
}

func TestFoldTermRejections(t *testing.T) {
	cases := map[string]string{
		"ab":                      rejectShort,
		"2 words 1":               rejectDigits,
		"gr8 stuff":               rejectDigits,
		"a word":                  rejectSingle,
		"one two three four five": rejectToks,
		"---":                     rejectEmpty,
	}
	for in, want := range cases {
		if _, why := foldTerm(in); why != want {
			t.Errorf("foldTerm(%q) rejected for %q, want %q", in, why, want)
		}
	}
	if term, why := foldTerm("sh1t talk"); why != "" || term != "shit talk" {
		t.Errorf("foldTerm leet: %q, %q", term, why)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		d         dsoEntry
		cat, tier string
		wantTag   string
	}{
		{dsoEntry{Tags: []string{"racial"}, Severity: 3}, pack.CategoryHate, pack.TierSevere, pack.TagRaceEthnicityReligion},
		{dsoEntry{Tags: []string{"racial"}, Severity: 2}, pack.CategoryProfanity, pack.TierMild, pack.TagRaceEthnicityReligion},
		{dsoEntry{Tags: []string{"lgbtq"}, Severity: 3}, pack.CategoryHate, pack.TierContextual, pack.TagSexualitySexGender},
		{dsoEntry{Tags: []string{"lgbtq"}, Severity: 4}, pack.CategoryHate, pack.TierSevere, pack.TagSexualitySexGender},
		{dsoEntry{Tags: []string{"religious"}, Severity: 3}, pack.CategoryHate, pack.TierContextual, pack.TagRaceEthnicityReligion},
		{dsoEntry{Tags: []string{"sexual"}, Severity: 4}, pack.CategoryProfanity, pack.TierStrong, pack.TagSexBased},
		{dsoEntry{Tags: []string{"general"}, Severity: 1}, pack.CategoryProfanity, pack.TierMild, pack.TagSwearing},
		{dsoEntry{Severity: 2}, pack.CategoryProfanity, pack.TierMild, pack.TagSwearing},
	}
	for i, c := range cases {
		cat, tier, tags := classify(c.d)
		if cat != c.cat || tier != c.tier || len(tags) != 1 || tags[0] != c.wantTag {
			t.Errorf("case %d: got %s/%s/%v, want %s/%s/[%s]", i, cat, tier, tags, c.cat, c.tier, c.wantTag)
		}
	}
}

func TestSkeletonBuilderKeepsOnlyEnglishLookalikes(t *testing.T) {
	src := []byte("# comment\n" +
		"0430 ;\t0061 ;\tMA\t# CYRILLIC a -> a\n" + // kept
		"0049 ;\t006C ;\tMA\t# ASCII I -> l: dropped, ASCII source\n" +
		"03BF ;\t006F ;\tMA\t# omicron -> o: kept\n" +
		"0410 ;\t0041 ;\tMA\t# CYRILLIC A -> A: kept, lowercased\n" +
		"2160 ;\t0049 ;\tMA\t# roman numeral one: NFKC already rewrites it\n" +
		"04CF ;\t006C 0307 ;\tMA\t# target has a non-ASCII mark: dropped\n" +
		"0430 0300 ;\t0061 ;\tMA\t# two-character source: dropped\n")
	out, n, err := buildSkeleton(src, "17.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("kept %d mappings, want 3", n)
	}
	for _, want := range []string{`{0x0410, "a"}`, `{0x0430, "a"}`, `{0x03BF, "o"}`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("table lacks %s", want)
		}
	}
	if bytes.Index(out, []byte("0x03BF")) > bytes.Index(out, []byte("0x0410")) {
		t.Error("table is not sorted by rune")
	}
}

// Needs the network, so it only runs when asked: it proves the committed table
// is what the pinned confusables.txt produces.
func TestSkeletonTableMatchesPinnedConfusables(t *testing.T) {
	if os.Getenv("POLYEMESIS_GENPACK_NETWORK") != "1" {
		t.Skip("set POLYEMESIS_GENPACK_NETWORK=1 to fetch the pinned confusables.txt")
	}
	p, err := loadPins("pins.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src, err := fetchConfusables(p.Unicode)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := buildSkeleton(src, p.Unicode.Version)
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile("../../internal/automod/pack/skeleton_table.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(have, want) {
		t.Fatal("skeleton_table.go differs from the generator output: run `go generate ./internal/automod/pack`")
	}
}
