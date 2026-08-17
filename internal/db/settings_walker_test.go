package db

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// THE WALKER GUARDS THE SETTINGS BLOBS AND NOTHING GUARDED THE WALKER.
//
// walk() decides which stored leaves the drift tests demand an answer for, so a
// walker that disagrees with encoding/json does not fail -- it checks the wrong
// paths and passes. Two such disagreements were found by hand review of the
// commit that introduced anonymous embedding here, and both were invisible to
// every existing test:
//
//   - `inlined` was computed BEFORE a deref loop that strips slices and maps as
//     well as pointers, so an embedded NAMED SLICE would have been walked as
//     inlined. encoding/json nests it under the type name instead.
//   - unexported embedded structs were skipped entirely, though encoding/json
//     promotes their exported fields onto the wire.
//
// Neither was reachable from the types in the tree that day, which is exactly
// why a test is worth more than a fix: this commit makes anonymous embedding
// the sharing pattern for the whole platform-capability expansion, so the next
// person to reach for it is the one who would have been bitten.
//
// The assertion is against encoding/json ITSELF rather than against a
// hand-written list of expected paths. A list would encode the same
// understanding that produced the bug; marshalling a real value and reading the
// keys back cannot.
func TestTheSettingsWalkerAgreesWithEncodingJSONAboutEmbedding(t *testing.T) {
	type inner struct {
		Deep string `json:"deep"`
	}
	// Unexported, embedded: encoding/json promotes Promoted onto the wire even
	// though the TYPE is unexported. Skipping it hides a stored leaf.
	type hidden struct {
		Promoted string `json:"promoted"`
	}
	// A NAMED SLICE OF STRUCTS, and the element type is what makes this the
	// regression case rather than decoration. Embedded anonymously,
	// encoding/json does NOT inline it -- there is no object to merge a list
	// into, so it nests under the Go type name. A slice of STRINGS would not
	// have caught the bug: the walker only consults `inlined` when the derefed
	// type is a struct, so []string never reaches the branch and the first
	// version of this test passed against the defect it was written for.
	type Items []inner

	type fixture struct {
		inner             // anonymous embedded struct: INLINED
		hidden            // anonymous embedded UNEXPORTED struct: inlined, promoted
		Items             // anonymous embedded NAMED SLICE OF STRUCTS: nested under "Items"
		Named      inner  `json:"named"`  // ordinary nested block
		Tagged     inner  `json:"tagged"` // embedded-looking but named
		Plain      string `json:"plain"`  // ordinary leaf
		Skipped    string `json:"-"`      // never on the wire
		unexported string // ordinary unexported: never on the wire
		NoTag      string // exported, untagged: serialises under its Go name
	}
	_ = fixture{}.unexported

	// What the walker thinks the leaf paths are.
	var walked []string
	walk(t, reflect.TypeOf(fixture{}), "", func(path, _ string) {
		walked = append(walked, path)
	})

	// What encoding/json actually produces. Values are set so no leaf is
	// omitted; none of these fields carries omitempty, but a zero value would
	// still make an accidental omitempty invisible.
	f := fixture{Plain: "p", NoTag: "n", Skipped: "s"}
	f.Deep = "d"
	f.Promoted = "x"
	f.Items = Items{{Deep: "id"}}
	f.Named.Deep = "nd"
	f.Tagged.Deep = "td"
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	actual := leafPaths(t, b)

	sort.Strings(walked)
	if got, want := strings.Join(walked, "\n"), strings.Join(actual, "\n"); got != want {
		t.Errorf("the walker and encoding/json disagree about which leaves exist.\n"+
			"A walker that is wrong here does not fail -- it demands the wrong paths be "+
			"classified and passes, leaving real stored leaves unchecked.\n\n"+
			"walker:\n  %s\n\nencoding/json:\n  %s\n\nbytes: %s",
			strings.ReplaceAll(got, "\n", "\n  "),
			strings.ReplaceAll(want, "\n", "\n  "), b)
	}

	// The two specific regressions, named, so a failure says which one.
	has := func(p string) bool {
		for _, w := range walked {
			if w == p {
				return true
			}
		}
		return false
	}
	if !has("promoted") {
		t.Error("an exported field promoted from an UNEXPORTED embedded struct was not walked; " +
			"it is on the wire and nothing would demand it be classified")
	}
	// The F1 regression, named. A buggy walker inlines the embedded slice and
	// emits its element leaf as a bare "deep" -- colliding with the genuinely
	// inlined struct above, so the collision is silent rather than loud.
	if !has("Items.deep") {
		t.Error("an anonymous embedded NAMED SLICE was inlined; encoding/json nests it " +
			"under the type name, so every path beneath it is off by one segment")
	}
	if has("deep") == false {
		t.Error("an anonymous embedded struct was not inlined; its leaves are keys of the " +
			"enclosing object and the type name appears on no wire")
	}
}

// leafPaths reports every scalar leaf in a JSON object as a dotted path, which
// is the shape walk() emits. Arrays are leaves: the walker does not descend
// into elements either, and a path that indexed one could not be compared.
func leafPaths(t *testing.T, b []byte) []string {
	t.Helper()
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var out []string
	var rec func(prefix string, m map[string]any)
	rec = func(prefix string, m map[string]any) {
		for k, v := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			switch tv := v.(type) {
			case map[string]any:
				rec(p, tv)
				continue
			case []any:
				// ARRAYS ARE TRANSPARENT HERE, because they are transparent to
				// the walker: it derefs a slice and keeps the same path, so a
				// leaf inside a list of structs is "items.deep" and never
				// "items[0].deep". Descending the first element models that.
				// An empty list contributes no leaves, same as the walker
				// contributes none for a slice of scalars.
				if len(tv) > 0 {
					if sub, ok := tv[0].(map[string]any); ok {
						rec(p, sub)
						continue
					}
				}
				out = append(out, p)
				continue
			}
			out = append(out, p)
		}
	}
	rec("", top)
	sort.Strings(out)
	return out
}
