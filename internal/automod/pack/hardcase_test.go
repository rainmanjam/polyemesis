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
		// Everyday phrasings next to the self-harm lexicon.
		"did you miss me chat", "ending it here tonight, gn", "i want out of this queue",
		"i can't do this anymore lol this boss",
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

// Ordinary chat that happens to hold digits is not leet: a link, a frame rate,
// a level, a numbered emote and a name with a number on the end.
func TestObfuscationIgnoresLinksNumbersAndEmotes(t *testing.T) {
	h := hc(t)
	for _, text := range []string{
		"https://www.youtube.com/watch?v=demo123",
		"watch this www.example.com/a1b2c3",
		"what's your setting, 30fps?",
		"lvl50 boss now", "wave10 incoming", "season2024 rocks", "player123 gg",
		"4Head 4Head 4Head 4Head 4Head 4Head", "5Head 3Head 4Head",
		"nasılsınız hepiniz", "cześć wszystkim", "yeni bölüm çok zor",
	} {
		if got := h.Signals(NewForms(text), SignalInput{Raw: text}); has(got, SignalObfuscation) {
			t.Errorf("%q read as obfuscation: %v", text, got)
		}
	}
}

// Real leet still reads, including words of only two letters.
func TestObfuscationStillReadsRealLeet(t *testing.T) {
	h := hc(t)
	for _, text := range []string{"y0u 5uck at th15 g4m3", "k1ll y0u", "h3ll0 th3r3 fr13nd", "g4m3"} {
		if got := h.Signals(NewForms(text), SignalInput{Raw: text}); !has(got, SignalObfuscation) {
			t.Errorf("%q gave no obfuscation signal: %v", text, got)
		}
	}
	for text, want := range map[string]int{"k1ll": 1, "30fps": 0, "gr8": 0, "1v1": 0, "x86": 0} {
		if n := substitutions(text); n != want {
			t.Errorf("substitutions(%q) = %d, want %d", text, n, want)
		}
	}
}

// The lines D14 names, which the lexicons used to miss.
func TestHardCaseNamedLinesAreCaught(t *testing.T) {
	h := hc(t)
	for text, want := range map[string]Signal{
		"i don't want to be alive anymore":               SignalSelfHarm,
		"i feel like ending it all":                      SignalSelfHarm,
		"i can't do this anymore, i want out for good":   SignalSelfHarm,
		"you won't be alive next week":                   SignalThreat,
		"you look hot today":                             SignalSexualAddressed,
		"ur voice is hot":                                SignalSexualAddressed,
		"dm me a pic u":                                  SignalSexualAddressed,
		"@marble_owl you are the worst mod ever":         SignalInsultReply,
		"@oak_signal everyone here thinks you're a joke": SignalInsultReply,
	} {
		if got := h.Signals(NewForms(text), SignalInput{Raw: text}); !has(got, want) {
			t.Errorf("%q gave %v, want %s", text, got, want)
		}
	}
}

// A non-decomposing Latin confusable inside an ASCII word is the disguise;
// letters of a real alphabet (Turkish, Polish, Nordic) are spelling.
func TestLatinConfusablesCountButAlphabetsDoNot(t *testing.T) {
	if n := substitutions("p\u0251ss w\u0251rd"); n < 2 {
		t.Errorf("IPA alpha look-alikes = %d, want at least 2", n)
	}
	if n := substitutions("\u0261ood \u0261ame"); n < 2 {
		t.Errorf("script g look-alikes = %d, want at least 2", n)
	}
	for _, text := range []string{"nas\u0131ls\u0131n\u0131z hepiniz", "mały łódź", "København Ærø", "größe Straße", "cześć wszystkim"} {
		if n := substitutions(text); n != 0 {
			t.Errorf("%q counts %d substitutions", text, n)
		}
	}
}

// Dots and a slash inside a word are not a link. "k1ll.y0u/x" used to read as
// an address and hide the obfuscation residue; real links still do.
func TestDotsAndASlashAreNotALinkUnlessTheyAreOne(t *testing.T) {
	h := hc(t)
	for _, text := range []string{"k1ll.y0u/x", "y0u.5uck/k1ll", "k1ll y0u k.y/s"} {
		if got := h.Signals(NewForms(text), SignalInput{Raw: text}); !has(got, SignalObfuscation) {
			t.Errorf("%q gave no obfuscation signal: %v (substitutions %d)", text, got, h.substitutions(text))
		}
	}
	for _, link := range []string{"https://a.com/x1", "http://clips.twitch.tv/Gr8Clip4", "www.x1y2.co", "twitch.tv/h3llo", "clips.twitch.tv/Abc123Def456", "example.com/p4th5"} {
		if !urlLike(link) {
			t.Errorf("%q is a link and was not read as one", link)
		}
		if n := substitutions(link); n != 0 {
			t.Errorf("substitutions(%q) = %d, want 0: a link is not spelling", link, n)
		}
	}
	for _, notLink := range []string{"a.b/c", "k.y/s", "k1ll.y0u/x", "x.zzzz/1"} {
		if urlLike(notLink) {
			t.Errorf("%q has no real host and was read as a link", notLink)
		}
	}
}

// A digit at the edge of a word is a number attached to it ("4Head", "30fps")
// unless the leet-folded word is a lexicon term.
func TestEdgeStandInsCountWhenTheWordIsALexiconTerm(t *testing.T) {
	h := hc(t)
	for _, text := range []string{"5uicide 5uicide", "5uicide k1ll"} {
		if got := h.Signals(NewForms(text), SignalInput{Raw: text}); !has(got, SignalObfuscation) {
			t.Errorf("%q gave no obfuscation signal: %v", text, got)
		}
	}
	for _, text := range []string{"4Head 4Head", "30fps 10k", "gr8 lvl50"} {
		if got := h.Signals(NewForms(text), SignalInput{Raw: text}); has(got, SignalObfuscation) {
			t.Errorf("%q wrongly read as obfuscation", text)
		}
	}
}
