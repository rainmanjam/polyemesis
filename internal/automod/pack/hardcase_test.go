package pack

import (
	"strings"
	"testing"
)

func hc(t *testing.T) *HardCase {
	t.Helper()
	h, err := DefaultHardCase()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func has(sigs []Signal, want Signal) bool {
	for _, s := range sigs {
		if s == want {
			return true
		}
	}
	return false
}

// The embedded lexicons parse, are small, and are versioned with the pack.
func TestHardCaseLexiconsLoadAndStaySmall(t *testing.T) {
	h := hc(t)
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != p.Version {
		t.Fatalf("hard-case lexicon v%d is not held in step with the pack v%d", h.Version, p.Version)
	}
	for name, l := range h.lex {
		if n := len(l.words) + len(l.phrases); n == 0 || n > maxLexiconEntries {
			t.Errorf("lexicon %s has %d entries, want 1-%d", name, n, maxLexiconEntries)
		}
	}
}

func TestHardCaseSignals(t *testing.T) {
	h := hc(t)
	cases := []struct {
		name  string
		text  string
		in    SignalInput
		want  Signal
		other bool // true: assert the signal is absent
	}{
		{"threat needs address", "i will find you and kill you", SignalInput{}, SignalThreat, false},
		{"violence alone is not a threat", "nice kill, that was a great shot", SignalInput{}, SignalThreat, true},
		{"a reply addresses", "hope they die", SignalInput{Reply: true}, SignalThreat, false},
		{"an @ addresses", "@someone shoot", SignalInput{}, SignalThreat, false},
		{"self harm needs no address", "i just want to die", SignalInput{}, SignalSelfHarm, false},
		{"abbreviation", "kys", SignalInput{}, SignalSelfHarm, false},
		{"sexual aimed", "you are so sexy", SignalInput{}, SignalSexualAddressed, false},
		{"sexual unaimed", "that movie was sexy", SignalInput{}, SignalSexualAddressed, true},
		{"insult in a reply", "what an idiot", SignalInput{Reply: true}, SignalInsultReply, false},
		{"insult with no target", "this boss is stupid", SignalInput{}, SignalInsultReply, true},
		{"contextual hate carried in", "hello", SignalInput{ContextualHate: true, TermHit: true}, SignalContextualHate, false},
		{"leet residue", "h3ll0 th3r3 fr13nd", SignalInput{Raw: "h3ll0 th3r3 fr13nd"}, SignalObfuscation, false},
		{"residue is moot after a term hit", "h3ll0 th3r3", SignalInput{Raw: "h3ll0 th3r3", TermHit: true}, SignalObfuscation, true},
		{"gaming numbers are not leet", "10k 4k 1v1 5v5 gr8", SignalInput{Raw: "10k 4k 1v1 5v5 gr8"}, SignalObfuscation, true},
		{"one substitution is not residue", "h3llo", SignalInput{Raw: "h3llo"}, SignalObfuscation, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.in.Raw == "" {
				c.in.Raw = c.text
			}
			got := h.Signals(NewForms(c.text), c.in)
			if has(got, c.want) == c.other {
				t.Fatalf("signals %v, want %s present=%v", got, c.want, !c.other)
			}
		})
	}
}

func TestHardCaseOrdinaryChatShowsNoSignal(t *testing.T) {
	h := hc(t)
	for _, text := range []string{
		"gg everyone", "what game is next?", "LUL", "that was close", "hello chat",
		"how long have you been streaming", "love the stream", "POG",
	} {
		if got := h.Signals(NewForms(text), SignalInput{Raw: text}); len(got) != 0 {
			t.Errorf("%q gave %v", text, got)
		}
	}
	var nilHC *HardCase
	if got := nilHC.Signals(NewForms("kill you"), SignalInput{}); got != nil {
		t.Fatalf("a nil lexicon gave %v", got)
	}
}

