package oauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	path := filepath.Join("..", "..", "ui", "src", "pages", "Dashboard.tsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)
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
