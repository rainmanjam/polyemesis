// Package captions carries live caption text into the video itself.
//
// Captions reach a player in two ways. A sidecar WebVTT file is read by the
// player, and CEA-608 byte pairs travel inside the H.264 stream as SEI user
// data, where a platform's ingest reads them and shows them with the picture.
// This package builds the second: the 608 byte script for a caption, and the
// SEI shim that places those pairs into the video.
//
// The 608 layer is pure. It knows nothing about transport, so it is tested on
// its own, and the SEI shim (tssei.go) consumes what it produces.
package captions

import (
	"strings"
	"unicode/utf8"
)

// Pair is one CEA-608 byte pair, odd parity already applied, as it goes on the
// wire. Field 1 control pairs are sent twice, as the standard recommends, so a
// decoder that lost one of the two still sees the code.
type Pair [2]byte

// Column and row limits of a 608 caption screen.
const (
	// Columns is how many characters fit on one caption row.
	Columns = 32
	// MaxRows is how many rows a pop-on caption may use. Rows 12 to 15 are the
	// bottom four, which is where a live caption sits.
	MaxRows = 4
)

// Control codes, as seven-bit values. Each is paired with a second byte that
// selects the function. Channel 1, field 1 throughout.
const (
	ctrlChannel1 = 0x14 // channel 1 control prefix
	ctrlRCL      = 0x20 // resume caption loading: pop-on mode
	ctrlEDM      = 0x2C // erase displayed memory
	ctrlENM      = 0x2E // erase non-displayed memory: clears the back buffer before a pop-on caption is written
	ctrlEOC      = 0x2F // end of caption: swap hidden memory onto the screen
	ctrlSpecial  = 0x11 // special-character prefix, channel 1
)

// oddParity sets bit 7 so the byte carries an odd number of one bits, which
// CEA-608 requires of every byte on the wire.
func oddParity(b byte) byte {
	b &= 0x7f
	ones := 0
	for x := b; x != 0; x >>= 1 {
		ones += int(x & 1)
	}
	if ones%2 == 0 {
		b |= 0x80
	}
	return b
}

// control builds a control pair, sent twice.
func control(a, b byte) []Pair {
	p := Pair{oddParity(a), oddParity(b)}
	return []Pair{p, p}
}

// paCode returns the two bytes of a preamble address code for a row (1 to 15)
// and an indent in columns (0 to 28, in steps of four). White, not underlined.
//
// The first byte is not sequential in the row number. Rows 1 and 2 share 0x11,
// rows 3 and 4 share 0x12, and so on; the odd row of each pair carries the
// 0x40 range and the even row the 0x60 range.
func paCode(row, indent int) (byte, byte) {
	firstByte := map[int]byte{
		1: 0x11, 2: 0x11, 3: 0x12, 4: 0x12,
		5: 0x15, 6: 0x15, 7: 0x16, 8: 0x16,
		9: 0x17, 10: 0x17, 11: 0x10, 12: 0x10,
		13: 0x13, 14: 0x13, 15: 0x14,
	}[row]
	second := byte(0x40)
	if row%2 == 0 {
		second = 0x60
	}
	second |= byte(indent/4) << 1 & 0x0e
	return firstByte, second
}

// basicRemap maps the accented letters that the North American basic set puts
// at seven-bit positions 0x2A, 0x5C, 0x5E, 0x5F, 0x60, 0x7B, 0x7C, 0x7D and
// 0x7E. Those positions show the accent, not the ASCII glyph, so the accented
// letter is the only character that can be sent there.
var basicRemap = map[rune]byte{
	'á': 0x2A, 'é': 0x5C, 'í': 0x5E, 'ó': 0x5F, 'ú': 0x60,
	'ç': 0x7B, '÷': 0x7C, 'Ñ': 0x7D, 'ñ': 0x7E,
}

// specialCodes maps the characters behind the 0x11 prefix, second byte 0x30 to
// 0x3F. Each is a control pair and is sent twice.
var specialCodes = map[rune]byte{
	'®': 0x30, '°': 0x31, '½': 0x32, '¿': 0x33, '™': 0x34, '¢': 0x35,
	'£': 0x36, '♪': 0x37, 'à': 0x38, 'è': 0x3A, 'â': 0x3B, 'ê': 0x3C,
	'î': 0x3D, 'ô': 0x3E, 'û': 0x3F,
}

