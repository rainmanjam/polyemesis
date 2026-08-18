package diag

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

// The size bound, and why the ring needed a second one.
//
// DefaultCapacity bounds how MANY records are kept. It says nothing about how
// large they are: a Record carries an unbounded string and an unbounded map of
// unbounded values, so "5,000 records" was a count the UI could report honestly
// and a size nobody could predict (#421). The bundle is handed to an operator's
// browser, which buffers it whole -- twice, once as text and once as a blob --
// so an unbounded export fails in the tab of the person trying to report a
// fault.
//
// A BUDGET PER RECORD RATHER THAN A LIMIT PER STRING. A per-string cap is
// trivially defeated by a record carrying a thousand attributes each just under
// it, which is not a contrived shape: a filter graph, an argv and a platform
// error body arrive on the same line. The budget is spent in a fixed order and
// what runs out is dropped, so one record cannot exceed the cap by any
// arrangement of its parts.
//
// MEASURED IN BYTES, NOT MARSHALLED. Observe is on the logging path and its
// contract says it must not block; serialising every record to measure it would
// put a json.Marshal between the process and every line it logs. Counting bytes
// as the existing scrub walk already visits them is free by comparison.
const MaxRecordBytes = 8192

// truncationMarker is what a cut value carries in place of the rest of itself.
//
// STATED, NOT IMPLIED, which is the same rule CaptureInfo.Truncated follows for
// dropped records: a shortened line that reads as a complete one is how an
// engineer concludes the fault left no trace. The marker names the number of
// bytes removed, because "there was more" and "there was another 40 KB" lead to
// different next steps.
const truncationMarker = "…[truncated"

// noteReserve is the room held back so a []string that loses elements can still
// say how many. Comfortably larger than "…[truncated, 4294967295 more]".
const noteReserve = 40

// maxLevelBytes bounds the level string. slog's own levels are four or five
// characters; this exists so a hand-built Record cannot make the budget
// negative before the message is paid for.
const maxLevelBytes = 64

func marker(dropped int) string {
	return fmt.Sprintf("%s, %d bytes dropped]", truncationMarker, dropped)
}

// encodedLen is how many bytes s costs once JSON has escaped it.
//
// THE CAP HAS TO BOUND THE FILE, NOT THE ARITHMETIC. It counted raw bytes, and
// JSON writes SIX for a control character (\u0000). A record of 8 KB of NULs
// measured 8,192 against the cap and serialised to 49,152 — so the ceiling this
// package states about itself was wrong by a factor of six, measured at 785,437
// bytes against an asserted 393,216.
//
// Counted in one pass with no allocation, which is what keeps it affordable on
// the logging path: Observe's contract is that it must not block, and
// marshalling every record to measure it would put a json.Marshal between the
// process and every line it logs. & < and > are one byte each because
// renderForScrub disables HTML escaping.
func encodedLen(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			n += 2
		case c == '\n' || c == '\r' || c == '\t' || c == '\b' || c == '\f':
			n += 2
		case c < 0x20:
			n += 6 // \u00XX
		default:
			n++
		}
	}
	return n
}

// cutString shortens s to fit budget, on a rune boundary, leaving room for the
// marker.
//
// ON A RUNE BOUNDARY because the bundle is JSON and a string cut through the
// middle of a multi-byte rune does not survive encoding -- the engineer
// receiving it gets a file that will not parse, which is worse than a file that
// is merely short.
func cutString(s string, budget int) (string, bool) {
	if encodedLen(s) <= budget {
		return s, false
	}
	if budget <= 0 {
		return "", true
	}
	// THE MARKER MUST FIT INSIDE THE BUDGET, NOT ON TOP OF IT. An earlier version
	// clamped `keep` at zero and returned the marker regardless, so a message
	// that spent the budget down to a few bytes still emitted ~30 -- landing the
	// record OVER the cap by way of the text saying it had been brought under.
	// Reviewed by agy, which reproduced it as an 8,224-byte record.
	m := marker(len(s))
	if len(m) > budget {
		// No room even to say it was cut. The record-level Truncated flag and the
		// capture's RecordsTruncated still carry that, so this is not silent.
		return "", true
	}
	// WALKED IN ENCODED UNITS, NOT SLICED BY RAW INDEX. The budget is a JSON
	// cost, so a byte index into s does not measure it: 8 KB of NULs costs six
	// times its length. Accumulate until the allowance runs out.
	allow := budget - len(m)
	keep, spent := 0, 0
	for keep < len(s) {
		c := s[keep]
		cost := 1
		switch {
		case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t' || c == '\b' || c == '\f':
			cost = 2
		case c < 0x20:
			cost = 6
		}
		if spent+cost > allow {
			break
		}
		spent += cost
		keep++
	}
	// Back off to the last complete rune: a string cut through a multi-byte rune
	// does not survive JSON encoding, and an engineer receiving a file that will
	// not parse is worse off than one receiving a short one.
	for keep > 0 && keep < len(s) && !utf8.RuneStart(s[keep]) {
		keep--
	}
	// marker(len(s)-keep) is never longer than marker(len(s)), which was the
	// figure reserved, so the result cannot exceed budget.
	return s[:keep] + marker(len(s)-keep), true
}

