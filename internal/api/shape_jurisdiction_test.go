package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ISSUES #168 AND #169. AN ISSUE NUMBER IS NOT A DISCHARGE, INCLUDING IN THE
// SHAPE CHANNEL.
//
// #164 deleted "a filed issue discharges an excuse" from the route channel and
// wrote the argument down at length. The shape channel then re-admitted it: step
// 7's decision was shapeVerdict, and shapeVerdict's third case was
//
//	case issueRef.MatchString(sh.Issue): return "deferred"
//
// so four emitted shapes were discharged by a string of the form #NNN. Two
// consequences, both live in the committed tree before this file:
//
//  1. The verdict read EXTERNAL STATE. Not the issue's contents -- nothing here
//     can see those -- but its NUMBER, which is a token whose whole meaning is
//     that somebody else is tracking it. A ledger conjunct that reads a tracker
//     is a ledger that goes green or red for reasons outside the repository.
//  2. #169 became UNCLOSEABLE. Its shape row discharged on "#169" and
//     TestNoLedgerCitationNamesAnIssueACommitClosed fails on a citation a commit
//     announces closing, so a commit saying "Closes #169" broke the build. The
//     port-80 listener genuinely is permanently open -- it binds in package main
//     before any router in internal/api exists, which is a jurisdiction boundary
//     rather than unfinished coverage -- and "permanently open" must be a record
//     somebody can retire with an edit, not a load-bearing part of the build.
//
// WHAT REPLACES IT. A shapeJurisdiction: the PACKAGE and the TEST NAME that
// assert the shape, RESOLVED here by running `go test -list` in that package and
// requiring the name to come back. That is:
//
//   - weaker than an inspector, and the rows say so. It establishes that an
//     assertion exists and runs somewhere, not that it read these bytes. Six
//     shapes have inspectors and those are the strong discharge.
//   - stronger than a citation, because it cannot name a test that does not
//     exist, cannot name a test in a package that does not compile, and cannot
//     survive the test being renamed or deleted. Those are exactly the three
//     defects the AST symbol index deleted in #176 was built to find in the old
//     free-text `by` string, and `go test -list` finds them without the index.
//   - verdict-pure. Blank every Issue in the registry and no verdict moves;
//     TestDeletingEveryShapeIssueChangesNoVerdict runs that mutation.
//
// WHY `go test -list` AND NOT AN AST WALK. The walk is what this package used to
// do and what #176 threw away, and its failure mode is that it resolves a NAME
// rather than establishing that a test RUNS: a Test function inside a build tag
// this configuration excludes, or in a package whose test binary does not
// compile, resolves perfectly and never executes. `go test -list` compiles the
// test binary and enumerates what the framework would run, which is the same
// oracle the framework itself uses.

// TestEveryJurisdictionRecordResolvesToALiveTest is the resolution.
//
// Called from TestLedgerPreflight as well as declared here, for the two reasons
// measured on ledger_ratchet_test.go: a guard nothing references is deletable in
// silence, and TestMain forces only ^TestLedgerPreflight$.
func TestEveryJurisdictionRecordResolvesToALiveTest(t *testing.T) {
	assertJurisdictionRecordsResolve(t)
}

func assertJurisdictionRecordsResolve(t *testing.T) {
	t.Helper()

	// Grouped by package so one `go test -list` answers every row in it. Three
	// packages today; a per-row invocation would compile internal/api's test
	// binary four times over.
	byPackage := map[string][]coverageShape{}
	for _, sh := range emittedShapes() {
		if sh.Jurisdiction == nil {
			continue
		}
		if sh.Jurisdiction.Package == "" || sh.Jurisdiction.Test == "" {
			t.Errorf("the shape %q carries a jurisdiction record with an empty package (%q) "+
				"or test (%q). Both halves are the record; a half-filled one discharges "+
				"nothing and shapeVerdict already refuses it.",
				sh.Shape, sh.Jurisdiction.Package, sh.Jurisdiction.Test)
			continue
		}
		byPackage[sh.Jurisdiction.Package] = append(byPackage[sh.Jurisdiction.Package], sh)
	}

	// THE POSITIVE CONTROL. Every check below is "the name came back"; a
	// registry with no jurisdiction records satisfies all of them having
	// resolved nothing, which is the vacuity this whole file is a reaction to.
	if len(byPackage) == 0 {
		t.Fatal("no shape in the registry carries a jurisdiction record, so this test " +
			"resolved nothing. If every emitted shape is now inspected that is good news " +
			"and this test is the thing to delete; if a row was moved back onto an Issue, " +
			"shapeVerdict no longer discharges it and step 7 is where that shows up.")
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		// The one place this check cannot run, said out loud on every run rather
		// than skipped. A source tree without go.mod is not a tree `go test
		// -list` can be pointed at.
		t.Logf("NO go.mod at %s: the jurisdiction records in this ledger are UNRESOLVED in "+
			"this build. That is a real hole and it is printed rather than skipped. "+
			"records: %v", root, sortedPackages(byPackage))
		return
	}

	for _, pkg := range sortedPackages(byPackage) {
		live, err := listTests(root, pkg)
		if err != nil {
			t.Errorf("`go test -list` in ./%s failed: %v\n"+
				"A jurisdiction record naming a package whose test binary does not build "+
				"is worth exactly what the old free-text `by` string was worth. The shapes "+
				"pointing here: %v", pkg, err, shapeNames(byPackage[pkg]))
			continue
		}
		for _, sh := range byPackage[pkg] {
			if live[sh.Jurisdiction.Test] {
				continue
			}
			t.Errorf("the shape %q is discharged by a jurisdiction record naming %s in "+
				"./%s, and `go test -list` there does not report that test.\n"+
				"This is the discharge, so the shape is now undischarged: either the test "+
				"was renamed or deleted, or the record was wrong when it was written. "+
				"Re-point it, or give the row an inspector and stop claiming another "+
				"package's suite.\n%d tests were listed in that package.",
				sh.Shape, sh.Jurisdiction.Test, pkg, len(live))
		}
	}
}

