package api

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// CONJUNCT 4: READBACK. THE META-GUARD OVER THE ARTIFACT, AND THE LAST RUNG OF
// THE LADDER.
//
// This ledger's recurring defect has a name by now: evidence that is measured,
// committed and never compared. It has landed EIGHT times in this file's
// history, and each time the fix was to add one more comparison:
//
//	sweepVerdicts.Sentinels, .Pointers, .Inert -- computed, written, read by
//	nothing. Inventing a sentinel list by hand passed. So did a plausible code
//	change that removed a real one.
//	partition.inertSubsetOfInvariant -- set to 999 by hand, whole suite green.
//	totals -- replaced wholesale with {"swept": 999, "denied": 42}, green, and
//	`make preflight-guard` green with it.
//	excuses / shapes / deferred -- three array sections, written by
//	-update-coverage and read back by nothing.
//
// Every one of those was found by a human perturbing the file and noticing. The
// guard against the ninth is therefore not another comparison: it is the
// PERTURBATION ITSELF, executed. For every field the artifact carries, change
// its committed value and require that the ledger's own comparison complains.
//
// WHY THIS TERMINATES THE LADDER, stated because "who guards the guard" is
// otherwise an infinite regress and this repository has argued about it before.
// Recursion stops at the first level whose vacuity is SELF-DETECTING. Level 1 --
// the comparisons in TestLedgerPreflight -- cannot see its own vacuity; the
// Sentinels/Pointers/Inert history is three proofs of that, since a comparison
// that was never written looks exactly like one that always passes. Level 2 CAN
// see its own, because a perturbation loop that stops perturbing stops producing
// complaints, and the declared canary below is the field for which a complaint
// must NOT appear. A walk that has gone blind reports the canary as covered and
// fails itself. So the canary is load-bearing rather than decorative: a
// meta-guard ships with its canary or does not ship, because a canary-less L2
// genuinely justifies an L3 and then there is no last rung.
//
// THE OTHER META-GUARD IN THIS PACKAGE, so the "exactly one over the artifact"
// claim is legible: shape_caller_guard_test.go is over the SOURCE -- it walks
// the call sites of shapeVerdict and refuses one that reads prose -- and it
// carries noteCanary for the same reason and by the same rule. This file is the
// only one over the ARTIFACT.
//
// WHAT THIS DELIBERATELY DOES NOT DO. It perturbs one occurrence per field PATH
// SHAPE (`routes[].coverage`, not `routes[37].coverage`), because the artifact
// holds 211 routes and 58 verdicts and a per-element sweep buys nothing: the
// comparisons are written over whole collections, so a field that is compared
// for element 0 is compared for element 200 by the same loop. A field the walk
// never reaches at all is what this is for, and that is a property of the path,
// not of the index.
//
// The path ENUMERATION does read every element, which is a different thing and
// was a surviving mutant: `json:",omitempty"` drops a false bool from the
// encoded row, so `sweepVerdicts[0]` carries no `inert` key, the path never
// existed, and deleting the comparison for it left this test green. Enumerate
// over all elements, perturb the first one that has the path.

// ledgerReporter is the subset of *testing.T the artifact comparisons use.
//
// It exists so that assertRouteSetsEqual, assertSweepVerdictsEqual,
// assertLedgerRatchets and the rest can be re-run by this file against a
// PERTURBED committed ledger and their complaints collected instead of failing
// the run. THE REAL FUNCTIONS, not copies: a meta-guard watching a restatement
// is exactly blocker 2 of #150, where redactSourceViewLikeViewSource was
// reverted and the production line was not, and projection_guard_test.go refuses
// the shape outright one directory over.
type ledgerReporter interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// complaintCollector is a ledgerReporter that records instead of failing.
//
// Fatalf panics with a sentinel and the caller recovers, because the real
// *testing.T's Fatalf is a runtime.Goexit and the comparison helpers are written
// expecting control not to return.
type complaintCollector struct{ msgs []string }

type collectorFatal struct{ msg string }

func (c *complaintCollector) Helper() {}

func (c *complaintCollector) Errorf(format string, args ...any) {
	c.msgs = append(c.msgs, fmt.Sprintf(format, args...))
}

func (c *complaintCollector) Fatalf(format string, args ...any) {
	panic(collectorFatal{fmt.Sprintf(format, args...)})
}

