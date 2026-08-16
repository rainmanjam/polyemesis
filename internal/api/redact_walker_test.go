package api

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// THE SAME WALKER SELF-TEST AS internal/db's, AND IT MATTERS MORE HERE.
//
// leafWalk decides which paths the redaction guard checks, and the guard does
// not merely read those paths -- setLeaf WRITES a probe value to each one and
// then asserts the probe does not appear in a response body. A walker that
// emits a path encoding/json does not produce plants the probe on the wrong
// leaf, or on no leaf at all, and the guard then proves that a value nothing
// ever wrote is absent from the output. It passes, loudly and uselessly, while
// the leaf it was supposed to watch goes unredacted.
//
// Two disagreements with encoding/json were found by review of the commit that
// made anonymous embedding the sharing pattern here:
//
//   - `inlined` was computed before a deref loop that strips slices and maps as
//     well as pointers, so an embedded NAMED SLICE OF STRUCTS was walked as
//     inlined and every path beneath it lost a segment.
//   - unexported embedded structs were skipped, though encoding/json promotes
//     their exported fields onto the wire -- stored, readable, unchecked.
//
// Asserted against encoding/json itself rather than a hand-written path list. A
// list would encode the same understanding that produced the defect.
func TestTheRedactionWalkerAgreesWithEncodingJSONAboutEmbedding(t *testing.T) {
	type inner struct {
		Deep string `json:"deep"`
	}
	type hidden struct {
		Promoted string `json:"promoted"`
	}
	// A named slice OF STRUCTS. The element type is the whole point: the walker
	// only consults `inlined` once the derefed type is a struct, so a []string
	// never reaches the branch and would not catch the regression.
	type Items []inner

	type fixture struct {
		inner             // embedded struct: INLINED, leaves are top-level keys
		hidden            // embedded UNEXPORTED struct: promoted onto the wire
		Items             // embedded NAMED SLICE OF STRUCTS: nests under "Items"
		Named      inner  `json:"named"`
		Plain      string `json:"plain"`
		Skipped    string `json:"-"`
		unexported string
	}
	_ = fixture{}.unexported

	var walked []string
	leafWalk(t, reflect.TypeOf(fixture{}), "", func(path string) {
		walked = append(walked, path)
	})
	sort.Strings(walked)

	f := fixture{Plain: "p", Skipped: "s"}
	f.Deep = "d"
	f.Promoted = "x"
	f.Items = Items{{Deep: "id"}}
	f.Named.Deep = "nd"
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if got, want := strings.Join(walked, "\n"), strings.Join(jsonLeafPaths(t, b), "\n"); got != want {
		t.Errorf("the redaction walker and encoding/json disagree about which leaves exist.\n"+
			"setLeaf plants its probe by path, so a wrong path means the guard watches a leaf "+
			"that does not exist and passes while the real one goes unredacted.\n\n"+
			"walker:\n  %s\n\nencoding/json:\n  %s\n\nbytes: %s",
			strings.ReplaceAll(got, "\n", "\n  "),
			strings.ReplaceAll(want, "\n", "\n  "), b)
	}
}

// jsonLeafPaths models paths the way leafWalk does: dotted, and ARRAYS ARE
// TRANSPARENT. The walker derefs a slice and keeps the same path, so a leaf
// inside a list of structs is "Items.deep" and never "Items[0].deep".
func jsonLeafPaths(t *testing.T, b []byte) []string {
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
			case []any:
				if len(tv) > 0 {
					if sub, ok := tv[0].(map[string]any); ok {
						rec(p, sub)
						continue
					}
				}
				out = append(out, p)
			default:
				out = append(out, p)
			}
		}
	}
	rec("", top)
	sort.Strings(out)
	return out
}
