package captions

// H.264 SEI carriage of CEA-608 pairs, as ATSC A/53 cc_data inside a
// user_data_registered_itu_t_t35 message.
//
// One SEI NAL per picture carries one pair. Frames with no caption text carry
// the null pair, 0x80 0x80, which a decoder ignores, so the caption channel
// stays populated the same way a real encoder populates it.

// Payload type 4 is user_data_registered_itu_t_t35 in H.264 Annex D.
const seiPayloadUserDataRegistered = 4

// a53Header is the fixed part of an ATSC A/53 cc_data payload: the US country
// code, the ATSC provider code, the "GA94" user identifier and the cc_data
// type code.
var a53Header = []byte{0xB5, 0x00, 0x31, 'G', 'A', '9', '4', 0x03}

// nullPair is the CEA-608 null pair with odd parity applied. It occupies a
// picture when there is no caption to send.
var nullPair = Pair{0x80, 0x80}

// splitAnnexB returns each NAL unit of an Annex B byte stream with its start
// code removed. Trailing zero bytes belong to the next start code, not to the
// unit before it.
func splitAnnexB(b []byte) [][]byte {
	var nals [][]byte
	var starts []int
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			starts = append(starts, i+3)
			i += 2
		}
	}
	for k, s := range starts {
		e := len(b)
		if k+1 < len(starts) {
			e = starts[k+1] - 3
			// A four-byte start code leaves one extra zero before the next 00 00 01.
			for e > s && b[e-1] == 0 {
				e--
			}
		}
		nals = append(nals, b[s:e])
	}
	return nals
}

// escapeRBSP inserts an emulation_prevention_three_byte wherever 00 00 is
// followed by a byte in 00 to 03, so the payload can never imitate a start
// code.
func escapeRBSP(rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+4)
	zeros := 0
	for _, c := range rbsp {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// buildCCSEI returns one SEI NAL unit, header byte included and without a start
// code, carrying a single cc_data triplet for field 1 with the given pair.
func buildCCSEI(p Pair) []byte {
	payload := make([]byte, 0, 14)
	payload = append(payload, a53Header...)
	payload = append(payload,
		0x40|0x01,      // process_em_data=0, process_cc_data=1, additional_data=0, cc_count=1
		0xFF,           // em_data, reserved
		0xF8|0x04|0x00, // marker bits 11111, cc_valid=1, cc_type=0 (608 field 1)
		p[0], p[1],     // cc_data_1, cc_data_2
		0xFF, // marker_bits closes the cc_data
	)
	rbsp := []byte{seiPayloadUserDataRegistered, byte(len(payload))}
	rbsp = append(rbsp, payload...)
	rbsp = append(rbsp, 0x80) // rbsp_trailing_bits
	return append([]byte{0x06}, escapeRBSP(rbsp)...)
}

// nalType is the low five bits of the NAL header byte.
func nalType(nal []byte) byte {
	if len(nal) == 0 {
		return 0
	}
	return nal[0] & 0x1f
}

// startsPicture reports whether a NAL is the first slice of a picture. A slice
// header begins with first_mb_in_slice as ue(v), and the value zero is the
// single bit 1, which is the most significant bit of the byte after the header.
func startsPicture(nal []byte) bool {
	t := nalType(nal)
	return (t == 1 || t == 5) && len(nal) > 1 && nal[1]&0x80 != 0
}

// insertCCSEI returns an Annex B access unit with the SEI placed immediately
// before the first slice of the picture. An SEI must follow the parameter sets
// and the access unit delimiter and precede the first VCL NAL, so that is the
// only position a decoder reads it at.
//
// An access unit with no picture-starting slice is returned unchanged. Every
// other NAL keeps its payload bytes; each is written behind a four-byte start
// code, which is valid Annex B whatever width the input used.
func insertCCSEI(es []byte, p Pair) []byte {
	nals := splitAnnexB(es)
	at := -1
	for i, n := range nals {
		if startsPicture(n) {
			at = i
			break
		}
	}
	if at < 0 {
		return es
	}
	sei := buildCCSEI(p)
	out := make([]byte, 0, len(es)+len(sei)+4)
	for i, n := range nals {
		if i == at {
			out = append(out, 0, 0, 0, 1)
			out = append(out, sei...)
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}