// compareCommittedLedger runs every comparison TestLedgerPreflight runs in its
// non-regenerating branch, against a `want` the caller may have perturbed and a
// `live` standing in for the measurement.
//
// One function so that what the preflight asserts and what this file perturbs
// cannot drift apart. If a comparison is added to the preflight and not here,
// the canary accounting below is what notices: a field that was already covered
// stays covered, but a NEW field arrives uncovered and is reported as a second
// canary, which fails.
func compareCommittedLedger(want, live coverageLedger, note, regen string) (msgs []string) {
	c := &complaintCollector{}
	defer func() {
		if r := recover(); r != nil {
			if f, ok := r.(collectorFatal); ok {
				msgs = append(c.msgs, f.msg)
				return
			}
			panic(r)
		}
		msgs = c.msgs
	}()

	assertRouteSetsEqual(c, want.Routes, live.Routes)
	assertSweepVerdictsEqual(c, want.SweepVerdicts, live.SweepVerdicts)
	assertPartitionTotalsEqual(c, want.Partition, live.Partition)
	assertCoverageTotalsEqual(c, want.Totals, live.Totals)
	if want.Note != note {
		c.Errorf("the note in %s is not the one this build writes.\ncommitted: %q\nlive: %q",
			coveragePath, want.Note, note)
	}
	assertProseSectionsEcho(c, want)
	assertCitationsAreWellFormed(c, want)
	assertLedgerRatchets(c, want, ledgerFacts{
		Excused:         live.Totals.Excused,
		Part:            live.Partition,
		Verdicts:        live.SweepVerdicts,
		NonGetWitnesses: live.NonGetDifferentialFloor,
		RegenCommand:    regen,
	})
	return c.msgs
}

// ledgerCanary is the ONE field this readback declares unasserted, and the
// declaration is what makes the walk's own blindness detectable.
//
// It is a real hole and a deliberate one. assertCitationsAreWellFormed checks
// that every citation the CODE makes appears in the committed list; it does not
// check the converse, so an EXTRA entry in citedIssues -- a number no excuse,
// deferral or shape names -- is committed evidence nothing reads. That asymmetry
// is not an oversight: it is the anti-laundering property. writeLedger
// INTERSECTS the live citations with the committed ones, so `-update-coverage`
// can only ever drop a citation, never introduce one, because the previous
// design regenerated the list wholesale and a fabricated #99999 failed once and
// was then written into the committed evidence by the very command the failure
// message recommended.
//
// So the field is unasserted in the ADD direction by design, and this file
// states it rather than letting it be discovered. If it ever starts producing a
// complaint, this declaration is stale and the test says so; if a SECOND field
// goes quiet, the walk has lost something and the test says that too.
const ledgerCanary = "citedIssues[+]"

// TestPerturbingAnyCommittedLedgerFieldIsCaught is conjunct 4.
//
// The comparison is committed-vs-committed: `live` is the pristine artifact, and
// `want` is a copy with one field changed. That is deliberate and it is what
// makes this cheap enough to run on every invocation -- no server, no sweep, no
// fixture -- while asserting exactly the property the criterion names, which is
// about the COMPARISON's reach and not about any particular measurement.
func TestPerturbingAnyCommittedLedgerFieldIsCaught(t *testing.T) {
	assertLedgerReadback(t)
}

