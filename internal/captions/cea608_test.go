package captions

import (
	"strings"
	"testing"
)

// These tests pin the CEA-608 byte layer that the SEI injector carries. The
// pair values are checked against the standard's own byte tables, not against
// the encoder under test, so a wrong code fails here and not on a player.

// TestOddParityMakesEveryByteHaveAnOddNumberOfOneBits pins the parity rule.
// CEA-608 bytes carry odd parity in bit 7 on the wire, and a decoder that sees
// an even count drops the byte silently.
func TestOddParityMakesEveryByteHaveAnOddNumberOfOneBits(t *testing.T) {
	for b := 0; b < 0x80; b++ {
		got := oddParity(byte(b))
		ones := 0
		for x := got; x != 0; x >>= 1 {
			ones += int(x & 1)
		}
		if ones%2 != 1 {
			t.Fatalf("oddParity(0x%02X) = 0x%02X has an even count of one bits", b, got)
		}
		if got&0x7f != byte(b) {
			t.Fatalf("oddParity(0x%02X) = 0x%02X changed the low seven bits", b, got)
		}
	}
}

// TestPopOnScriptStartsWithResumeCaptionLoadingAndEndsWithEndOfCaption pins the
// control framing. RCL (resume caption loading) selects pop-on, and EOC (end of
// caption) is what flips the hidden memory onto the screen. Without EOC the
// text never appears, and without RCL the decoder may treat it as roll-up.
func TestPopOnScriptStartsWithResumeCaptionLoadingAndEndsWithEndOfCaption(t *testing.T) {
	script := PopOnScript("HI")
	if len(script) < 4 {
		t.Fatalf("script too short: %d pairs", len(script))
	}
	if script[0] != pair(0x14, 0x20) || script[1] != pair(0x14, 0x20) {
		t.Fatalf("script must open with RCL sent twice, got %v %v", script[0], script[1])
	}
	last := script[len(script)-1]
	if last != pair(0x14, 0x2F) || script[len(script)-2] != pair(0x14, 0x2F) {
		t.Fatalf("script must close with EOC sent twice, got %v %v", script[len(script)-2], last)
	}
}

// TestPopOnScriptCarriesTheTextAsPrintablePairsAfterTheRowAddress checks the
// shape of one line: a preamble address code for the row, then the characters
// two per pair. A single-line caption on the bottom row is the common case.
func TestPopOnScriptCarriesTheTextAsPrintablePairsAfterTheRowAddress(t *testing.T) {
	script := PopOnScript("HELLO")
	// Find the first character pair ('H','E') after the RCL pairs and the PAC.
	var data []Pair
	for _, p := range script {
		if p[0]&0x7f >= 0x20 {
			data = append(data, p)
		}
	}
	if len(data) < 3 {
		t.Fatalf("expected character pairs, got %d", len(data))
	}
	if data[0] != pair('H', 'E') || data[1] != pair('L', 'L') || data[2] != pair('O', 0) {
		t.Fatalf("text pairs = %v, want HE LL O\\0", data[:3])
	}
}

// TestPreambleAddressCodeForBottomRowIsRow15 pins the bottom row, where a
// caption belongs. The row codes are not sequential; rows 1 and 2 share first
// byte 0x11, and row 15 is the one with 0x14 0x40. A wrong row here moves every
// caption to the wrong place on screen without any error.
func TestPreambleAddressCodeForBottomRowIsRow15(t *testing.T) {
	first, second := paCode(15, 0)
	if first != 0x14 || second != 0x40 {
		t.Fatalf("row 15 indent 0 = %#x %#x, want 0x14 0x40", first, second)
	}
	// Row 14 is the even row of the 13/14 pair: first byte 0x13, second 0x60.
	first, second = paCode(14, 0)
	if first != 0x13 || second != 0x60 {
		t.Fatalf("row 14 indent 0 = %#x %#x, want 0x13 0x60", first, second)
	}
	// Row 1 is the first row of the 0x11 pair.
	first, second = paCode(1, 0)
	if first != 0x11 || second != 0x40 {
		t.Fatalf("row 1 indent 0 = %#x %#x, want 0x11 0x40", first, second)
	}
}

// TestBasicCharsetMapsSpanishAccentsToTheirOwnCodes pins the North American
// basic set. Several printable ASCII positions are accented letters on a 608
// decoder, so sending '*' displays 'á'. The accents must go out at their own
// positions or they render as punctuation.
func TestBasicCharsetMapsSpanishAccentsToTheirOwnCodes(t *testing.T) {
	cases := map[rune]byte{
		'á': 0x2A, 'é': 0x5C, 'í': 0x5E, 'ó': 0x5F, 'ú': 0x60,
		'ç': 0x7B, '÷': 0x7C, 'Ñ': 0x7D, 'ñ': 0x7E,
	}
	for r, want := range cases {
		got, ok := basicCode(r)
		if !ok || got != want {
			t.Errorf("basicCode(%q) = %#x, %v; want %#x", r, got, ok, want)
		}
	}
}

