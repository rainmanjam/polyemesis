package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// The operator's own key, and the minted key Twitch answers a successful
// Enhanced Broadcasting negotiation with.
//
// The minted value is SYNTHETIC but its structure is the measured one, and the
// structure is the whole point of this test: v1_<64 hex signature>_<8 hex>_<hex
// manifest>_<the operator's original key>. The original is a SUFFIX of the
// minted key, which is what makes the naive protection fail quietly.
const (
	mintedOperatorKey = "live_2468013579_TheOperatorTypedThis"
	mintedSignature   = "v1_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"_a1b2c3d4_" + "7b2276223a312c2262223a343832307d_"
	mintedKeyWhole = mintedSignature + mintedOperatorKey
)

// TestTheMintedKeyIsMaskedWholeAndNotJustItsTail is the guard on a credential
// that only exists at go-live, and it is written against the specific way this
// protection fails: PARTIALLY.
//
// Twitch mints a 312-character stream key on a successful negotiation and it
// ends with the operator's own. polyemesis publishes with the minted value, so
// it reaches an FFmpeg command line and therefore reaches process.log, the
// monitoring page's argv, and every error the supervisor renders.
// supervisor.Process removes exactly the literals it was handed in Spec.Secrets
// and alerts.SecretSet.Scrub does that with strings.ReplaceAll.
//
// So registering the ORIGINAL key -- which destSecrets did, and which is what a
// reasonable person would assume covers "the stream key" -- masks the minted
// key's last segment and leaves
//
//	v1_<64 hex signature>_<8 hex>_<hex manifest>_<MASK>
//
// standing in the log. That is a partially redacted live credential, and it
// reads as protection to anyone glancing at the file, which is worse than no
// redaction at all. #310 and #324 were both this class.
//
// The assertion is therefore NOT "the log does not contain the original key" --
// that passes on the broken version. It is that the SIGNATURE PREFIX is gone
// too, which only happens if the minted value was registered in its own right.
//
// MUTATION: `out = append(out, extra...)` deleted from destSecrets, i.e. the
// minted key is never registered and only the original is. Observed: FAIL,
// "the minted key's signature survived scrubbing" with the masked-tail form
// above printed. Restored from /tmp backup; `git diff --stat` clean.
// MUTATION: destSecrets given the minted key but wireSpellings dropped.
// Observed: PASS -- so the test does NOT depend on wireSpellings, which is
// correct: that expands truncations and is a separate concern.
func TestTheMintedKeyIsMaskedWholeAndNotJustItsTail(t *testing.T) {
	row := &db.Destination{
		Kind:      db.DestRTMP,
		URL:       "rtmps://ingest.global-contribute.live-video.net/app",
		StreamKey: mintedOperatorKey,
	}

	// What the supervisor would be told to remove, WITH the minted key declared.
	set := alerts.NewSecretSet(nil, destSecrets(row, mintedKeyWhole)...)

	// A log line of the shape FFmpeg actually produces when a publish fails: the
	// whole publish URL, minted key and all.
	line := "rtmps://ingest.global-contribute.live-video.net/app/" + mintedKeyWhole +
		" Failed to open output"
	got := set.Scrub(line)

	if strings.Contains(got, mintedKeyWhole) {
		t.Fatalf("the whole minted key survived scrubbing:\n%s", got)
	}
	// THE LOAD-BEARING ASSERTION. The tail is masked even by the broken version,
	// because the original is a suffix of the minted key. Only registering the
	// minted value removes the signature.
	if strings.Contains(got, mintedSignature) {
		t.Errorf("the minted key's signature survived scrubbing, so the log carries a "+
			"partially redacted live credential:\n%s", got)
	}
	if strings.Contains(got, "0123456789abcdef") {
		t.Errorf("the signature hex is still in the log line:\n%s", got)
	}
	// And the ordinary key is still covered, or this traded one leak for another.
	if strings.Contains(got, mintedOperatorKey) {
		t.Errorf("the operator's own key survived scrubbing:\n%s", got)
	}
}

// TestRegisteringOnlyTheOriginalKeyLeavesTheSignatureStanding is the negative
// control, and it exists because the test above could pass for the wrong
// reason -- alerts.Redact runs a residual pattern pass, and if THAT were what
// removed the signature then destSecrets would be untested and the protection
// would be an accident.
//
// This asserts the gap is real: with only the original registered, the
// signature IS still there. If this test ever starts failing, the protection
// has moved somewhere else and the comment above needs rewriting rather than
// the test deleting.
//
// MUTATION: not applicable in the usual direction -- this test asserts the
// ABSENCE of protection, so it is mutated by ADDING the minted key to the set,
// which is the fix. Observed with `destSecrets(row, mintedKeyWhole)`: FAIL,
// "the signature was already gone without registering the minted key".
// Restored from /tmp backup; `git diff --stat` clean.
func TestRegisteringOnlyTheOriginalKeyLeavesTheSignatureStanding(t *testing.T) {
	row := &db.Destination{
		Kind:      db.DestRTMP,
		URL:       "rtmps://ingest.global-contribute.live-video.net/app",
		StreamKey: mintedOperatorKey,
	}
	// Deliberately NOT passing the minted key: this is the naive protection.
	set := alerts.NewSecretSet(nil, destSecrets(row)...)
	got := set.Scrub("publishing to /app/" + mintedKeyWhole)

	if !strings.Contains(got, mintedSignature) {
		t.Fatalf("the signature was already gone without registering the minted key, so "+
			"TestTheMintedKeyIsMaskedWholeAndNotJustItsTail is not testing what it says:\n%s", got)
	}
	// And the tail IS masked, which is precisely why the gap is easy to miss.
	if strings.Contains(got, mintedOperatorKey) {
		t.Errorf("expected the original key to be masked even here: %s", got)
	}
}