func assertLedgerReadback(t *testing.T) {
	t.Helper()

	raw, err := os.ReadFile(coveragePath)
	if err != nil {
		t.Fatalf("read %s: %v", coveragePath, err)
	}
	var pristine coverageLedger
	if err := json.Unmarshal(raw, &pristine); err != nil {
		t.Fatalf("parse %s: %v", coveragePath, err)
	}
	// THE PREFLIGHT'S regeneration command, not this test's. ledgerRegenCommand
	// derives it from the RUNNING test's name so that no failure message ever
	// tells a reader to run a filter that would regenerate nothing -- which
	// means the note this file has to reproduce is the one TestLedgerPreflight
	// writes, whichever entry point is executing. The literal is not a duplicate
	// left to rot: TestEveryDocumentedRunFilterNamesALiveTest scans this package
	// for exactly this shape and fails if the test it names stops existing.
	regen := "go test ./internal/api -run '^TestLedgerPreflight$' -update-coverage"
	note := ledgerNoteWith(regen)

	// THE NEGATIVE CONTROL, first. Pristine against pristine must be silent, or
	// every "the perturbation was caught" below is meaningless because
	// everything is a complaint.
	if msgs := compareCommittedLedger(pristine, pristine, note, regen); len(msgs) > 0 {
		t.Fatalf("the committed ledger does not compare clean against ITSELF, so this "+
			"readback cannot distinguish a caught perturbation from noise. %d complaint(s), "+
			"first: %s\nThis usually means the artifact is stale against the code; "+
			"regenerate with `%s`.", len(msgs), msgs[0], regen)
	}

	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("re-parse %s as generic JSON: %v", coveragePath, err)
	}
	paths := ledgerFieldPaths(generic)
	if len(paths) < 20 {
		t.Fatalf("the field walk found only %d paths in %s, which is a 2,000-line "+
			"artifact. A walk that under-populates reports every field as caught and this "+
			"whole test as green; that is the failure mode a meta-guard is most prone to. "+
			"paths: %v", len(paths), coveragePath, paths)
	}

	var uncaught []string
	for _, path := range paths {
		perturbed := perturbLedgerField(t, raw, path)
		if msgs := compareCommittedLedger(perturbed, pristine, note, regen); len(msgs) == 0 {
			uncaught = append(uncaught, path)
		}
	}
	// The canary is not a path in the walk -- it is an ADDITION, not a change --
	// so it is perturbed separately and appended to the same list.
	if msgs := compareCommittedLedger(withExtraCitation(t, raw), pristine, note, regen); len(msgs) == 0 {
		uncaught = append(uncaught, ledgerCanary)
	}
	sort.Strings(uncaught)

	if len(uncaught) == 1 && uncaught[0] == ledgerCanary {
		t.Logf("READBACK: %d committed field paths perturbed, all caught except the one "+
			"declared canary %q -- an entry added to citedIssues that no excuse, deferral "+
			"or shape names. That hole is the anti-laundering asymmetry and is deliberate; "+
			"see this file's ledgerCanary comment.", len(paths), ledgerCanary)
		return
	}
	t.Errorf("READBACK: exactly one field is declared unasserted (%q) and %d came back "+
		"uncaught: %v\n"+
		"Each of those is a value committed to %s that no comparison in this package "+
		"reads, which is the defect this ledger has shipped EIGHT times -- sentinels, "+
		"pointers, inert, inertSubsetOfInvariant, the whole totals block, and three "+
		"array sections. Add the comparison, or, if the field is genuinely and "+
		"deliberately unread, that is a second canary and this test's single-canary "+
		"rule is what refuses it: a guard with a growing exemption list is the seventh "+
		"free pass this repository has invented.\n"+
		"If %q is MISSING from that list, the declaration is stale in the other "+
		"direction: something now asserts it, which is good news, and the canary has to "+
		"move to a field that is still genuinely unread or this walk loses its ability "+
		"to detect its own blindness.",
		ledgerCanary, len(uncaught), uncaught, coveragePath, ledgerCanary)
}

// ledgerFieldPaths enumerates the artifact's field path SHAPES: object keys
// joined by ".", arrays collapsed to "[]".
func ledgerFieldPaths(v any) []string {
	set := map[string]bool{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, sub := range t {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				walk(p, sub)
			}
		case []any:
			if len(t) == 0 {
				// An empty array is still a committed field, and a comparison
				// that never sees a non-empty one is exactly the kind of gap
				// this walk is for. Recorded as a leaf.
				set[prefix] = true
				return
			}
			// EVERY element, not just the first, and that correction came from a
			// surviving mutant. `json:",omitempty"` means a false bool or an
			// empty slice is ABSENT from the encoded row -- so sweepVerdicts[0],
			// an ordinary non-inert row, does not carry `inert` at all, the walk
			// never produced that path, and deleting the comparison for it left
			// this test green. A readback blind to exactly the fields that are
			// usually zero is blind to the interesting ones.
			for _, el := range t {
				walk(prefix+"[]", el)
			}
		default:
			set[prefix] = true
		}
	}
	walk("", v)
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// perturbLedgerField changes the value at one path shape (first array element
// throughout) and returns the artifact as a coverageLedger.
func perturbLedgerField(t *testing.T, raw []byte, path string) coverageLedger {
	t.Helper()
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("parse %s: %v", coveragePath, err)
	}
	if !setAtPath(generic, strings.Split(path, "."), path) {
		t.Fatalf("the readback could not reach %q to perturb it, so that path is reported "+
			"as CAUGHT while nothing was ever changed -- a false green in the guard whose "+
			"whole job is finding false greens. Fix ledgerFieldPaths or setAtPath.", path)
	}
	b, err := json.Marshal(generic)
	if err != nil {
		t.Fatalf("re-marshal the perturbed ledger: %v", err)
	}
	var out coverageLedger
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("parse the perturbed ledger: %v", err)
	}
	return out
}