// TestSpecialCharactersUseTheExtendedTwoByteCodes pins the special set, which
// lives behind the 0x11 control prefix. The music note is the one a
// captioner reaches for most, so it is pinned by name.
func TestSpecialCharactersUseTheExtendedTwoByteCodes(t *testing.T) {
	script := PopOnScript("♪")
	// The special character is a control pair (0x11, 0x37), sent twice like
	// every control code, and it must come before the EOC pairs.
	want := pair(0x11, 0x37)
	found := 0
	for _, p := range script {
		if p == want {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("music note pair %v found %d times, want 2 (control codes are sent twice)", want, found)
	}
}

// TestCharactersOutsideTheSetBecomeAQuestionMark stops a non-Latin caption
// from emitting garbage bytes. 608 cannot carry Cyrillic, CJK or most of the
// locales the app ships in, so those become '?', and the docs say so.
func TestCharactersOutsideTheSetBecomeAQuestionMark(t *testing.T) {
	script := PopOnScript("Ж")
	for _, p := range script {
		if p[0]&0x7f == '?' {
			return
		}
	}
	t.Fatalf("Cyrillic character must map to '?' in the 608 script, got %v", script)
}

// TestAsciiPositionsTheBasicSetRemapAreNotSentAsLiteralCharacters checks that
// '*' (which 608 displays as 'á') is not passed through, because that would put
// the wrong glyph on screen. It becomes '?' instead.
func TestAsciiPositionsTheBasicSetRemapAreNotSentAsLiteralCharacters(t *testing.T) {
	for _, r := range []rune{'*', '\\', '^', '_', '`', '{', '|', '}', '~'} {
		script := PopOnScript(string(r))
		for _, p := range script {
			if p[0] == byte(r)|0x80 || p[0] == byte(r) {
				t.Fatalf("character %q was sent literally as %v", r, p)
			}
		}
	}
}

// TestWrapsAtThirtyTwoColumnsOnWordBoundaries pins the screen width. A 608 row
// holds 32 columns, and a long line that is not wrapped loses its tail.
func TestWrapsAtThirtyTwoColumnsOnWordBoundaries(t *testing.T) {
	rows := wrapLines("one two three four five six seven eight nine ten eleven twelve")
	for _, r := range rows {
		if len(r) > 32 {
			t.Fatalf("row %q is %d columns, the limit is 32", r, len(r))
		}
	}
	if strings.Join(rows, " ") != "one two three four five six seven eight nine ten eleven twelve" {
		t.Fatalf("wrapping lost or reordered words: %q", rows)
	}
}

// TestAWordLongerThanARowIsSplitRatherThanDropped covers a URL or a long
// compound. Splitting it keeps every character on screen.
func TestAWordLongerThanARowIsSplitRatherThanDropped(t *testing.T) {
	long := strings.Repeat("x", 40)
	rows := wrapLines(long)
	if strings.Join(rows, "") != long {
		t.Fatalf("long word lost characters: %q", rows)
	}
	for _, r := range rows {
		if len(r) > 32 {
			t.Fatalf("split row %q is over 32 columns", r)
		}
	}
}

// TestMoreThanFourRowsKeepsTheNewestLines pins the pop-on limit. Four rows fit
// on screen, and the caption that is still being spoken is the last one, so the
// oldest rows go.
func TestMoreThanFourRowsKeepsTheNewestLines(t *testing.T) {
	text := "alpha\nbravo\ncharlie\ndelta\necho\nfoxtrot"
	rows := lastRows(wrapLines(text), 4)
	if len(rows) != 4 || rows[0] != "charlie" || rows[3] != "foxtrot" {
		t.Fatalf("rows = %q, want the newest four", rows)
	}
}

// TestClearScriptIsEraseDisplayedMemorySentTwice pins the clear code. EDM
// (0x14 0x2C) removes what is on screen. A caption that is never cleared stays
// up across the next speaker.
func TestClearScriptIsEraseDisplayedMemorySentTwice(t *testing.T) {
	script := ClearScript()
	if len(script) != 2 || script[0] != pair(0x14, 0x2C) || script[1] != pair(0x14, 0x2C) {
		t.Fatalf("ClearScript() = %v, want EDM twice", script)
	}
}

// pair builds an odd-parity pair the way the wire carries it, so the tests can
// name bytes by their 7-bit values.
func pair(a, b byte) Pair { return Pair{oddParity(a), oddParity(b)} }
