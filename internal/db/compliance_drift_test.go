package db

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

/* ===========================================================================
   What is left in Go, and why, after issue #107.

   This file used to hold three guards that read ui/src/**.tsx and asserted on
   the text: that a <SelectItem> was written inside the Facebook audience block,
   that the block was gated on the account not being a Page, and that save()
   looped the server's warnings into toast.warning. All three were claims about
   what React RENDERS, and source text cannot answer that -- as those guards'
   own comments admitted, one of them in so many words ("this reads source text,
   so it proves the gate is written, not that React renders it").

   They now live in ui/e2e/facebook-destination.spec.ts, where a browser opens
   the real dialog and drives the real controls.

   ONE claim stayed, because it is the one no browser and no compiler can make:
   the set of audiences the UI offers must be the set of audiences the SERVER
   accepts. Go owns db.FacebookPrivacies; TypeScript cannot see it, and a
   browser can only observe what the UI happens to render. So the mirror is
   pinned here -- against a data declaration, ui/src/lib/facebookPrivacy.ts,
   rather than against JSX. The dialog maps over that array, so the pin is
   load-bearing rather than decorative: an entry deleted there is an option
   deleted from the select.
   =========================================================================== */

// The audiences the UI offers must be exactly the audiences the server accepts.
//
// Both directions, because they fail differently and both have shipped here:
//
//   - A Go value the UI does not offer is an audience no operator can choose,
//     with every line of Facebook privacy handling behind it unreachable. That
//     is the third-time-lucky defect this repository keeps rediscovering:
//     complete in every layer except the one a person touches.
//   - A UI value Go does not accept is worse in a quieter way. ValidCompliance
//     rejects it, so the operator picks an audience, presses Save, and gets a
//     400 with no field to attach it to.
//
// It reads ui/src/lib/facebookPrivacy.ts, which is DATA -- an exported array of
// {value, labelKey} -- and not a component. That distinction is the whole point
// of issue #107. Whether the select renders, whether an operator can click an
// option, and whether the chosen value survives a save are questions about a
// running browser, and ui/e2e/facebook-destination.spec.ts asks them there.
// What is asked here is a question about two enums in two languages, which is
// exactly what a cross-language pin is for.
//
// MUTATION: delete the FRIENDS_OF_FRIENDS entry from facebookPrivacy.ts -- this
// fails naming it, and the option disappears from the dialog, because the
// dialog maps over the same array.
func TestTheUIOffersExactlyTheAudiencesTheServerAccepts(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "lib", "facebookPrivacy.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		// Not skipped: this file existing is the point. Losing it takes the
		// select's options with it, since the dialog renders from it.
		t.Fatalf("cannot read %s: %v", path, err)
	}

	// The array literal only. A `value:` in the doc comment above it, or in some
	// unrelated export added later, is not an option the select renders.
	body, ok := tsExportedArray(string(raw), "FACEBOOK_PRIVACIES")
	if !ok {
		t.Fatalf("no `export const FACEBOOK_PRIVACIES = [` in %s. Either it was "+
			"renamed -- in which case this guard is watching nothing and the rename "+
			"has to reach here too -- or the dialog is back to listing its options by "+
			"hand, where nothing can pin them to the Go enum.", path)
	}

	offered := map[string]bool{}
	for _, m := range regexp.MustCompile(`value:\s*"([^"]*)"`).FindAllStringSubmatch(body, -1) {
		offered[m[1]] = true
	}
	if len(offered) == 0 {
		t.Fatalf("FACEBOOK_PRIVACIES in %s has no `value:` entries at all; the "+
			"audience select renders from it and would be empty", path)
	}

	accepted := map[string]bool{}
	for _, p := range FacebookPrivacies {
		// FBPrivacyUnchanged is the absence of an audience rather than one of
		// them; the select renders it as its own "leave it as it is" row and it
		// is deliberately not in the mirrored array.
		if p == FBPrivacyUnchanged {
			continue
		}
		accepted[string(p)] = true
	}

	for _, v := range sortedKeys(accepted) {
		if !offered[v] {
			t.Errorf("db.FacebookPrivacies accepts %q and ui/src/lib/facebookPrivacy.ts "+
				"does not offer it. An audience an operator cannot choose is a setting "+
				"that can only ever be empty, and every line of Facebook privacy "+
				"handling behind it is unreachable.", v)
		}
	}
	for _, v := range sortedKeys(offered) {
		if !accepted[v] {
			t.Errorf("ui/src/lib/facebookPrivacy.ts offers %q and db.FacebookPrivacies "+
				"does not accept it. ValidFacebookPrivacy refuses the save, so an "+
				"operator picks that audience and meets a 400 with no field to blame.", v)
		}
	}
}

// tsExportedArray returns the text between the brackets of
// `export const <name> = [ ... ]`.
//
// It bounds itself to the literal rather than searching the file because the
// doc comment above this particular array quotes the very strings it declares,
// and a whole-file search would be satisfied by prose.
func tsExportedArray(src, name string) (string, bool) {
	head := "export const " + name + " = ["
	start := strings.Index(src, head)
	if start < 0 {
		return "", false
	}
	open := start + len(head) - 1
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return src[open+1 : i], true
			}
		}
	}
	return "", false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