// listTests runs `go test -list` and returns the top-level test names it
// reports.
//
// -run is set to a pattern that matches nothing so that -list never executes
// anything; -list already implies that, and the belt is cheap next to the cost
// of this guard accidentally running another package's suite inside a preflight.
func listTests(root, pkg string) (map[string]bool, error) {
	cmd := exec.Command("go", "test", "-list", "^Test", "-run", "XXXNoSuchTest", "./"+pkg)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmtError(err, out)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Test") && !strings.ContainsAny(line, " \t") {
			names[line] = true
		}
	}
	if len(names) == 0 {
		return nil, fmtError(errNoTestsListed, out)
	}
	return names, nil
}

var errNoTestsListed = errNoTests{}

type errNoTests struct{}

func (errNoTests) Error() string {
	return "`go test -list` reported no test names, so this package's records resolved " +
		"against an empty set -- which is the failure mode a listing-based guard is most " +
		"prone to and would otherwise pass silently"
}

func fmtError(err error, out []byte) error {
	return &listError{err: err, out: strings.TrimSpace(string(out))}
}

type listError struct {
	err error
	out string
}

func (e *listError) Error() string { return e.err.Error() + "\n" + truncateForFailure(e.out) }

func sortedPackages(m map[string][]coverageShape) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func shapeNames(rows []coverageShape) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Shape)
	}
	sort.Strings(out)
	return out
}

// TestDeletingEveryShapeIssueChangesNoVerdict is conjunct 3's acceptance
// criterion for the shape channel, executed rather than asserted.
//
// It is the exact sibling of TestBlankingEveryShapeNoteChangesNoVerdict and of
// the route-level TestDeletingEveryProseReasonChangesNoVerdict: mutate the LIVE
// registry, re-run THE function step 7 calls, require the verdicts to be
// identical. Restore the `case issueRef.MatchString(sh.Issue)` branch to
// shapeVerdict and this fails by name on every row that carries an Issue.
//
// The positive control is not optional and is not the count of issues: it is
// that at least one row must currently be discharged by the field this test
// claims is NOT load-bearing's replacement -- a jurisdiction record -- or the
// mutation is being applied to a registry where nothing was ever at stake.
func TestDeletingEveryShapeIssueChangesNoVerdict(t *testing.T) {
	live := emittedShapes()

	cited, jurisdictional := 0, 0
	for _, sh := range live {
		if sh.Issue != "" {
			cited++
		}
		if shapeVerdict(sh) == "out-of-jurisdiction" {
			jurisdictional++
		}
	}
	if cited == 0 {
		t.Fatal("no shape in the registry carries an Issue, so deleting them all cannot " +
			"demonstrate anything. Issues are citations for a READER and this test is what " +
			"says they are only that; with none present it is an assertion about an empty " +
			"set.")
	}
	if jurisdictional == 0 {
		t.Fatal("no shape is discharged by a jurisdiction record, so every uninspected row " +
			"is either absent or failing and this test's subject does not exist. If the " +
			"registry really has become fully inspected, delete this test and " +
			"shapeJurisdiction with it rather than leaving a guard over nothing.")
	}

	before := shapeVerdicts(live)

	blanked := emittedShapes()
	for i := range blanked {
		blanked[i].Issue = ""
	}
	after := shapeVerdicts(blanked)

	for shape, want := range before {
		if got := after[shape]; got != want {
			t.Errorf("deleting every Issue changed the verdict for %q from %q to %q.\n"+
				"An issue number is a citation for a reader and discharges nothing. A "+
				"verdict that moves when the citations are deleted is a verdict computed "+
				"from an external tracker's identifiers -- which is what shapeVerdict's "+
				"third case used to be, and what made #169 uncloseable.", shape, want, got)
		}
	}
}
