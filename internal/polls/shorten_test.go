package polls

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A cut never lands inside what renders as one character.
func TestTruncateKeepsGraphemesWhole(t *testing.T) {
	family := "👨‍👩‍👧" // seven runes, one character
	flags := "🇯🇵🇺🇸"   // two flags, four runes
	e2 := "é̂"       // a letter with two combining marks
	for _, c := range []struct {
		in    string
		n     int
		parts []string // the only prefixes the cut may keep, ellipsis aside
	}{
		{"ab" + family + "cd", 5, []string{"ab"}},
		{"a" + flags + "z", 4, []string{"a", "a🇯🇵"}},
		{e2 + "xyyyy", 4, []string{e2}},
		{"👍🏽 great plan", 2, []string{"👍🏽"[:0]}},
	} {
		got := truncate(c.in, c.n)
		if !strings.HasSuffix(got, "…") {
			t.Errorf("%q cut to %d has no ellipsis: %q", c.in, c.n, got)
			continue
		}
		body := strings.TrimSuffix(got, "…")
		ok := false
		for _, p := range c.parts {
			if body == p {
				ok = true
			}
		}
		if !ok {
			t.Errorf("truncate(%q, %d) = %q, want one of %q followed by an ellipsis", c.in, c.n, got, c.parts)
		}
	}
}

// The question gives way before an option label is cut: options are what a
// viewer types a number against.
func TestTheQuestionIsShortenedBeforeTheLabels(t *testing.T) {
	s := Spec{
		Question:        strings.Repeat("Which map should we play next on stream tonight? ", 2),
		Options:         []string{"Dust two", "Mirage", "Nuke"},
		DurationSeconds: 60,
	}
	got := StartTextMax(s, 120)
	for _, l := range s.Options {
		if !strings.Contains(got, l) {
			t.Errorf("label %q was cut while the question still had room to give: %q", l, got)
		}
	}
	if utf8.RuneCountInString(got) > 120 || !strings.Contains(got, "…") {
		t.Errorf("text is %d characters: %q", utf8.RuneCountInString(got), got)
	}
}