// basicCode returns the single byte for a character in the basic set, when the
// character is one the set can show at its own position: printable ASCII that
// the set does not redefine, or one of the accented letters in basicRemap.
func basicCode(r rune) (byte, bool) {
	if b, ok := basicRemap[r]; ok {
		return b, true
	}
	if r < 0x20 || r > 0x7e {
		return 0, false
	}
	switch r {
	case '*', '\\', '^', '_', '`', '{', '|', '}', '~':
		// These seven-bit positions show an accent, not the glyph. There is no
		// way to put the glyph on screen, so the caller substitutes.
		return 0, false
	}
	return byte(r), true
}

// wrapLines splits text on newlines, then wraps each line at Columns on word
// boundaries. A word longer than a row is split so no character is lost.
//
// Spaces are dropped at a wrap point. Joining the rows with a single space gives
// the original text back, which the tests rely on.
func wrapLines(text string) []string {
	var rows []string
	for _, line := range strings.Split(text, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			continue
		}
		cur := ""
		for _, w := range words {
			// Columns are runes, not bytes: a multi-byte character takes one column, and a cut must
			// fall between characters.
			wr := []rune(w)
			for len(wr) > Columns {
				if cur != "" {
					rows = append(rows, cur)
					cur = ""
				}
				rows = append(rows, string(wr[:Columns]))
				wr = wr[Columns:]
			}
			w = string(wr)
			switch {
			case cur == "":
				cur = w
			case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) <= Columns:
				cur += " " + w
			default:
				rows = append(rows, cur)
				cur = w
			}
		}
		if cur != "" {
			rows = append(rows, cur)
		}
	}
	return rows
}

// lastRows keeps the newest n rows. A pop-on caption has at most MaxRows, and
// the line still on air is the last one.
func lastRows(rows []string, n int) []string {
	if len(rows) <= n {
		return rows
	}
	return rows[len(rows)-n:]
}

// charPairs packs a row's characters two to a pair. A special character is a
// control pair, so any pending single character is padded to a pair first. An
// odd tail is padded with a null, which decoders ignore.
func charPairs(s string) []Pair {
	var out []Pair
	var pending []byte
	flushPending := func() {
		for i := 0; i < len(pending); i += 2 {
			var b byte
			if i+1 < len(pending) {
				b = pending[i+1]
			}
			out = append(out, Pair{oddParity(pending[i]), oddParity(b)})
		}
		pending = pending[:0]
	}
	for _, r := range s {
		if code, ok := specialCodes[r]; ok {
			flushPending()
			out = append(out, control(ctrlSpecial, code)...)
			continue
		}
		if b, ok := basicCode(r); ok {
			pending = append(pending, b)
			continue
		}
		// Not representable. A question mark is what a reader expects where a
		// glyph is missing, and it is honest.
		pending = append(pending, '?')
	}
	flushPending()
	return out
}

// PopOnScript builds the byte pairs for one pop-on caption. The text is wrapped
// to 32 columns, the newest four rows are kept, and the last row lands on row
// 15, the bottom of the screen. The sequence is RCL, then ENM to clear the back
// buffer, then each row's address and text, then EOC, which puts the caption on
// screen.
//
// Control pairs are sent twice. A caption that cannot be shown in full because
// of the 608 character set still goes out, with '?' where a glyph is missing.
func PopOnScript(text string) []Pair {
	var out []Pair
	out = append(out, control(ctrlChannel1, ctrlRCL)...)
	// ENM clears the back buffer, so rows from an earlier, longer caption cannot show through a shorter one.
	out = append(out, control(ctrlChannel1, ctrlENM)...)
	rows := lastRows(wrapLines(text), MaxRows)
	first := 15 - len(rows) + 1
	for i, row := range rows {
		a, b := paCode(first+i, 0)
		out = append(out, control(a, b)...)
		out = append(out, charPairs(row)...)
	}
	out = append(out, control(ctrlChannel1, ctrlEOC)...)
	return out
}

// ClearScript erases the displayed memory. It is sent when a caption is due to
// come off screen, which is the end of its cue.
func ClearScript() []Pair {
	return control(ctrlChannel1, ctrlEDM)
}
