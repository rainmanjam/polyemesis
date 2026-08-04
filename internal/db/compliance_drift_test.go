package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
func TestEveryFacebookPrivacyIsOfferedByTheDestinationEditor(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationDialog.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)
	for _, p := range FacebookPrivacies {
		option := `<SelectItem value="` + string(p) + `">`
		if !strings.Contains(src, option) {
			t.Errorf("no %s in DestinationDialog.tsx. A privacy an operator cannot "+
				"choose is a setting that can only ever be empty, and every line of "+
				"Facebook privacy handling behind it is unreachable.", option)
		}
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
// dialog that gated on `facebookTargetsAPage && false` would satisfy it. The
// honest guard for that is a browser test against a Page-connected account,
// which needs an OAuth fixture this suite has no way to build.
func TestTheFacebookAudienceControlIsHiddenForAPageAccount(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationDialog.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "!facebookTargetsAPage") {
		t.Error("the Facebook audience control is not gated on the account being a profile. " +
			"A Page broadcast is public and the server drops the value, so an operator " +
			"who sets it here is choosing something that cannot happen.")
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
func TestTheDialogShowsWhatTheServerDropped(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationDialog.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "toast.warning(w") {
		t.Error("the destination dialog does not render the server's warnings. " +
			"Settings dropped because the platform cannot send them would then " +
			"disappear with no explanation, which reads as the form losing them.")
	}
}
