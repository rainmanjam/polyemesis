package captions

import (
	"bytes"
	"testing"
)

// TestCCSEIRoundTripsThePairThroughTheA53Layout reads the SEI back field by
// field. A decoder that finds the country code, provider, user identifier or
// the cc_data marker out of place drops the whole message, so each one is
// pinned against the value A/53 gives it.
func TestCCSEIRoundTripsThePairThroughTheA53Layout(t *testing.T) {
	p := pair(0x14, 0x20) // RCL, the first pair of any pop-on caption
	nal := buildCCSEI(p)
	if nal[0] != 0x06 {
		t.Fatalf("NAL header = %#x, want 0x06 (SEI)", nal[0])
	}
	rbsp := nal[1:]
	if rbsp[0] != seiPayloadUserDataRegistered {
		t.Fatalf("payloadType = %d, want %d", rbsp[0], seiPayloadUserDataRegistered)
	}
	if int(rbsp[1]) != 14 {
		t.Fatalf("payloadSize = %d, want 14", rbsp[1])
	}
	payload := rbsp[2 : 2+14]
	if !bytes.Equal(payload[:8], a53Header) {
		t.Fatalf("A/53 header = % x, want % x", payload[:8], a53Header)
	}
	if payload[8] != 0x41 {
		t.Fatalf("process_em/process_cc/cc_count byte = %#x, want 0x41", payload[8])
	}
	if payload[10] != 0xFC {
		t.Fatalf("cc_valid triplet marker = %#x, want 0xFC (valid, field 1)", payload[10])
	}
	if payload[11] != p[0] || payload[12] != p[1] {
		t.Fatalf("pair on the wire = %#x %#x, want %#x %#x", payload[11], payload[12], p[0], p[1])
	}
	if payload[13] != 0xFF {
		t.Fatalf("marker_bits = %#x, want 0xFF", payload[13])
	}
	if rbsp[2+14] != 0x80 {
		t.Fatalf("rbsp_trailing_bits = %#x, want 0x80", rbsp[2+14])
	}
}

// TestEscapeRBSPStopsAZeroZeroRunLookingLikeAStartCode pins emulation
// prevention. A payload byte sequence 00 00 01 inside an SEI would end the NAL
// early in every decoder, so the 03 must be inserted, and 00 00 04 is left
// alone because it cannot form a start code.
func TestEscapeRBSPStopsAZeroZeroRunLookingLikeAStartCode(t *testing.T) {
	if got := escapeRBSP([]byte{0, 0, 1}); !bytes.Equal(got, []byte{0, 0, 3, 1}) {
		t.Fatalf("escapeRBSP(00 00 01) = % x, want 00 00 03 01", got)
	}
	if got := escapeRBSP([]byte{0, 0, 4}); !bytes.Equal(got, []byte{0, 0, 4}) {
		t.Fatalf("escapeRBSP(00 00 04) = % x, want it unchanged", got)
	}
}

// TestSplitAnnexBDropsStartCodesAndKeepsEachUnitWhole checks both start-code
// widths. A four-byte start code leaves a zero on the end of the previous unit,
// which must be trimmed or the unit grows a byte that is not part of it.
func TestSplitAnnexBDropsStartCodesAndKeepsEachUnitWhole(t *testing.T) {
	es := []byte{0, 0, 0, 1, 0x67, 0xAA, 0, 0, 1, 0x68, 0xBB, 0, 0, 0, 1, 0x65, 0xCC}
	nals := splitAnnexB(es)
	want := [][]byte{{0x67, 0xAA}, {0x68, 0xBB}, {0x65, 0xCC}}
	if len(nals) != len(want) {
		t.Fatalf("got %d NALs, want %d", len(nals), len(want))
	}
	for i := range want {
		if !bytes.Equal(nals[i], want[i]) {
			t.Fatalf("NAL %d = % x, want % x", i, nals[i], want[i])
		}
	}
}

// TestInsertCCSEIPlacesTheSEIAfterParameterSetsAndBeforeTheFirstSlice is the
// placement rule. Access unit delimiter, SPS and PPS come first, then the SEI,
// then the picture. A non-first slice of the same picture must not get a second
// SEI, because one SEI per picture is what a decoder expects.
func TestInsertCCSEIPlacesTheSEIAfterParameterSetsAndBeforeTheFirstSlice(t *testing.T) {
	es := []byte{
		0, 0, 0, 1, 0x09, 0xF0, // access unit delimiter
		0, 0, 0, 1, 0x67, 0x42, // SPS
		0, 0, 0, 1, 0x68, 0xCE, // PPS
		0, 0, 0, 1, 0x65, 0x88, 0x84, // IDR slice, first_mb_in_slice == 0
		0, 0, 0, 1, 0x41, 0x40, 0x10, // a later slice of the same picture
	}
	out := insertCCSEI(es, pair(0x14, 0x20))
	var types []byte
	for _, n := range splitAnnexB(out) {
		types = append(types, nalType(n))
	}
	want := []byte{9, 7, 8, 6, 5, 1}
	if !bytes.Equal(types, want) {
		t.Fatalf("NAL types = %v, want %v", types, want)
	}
}

// TestInsertCCSEILeavesAnAccessUnitWithNoPictureAloneWhenNothingStartsOne
// guards against inserting into an access unit that has no picture start. A
// parameter-set-only unit gets no caption, because a decoder would attach the
// SEI to a picture that is not there.
func TestInsertCCSEILeavesAnAccessUnitWithNoPictureAloneWhenNothingStartsOne(t *testing.T) {
	es := []byte{0, 0, 0, 1, 0x67, 0x42, 0, 0, 0, 1, 0x68, 0xCE}
	if got := insertCCSEI(es, nullPair); !bytes.Equal(got, es) {
		t.Fatalf("insertCCSEI changed a unit with no picture: % x", got)
	}
}
