package api

import (
	"os"
	"strings"
	"testing"
)

// THE SHAPE REGISTRY NAMED ITS PROOFS; IT NOW RUNS THEM. That is #176.
//
// The history matters, because each step of it looked like the fix:
//
//	1. coverageShape.By was a bare string and NOTHING read it. Step 7 skipped
//	   any row with Inspected==true without asking whether By named anything,
//	   so `{shape, true, true, "", note}` -- inspected by the empty string --
//	   passed silently. Three of seven inspected rows named a proof this
//	   package cannot run: two pointed at a test in package main, one at
//	   TestPlayoutCookieHandoff, which does not exist and never did.
//	2. This file then RESOLVED those strings against an AST index of every func
//	   in the repository, and required each to be a test in package api or a key
//	   in counterpartProofs. That caught all three, plus `By: "readLedger"` --
//	   a helper that asserts nothing.
//
// Step 2 was still a check on a NAME. A resolvable name is not a run: the
// registry could name a green test that never touches the shape, and one did.
// `streaming-media` named playoutManifestBytes, a real proof that really runs
// and really passes -- and that reads a 50-byte {"error":"this stream requires a
// playback token"} rather than a manifest, because the fixture it uses protects
// the stream. Nothing was wrong with the proof. It simply was not an inspection
// of that shape, and no amount of resolving the string could have said so.
//
// So the string is gone. shapeRow.Inspector is a func value, `inspected` and
// `inspectedBy` are DERIVED from it, and this file's job is to call each one and
// require it to witness its own shape in real bytes. The AST symbol index that
// used to live here went with the string it existed to second-guess: a func
// value cannot name a function that does not exist, cannot name another
// package's test, and cannot name a helper the ledger will not run.
//
// THE DIRECTION RULE still applies and is now trivially satisfied: nothing below
// computes a verdict, marks a shape inspected, or satisfies a sweep. It can only
// fail.

// TestEveryInspectedShapeWitnessesItself RUNS the shape registry.
//
// Called from TestLedgerPreflight as well as declared here, for the two reasons
// measured on ledger_ratchet_test.go: `rm internal/api/shape_reference_test.go`
// must not leave the suite green, and the TestMain preflight forces only
// ^TestLedgerPreflight$, so a guard outside it does not run in the filtered
// invocation the preflight exists to survive. The call IS the compile-time
// reference.
func TestEveryInspectedShapeWitnessesItself(t *testing.T) { runShapeInspectors(t) }

func runShapeInspectors(t *testing.T) {
	strict := os.Getenv("POLYEMESIS_LEDGER") == "strict"

	// The rig is built ONCE, on the PARENT t, before any subtest starts.
	//
	// The first version built it lazily inside whichever subtest asked first,
	// and plantedServer registers its teardown with t.Cleanup -- so the shared
	// server was closed the moment that subtest returned and every later
	// inspector dialled a dead fixture (the websocket one failed 401, which
	// reads as an authorization bug and is not one). A shared fixture has to
	// outlive the subtests that share it, which means it belongs to their
	// parent.
	//
	// It is built only if something will actually use it, so that a registry
	// whose inspectors are all LiveTools does not pay for a server nobody reads.
	willRun := false
	for _, row := range shapeRegistry() {
		if row.Inspector != nil && row.Emitted && !(row.LiveTools && !strict) {
			willRun = true
		}
	}
	var rig shapeRig
	if willRun {
		rig = newShapeRig(t)
	}

	ran, byBudget := 0, []string(nil)
	for _, row := range shapeRegistry() {
		if row.Inspector == nil {
			continue
		}
		if !row.Emitted {
			t.Errorf("the shape %q is recorded as NOT emitted and carries an inspector. "+
				"An inspector witnesses emitted bytes; a shape this API does not produce "+
				"cannot have any. Drop the inspector or correct Emitted.", row.Shape)
			continue
		}
		if row.LiveTools && !strict {
			byBudget = append(byBudget, row.Shape)
			continue
		}
		t.Run(row.Shape, func(t *testing.T) {
			obs := row.Inspector(t, rig)
			// WIRED TO THE WRONG ROW, and this is the check that makes the
			// registry's rows joined to their proofs rather than adjacent to
			// them. Every inspector hard-codes the shape it believes it
			// witnessed; moving one to another row -- the mutation a rename
			// invites -- fails here naming both.
			if obs.Shape != row.Shape {
				t.Errorf("the row %q is inspected by %s, and that inspector reports having "+
					"witnessed the shape %q. One of the two is wired wrong: an inspector "+
					"that witnesses a different shape from its row's is a coverage claim "+
					"transferred to a surface nothing looked at.",
					row.Shape, inspectorName(row.Inspector), obs.Shape)
			}
			if strings.TrimSpace(obs.Sample) == "" {
				t.Errorf("the inspector %s for the shape %q returned no bytes. `Inspected` "+
					"means something read the real emitted output; an empty sample is the "+
					"same claim the empty `By` string used to make.",
					inspectorName(row.Inspector), row.Shape)
			}
		})
		ran++
	}

	// THE POSITIVE CONTROL. Every assertion above is inside a loop, and a
	// registry that lost its inspectors would satisfy all of them having
	// examined nothing. The ratchet catches that too -- Inspector==nil moves the
	// row into shapesNotInspected and min() refuses to bank the rise -- but that
	// failure names a count, and this one names the fact.
	if ran == 0 {
		t.Fatal("no shape inspector ran. shapeRegistry holds no callable inspector, so " +
			"every check in this test examined nothing. `inspected` in the artifact is " +
			"derived from these func values; with none of them present the whole shape " +
			"column is a claim about an empty set.")
	}
	if len(byBudget) > 0 {
		t.Logf("shape inspectors not run in this invocation (LiveTools; they spawn a real "+
			"FFmpeg stand-in and wait for a child to write): %v. They run under "+
			"POLYEMESIS_LEDGER=strict, which CI sets. Recorded in the "+
			"counterpart-proofs-outside-the-preflight deferral.", byBudget)
	}
}

