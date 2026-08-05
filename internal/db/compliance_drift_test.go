package db

import (
	"strings"
	"testing"
)

// The head of the Facebook audience block, and of the sentence shown instead of
// it when the connected account publishes to a Page. Both are whole conditions
// rather than identifiers, for the reason facebook_ui_drift_test.go's header
// sets out: an identifier is still written in a block that no longer renders.
const (
	facebookAudienceHead = `{platform === "facebook" && !facebookTargetsAPage && (`
	facebookPageCaseHead = `{platform === "facebook" && facebookTargetsAPage && (`
)

// Every Facebook privacy value must be offered by the destination editor.
//
// This exists because the feature shipped unreachable once already: the whole
// create-time and push path was built while facebookPrivacy appeared only in
// types.ts, and TestUITypesCanNameEveryDestinationField was green throughout
// because naming a field is all it asks for. Declaring a type is what the code
// compiles against; a SelectItem is what a human can click.
//
// It matches `<SelectItem value="...">` rather than searching the file, because
// these values appear in types.ts and in comments for unrelated reasons, and a
// whole-file search would pass on an editor that still offered nothing.
//
// It also bounds itself to the audience block rather than to the file. A
// whole-file search passed on a dialog whose audience block was disabled --
// every SelectItem is still written inside a block that no longer renders, and
// it would also have passed on options that had drifted into some other
// platform's select entirely.
//
// MUTATION, run against a committed tree: `{platform === "facebook" &&
// !facebookTargetsAPage && false && (` -- the head stops matching and this
// fails. The previous whole-file version stayed green.
func TestEveryFacebookPrivacyIsOfferedByTheDestinationEditor(t *testing.T) {
	src := readUI(t, "components", "DestinationDialog.tsx")
	block := jsxBlockUnder(t, src, facebookAudienceHead, "DestinationDialog.tsx")

	for _, p := range FacebookPrivacies {
		option := `<SelectItem value="` + string(p) + `">`
		if !strings.Contains(block, option) {
			t.Errorf("no %s inside the Facebook audience block in DestinationDialog.tsx. "+
				"A privacy an operator cannot choose is a setting that can only ever be "+
				"empty, and every line of Facebook privacy handling behind it is "+
				"unreachable.", option)
		}
	}
	// The select has to write what it reads. An audience picker wired to some
	// other field is reachable, clickable and inert.
	if !strings.Contains(block, "facebookPrivacy:") {
		t.Error("the audience select inside the Facebook block does not set " +
			"facebookPrivacy, so choosing an audience changes nothing that is saved.")
	}
}

// The Facebook audience control must be hidden when the connected account
// publishes to a Page.
//
// A Page broadcast is public: Facebook has no personal audience to restrict it
// to, and IngestFor suppresses privacy for a Page target regardless of what is
// stored. Offering the control anyway is a setting that silently does nothing,
// which is the defect roadmap item 0 exists for -- and this repo has now shipped
// three features nobody could reach, so a control that CAN be reached and does
// nothing is the same mistake wearing the opposite coat.
//
// It matches the negated guard rather than the identifier alone, because
// `facebookTargetsAPage` also appears at its definition and in the branch that
// explains the Page case to the operator. Searching for the bare name would
// pass on a dialog that computed the answer and then ignored it.
//
// LIMITATION, and it is the same one action_drift_test.go documents: this reads
// source text, so it proves the gate is written, not that React renders it. A
// dialog that gated on `facebookTargetsAPage && false` would satisfy it -- the
// head would still match, because that mutation is inside the block rather than
// in front of the condition. The honest guard for that is a browser test
// against a Page-connected account, which needs an OAuth fixture this suite has
// no way to build; ui/e2e/ is where it would go.
//
// What it can now say, and could not before, is that BOTH halves of the
// decision are written: the control gated on a profile, and the sentence that
// replaces it for a Page. Matching the bare identifier `!facebookTargetsAPage`
// was satisfied by the gate alone, so deleting the explanation left an operator
// with a setting that had silently vanished and nothing saying why.
//
// MUTATION, run against a committed tree: delete the Page-case block from
// DestinationDialog.tsx, leaving the gate on the audience control -- fails on
// the second assertion. The previous version, which searched the file for
// `!facebookTargetsAPage`, stayed green.
func TestTheFacebookAudienceControlIsHiddenForAPageAccount(t *testing.T) {
	src := readUI(t, "components", "DestinationDialog.tsx")

	// jsxBlockUnder fatals if either head is missing or duplicated, which is the
	// whole assertion for the gate: a control that is no longer gated on the
	// account being a profile has no block under this head at all.
	jsxBlockUnder(t, src, facebookAudienceHead, "DestinationDialog.tsx")

	page := jsxBlockUnder(t, src, facebookPageCaseHead, "DestinationDialog.tsx")
	if !strings.Contains(page, "Page") {
		t.Error("the Page case renders nothing that mentions a Page. An operator " +
			"whose account publishes to a Page sees the audience control disappear " +
			"with no statement of why, which reads as the form losing a setting.")
	}
}

// The server drops compliance a destination's platform cannot send and returns
// a warning saying which. A drop nobody is shown is a setting that vanishes
// between one save and the next open, so the dialog has to render it.
//
// This guard watches the CONSUMPTION, not the field. api.ts declaring
// `warnings?: string[]` proves only that the type exists -- the same shape of
// mistake that let unsendable tags ship, where every end of the wire was named
// and nothing carried a value across it.
//
// This one has no enclosing JSX block to bound it to -- it is a statement in
// save(), not a render -- so it matches the WHOLE statement instead of the call
// alone. That is the same principle: `for (const w of warnings) false &&
// toast.warning(w, ...)` leaves "toast.warning(w" in the file untouched, and so
// does moving the call behind any other condition.
//
// MUTATION, run against a committed tree: `for (const w of warnings) false &&
// toast.warning(w, { duration: 10000 });` -- fails. It compiles, which matters:
// `warnings` is still read, so noUnusedLocals stays quiet and the mutation is
// one CI would otherwise accept. The previous version, matching "toast.warning(w",
// stayed green.
func TestTheDialogShowsWhatTheServerDropped(t *testing.T) {
	src := stripJSComments(readUI(t, "components", "DestinationDialog.tsx"))

	if !strings.Contains(src, "for (const w of warnings) toast.warning(w") {
		t.Error("the destination dialog does not render the server's warnings. " +
			"Settings dropped because the platform cannot send them would then " +
			"disappear with no explanation, which reads as the form losing them.")
	}
}
