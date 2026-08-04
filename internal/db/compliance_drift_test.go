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