// TestBlankingEveryShapeNoteChangesNoVerdict is D's R2, scoped to shapes.
//
// THE PREVIOUS VERSION OF THIS TEST COULD NOT FAIL, and saying so plainly is
// the point of this comment. It blanked Note on the OUTPUT of emittedShapes()
// and re-ran shapeInspectionVerdicts, a helper added in the same commit,
// twenty lines below, commented "Deliberately does NOT consult Note." Blanking
// a field the reducer never reads cannot change the reducer's answer. It was a
// tautology wearing the name of a real guarantee, inside the file whose stated
// purpose is preventing exactly that, and it shipped green through an
// adversarial review and twenty CI checks.
//
// Worse, the property it announced was FALSE of the ledger at the time. The
// real discharge in step 7 was shapeDeferralIsDeclared(sh.Note) -- a substring
// search over an explanation. Mirroring the actual rule and blanking the notes
// moved six of eleven shape verdicts from discharged to failing. Prose decided.
//
// Two things had to change for this test to mean anything, and only one of them
// was in this file:
//
//  1. The discharge moved out of prose into coverageShape.Issue, a structured
//     field that also reaches citedIssues() and therefore the form check and the
//     git liveness scan.
//  2. This test now re-runs shapeVerdict -- THE function step 7 calls, not a
//     helper written to be this test's subject -- over the LIVE registry.
//
// That is what the route-level analogue, TestDeletingEveryProseReasonChanges-
// NoVerdict, has always done: mutate the real input, re-run the real
// classifier. The acceptance criterion for this test is that a mutation makes
// it fail. Add `strings.Contains(sh.Note, "Deferred")` to shapeVerdict and it
// fails by name on six shapes.
func TestBlankingEveryShapeNoteChangesNoVerdict(t *testing.T) {
	live := emittedShapes()

	// The positive control, and it is not optional here. Every assertion below
	// is "nothing changed"; a registry with no deferred rows, or a verdict
	// function collapsed to one answer, would satisfy all of them having
	// examined nothing. At least one shape must currently be discharged BY the
	// field this test claims is load-bearing.
	deferred := 0
	for _, sh := range live {
		if shapeVerdict(sh) == "deferred" {
			deferred++
		}
	}
	if deferred == 0 {
		t.Fatal("no shape in the registry is currently discharged by its Issue field, so " +
			"blanking the notes cannot demonstrate anything. This test asserts that the " +
			"deferral is structural rather than prose; with nothing deferred it is an " +
			"assertion about an empty set.")
	}

	before := shapeVerdicts(live)

	blanked := emittedShapes()
	for i := range blanked {
		blanked[i].Note = ""
	}
	after := shapeVerdicts(blanked)

	if len(before) != len(after) {
		t.Fatalf("blanking notes changed the shape COUNT: %d -> %d", len(before), len(after))
	}
	for _, shape := range sortedKeys(before) {
		if after[shape] != before[shape] {
			t.Errorf("blanking every note changed the ledger's verdict for the shape %q: "+
				"%q -> %q.\n"+
				"shapeVerdict is what step 7 acts on, and it must read Emitted, Inspected "+
				"and Issue -- never Note. A shape's coverage is a fact about what runs and "+
				"about which issue defers it; if prose can move this, prose can discharge "+
				"it, and the discharge stops being reachable by the citation form check "+
				"and the git liveness scan.\n"+
				"%d of %d shapes are currently discharged by Issue; move the deferral into "+
				"that field rather than into the note.", shape, before[shape], after[shape],
				deferred, len(live))
		}
	}
}
