package oauth

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// dashboardSource is Dashboard.tsx with its comments blanked, #379.
//
// Both guards below are substring searches over a window of this file, and a
// substring search is satisfied by a comment. Neither of them stripped, so
// either could have been kept green by deleting the code and leaving the words
// -- and for the compliance guard, whose window is the whole file, a comment
// anywhere at all would have done it. Blanking happens here, once, so a third
// guard added to this file cannot forget.
func dashboardSource(t *testing.T) string {
	t.Helper()
	return testenv.StripJSComments(testenv.ReadUI(t, "pages", "Dashboard.tsx"))
}

// The composer must be able to SEND tags, not merely render them back.
//
// TestUITypesCanNameEveryMetadataField walks the field NAMES a push result can
// carry, and MetaField already listed "tags" before anything could set one --
// so that guard was green while the feature was unreachable. Naming a field in
// a result and offering an operator a way to fill it are different claims, and
// only the second one is what makes a feature exist.
//
// Matches the push body specifically, not the file: "tags" appears in
// Dashboard.tsx for unrelated reasons, so a whole-file search would pass on a
// composer that still cannot send them.
func TestTheComposerCanSendFacebookTags(t *testing.T) {
	src := dashboardSource(t)
	body := strings.Index(src, `metaFetch<MetaJob>("/metadata/push"`)
	if body < 0 {
		t.Fatal("cannot find the metadata push call in Dashboard.tsx; this guard " +
			"is no longer looking where the push body lives, so it asserts nothing")
	}
	window := src[body:min(body+400, len(src))]
	if !strings.Contains(window, "tags") {
		t.Error("the composer's push body carries no tags field, so Metadata.Tags " +
			"is always empty in production and every line of tag resolution is " +
			"unreachable. Add it to the body and give the operator an input.")
	}
}

// The composer must tell the operator that a push sends stored compliance.
//
// Compliance is configured per DESTINATION and has no field in the composer, so
// without this an operator presses Push and a COPPA declaration, a privacy
// setting or a set of content labels goes out with nothing on screen having
// mentioned it. A push that does MORE than it says is the same complaint as one
// that does less, and this half is the harder one to notice.
//
// It also guards the button: `empty` disables Push, and a version computing it
// from the composer alone would leave the server's own allowance for a
// compliance-only push unreachable — the exact both-ends-of-one-wire mistake
// that shipped tags nobody could send.
//
// Matches the derived list's use rather than its definition, because the name
// appears at both and only the uses do anything.
func TestTheComposerSaysWhenAPushCarriesStoredCompliance(t *testing.T) {
	src := dashboardSource(t)
	if !strings.Contains(src, "withCompliance.length > 0") {
		t.Error("the composer never mentions stored compliance, so a push sends a COPPA " +
			"declaration or a privacy setting with nothing on screen having said so")
	}
	if !strings.Contains(src, "withCompliance.length === 0") {
		t.Error("the Push button's disabled state ignores stored compliance, so a " +
			"compliance-only push cannot be started even though the server allows it")
	}
}
