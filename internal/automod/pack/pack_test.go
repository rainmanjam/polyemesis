package pack

import (
	"bytes"
	"strings"
	"testing"
)

func TestDefaultPackLoadsAndValidates(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "en" || p.Version != 1 {
		t.Fatalf("id/version = %s/%d", p.ID, p.Version)
	}
	c := p.Count()
	for _, k := range []string{"hate_speech/severe", "hate_speech/contextual", "profanity/mild", "profanity/strong"} {
		if c[k] == 0 {
			t.Errorf("no %s entries", k)
		}
	}
}

// A term is stored in pack form. If it weren't, it would silently never match
// the text it was written to catch. This is the stored-form drift check.
func TestEveryTermIsStoredInPackForm(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range p.Entries {
		if got := Fold(e.Term); got != e.Term {
			t.Errorf("entry %d: term drifts under Fold (%d runes in, %d out)", i, len(e.Term), len(got))
		}
		if n := len(strings.Fields(e.Term)); n < 1 || n > 4 {
			t.Errorf("entry %d: %d tokens, want 1 to 4", i, n)
		}
		for _, x := range e.Except {
			if Fold(x) != x {
				t.Errorf("entry %d: an exception is not in pack form", i)
			}
		}
	}
}

func TestHeaderRecordsEverySourceExactly(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"MIT": "", "CC-BY-4.0": ""}
	for _, s := range p.Sources {
		if len(s.SHA) != 40 || strings.Trim(s.SHA, "0123456789abcdef") != "" {
			t.Errorf("source %q: sha %q is not a full commit id", s.Name, s.SHA)
		}
		if !strings.HasPrefix(s.URL, "https://github.com/") {
			t.Errorf("source %q: url %q", s.Name, s.URL)
		}
		delete(want, s.Licence)
	}
	if len(want) != 0 {
		t.Errorf("licences missing from the header: %v", want)
	}
}

func TestMarshalIsTheCommittedLayout(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, enV1) {
		t.Fatal("Marshal(Default()) differs from the embedded file: the layout the generator writes and the one read have drifted")
	}
}

func TestParseRejectsMalformedPacks(t *testing.T) {
	good := `{"id":"x","version":1,"sources":[{"name":"n","url":"u","licence":"MIT","sha":"s"}],"entries":[%s]}`
	cases := map[string]string{
		"term not in pack form":  `{"term":"Bad Term","category":"profanity","tier":"mild","tags":[],"except":[]}`,
		"unknown category":       `{"term":"word","category":"nope","tier":"mild","tags":[],"except":[]}`,
		"tier of wrong kind":     `{"term":"word","category":"hate_speech","tier":"mild","tags":[],"except":[]}`,
		"exception without term": `{"term":"word","category":"profanity","tier":"mild","tags":[],"except":["other"]}`,
		"duplicate term": `{"term":"word","category":"profanity","tier":"mild","tags":[],"except":[]},` +
			`{"term":"word","category":"profanity","tier":"mild","tags":[],"except":[]}`,
	}
	for name, entries := range cases {
		if _, err := Parse([]byte(strings.Replace(good, "%s", entries, 1))); err == nil {
			t.Errorf("%s: Parse accepted it", name)
		}
	}
	if _, err := Parse([]byte(`{"id":"x","version":1,"sources":[],"entries":[],"extra":1}`)); err == nil {
		t.Error("Parse accepted an unknown field")
	}
}

func TestLicencesAreEmbeddedWithTheirAttribution(t *testing.T) {
	l, err := Licences()
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"LICENSE-dsojevic-profanity-list.txt": {"MIT License", "David Sojevic"},
		"LICENSE-ldnoobw-CC-BY-4.0.txt":       {"Attribution 4.0 International", "Creative Commons"},
		"LICENSE-Unicode-3.0.txt":             {"UNICODE LICENSE V3"},
		"NOTICE.txt":                          {"LDNOOBW contributors", "CHANGES MADE", "modified", "creativecommons.org/licenses/by/4.0"},
	}
	for name, subs := range checks {
		b, ok := l[name]
		if !ok {
			t.Errorf("%s is not embedded", name)
			continue
		}
		for _, s := range subs {
			if !strings.Contains(string(b), s) {
				t.Errorf("%s does not contain %q", name, s)
			}
		}
	}
	p, _ := Default()
	for _, s := range p.Sources {
		if !strings.Contains(string(l["NOTICE.txt"]), s.SHA) {
			t.Errorf("NOTICE.txt does not cite the pinned commit of %q", s.Name)
		}
	}
}