// Zero-width characters and look-alike letters are substitutions; a symbols-only
// line has an empty pack form and is judged on obfuscation alone.
func TestSubstitutionsCountsFormatAndLookalikes(t *testing.T) {
	if n := substitutions("he​l​lo"); n != 2 {
		t.Errorf("zero-width = %d, want 2", n)
	}
	if n := substitutions("hеllo wоrld"); n < 2 {
		t.Errorf("cyrillic look-alikes = %d, want at least 2", n)
	}
	if n := substitutions("plain ordinary text"); n != 0 {
		t.Errorf("plain text = %d", n)
	}
	// A line of nothing but symbols has no word to read, so no signal.
	zw := "\u200b\u200b!!"
	if got := hc(t).Signals(NewForms(zw), SignalInput{Raw: zw}); len(got) != 0 {
		t.Errorf("symbols-only gave %v", got)
	}
	// A zero-width joiner between emoji (every family emoji) is not a word.
	fam := "\U0001F468\u200d\U0001F469\u200d\U0001F467 \U0001F468\u200d\U0001F469\u200d\U0001F466"
	if n := substitutions(fam); n != 0 {
		t.Errorf("a family emoji counts %d substitutions", n)
	}
}

// Ordinary chat in a non-Latin script is not obfuscation, however many of its
// letters have a Latin look-alike: only a MIXED-script word is the tell.
func TestPlainCyrillicAndGreekAreNotObfuscation(t *testing.T) {
	h := hc(t)
	for _, text := range []string{
		"\u043f\u0440\u0438\u0432\u0435\u0442 \u0432\u0441\u0435\u043c, \u043a\u0430\u043a \u0434\u0435\u043b\u0430 \u0441\u0435\u0433\u043e\u0434\u043d\u044f",
		"\u043e\u0447\u0435\u043d\u044c \u043a\u0440\u0443\u0442\u043e\u0439 \u0441\u0442\u0440\u0438\u043c, \u0441\u043f\u0430\u0441\u0438\u0431\u043e",
		"\u03b3\u03b5\u03b9\u03b1 \u03c3\u03bf\u03c5 \u03c0\u03bf\u03bb\u03cd \u03c9\u03c1\u03b1\u03af\u03bf \u03c3\u03c4\u03c1\u03b9\u03bc",
	} {
		if n := substitutions(text); n != 0 {
			t.Errorf("%q counts %d substitutions", text, n)
		}
		if got := h.Signals(NewForms(text), SignalInput{Raw: text}); has(got, SignalObfuscation) {
			t.Errorf("%q reads as obfuscation", text)
		}
	}
}

// The self-harm lexicon matches the typed apostrophe: the pack form splits
// "don't" in two, and so does the entry.
func TestHardCaseApostropheSpellingsMatch(t *testing.T) {
	h := hc(t)
	for _, text := range []string{"i don't want to live", "i dont want to live", "I can't go on", "i don\u2019t wanna live"} {
		if got := h.Signals(NewForms(text), SignalInput{Raw: text}); !has(got, SignalSelfHarm) {
			t.Errorf("%q gave %v", text, got)
		}
	}
	if got := h.Signals(NewForms("you'll regret it"), SignalInput{Raw: "you'll regret it"}); !has(got, SignalThreat) {
		t.Errorf("you'll regret gave %v", got)
	}
}

func TestParseHardCaseRejectsABadFile(t *testing.T) {
	for name, src := range map[string]string{
		"not json":       `{`,
		"unknown field":  `{"id":"en","version":1,"nope":1}`,
		"no id":          `{"version":1}`,
		"empty lexicon":  `{"id":"en","version":1,"address":["you"],"lexicons":{"violence":[]}}`,
		"phrase address": `{"id":"en","version":1,"address":["find you"],"lexicons":{}}`,
		"extra lexicon":  validWith(`,"bogus":["x"]`),
		"no letters":     strings.Replace(validWith(``), `"kill"`, `"!!!"`, 1),
	} {
		if _, err := ParseHardCase([]byte(src)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := ParseHardCase([]byte(validWith(``))); err != nil {
		t.Fatalf("the control file is rejected: %v", err)
	}
}

func validWith(extra string) string {
	return `{"id":"en","version":1,"address":["you"],"lexicons":{"violence":["kill"],"self_harm":["kms"],"sexual":["sex"],"insult":["idiot"]` + extra + `}}`
}
