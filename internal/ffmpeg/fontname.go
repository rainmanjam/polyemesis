package ffmpeg

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"unicode/utf16"
)

// FontFamily reads a TrueType/OpenType font's family name and whether it is a
// bold weight, from its name and OS/2 tables.
//
// libass selects a font by family name (through fontconfig), while a caption
// style names a FILE the operator chose from the fonts directory. This is the
// bridge, and it reads the file itself rather than guessing from the name: a
// file called Inter-Bold.ttf declares the family "Inter" at weight 700.
//
// The typographic family (name ID 16) is preferred, then the legacy family
// (ID 1); Windows-platform UTF-16 records are preferred, then Macintosh
// Roman. Bold is a weight class of 600 or more. A collection (.ttc) is read
// through its first font.
func FontFamily(path string) (family string, bold bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	base := 0
	if len(b) >= 16 && string(b[:4]) == "ttcf" {
		base = int(binary.BigEndian.Uint32(b[12:16]))
	}
	tables, err := sfntTables(b, base)
	if err != nil {
		return "", false, err
	}
	name, ok := tables["name"]
	if !ok {
		return "", false, errNotAFont
	}
	family = nameRecord(name, 16)
	if family == "" {
		family = nameRecord(name, 1)
	}
	if family == "" {
		return "", false, errNotAFont
	}
	if os2, ok := tables["OS/2"]; ok && len(os2) >= 6 {
		bold = binary.BigEndian.Uint16(os2[4:6]) >= 600
	}
	return family, bold, nil
}

var errNotAFont = errors.New("not a TrueType or OpenType font")

func sfntTables(b []byte, base int) (map[string][]byte, error) {
	if base < 0 || len(b) < base+12 {
		return nil, errNotAFont
	}
	switch v := binary.BigEndian.Uint32(b[base : base+4]); v {
	case 0x00010000, 0x4F54544F, 0x74727565: // 1.0, "OTTO", "true"
	default:
		return nil, errNotAFont
	}
	n := int(binary.BigEndian.Uint16(b[base+4 : base+6]))
	out := map[string][]byte{}
	for i := 0; i < n; i++ {
		rec := base + 12 + i*16
		if rec+16 > len(b) {
			return nil, errNotAFont
		}
		tag := string(b[rec : rec+4])
		off := int(binary.BigEndian.Uint32(b[rec+8 : rec+12]))
		ln := int(binary.BigEndian.Uint32(b[rec+12 : rec+16]))
		if off < 0 || ln < 0 || off+ln > len(b) {
			return nil, errNotAFont
		}
		out[tag] = b[off : off+ln]
	}
	return out, nil
}

// nameRecord returns name id from a name table, preferring Windows UTF-16.
func nameRecord(t []byte, id uint16) string {
	if len(t) < 6 {
		return ""
	}
	count := int(binary.BigEndian.Uint16(t[2:4]))
	strOff := int(binary.BigEndian.Uint16(t[4:6]))
	var mac string
	for i := 0; i < count; i++ {
		r := 6 + i*12
		if r+12 > len(t) {
			break
		}
		platform := binary.BigEndian.Uint16(t[r:])
		nameID := binary.BigEndian.Uint16(t[r+6:])
		ln := int(binary.BigEndian.Uint16(t[r+8:]))
		off := strOff + int(binary.BigEndian.Uint16(t[r+10:]))
		if nameID != id || off+ln > len(t) {
			continue
		}
		raw := t[off : off+ln]
		switch platform {
		case 3, 0:
			if s := utf16NameString(raw); s != "" {
				return s
			}
		case 1:
			if mac == "" {
				mac = strings.TrimSpace(string(raw))
			}
		}
	}
	return mac
}

// utf16NameString decodes a big-endian UTF-16 name record, trimmed of spaces.
func utf16NameString(raw []byte) string {
	u := make([]uint16, len(raw)/2)
	for j := range u {
		u[j] = binary.BigEndian.Uint16(raw[2*j:])
	}
	return strings.TrimSpace(string(utf16.Decode(u)))
}
