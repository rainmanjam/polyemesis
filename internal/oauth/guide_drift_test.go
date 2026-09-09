package oauth

import (
	"os"
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

// Ways a guide can tell somebody publishing is optional. Each has to mean "you
// can leave it in Testing", not merely mention publishing.
//
// ONE SLICE, BOTH SURFACES. #734 put this claim in two places -- the Go guide
// in oauth.go and the YouTube section of docs/PLATFORMS.md -- and the test
// written to catch it walked only the Go structs. The comment above that test
// SAYS the claim lived in both places, so the guard's own header records a
// surface the guard does not cover: restoring the sentence in the document
// alone reproduces #734 in full with this file green. Declared at package level
// so the doc check below cannot drift from the guide check above.
var publishingIsOptionalClaims = []string{
	"do not need to publish",
	"don't need to publish",
	"no need to publish",
	"without publishing",
	"publishing is optional",
}

// A GOOGLE GUIDE MAY NOT SAY PUBLISHING IS OPTIONAL. #734.
//
// The YouTube guide told operators "You do not need to publish the app" for
// months, in both this file and docs/PLATFORMS.md. It is false in the way that
// matters: Google issues a refresh token expiring in SEVEN DAYS to any External
// app whose publishing status is Testing, unless the only scopes it requests
// are a subset of name, email address and user profile. polyemesis requests
// https://www.googleapis.com/auth/youtube, which is not in that set.
//
// So every install that followed the guide lost its YouTube connection weekly,
// the guide did not say so, and reconnecting restarted the same clock. The
// operator's evidence is an integration that breaks every Monday for no visible
// reason -- the worst shape a documentation bug can take, because the thing it
// misleads you about is invisible until it fires and looks like a different
// fault when it does.
//
// WHY A TEST AND NOT A CORRECTED SENTENCE. The sentence was corrected; this is
// what stops the next person restoring it. It is easy to restore in good faith,
// because for a Google app requesting only profile scopes it would be TRUE, and
// that is the version of this advice everywhere on the internet. The check is
// therefore on the pairing -- the claim AND the scopes -- rather than on the
// words alone.
//
// Rung 2: it announces in CI rather than making the sentence unwritable.
// Control would mean the guide deriving this line from the scope list instead
// of stating it in prose, which is more machinery than one sentence deserves.
func TestNoGoogleGuideSaysPublishingIsUnnecessaryWhileAskingForASensitiveScope(t *testing.T) {
	// Google's own exemption, quoted from the OAuth documentation: a Testing
	// app's refresh token does NOT expire when the requested scopes are a
	// subset of these three.
	exempt := map[string]bool{
		"https://www.googleapis.com/auth/userinfo.email":   true,
		"https://www.googleapis.com/auth/userinfo.profile": true,
		"openid":  true,
		"email":   true,
		"profile": true,
	}

	checked := 0
	for _, g := range guides() {
		google := false
		sensitive := false
		for _, sc := range g.Scopes {
			if strings.Contains(sc, "googleapis.com") {
				google = true
			}
			if !exempt[sc] {
				sensitive = true
			}
		}
		if !google {
			continue
		}
		checked++
		if !sensitive {
			// A Google guide asking only for exempt scopes MAY say publishing
			// is unnecessary, because there it is true.
			continue
		}
		hay := strings.ToLower(g.Note + " " + strings.Join(g.Steps, " "))
		for _, claim := range publishingIsOptionalClaims {
			if strings.Contains(hay, claim) {
				t.Errorf("the %s guide says %q while requesting %v.\n"+
					"An External Google app left in Testing issues refresh tokens that expire "+
					"after 7 days unless its scopes are a subset of name, email and profile. "+
					"This app is not exempt, so the connection breaks weekly and the guide "+
					"does not warn anybody. Either say the publishing status must be In "+
					"production, or explain the seven-day expiry where the operator will read "+
					"it before choosing.", g.Platform, claim, g.Scopes)
			}
		}
	}

	// POSITIVE CONTROL. Everything above passes if guides() returns nothing
	// Google-shaped -- a renamed scope constant, a reordered slice, a guide
	// moved elsewhere -- and a green run over zero guides asserts nothing.
	if checked == 0 {
		t.Fatal("no Google guide was examined; guides() returned nothing with a " +
			"googleapis.com scope. The walk is broken, so this test is asserting " +
			"nothing about the claim it exists to catch.")
	}
}

// The correction has to actually be present, not merely the false claim absent.
// Deleting the sentence would satisfy the test above and leave an operator with
// no way to know -- which is the state #734 was filed about.
//
// IT CHECKS THE WHOLE GUIDE, NOT THE Note FIELD, and that is deliberate rather
// than lax. Deleting the Note alone leaves the warning in the step beside it and
// this test passes -- an EQUIVALENT MUTANT, recorded as one: the guide still
// warns, less prominently, and no assertion about prominence would survive
// somebody legitimately restructuring the guide. Removing it from both places
// fails, which was measured, and is the outcome that matters.
func TestTheYouTubeGuideWarnsAboutTheSevenDayExpiry(t *testing.T) {
	for _, g := range guides() {
		if g.Platform != db.PlatformYouTube {
			continue
		}
		hay := strings.ToLower(g.Note + " " + strings.Join(g.Steps, " "))
		for _, want := range []string{"7 days", "testing", "in production"} {
			if !strings.Contains(hay, want) {
				t.Errorf("the YouTube guide does not mention %q.\n"+
					"An operator choosing a publishing status has to be told that Testing "+
					"expires the connection weekly and that In production is what stops it. "+
					"Removing the warning is the defect #734 recorded, not a tidy-up.", want)
			}
		}
		return
	}
	t.Fatal("no YouTube guide found; the walk is broken")
}

// THE SAME CLAIM, ON THE OTHER SURFACE. #734.
//
// The guide checks above walk Go structs. The false sentence also lived in
// docs/PLATFORMS.md -- the header on TestNoGoogleGuideSaysPublishingIsUnnecessary
// WhileAskingForASensitiveScope says so in as many words -- and nothing has ever
// read that file for it. platforms_doc_drift_test.go reads the same document but
// only to compare the capability matrix; it never looks at the prose.
//
// So the state before this test: correct the Go guide, restore the sentence in
// the document, and an operator following the documentation loses their YouTube
// connection every seven days with the whole suite green. The document is the
// surface most operators actually read.
func TestThePlatformsDocDoesNotSayPublishingIsOptionalForYouTube(t *testing.T) {
	section := youTubeSectionOfPlatformsDoc(t)
	hay := strings.ToLower(section)

	for _, claim := range publishingIsOptionalClaims {
		if strings.Contains(hay, claim) {
			t.Errorf("docs/PLATFORMS.md's YouTube section says %q.\n"+
				"polyemesis requests https://www.googleapis.com/auth/youtube, which is "+
				"not one of the three scopes Google exempts, so an External app left in "+
				"Testing issues refresh tokens that expire after 7 days. The connection "+
				"breaks weekly and the document does not warn anybody.", claim)
		}
	}

	// The correction has to be PRESENT, not merely the false claim absent --
	// deleting the paragraph satisfies the loop above and leaves the operator
	// with no way to know, which is the state #734 was filed about.
	if !strings.Contains(hay, "seven days") {
		t.Error("docs/PLATFORMS.md's YouTube section no longer mentions the seven-day " +
			"expiry. Removing the warning is the same defect as denying it: the " +
			"operator chooses a publishing status without the one fact that matters.")
	}
}

// youTubeSectionOfPlatformsDoc returns every heading about YouTube and the prose
// under it. Both `### YouTube (Google)` and the `### What YouTube chat costs`
// that follows it are in scope; the region ends at the first heading that is
// about something else.
func youTubeSectionOfPlatformsDoc(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../docs/PLATFORMS.md")
	if err != nil {
		t.Fatalf("read PLATFORMS.md: %v", err)
	}
	var out []string
	inSection := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#") {
			// A heading naming YouTube opens or continues the region; any other
			// heading closes it.
			inSection = strings.Contains(strings.ToLower(line), "youtube")
		}
		if inSection {
			out = append(out, line)
		}
	}

	// POSITIVE CONTROL. A renamed heading, a restructured document, or a wrong
	// relative path all yield an empty region -- over which every assertion
	// above passes while reading nothing at all. That is the exact failure this
	// file keeps finding elsewhere.
	if len(out) < 10 {
		t.Fatalf("found %d lines of YouTube section in docs/PLATFORMS.md; the "+
			"heading has moved or been renamed, so this test is asserting nothing",
			len(out))
	}
	return strings.Join(out, "\n")
}
