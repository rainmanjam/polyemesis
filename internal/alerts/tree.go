package alerts

import (
	"encoding/json"
	"strings"
)

// The residual redaction pass over a free-form payload, by its JSON shape.
//
// MOVED HERE FROM internal/api/ws_policy.go, unchanged, when the real-time
// events API (internal/eventstream) became its second caller. Two copies of
// the argv rule below would be two places for #162 to come back, and the
// second copy would be the one nobody reviewed.

// RedactTree applies the residual pass to every string leaf of a payload
// while PRESERVING ITS WIRE SHAPE.
//
// Through the JSON form rather than a type switch, because the payload types
// come from three chat adapters, the caption pipeline and the engine, and a
// switch here would silently pass through whichever one nobody updated. Through
// the decoded tree rather than the raw bytes, because running a text redactor
// over JSON source can in principle produce something that is no longer JSON,
// and a socket frame the browser cannot parse is an outage.
//
// A field whose NAME says credential is masked outright rather than pattern-
// matched, which is the same rule Event.Redacted already applies.
func RedactTree(data any) any {
	if data == nil {
		return nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		return nil
	}
	return RedactJSONTree(tree)
}

func RedactJSONTree(v any) any {
	switch t := v.(type) {
	case string:
		return Redact(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if SecretName(k) {
				out[k] = Mask
				continue
			}
			out[k] = RedactJSONTree(e)
		}
		return out
	case []any:
		// AN ARRAY OF STRINGS IS THE ONE SHAPE WHERE ELEMENT-WISE REDACTION IS
		// THE BUG (#162), because an array of strings in a payload is an ARGV,
		// and an argv is where a credential arrives split across elements.
		//
		//	{"argv":["-headers","Authorization:","Bearer","<key>"]}
		//
		// Redact over each element in turn masks NOTHING there: "Authorization:"
		// has no value after the separator, "Bearer" has no token after it, and
		// "<key>" has no label before it. Over the JOIN it masks both. That is
		// the whole of #162 -- Redact's grammar is `label SEP value`, and the
		// split severs every one of its rules -- and the same argv reaches
		// /processes and the log ring, where #150 fixed it STRUCTURALLY with
		// supervisor.Spec.Secrets rather than with more patterns.
		//
		// There is no exact set to consult here: this tree is decoded from
		// somebody else's payload and nothing declared what in it is secret. So
		// the array is treated as ALL-OR-NOTHING, which is sound in the only
		// direction that matters:
		//
		//	Redact replaces SUBSTRINGS. Every element is a substring of the
		//	space-joined text, and the join only ever ADDS context (it can
		//	complete a `label SEP value` that the split broke; it cannot break
		//	one, because every rule stops at whitespace or at a word boundary
		//	and a space is both). So an UNCHANGED join proves every element is
		//	unchanged, and there is nothing to mask.
		//
		// A CHANGED join proves the opposite -- something in this array is a
		// credential -- but not WHICH element, and guessing is the grammar that
		// already failed. The array collapses to a single mask.
		//
		// The cost is a wire-shape change on exactly the arrays that would
		// otherwise leak, which is the correct trade for a read-scoped socket
		// and is measured rather than assumed: TestReadScopedFramesAreUnchanged
		// drives today's real Source, Chat and Caption payloads through this
		// function and asserts byte-identity.
		if joined, ok := joinedStrings(t); ok && Redact(joined) != joined {
			return []any{Mask}
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = RedactJSONTree(e)
		}
		return out
	default:
		return v
	}
}

// joinedStrings joins the STRING elements of a decoded JSON array with a
// single space, reporting false when there are none.
//
// Only the direct string elements: a nested object or array carries its own
// structure and is handled by the recursion, and flattening it into this text
// would make an unrelated neighbour's contents decide whether this array is
// masked.
//
// Non-string elements are SKIPPED rather than disqualifying the array, and that
// asymmetry is deliberate. Requiring an array to be wholly strings before the
// rule applies was the reviewer's suggestion and it is the wrong trade here: one
// nested object anywhere in an argv would switch the rule off and hand the
// remaining elements back to the per-element path, which is the path that masks
// nothing on a split header. The cost of the choice made instead is that a mixed
// array whose strings happen to join into a match collapses when it perhaps need
// not -- an over-mask, in the safe direction, on a shape nothing in this build
// produces. See the "a non-string neighbour must not switch the rule off" row in
// TestRedactJSONTreeDoesNotLeakAnArgvHeader.
//
// The separator is a space and not the empty string on purpose. Joining with
// nothing would GLUE two innocent elements into a match -- ["price", "=", "9"]
// is not a credential and "price=9" reads like one to a `label SEP value`
// grammar -- so an empty separator would over-mask, while a space reproduces
// exactly the text the process would have had on one command line.
func joinedStrings(arr []any) (string, bool) {
	var b strings.Builder
	n := 0
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			continue
		}
		if n > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s)
		n++
	}
	return b.String(), n > 0
}
