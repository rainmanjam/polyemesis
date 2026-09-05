package oauth

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The setup guides are the third place a capability claim lives.
//
// capabilities_drift_test.go already pins the Go matrix against the TypeScript
// one, and it earned its keep: it caught kick/streamKey saying "yes" in Go and
// "manual" in the UI. But the SetupGuide prose is a separate surface, and it
// drifted the same way and stayed wrong for longer — the guide told operators
// "Kick is the one platform where the stream key stays manual", and listed a
// step telling them to paste it, months after polyemesis started fetching it
// over streamkey:read.
//
// That is the worst kind of documentation bug: not merely stale, but actively
// instructing somebody to do unnecessary work and to believe a limitation that
// does not exist. A matrix and a paragraph that disagree cannot both be right,
// and the paragraph is the one an operator actually reads.

// manualPhrases are the ways a guide can tell somebody to paste a key by hand.
//
// Every entry has to mean "you must do this yourself", not merely "the key is
// unusual". The first draft included "no permanent key", which the check below
// immediately caught: Facebook fetches its key perfectly well and the guide
// says so, it just issues a fresh one per broadcast. Describing a key's
// lifetime is not the same as asking somebody to type it.
var manualPhrases = []string{
	"stays manual",
	"cannot fetch",
	"paste the ingest",
	"paste both",
	"must be pasted",
}

func TestNoGuideClaimsAManualStreamKeyForAPlatformThatFetchesIt(t *testing.T) {
	caps := map[db.Platform]Support{}
	for _, p := range PlatformCapabilities() {
		caps[p.Platform] = p.Caps[CapStreamKey]
	}

	for _, g := range guides() {
		support, ok := caps[g.Platform]
		if !ok {
			t.Errorf("%s has a setup guide but no capability preset", g.Platform)
			continue
		}
		if support != SupportYes {
			continue // a guide MAY describe manual work when the matrix agrees
		}

		// The matrix says polyemesis fetches the key. Nothing in the guide may
		// tell the operator otherwise.
		hay := strings.ToLower(g.Note + " " + strings.Join(g.Steps, " "))
		for _, phrase := range manualPhrases {
			if strings.Contains(hay, phrase) {
				t.Errorf("%s: the capability matrix says polyemesis FETCHES the stream key "+
					"(CapStreamKey = %q), but the setup guide says %q.\n"+
					"One of the two is wrong, and the guide is the one operators read. "+
					"Fix whichever is stale rather than deleting this check.",
					g.Platform, support, phrase)
			}
		}
	}
}

// Facebook is the case that proves the check above is not vacuous: its matrix
// entry is SupportYes and its guide legitimately explains a caveat about keys.
// If the phrase list ever grows to swallow that, this fails and says so.
//
// IT IS NOW A TWO-KEY SPECIMEN, WHICH IS A STRONGER CALIBRATION THAN BEFORE.
// The guide used to say Facebook issues a fresh key per broadcast and "there is
// no permanent key to reuse". That second half was FALSE: Live Producer has a
// persistent stream key under Advanced settings, reusable every time you go
// live. polyemesis cannot read it -- Meta's Graph reference documents no way to
// -- so the corrected guide describes both: a key this product FETCHES, and a
// different key the operator PASTES.
//
// That is precisely the shape the phrase list must not swallow. "polyemesis
// cannot read it" is true of the persistent key and says nothing about the
// fetched one, so a maintainer tempted to add "cannot read" to manualPhrases
// would break a guide that is correct. The first draft of that list already
// included "no permanent key" and was caught the same way -- for the opposite
// reason, as it turns out, since the phrase was not merely over-broad but
// wrong.
func TestTheGuideDriftCheckStillAllowsLegitimateCaveats(t *testing.T) {
	var fb SetupGuide
	for _, g := range guides() {
		if g.Platform == db.PlatformFacebook {
			fb = g
		}
	}
	// BOTH OF THESE WERE t.Skip. This test exists to prove the manual-key
	// phrase list is not so broad that it swallows a legitimate caveat, and
	// Facebook's per-broadcast note is the only specimen. "The guide is gone"
	// and "the wording moved" are exactly the two events that invalidate the
	// calibration -- so stepping aside for them left the phrase list
	// uncalibrated while printing ok.
	//
	// The note is pinned in testdata/guide-notes.json now: a reword fails
	// TestProviderGolden with a readable diff, and this asserts the specimen
	// still exists.
	if fb.Platform == "" {
		t.Fatal("there is no Facebook setup guide, so the phrase list below has no " +
			"legitimate caveat to be calibrated against and the drift check above is " +
			"unverified. Pick another specimen or delete both.")
	}
	if !strings.Contains(strings.ToLower(fb.Note), "the key it fetches belongs to one live video") {
		t.Fatalf("Facebook's per-broadcast caveat has been reworded, which is the moment "+
			"the manual-key phrase list needs re-checking rather than the moment to stop "+
			"looking. Re-read the phrases against the new wording, update this probe, "+
			"and regenerate testdata/guide-notes.json.\nnote: %q", fb.Note)
	}
	hay := strings.ToLower(fb.Note + " " + strings.Join(fb.Steps, " "))
	for _, phrase := range manualPhrases {
		if strings.Contains(hay, phrase) {
			t.Fatalf("the manual-key phrase %q now matches Facebook's legitimate "+
				"per-broadcast caveat, so the drift check above would fail for a "+
				"guide that is correct. Narrow the phrase.", phrase)
		}
	}
}