// valueBytes is what a value costs, walking the same shapes scrubValue does.
func valueBytes(v any) int {
	switch t := v.(type) {
	case string:
		return encodedLen(t)
	case []string:
		n := 0
		for _, s := range t {
			n += encodedLen(s)
		}
		return n
	case map[string]any:
		n := 0
		for k, vv := range t {
			n += len(k) + valueBytes(vv)
		}
		return n
	case error:
		if t == nil {
			return 0
		}
		return len(t.Error())
	case []byte:
		// Stored as text now, not base64 — see diag.renderForScrub.
		return len(t)
	case fmt.Stringer:
		return len(t.String())
	default:
		// A flat price for genuine scalars: a number or a bool encodes to a couple
		// of dozen bytes at most, and charging something rather than nothing means
		// a record of ten thousand integers still spends its budget.
		if isScalar(v) {
			return 8
		}
		// ANYTHING ELSE IS MEASURED, NOT GUESSED. Pricing an unrecognised value at
		// 8 bytes made a 1 MiB []byte measure as 16 and let capRecord return early
		// believing the record was already small. In the Observe path scrubValue
		// has already rendered these to strings, so this is the belt to that
		// braces -- it is what makes capRecord's contract true for direct callers.
		if b, err := json.Marshal(v); err == nil {
			return len(b)
		}
		return len(fmt.Sprint(v))
	}
}

// recordBytes is the measured size of a record, in the same units as the cap.
func recordBytes(rec Record) int {
	n := encodedLen(rec.Message) + encodedLen(rec.Level)
	for k, v := range rec.Attrs {
		n += encodedLen(k) + valueBytes(v)
	}
	return n
}

// capValue shortens a value to fit budget, returning what is left of the budget.
func capValue(v any, budget int) (any, int, bool) {
	switch t := v.(type) {
	case string:
		out, cut := cutString(t, budget)
		return out, budget - encodedLen(out), cut
	case []string:
		out := make([]string, 0, len(t))
		cut := false
		dropped := 0
		// ROOM FOR THE NOTE IS RESERVED BEFORE THE ELEMENTS SPEND THE BUDGET.
		// Without it the loop consumes everything and the "N more" note cannot be
		// afforded at the one moment it is needed -- which is precisely when
		// elements were dropped.
		reserve := 0
		if len(t) > 1 && budget > noteReserve {
			reserve = noteReserve
			budget -= reserve
		}
		for i, s := range t {
			if budget <= 0 {
				// The remaining elements are dropped rather than emptied: an argv
				// of blank strings looks like an argv that WAS blank.
				dropped = len(t) - i
				cut = true
				break
			}
			c, did := cutString(s, budget)
			// AN ELEMENT THAT CANNOT BE REPRESENTED AT ALL IS DROPPED, NOT
			// APPENDED BLANK. When the remaining budget is smaller than the
			// marker, cutString returns "" -- and appending that produced exactly
			// the argv of blank strings this branch exists to avoid, which reads
			// as arguments that were genuinely empty.
			if s != "" && c == "" {
				dropped = len(t) - i
				cut = true
				break
			}
			out = append(out, c)
			budget -= encodedLen(c)
			cut = cut || did
		}
		budget += reserve
		if dropped > 0 {
			// SAID, NOT SILENT. ["ffmpeg","-i","src","-f","flv","rtmp://…"] cut to
			// ["ffmpeg","-i","src"] reads as a command that was genuinely that
			// short, and the argv is usually the thing being diagnosed.
			note := fmt.Sprintf("%s, %d more]", truncationMarker, dropped)
			if len(note) <= budget {
				out = append(out, note)
				budget -= len(note)
			}
		}
		return out, budget, cut
	case map[string]any:
		out := make(map[string]any, len(t))
		cut := false
		for _, k := range sortedKeys(t) {
			// THE KEY MUST FIT BEFORE IT IS TAKEN. Checking only `budget <= 0`
			// here let a 2 KB key past a 1-byte budget and expanded the record
			// beyond the cap -- capRecord already guarded this and capValue did
			// not, which is exactly the kind of gap a second reader finds.
			if budget <= len(k) {
				cut = true
				break
			}
			budget -= len(k)
			vv, rest, did := capValue(t[k], budget)
			out[k] = vv
			budget = rest
			cut = cut || did
		}
		if len(out) != len(t) {
			cut = true
		}
		return out, budget, cut
	case error:
		// Reached only when capRecord is called directly: Observe's scrubValue has
		// already flattened errors to strings by this point. Handled anyway,
		// because capRecord's contract is that NOTHING it returns exceeds the cap,
		// and an error wrapping a response body is the classic oversized value.
		if t == nil {
			return nil, budget, false
		}
		out, did := cutString(t.Error(), budget)
		return out, budget - len(out), did
	case fmt.Stringer:
		out, did := cutString(t.String(), budget)
		return out, budget - len(out), did
	default:
		n := valueBytes(v)
		if n > budget {
			// Dropped to an empty string rather than passed through unbudgeted, as
			// an earlier version did -- that returned a negative budget AND
			// reported no cut, so the record went over the cap claiming not to have.
			return "", budget, true
		}
		if isScalar(v) {
			return v, budget - n, false
		}
		// Unrecognised and it fits: carry its rendering, so what the bundle holds
		// is what was measured.
		if b, err := json.Marshal(v); err == nil {
			return string(b), budget - n, false
		}
		return fmt.Sprint(v), budget - n, false
	}
}

