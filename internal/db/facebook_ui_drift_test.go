package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Facebook block must have a control, not merely a name.
//
// This is the same failure the ledger already records twice: crosspost and
// donateCharityId appeared in ui/src/lib/types.ts and nowhere else, so
// TestUITypesCanNameEveryDestinationField was green the entire time nothing
// in the dialog could set either field. Declaring a type is what the code
// compiles against; an input the operator can type into is what makes a
// feature exist. Matches specific label/id markers rather than searching the
// whole file, because a whole-file search would pass on a dialog that only
// mentions the words in a comment.
func TestFacebookCrosspostAndDonateAreOfferedByTheDestinationEditor(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationDialog.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)

	if !strings.Contains(src, "Crosspost to Pages") {
		t.Error("no crosspost control in DestinationDialog.tsx. A crosspost list an " +
			"operator cannot build is a field that can only ever be empty, and every " +
			"line of Facebook crossposting behind it is unreachable.")
	}
	if !strings.Contains(src, `id="dest-fb-donate"`) {
		t.Error("no donate-charity-id input in DestinationDialog.tsx. A charity id an " +
			"operator cannot type is a field that can only ever be empty, and " +
			"donate_button_charity_id never leaves polyemesis.")
	}
}

// The Facebook block's control values must actually leave the dialog.
//
// A control that renders but is missing from save()'s payload literal is the
// same unreachable feature with a nicer disguise -- exactly what Task 4b
// found once already at the API layer (handlePushMetadata silently dropping
// tags) and what this branch's own C1 review finding was: the Audience
// select existed, the payload just never carried `facebook`. Matches the
// payload literal specifically, not the file, because "facebook" appears
// elsewhere (state, comments, the platform preset) for unrelated reasons.
func TestDestinationDialogSavePayloadCarriesTheFacebookBlock(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationDialog.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)

	marker := "const payload: Partial<Destination> = {"
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatal("cannot find the save payload literal in DestinationDialog.tsx; this " +
			"guard is no longer looking where the payload lives, so it asserts nothing")
	}
	end := strings.Index(src[start:], "};")
	if end < 0 {
		t.Fatal("save payload literal has no closing brace within the file; cannot bound the window")
	}
	window := src[start : start+end]
	if !strings.Contains(window, "facebook") {
		t.Error("the save payload does not carry facebook, so dest.Facebook is " +
			"permanently the zero value no matter what the crosspost and donate " +
			"controls are set to. Add it to the payload literal.")
	}
}

// A public page is created on the operator's behalf; giving them no way to
// reach it is half a feature. It also makes a dead broadcast legible -- when
// the link 404s they can see what the situation is for themselves.
//
// This watches the RENDERED link, not the type. types.ts declaring
// `broadcastId?: string` proves only that the type exists, which is the shape
// of mistake that shipped unsendable tags: every end of the wire named, and
// nothing carrying a value across it.
func TestTheCardLinksToTheScheduledBroadcast(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationCard.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "facebook.com/${dest.facebookBroadcastId}") {
		t.Error("the destination card does not link to the scheduled Facebook " +
			"broadcast, so an operator has no way to reach the event page " +
			"polyemesis created for them.")
	}
}

// A backup nobody can see is worse than no backup: the operator believes they
// have redundancy. Watches the RENDERED state, not the type -- types.ts
// declaring backupProcess proves only that the type exists.
func TestTheCardShowsTheBackupFeedsState(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationCard.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "dest.backupProcess") {
		t.Error("the card does not render the backup feed's state, so a backup " +
			"that has been dead for an hour looks identical to a healthy one.")
	}
	if !strings.Contains(string(raw), "dest.backupError") {
		t.Error("the card does not render why a requested backup is missing, so " +
			"an operator who enabled redundancy and did not get it is never told.")
	}
}

// The toggle has to be reachable. A setting the server honours and the dialog
// never offers is the unreachable-feature shape this repo keeps finding.
func TestTheDialogOffersTheBackupIngestToggle(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "components", "DestinationDialog.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)
	if !strings.Contains(src, "backupIngest: e.target.checked") {
		t.Error("the destination dialog has no control that SETS backupIngest, so " +
			"the backup feed can never be turned on from the UI.")
	}
	// The two costs are not guessable and an operator who learns about them
	// afterwards has already paid one of them.
	if !strings.Contains(src, "upload bandwidth") || !strings.Contains(src, "reconnects the stream") {
		t.Error("the toggle does not state its costs: it doubles upload bandwidth " +
			"and reconnects the stream once when enabled.")
	}
}
