package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// TestScrubDestinationTextCoversTheRetainedTopic is the #160 half that has no
// principal and no expiry.
//
// cmd/polyemesis/mqtt.go publishes DestState.Error to a RETAINED topic. Retained
// is the whole severity argument: the broker keeps the last message and hands it
// to every future subscriber, so a credential that reaches it cannot be undone
// by rotating anything -- the old value is already sitting on somebody else's
// broker, and it will be delivered again to the next client that connects.
//
// SourceState.IngestError on the same tree was already covered, by
// supervisor.(*Process).scrub at the point the status is built. DestState.Error
// came from the engine and had NOTHING. That asymmetry is the gap, and it is a
// gap rather than a leak-in-practice: the strings that reach this field are
// compile diagnostics. Zero coverage on a permanent sink is still worth one
// function.
func TestScrubDestinationTextCoversTheRetainedTopic(t *testing.T) {
	e, _ := storeEngine(t)

	// Named `sentinel`, not `key`: see the note in
	// internal/api/ws_policy_array_test.go. gitleaks matches on the identifier,
	// so `const key = "<high-entropy string>"` is a finding, and a finding in
	// the working tree disables the allowlist self-test entirely.
	const sentinel = "SENTINEL-dest-retained-3b9f11ac77"
	row := &db.Destination{
		ID: 42, Name: "twitch", Kind: db.DestRTMP,
		URL: "rtmp://live.example/app", StreamKey: sentinel,
	}
	e.mu.Lock()
	e.dests[row.ID] = &destination{row: row}
	e.mu.Unlock()

	// The shapes the exact pass exists for: the credential GLUED to whatever
	// surrounded it, which is what defeats a grammar and what a literal
	// replacement does not notice.
	for _, in := range []string{
		"cannot start: rtmp://live.example/app/" + sentinel,
		"-rtmp_conn S:" + sentinel,
		"-passphrase " + sentinel,
		"bare " + sentinel + " in the middle",
	} {
		got := e.ScrubDestinationText(row.ID, in)
		if strings.Contains(got, sentinel) {
			t.Errorf("ScrubDestinationText(%q) = %q, which still carries the stream key. "+
				"This string is published to a RETAINED MQTT topic and cannot be recalled.",
				in, got)
		}
		if !strings.Contains(got, alerts.Mask) {
			t.Errorf("ScrubDestinationText(%q) = %q -- the key is gone but nothing says a "+
				"redaction happened, which usually means the text was destroyed rather "+
				"than masked", in, got)
		}
	}

	// The diagnostic around the credential survives. Masking the message along
	// with the key would take away the only thing this topic exists for on a
	// headless box.
	if got := e.ScrubDestinationText(row.ID, "cannot start: rtmp://live.example/app/"+sentinel); !strings.Contains(got, "cannot start") {
		t.Errorf("the diagnostic was lost: %q", got)
	}

	// Empty stays empty. omitempty drops the field, and a bare "[redacted]"
	// against a destination with no error would read as a fault.
	if got := e.ScrubDestinationText(row.ID, ""); got != "" {
		t.Errorf("ScrubDestinationText(\"\") = %q, want \"\"", got)
	}

	// An id with no RUNNING destination gets the residual only, and must not
	// panic or mask the message.
	if got := e.ScrubDestinationText(9999, "rendition 3 is no longer available"); got != "rendition 3 is no longer available" {
		t.Errorf("a compile diagnostic for a destination that is not running came back "+
			"as %q; nothing in routing.Compile's output is a credential", got)
	}
}