// sortedKeys gives the budget a deterministic spending order.
//
// WITHOUT IT THE SAME RECORD CUTS DIFFERENTLY ON EVERY CAPTURE, because Go
// randomises map iteration. Two operators reproducing the same fault would send
// bundles that disagree about which attributes survived, and the first thing
// anybody does with two diagnostics is diff them.
func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// capRecord brings a record within MaxRecordBytes.
//
// CALLED AFTER THE SCRUB, NEVER BEFORE, and the ordering is the whole safety
// argument. The secret set matches WHOLE literals, longest first. Cutting first
// would leave the front half of a stream key standing as text no scrubber
// recognises, and it would travel to a stranger in plaintext -- the exact
// disclosure the scrub-on-the-way-in design exists to prevent. Truncation is a
// size concern and must run downstream of every safety concern.
//
// THE MESSAGE IS PAID FIRST because it is the line a human reads; attributes
// are context for it. A record whose message was dropped to preserve its
// attributes would be unreadable in the way that matters.
func capRecord(rec Record) (Record, bool) {
	if recordBytes(rec) <= MaxRecordBytes {
		return rec, false
	}
	// EVERYTHING REACHING HERE WAS OVER THE CAP -- the early return above is the
	// only way under it -- so this record IS truncated by definition. Deriving
	// the flag from whether a particular branch happened to trim something let a
	// record carrying an oversized error value through with truncated:false,
	// reporting a clean capture while holding a line that stops for no stated
	// reason.
	cut := true
	// THE LEVEL IS CAPPED BEFORE IT IS SUBTRACTED. It comes from slog and is
	// always four or five characters in practice, but capRecord's contract is
	// that nothing it returns exceeds the cap, and an oversized level made the
	// budget negative while the level itself survived untouched.
	if len(rec.Level) > maxLevelBytes {
		rec.Level, _ = cutString(rec.Level, maxLevelBytes)
	}
	budget := MaxRecordBytes - encodedLen(rec.Level)

	msg, _ := cutString(rec.Message, budget)
	rec.Message = msg
	budget -= encodedLen(msg)

	if len(rec.Attrs) > 0 {
		out := make(map[string]any, len(rec.Attrs))
		for _, k := range sortedKeys(rec.Attrs) {
			if budget <= len(k) {
				// Dropped outright rather than kept with an empty value, which
				// would assert that the attribute was present and blank.
				cut = true
				break
			}
			budget -= encodedLen(k)
			v, rest, did := capValue(rec.Attrs[k], budget)
			out[k] = v
			budget = rest
			cut = cut || did
		}
		if len(out) != len(rec.Attrs) {
			cut = true
		}
		rec.Attrs = out
	}
	return rec, cut
}
