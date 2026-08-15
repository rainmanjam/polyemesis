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

	// mintedConfigID and mintedKeyAsRegistered are what the ENGINE actually
	// hands destSecrets, and the difference between this and mintedKeyWhole was
	// a live gap.
	//
	// multitrack.Resolve returns Target.Key already composed by withConfigID --
	// the minted key with "?clientConfigId=<uuid>" appended -- and
	// engine/multitrack.go sets `d.MintedKey = out.Target.Key` from it. There is
	// no field anywhere carrying the bare minted key, so the literal that
	// reaches the SecretSet is strictly LONGER than the credential.
	//
	// The tests below used to register mintedKeyWhole, which no code path ever
	// produces. They passed, and the shipped code leaked: SecretSet.Scrub is a
	// substring replace, so a 44-byte-longer needle finds nothing in text
	// carrying the key without its query. THE GUARD AND THE SHIPPED CODE HAD
	// DIVERGED, and the guard is the half that was wrong.
	mintedConfigID        = "49456f79-a985-4011-941f-3cde9897a0c6"
	mintedKeyAsRegistered = mintedKeyWhole + "?clientConfigId=" + mintedConfigID
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
// IT NOW REGISTERS WHAT THE ENGINE REGISTERS. It used to pass mintedKeyWhole,
// a value no code path produces, while engine/multitrack.go registered the
// clientConfigId-suffixed form -- so the guard tested a stricter world than the
// one that shipped and could not see the leak. See mintedKeyAsRegistered.
//
// MUTATION: `out = append(out, extra...)` deleted from destSecrets, i.e. the
// minted key is never registered and only the original is. Observed: FAIL,
// "the minted key's signature survived scrubbing" with the masked-tail form
// above printed. Restored with `command cp -f` from a file backup; `diff`
// against the backup reported IDENTICAL.
//
// MUTATION: the '?' prefix expansion deleted from wireSpellings (secrets.go).
// Observed: FAIL on the same assertion. That is a CHANGE from what this comment
// said before -- it used to record that dropping wireSpellings left the test
// passing, which was true only because the test registered the bare key. Now
// that it registers the composed one, the '?' expansion is exactly what closes
// the gap, and the mutation proves it.
func TestTheMintedKeyIsMaskedWholeAndNotJustItsTail(t *testing.T) {
	row := &db.Destination{
		Kind:      db.DestRTMP,
		URL:       "rtmps://ingest.global-contribute.live-video.net/app",
		StreamKey: mintedOperatorKey,
	}

	// What the supervisor would be told to remove -- the value engine/
	// multitrack.go really puts in Destination.MintedKey, query and all.
	set := alerts.NewSecretSet(nil, destSecrets(row, mintedKeyAsRegistered)...)

	// A log line of the shape FFmpeg actually produces when a publish fails, and
	// carrying the key WITHOUT its query -- which is the case the registered
	// literal cannot match on its own. FFmpeg's own error text is one route to
	// it; so is anything that splits a URL at its query before printing.
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

	// And the composed form is still covered, so this did not trade the bare
	// spelling for the one that was already working.
	withQuery := set.Scrub("opening rtmps://h/app/" + mintedKeyAsRegistered)
	if strings.Contains(withQuery, mintedSignature) {
		t.Errorf("the minted key with its clientConfigId survived scrubbing:\n%s", withQuery)
	}
}

// TestTheMintedKeyIsMaskedWithoutItsClientConfigIDQuery isolates SEC-4 from the
// half of the protection that already worked.
//
// The test above would pass on a build that registered the bare minted key and
// nothing else -- that is the world it was written for. This one asserts the
// exact gap: the ONLY literal registered is the composed value the engine
// really produces, and the text carries the key without its query. Nothing but
// the '?' expansion in wireSpellings can mask it.
//
// MUTATION: delete the `strings.IndexByte(v, '?')` block from wireSpellings
// (engine/secrets.go). Observed: FAIL, "the minted key survived scrubbing
// because the registered literal carried a ?clientConfigId= the log line did
// not". Restored with `command cp -f`; `diff` against the backup reported
// IDENTICAL.
//
// MUTATION: change the guard from `i > 0` to `i >= 0`. Observed: PASS here, and
// FAIL in TestWireSpellingsNeverEmitsAnEmptyLiteral, which is the test written
// for that boundary.
func TestTheMintedKeyIsMaskedWithoutItsClientConfigIDQuery(t *testing.T) {
	// No row key, no URL: the composed minted value is the only secret in play,
	// so nothing else can account for a pass.
	set := alerts.NewSecretSet(nil, wireSpellings([]string{mintedKeyAsRegistered})...)

	got := set.Scrub("Failed to open output rtmps://h/app/" + mintedKeyWhole)
	if strings.Contains(got, mintedKeyWhole) {
		t.Errorf("the minted key survived scrubbing because the registered literal carried a "+
			"?clientConfigId= the log line did not -- Scrub is a substring replace, so the "+
			"longer needle cannot be found in the shorter haystack:\n%s", got)
	}
	if strings.Contains(got, mintedSignature) {
		t.Errorf("the minted key's signature survived scrubbing:\n%s", got)
	}
}

// TestWireSpellingsNeverEmitsAnEmptyLiteral guards the boundary both prefix
// expansions share.
//
// An empty string in an alerts.SecretSet matches at every position of every
// line, so a `i >= 0` where the code says `i > 0` would not fail loudly -- it
// would quietly turn every log line into mask characters, or be dropped
// downstream and look like nothing happened. Either way the value that got the
// guard wrong is the one nobody would look at.
//
// MUTATION: change either prefix guard in wireSpellings from `i > 0` to
// `i >= 0`. Observed: FAIL, "wireSpellings emitted an empty literal for ...".
// Restored with `command cp -f`; `diff` against the backup reported IDENTICAL.
func TestWireSpellingsNeverEmitsAnEmptyLiteral(t *testing.T) {
	for _, in := range []string{
		"?clientConfigId=abc", // a value that IS a query
		"\x1b[27;2;13",        // a value that starts with a control character
		"?",
		"",
		"   ",
	} {
		for _, got := range wireSpellings([]string{in}) {
			if got == "" && in != "" {
				t.Errorf("wireSpellings emitted an empty literal for %q: an empty secret matches "+
					"at every position of every line", in)
			}
		}
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
