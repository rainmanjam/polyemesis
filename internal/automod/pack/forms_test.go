package pack

import (
	"reflect"
	"strings"
	"testing"
)

func TestNFKCFoldsCompatibilityAndStripsFormatCharacters(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ｈｅｌｌｏ", "hello"},       // fullwidth
		{"he​l‍lo", "hello"},     // zero-width space and joiner
		{"\ufeffhello", "hello"}, // BOM
		{"he‮l‬lo", "hello"},     // bidi controls
		{"ﬁne", "fine"},          // ligature
		{"plain ascii", "plain ascii"},
	}
	for _, c := range cases {
		if got := NFKC(c.in); got != c.want {
			t.Errorf("NFKC(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSkeletonMapsLookalikesAndLeavesASCIIAlone(t *testing.T) {
	// "example" in Cyrillic look-alikes: е х а м р l е
	if got := Skeleton("ехаmрlе"); got != "example" {
		t.Errorf("Skeleton = %q, want example", got)
	}
	// ASCII is never rewritten: UTS #39 would send 'm' to "rn" and 'I' to 'l'.
	for _, s := range []string{"mmm IIII rn 0O", "twitch.tv"} {
		if got := Skeleton(s); got != s {
			t.Errorf("Skeleton(%q) = %q, want it unchanged", s, got)
		}
	}
	if n := testing.AllocsPerRun(50, func() { _ = Skeleton("pure ascii text") }); n != 0 {
		t.Errorf("Skeleton allocates %v times on ASCII, want 0", n)
	}
}

func TestSkeletonTableIsSortedAndASCIIOnly(t *testing.T) {
	for i, e := range skeletonTable {
		if e.r < 0x80 {
			t.Fatalf("entry %d maps an ASCII rune", i)
		}
		if i > 0 && skeletonTable[i-1].r >= e.r {
			t.Fatalf("entry %d is out of order or repeated", i)
		}
		for _, c := range []byte(e.to) {
			if !('a' <= c && c <= 'z' || '0' <= c && c <= '9') {
				t.Fatalf("entry %d maps to %q, which is not lowercase ASCII letters and digits", i, e.to)
			}
		}
	}
	if len(skeletonTable) < 300 {
		t.Fatalf("skeleton table has only %d entries; the generator output looks truncated", len(skeletonTable))
	}
}

func TestFold(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"lowercase and tokens", "Hello, World!", "hello world"},
		{"leet inside a word", "h3ll0 w0rld", "hello world"},
		{"digits stay digits without a letter", "score 2024 or 1-0", "score or"},
		{"symbols inside a word", "pa$$w0rd", "password"},
		{"mention is not leet", "@alice hi", "alice hi"},
		{"trailing bang is punctuation", "wow!", "wow"},
		{"bang inside a word", "wh!te", "white"},
		{"repeats collapse to two", "fuuuuuck", "fuuck"},
		{"double letters survive", "good book", "good book"},
		{"zero-width inside a word", "he​llo", "hello"},
		{"fullwidth", "ｈｅｌｌｏ", "hello"},
		{"confusables", "ехаmрlе", "example"},
		{"empty", "", ""},
		{"only punctuation", "!!! ... ???", ""},
	}
	for _, c := range cases {
		if got := Fold(c.in); got != c.want {
			t.Errorf("%s: Fold(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestFoldKeepsWordsInOtherScriptsWhole(t *testing.T) {
	// Combining marks belong to the word: Devanagari, Thai and Vietnamese must
	// not shatter into one-letter tokens.
	for _, s := range []string{"नमस्ते दुनिया", "สวัสดีครับ", "xin chào các bạn"} {
		f := NewForms(s)
		for _, tok := range f.Tokens {
			if len([]rune(tok)) < 2 {
				t.Errorf("NewForms(%q) produced the one-rune token %q", s, tok)
			}
		}
	}
}

func TestFoldIsIdempotent(t *testing.T) {
	for _, s := range []string{"h3ll0 w0rld", "fuuuuuck you", "ＡＢＣ ｄｅｆ", "pa$$w@rd!!", "ехаmрlе"} {
		once := Fold(s)
		if twice := Fold(once); twice != once {
			t.Errorf("Fold is not idempotent on %q: %q then %q", s, once, twice)
		}
	}
}

func TestNewFormsTokensAreSubstringsOfPack(t *testing.T) {
	f := NewForms("Hello   there, W0rld!  ")
	if f.Pack != "hello there world" {
		t.Fatalf("Pack = %q", f.Pack)
	}
	if want := []string{"hello", "there", "world"}; !reflect.DeepEqual(f.Tokens, want) {
		t.Fatalf("Tokens = %q, want %q", f.Tokens, want)
	}
	if f.Despaced != "" {
		t.Errorf("Despaced = %q for ordinary text, want empty", f.Despaced)
	}
}

func TestDespacedOnlyWhenDeliberate(t *testing.T) {
	if got := NewForms("b a d w o r d").Despaced; got != "badword" {
		t.Errorf("Despaced = %q, want badword", got)
	}
	if got := NewForms("s s s s s p a m").Despaced; got != "sspam" {
		t.Errorf("Despaced = %q, want runs collapsed: sspam", got)
	}
	// The Scunthorpe road: ordinary short words must not be glued together.
	for _, s := range []string{"a bad wordsmith", "I am a big fan", "so I go to a bar"} {
		if got := NewForms(s).Despaced; got != "" {
			t.Errorf("Despaced(%q) = %q, want empty", s, got)
		}
	}
}

func TestNFKCFormKeepsDigitsForDoxxing(t *testing.T) {
	f := NewForms("call ＋１ ５５５ ０１００ now")
	if !strings.Contains(f.NFKC, "+1 555 0100") {
		t.Errorf("NFKC form lost the digits: %q", f.NFKC)
	}
}

func TestNameFoldsLookalikes(t *testing.T) {
	// twitch.tv against a Cyrillic і, and a streamer name with decoration.
	if a, b := Name("twitch.tv"), Name("twіtch.tv"); a != b || a != "twitchtv" {
		t.Errorf("Name = %q and %q, want both twitchtv", a, b)
	}
	if a, b := Name("Streamer_Name"), Name("ѕtreаmer-name"); a != b {
		t.Errorf("Name(%q) != Name(%q)", a, b)
	}
	if Name("streamer") == Name("streamer2") {
		t.Error("Name drops digits; the impersonation fold strips digit suffixes itself")
	}
}

// Pack matching is whole-token against an English list. These ordinary words in
// other languages and places must not skeletonise onto a hate term.
func TestOrdinaryWordsAreNotHateTerms(t *testing.T) {
	p, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	hate := map[string]bool{}
	for _, e := range p.Entries {
		if e.Category == CategoryHate {
			hate[e.Term] = true
		}
	}
	words := []string{
		// Scunthorpe-style English
		"Scunthorpe", "Penistone", "Middlesex", "Essex", "cocktail", "assassin", "therapist",
		"grapes", "analysis", "Dickens", "shiitake", "wordsmith", "class", "classic", "bass",
		// other languages, in their own scripts and romanised
		"привет", "спасибо", "дякую", "merhaba", "teşekkürler", "dziękuję", "danke", "gracias",
		"terima kasih", "konnichiwa", "nihao", "नमस्ते", "สวัสดี", "xin chào", "Nikita", "Dikshit",
		// numbers and versions
		"2024", "1-0", "21-19", "12:30:45", "v1.2.3.4", "1920x1080", "6000 kbps",
	}
	for _, w := range words {
		for _, tok := range NewForms(w).Tokens {
			if hate[tok] {
				t.Errorf("%q folds onto a hate term", w)
			}
		}
	}
}