func setAtPath(node any, segs []string, full string) bool {
	seg := segs[0]
	key := strings.TrimSuffix(seg, "[]")
	m, ok := node.(map[string]any)
	if !ok {
		return false
	}
	child, ok := m[key]
	if !ok {
		return false
	}
	if strings.HasSuffix(seg, "[]") {
		arr, ok := child.([]any)
		if !ok || len(arr) == 0 {
			return false
		}
		if len(segs) == 1 {
			arr[0] = perturbedValue(arr[0])
			return true
		}
		// THE FIRST ELEMENT THAT ACTUALLY HAS THE PATH. An omitempty field is
		// absent from most rows and present on a few, and perturbing element 0
		// unconditionally would silently perturb nothing -- reporting the path
		// as caught while the artifact was never changed, which is the false
		// green this whole file exists to find.
		for _, el := range arr {
			if setAtPath(el, segs[1:], full) {
				return true
			}
		}
		return false
	}
	if len(segs) == 1 {
		m[key] = perturbedNumberForRatchet(key, child)
		return true
	}
	return setAtPath(child, segs[1:], full)
}

// perturbedNumberForRatchet perturbs a top-level scalar in the direction its
// ratchet ACTUALLY ASSERTS, read out of ratchetDirection rather than chosen.
//
// A ratchet is one-directional by construction and that is the whole guarantee:
// a ceiling may be RAISED by a hand edit (a reviewable act, and the designed
// escape hatch) and may not fall below the live count; a floor is the mirror. So
// perturbing a ceiling upward is legal and produces no complaint, and a readback
// that treated that as a gap would be demanding that the ratchets stop being
// ratchets.
//
// Derived, not listed. ledger_ratchet_test.go already parses writeLedger's AST
// and requires every field it writes to declare min, max or measurement, so the
// direction this reads is the same declaration the clamp guard enforces --
// which means a new ratchet field arrives here with its direction already
// stated, and a field whose direction is wrong fails there rather than silently
// weakening this.
func perturbedNumberForRatchet(jsonKey string, v any) any {
	n, isNumber := v.(float64)
	if !isNumber {
		return perturbedValue(v)
	}
	switch ratchetDirection[goFieldForJSONKey(jsonKey)] {
	case "min": // a ceiling: falling below the live count is what must fail
		return float64(0)
	case "max": // a floor: rising above the live measurement is what must fail
		return n + 9999
	}
	return perturbedValue(v)
}

// goFieldForJSONKey maps a JSON key to the coverageLedger field name
// ratchetDirection is keyed by. Both are derived from the same struct, so this
// is a case conversion rather than a second list.
func goFieldForJSONKey(key string) string {
	if key == "" {
		return ""
	}
	return strings.ToUpper(key[:1]) + key[1:]
}

// perturbedValue returns a value that is definitely different, whatever the
// type. Numbers move by a large amount in the LOOSENING direction for a ceiling
// and the tightening one for a floor -- so a ratchet is perturbed both ways
// across the run and the direction that is asserted is the one that has to fire.
func perturbedValue(v any) any {
	switch t := v.(type) {
	case string:
		return t + "-PERTURBED-BY-THE-READBACK"
	case float64:
		// Zero is the value that catches both a ceiling (which then refuses any
		// live count above it) and an equality; a floor perturbed to zero is the
		// hand-edit direction and is covered by the paired sweep below.
		if t == 0 {
			return float64(9999)
		}
		return float64(0)
	case bool:
		return !t
	case nil:
		return "PERTURBED-BY-THE-READBACK"
	case []any:
		return append([]any{"PERTURBED-BY-THE-READBACK"}, t...)
	case map[string]any:
		out := map[string]any{"perturbedByTheReadback": true}
		for k, v := range t {
			out[k] = v
		}
		return out
	}
	return v
}

// withExtraCitation is the canary's perturbation: a citation nothing names.
func withExtraCitation(t *testing.T, raw []byte) coverageLedger {
	t.Helper()
	var l coverageLedger
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatalf("parse %s: %v", coveragePath, err)
	}
	l.CitedIssues = append(append([]string{}, l.CitedIssues...), "#99999")
	sort.Strings(l.CitedIssues)
	return l
}
