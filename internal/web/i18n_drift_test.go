package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The fifteen locale files have to carry the same keys, and nothing was checking.
//
// They are in sync today, which is the reason to write this now rather than
// after they are not: a locale that quietly loses a key does not fail anything.
// The translator falls back to the key name, so the UI renders "nav.settings"
// where a word should be -- in one language, for the operators who read that
// language, and never for whoever added the key.
//
// The same shape as the settings and limits drift guards in internal/db: a
// mirrored set that drifts silently, checked rather than trusted. This one lives
// here because internal/web is the package that embeds the UI.
func TestEveryLocaleCarriesTheSameKeys(t *testing.T) {
	dir := filepath.Join("..", "..", "ui", "src", "lib", "i18n")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Not skipped: these files existing is the point.
		t.Fatalf("cannot read %s: %v", dir, err)
	}

	locales := map[string]map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("cannot read %s: %v", e.Name(), err)
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s is not a flat string map: %v", e.Name(), err)
		}
		locales[e.Name()] = m
	}

	base, ok := locales["en.json"]
	if !ok {
		t.Fatal("en.json is missing; it is the source every other locale is measured against")
	}
	if len(locales) < 2 {
		t.Fatalf("found %d locale files; the guard has nothing to compare", len(locales))
	}

	for name, m := range locales {
		if name == "en.json" {
			continue
		}
		for _, k := range missing(base, m) {
			t.Errorf("%s is missing %q. The UI falls back to the key name, so an "+
				"operator reading that language sees %q where a word should be -- "+
				"and never sees it in English, which is why nobody notices.",
				name, k, k)
		}
		for _, k := range missing(m, base) {
			t.Errorf("%s has %q, which en.json does not. Either it was renamed in "+
				"English and not here, or it is dead weight nothing reads.", name, k)
		}
	}
}

// An empty translation is worse than a missing one: the fallback does not fire,
// so the UI renders nothing at all where a label belongs.
func TestNoTranslationIsEmpty(t *testing.T) {
	dir := filepath.Join("..", "..", "ui", "src", "lib", "i18n")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			continue // reported by the guard above
		}
		for k, v := range m {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s has an empty translation for %q; the fallback does not "+
					"fire on empty, so the UI renders nothing there", e.Name(), k)
			}
		}
	}
}

// A key whose English value carries a placeholder must carry it in every
// language, or the count simply vanishes from the sentence.
func TestPlaceholdersSurviveTranslation(t *testing.T) {
	dir := filepath.Join("..", "..", "ui", "src", "lib", "i18n")
	raw, err := os.ReadFile(filepath.Join(dir, "en.json"))
	if err != nil {
		t.Fatalf("cannot read en.json: %v", err)
	}
	var base map[string]string
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatalf("en.json: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "en.json" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		for k, en := range base {
			want := placeholders(en)
			if len(want) == 0 {
				continue
			}
			got := placeholders(m[k])
			if strings.Join(want, ",") != strings.Join(got, ",") {
				t.Errorf("%s %q has placeholders %v, English has %v -- the value "+
					"is dropped from the sentence rather than mistranslated, which "+
					"reads as a bug in the product",
					e.Name(), k, got, want)
			}
		}
	}
}

// missing returns the keys of a that b does not have, sorted so the failure is
// stable across runs.
func missing(a, b map[string]string) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// placeholders extracts {name} spans, sorted, so order in the sentence may
// differ between languages without failing.
func placeholders(s string) []string {
	var out []string
	for {
		i := strings.Index(s, "{")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "}")
		if j < 0 {
			break
		}
		out = append(out, s[i:i+j+1])
		s = s[i+j+1:]
	}
	sort.Strings(out)
	return out
}