// TestScrubDestinationTextCoversTheMintedKeyToo closes the divergence between
// this collection of a destination's secrets and the one at the supervisor
// Spec in destinations.go.
//
// That one passes destSecrets(row, mt.MintedKey). This one passed
// destSecrets(row) -- so the two answers to "which strings on this destination
// are secret" disagreed, and the shorter answer belonged to the function that
// hands text BACK to a caller, on its way to a retained MQTT topic that cannot
// be recalled.
//
// NOT REACHABLE TODAY. The only text currently reaching ScrubDestinationText is
// a routing.Compile diagnostic, which predates the negotiation and cannot carry
// a minted key. That is a fact about today's callers, not about this function,
// and it is exactly the argument that leaves a gap in place until something
// walks it. The two collections must agree.
//
// Mutation: revert ScrubDestinationText to `destSecrets(row)` (status.go).
// Observed to fail with "the minted key survived ScrubDestinationText". A
// second mutation, passing `d.row.StreamKey` instead of the minted key, fails
// the same way -- which is the point: the operator's key is a SUFFIX of the
// minted one, so registering it masks the tail and leaves the signature. Both
// restored with `command cp -f`; `diff` against the backup reported IDENTICAL.
func TestScrubDestinationTextCoversTheMintedKeyToo(t *testing.T) {
	e, _ := storeEngine(t)

	const sentinel = "SENTINEL-dest-minted-7c1e04bb92"
	row := &db.Destination{
		ID: 43, Name: "twitch", Kind: db.DestRTMP,
		URL: "rtmp://live.example/app", StreamKey: sentinel,
	}
	// The value engine/multitrack.go really stores: the minted key ENDING with
	// the operator's own, with clientConfigId appended by multitrack.Resolve.
	const signature = "v1_00112233445566778899aabbccddeeff_a1b2c3d4_7b2276223a317d_"
	minted := signature + sentinel
	e.mu.Lock()
	e.dests[row.ID] = &destination{
		row:        row,
		multitrack: mtDecision{Use: true, MintedKey: minted + "?clientConfigId=cfg-1"},
	}
	e.mu.Unlock()

	// NOT a URL, deliberately. alerts.Redact runs a residual pass that masks the
	// last path segment of anything it recognises as a publish URL, so a
	// "rtmp://h/app/<minted>" fixture passes on the UNFIXED code and this test
	// would prove nothing -- verified by mutating status.go back and watching it
	// pass. The negative control below pins that the residual pass is not what
	// covers this shape. `-rtmp_conn S:` is one of the forms
	// TestScrubDestinationTextCoversTheRetainedTopic already uses for the same
	// reason: it is what defeats a grammar.
	got := e.ScrubDestinationText(row.ID, "cannot start: -rtmp_conn S:"+minted+" refused")
	if strings.Contains(got, minted) {
		t.Errorf("the minted key survived ScrubDestinationText: %q. This string is published "+
			"to a RETAINED MQTT topic, so a credential that reaches it is delivered again "+
			"to every future subscriber and cannot be recalled by rotating anything.", got)
	}
	// THE LOAD-BEARING ASSERTION. The tail is masked by the row's own key
	// whatever this function does, because the operator's key is a suffix of the
	// minted one. Only registering the minted value removes the signature.
	if strings.Contains(got, signature) {
		t.Errorf("the minted key's signature survived, so the topic carries a partially "+
			"redacted live credential: %q", got)
	}
	if !strings.Contains(got, "cannot start") {
		t.Errorf("the diagnostic was lost: %q", got)
	}

	// A destination that never negotiated is unchanged -- the empty minted key
	// must not become a literal that matches everywhere.
	plain := &db.Destination{ID: 44, Name: "kick", Kind: db.DestRTMP, URL: "rtmp://k/app", StreamKey: "abc"}
	e.mu.Lock()
	e.dests[plain.ID] = &destination{row: plain}
	e.mu.Unlock()
	if got := e.ScrubDestinationText(plain.ID, "rendition 3 is no longer available"); got != "rendition 3 is no longer available" {
		t.Errorf("a destination that never negotiated had its diagnostic mangled: %q", got)
	}

	// THE NEGATIVE CONTROL. alerts.Redact runs a residual pass of its own, and
	// if THAT were what removed the signature above then this fix would be
	// untested and the protection an accident. It is not hypothetical: the
	// first draft of this test used a "rtmp://h/app/<minted>" fixture and
	// passed against the unfixed code, because Redact masks the last path
	// segment of anything it recognises as a publish URL. This asserts the
	// residual pass really does NOT cover the shape the test above uses.
	bare := &db.Destination{ID: 45, Name: "twitch", Kind: db.DestRTMP,
		URL: "rtmp://live.example/app", StreamKey: sentinel}
	e.mu.Lock()
	e.dests[bare.ID] = &destination{row: bare} // no negotiation recorded
	e.mu.Unlock()
	if got := e.ScrubDestinationText(bare.ID, "cannot start: -rtmp_conn S:"+minted+" refused"); !strings.Contains(got, signature) {
		t.Fatalf("the signature was already gone without the minted key being registered, so "+
			"TestScrubDestinationTextCoversTheMintedKeyToo is not testing what it says:\n%s", got)
	}
}
