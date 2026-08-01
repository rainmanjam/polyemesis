package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The golden table is the safety net for generalising the selector, and it
// exists before any of that work rather than alongside it.
//
// chooseSource is pure and every field it branches on is an enum or a boolean,
// so its input space is 3200 wide: 5 current x 5 pinned x 2^7 flags. That is
// not a number that needs sampling -- it is a number you enumerate. So "does
// the new implementation behave like the old one" stops being a question about
// 25 hand-written cases and becomes a question that can be ANSWERED, on every
// input the function can see.
//
// It freezes the reason strings as well as the verdicts, deliberately. They
// reach an operator through Failover.Reason, so rewording one is a
// user-visible change and ought to show up in review as a diff across many
// rows rather than slipping through as a string edit.
//
// Regenerate with: go test ./internal/engine/ -run Golden -update
//
// WHAT THIS DOES NOT PROVE. It freezes current behaviour INCLUDING any current
// bug. That is the point -- a refactor and a bug fix in one commit are
// indistinguishable in a bisect -- but "the golden test passes" means "nothing
// moved", not "the selector is correct". And 3200 is exhaustive over the fields
// the function branches on, not over reality: liveness carries timestamps that
// this collapses to alive/not-alive and stable/not-stable, so an off-by-one at
// exactly returnStable is outside the net. TestChooseSourceSwitchesOnDelivery-
// NotOnProcessState covers those boundaries and must be kept, not replaced.
var updateGolden = flag.Bool("update", false, "rewrite the selector golden file")

// goldenNow is fixed so the table is reproducible. Every duration below is
// relative to it.
var goldenNow = time.Unix(1_700_000_000, 0)

const (
	goldenGrace        = 5 * time.Second
	goldenReturnStable = 30 * time.Second
)

// allSourceChoices enumerates every input chooseSource can distinguish.
//
// Ordered, not map-iterated, so the golden file diffs cleanly: a reordering
// would otherwise read as a behaviour change and bury the one row that moved.
// sourcePlayout is appended LAST to kinds, and playoutRunning is nested
// between slateEnabled and autoReturn, so that filtering the enumeration down
// to "the playlist is not in play" yields the pre-playout 1024 in their
// original order -- which is what makes the byte-for-byte comparison in
// TestAdmittingThePlaylistMovedNoDecisionThatDidNotInvolveIt possible at all.
func allSourceChoices() []sourceChoice {
	kinds := []sourceKind{sourceNone, sourcePrimary, sourceBackup, sourceSlate, sourcePlayout}
	bools := []bool{false, true}

	// Two livenesses per source: one delivering inside the grace window, one
	// that has never delivered at all. `alive` is the only thing chooseSource
	// asks of the backup, so two values exhaust it.
	dead := liveness{}
	live := func(stable bool) liveness {
		// A run longer than returnStable, or shorter than it. This is the
		// second bit the primary contributes -- autoReturn consults
		// stableFor(now) >= returnStable and nothing else does.
		run := goldenReturnStable / 2
		if stable {
			run = goldenReturnStable * 2
		}
		return liveness{rx: 1, at: goldenNow.Add(-time.Second), since: goldenNow.Add(-run)}
	}

	var out []sourceChoice
	for _, cur := range kinds {
		for _, pinned := range kinds {
			for _, primaryLive := range bools {
				for _, stable := range bools {
					for _, backupLive := range bools {
						for _, backupEnabled := range bools {
							for _, slateEnabled := range bools {
								// The playlist contributes exactly one bit. It
								// plays out of a file, so unlike an ingest it
								// has no liveness history to enumerate: running
								// or not is the whole of what the decision can
								// ask about it.
								for _, playoutRunning := range bools {
									for _, autoReturn := range bools {
										p := dead
										if primaryLive {
											p = live(stable)
										}
										b := dead
										if backupLive {
											b = live(false)
										}
										out = append(out, sourceChoice{
											now:            goldenNow,
											cur:            cur,
											pinned:         pinned,
											primary:        p,
											backup:         b,
											backupEnabled:  backupEnabled,
											slateEnabled:   slateEnabled,
											playoutRunning: playoutRunning,
											grace:          goldenGrace,
											autoReturn:     autoReturn,
											returnStable:   goldenReturnStable,
										})
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

// goldenRow renders one input and its answer as a single stable line.
func goldenRow(c sourceChoice) string {
	kind, reason := chooseSource(c)
	return fmt.Sprintf(
		"cur=%-7s pinned=%-7s primaryLive=%-5t stable=%-5t backupLive=%-5t backupEnabled=%-5t slateEnabled=%-5t playoutRunning=%-5t autoReturn=%-5t => %-7s %q",
		orNone(c.cur), orNone(c.pinned),
		c.primary.alive(c.now, c.grace),
		c.primary.stableFor(c.now) >= c.returnStable,
		c.backup.alive(c.now, c.grace),
		c.backupEnabled, c.slateEnabled, c.playoutRunning, c.autoReturn,
		orNone(kind), reason,
	)
}

// goldenRowBeforeThePlaylist renders a row in the EXACT format the table used
// before sourcePlayout existed -- no playoutRunning column.
//
// It is not dead weight and it is not a duplicate of goldenRow. Adding a field
// to goldenRow's format string rewrites the text of all 1024 pre-existing lines,
// so the golden diff for this change shows 100% of rows moved and proves
// nothing at all: the one question worth answering -- did admitting a fourth
// candidate change a decision that has nothing to do with it -- is invisible in
// it. Rendering the old format for the rows where the playlist is out of play
// asks that question directly and answers it in bytes.
func goldenRowBeforeThePlaylist(c sourceChoice) string {
	kind, reason := chooseSource(c)
	return fmt.Sprintf(
		"cur=%-7s pinned=%-7s primaryLive=%-5t stable=%-5t backupLive=%-5t backupEnabled=%-5t slateEnabled=%-5t autoReturn=%-5t => %-7s %q",
		orNone(c.cur), orNone(c.pinned),
		c.primary.alive(c.now, c.grace),
		c.primary.stableFor(c.now) >= c.returnStable,
		c.backup.alive(c.now, c.grace),
		c.backupEnabled, c.slateEnabled, c.autoReturn,
		orNone(kind), reason,
	)
}

func orNone(k sourceKind) string {
	if k == sourceNone {
		return "none"
	}
	return string(k)
}

func TestChooseSourceGoldenIsExhaustive(t *testing.T) {
	choices := allSourceChoices()

	// The count is asserted rather than trusted. A harness that silently
	// enumerated 900 would be a safety net with a hole in the middle, and the
	// hole would be invisible: the file would still diff cleanly.
	//
	// 3200 = 5 cur x 5 pinned x 2^7 flags. Admitting the playlist did NOT
	// quadruple 1024: it added one value to each of the two kind-valued fields
	// (4->5, twice) and one boolean (playoutRunning), so the growth is
	// (5/4)^2 x 2 = 3.125x. Written as the product it is derived from rather
	// than as a literal, because "4096" is what this looks like to arithmetic
	// done from memory, and a wrong constant here fails as a count mismatch
	// that reads like a broken enumeration.
	const want = 5 * 5 * 2 * 2 * 2 * 2 * 2 * 2 * 2
	if len(choices) != want {
		t.Fatalf("enumerated %d inputs, want %d -- the net has a hole in it", len(choices), want)
	}

	lines := make([]string, 0, len(choices))
	for _, c := range choices {
		lines = append(lines, goldenRow(c))
	}
	got := strings.Join(lines, "\n") + "\n"

	path := filepath.Join("testdata", "selector_golden.txt")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d rows)", path, len(lines))
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v\nRegenerate with: go test ./internal/engine/ -run Golden -update", path, err)
	}

	// Normalise CRLF before comparing, because this table is compared byte for
	// byte and Windows checks text files out with CRLF unless told otherwise.
	//
	// Without this the test fails on windows-latest and nowhere else, reporting
	// "3200 of 3200 rows changed" -- every row differing only by an invisible
	// \r. The diff it prints to explain itself comes out BLANK, because the
	// carriage return sends the cursor back over the line it just wrote. So the
	// failure names the loudest possible cause, a total rewrite of the selector,
	// and shows no evidence for it.
	//
	// .gitattributes pins this file to LF, which is the real fix. This line is
	// the belt to that pair of braces: it keeps the test honest in a clone made
	// before .gitattributes existed, where the working copy is already CRLF.
	prev := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if prev == got {
		return
	}

	// Name the rows that moved rather than dumping 3200 lines. The diff is the
	// review artifact for any change to this function, so it has to be readable.
	oldLines := strings.Split(strings.TrimRight(prev, "\n"), "\n")
	moved := 0
	for i := range lines {
		if i >= len(oldLines) {
			t.Errorf("row %d is new: %s", i, lines[i])
			moved++
			continue
		}
		if oldLines[i] != lines[i] {
			moved++
			if moved <= 12 {
				t.Errorf("row %d moved:\n  was: %s\n  now: %s", i, oldLines[i], lines[i])
			}
		}
	}
	if len(oldLines) > len(lines) {
		t.Errorf("%d rows disappeared", len(oldLines)-len(lines))
	}
	if moved > 12 {
		t.Errorf("... and %d further rows moved", moved-12)
	}
	t.Errorf("%d of %d rows changed. If that was deliberate, regenerate with "+
		"-update and review the diff row by row; every changed row is a moment "+
		"an operator would have to explain.", moved, len(lines))
}

// goldenNoPlayoutFile is the decision table EXACTLY as it stood before
// sourcePlayout existed: 1024 rows, the pre-playout format, generated by the
// commit before the playlist was admitted.
//
// It has no -update path, on purpose. Every other golden file in this package
// is regenerated when behaviour moves deliberately; this one is a historical
// record, and a record you can rewrite when it contradicts you is not one. If a
// future change genuinely has to move a decision frozen here, deleting and
// re-cutting this file by hand is the ceremony that change deserves.
const goldenNoPlayoutFile = "selector_golden_no_playout.txt"

// goldenNoPlayoutSHA256 is what binds that file to history, and without it the
// test below is load-bearing under the failure it targets but VACUOUS under the
// likeliest accident.
//
// "No -update path" is a convention, and the convention lives in this file
// rather than in the data. Nothing stops somebody whose invariance test is
// failing from re-cutting the frozen table with a one-line script -- the same
// reflex that regenerating the main golden table correctly rewards -- at which
// point the comparison is the change against itself, it passes green, and the
// proof that Task 4 was additive has quietly become a proof of nothing.
//
// A hash checked in source cannot be re-cut by regenerating the file. It has to
// be edited deliberately, in a different file, by somebody who has read why it
// is here.
//
// The bytes it covers are `git show <task-4-parent>:internal/engine/testdata/
// selector_golden.txt` -- the decision table exactly as it stood before
// sourcePlayout existed. The file carries no header line saying so, and that
// was checked rather than assumed: the comparison below is positional, line i
// of the file against line i of the rendered subset, so any header shifts every
// row and fails the test. Byte-identity with the table main actually shipped is
// worth more than a comment inside it, and this hash is the warning the file
// itself cannot carry.
const goldenNoPlayoutSHA256 = "4e06849e2c0d912861061fea52f0e926f321b4f38b1802db2988a2eefaf6845c"

// TestAdmittingThePlaylistMovedNoDecisionThatDidNotInvolveIt is the real review
// of Task 4, and it exists because the review the plan asked for cannot be done.
//
// "Review the golden diff row by row" assumes the diff carries signal. It does
// not: goldenRow renders every field into one line, so ADDING the playoutRunning
// column rewrites the text of all 1024 pre-existing rows whether or not a single
// decision moved. git diff reports 100% changed, a human reviewer confirms that
// 100% of rows look plausible, and a regression hiding among them is reviewed
// past rather than caught.
//
// So the claim is made directly instead. For every one of the 1024 inputs that
// existed before -- the playlist not running, and neither cur nor pinned set to
// it -- the decision must be BYTE-IDENTICAL to what the selector decided before
// the fourth candidate was admitted, reason string included. That is what
// "additive" means, and it is checkable rather than eyeballable.
//
// A failure here is not a golden file to regenerate. It means admitting the
// playlist changed a decision that has nothing to do with a playlist, which is
// precisely the regression the candidate-list refactor was built to make
// impossible, and it wants explaining before anything is regenerated.
func TestAdmittingThePlaylistMovedNoDecisionThatDidNotInvolveIt(t *testing.T) {
	var lines []string
	for _, c := range allSourceChoices() {
		// The pre-playout subset: the playlist is not running, and it is
		// neither what is on air nor what is pinned. Filtering an inner loop
		// and the tail of `kinds` preserves the original enumeration order, so
		// this can be compared to the old file line for line rather than as a
		// set -- see the comment on allSourceChoices.
		if c.playoutRunning || c.cur == sourcePlayout || c.pinned == sourcePlayout {
			continue
		}
		lines = append(lines, goldenRowBeforeThePlaylist(c))
	}

	// Asserted, because a filter that accidentally excluded half the table
	// would make this test pass by having nothing left to disagree about.
	const want = 4 * 4 * 2 * 2 * 2 * 2 * 2 * 2
	if len(lines) != want {
		t.Fatalf("the pre-playout subset is %d rows, want %d -- the filter is wrong, "+
			"and a smaller subset proves proportionally less", len(lines), want)
	}

	path := filepath.Join("testdata", goldenNoPlayoutFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v -- this file is a historical record and is "+
			"not regenerated; restore it from history rather than rewriting it", path, err)
	}
	// Checked BEFORE the rows are compared, so a re-cut file fails as "you
	// rewrote the evidence" rather than as a clean pass. Hashed over the bytes
	// on disk with the same CRLF normalisation the comparison uses, or the
	// check would fail on Windows for a file nobody touched.
	if sum := sha256.Sum256([]byte(strings.ReplaceAll(string(raw), "\r\n", "\n"))); hex.EncodeToString(sum[:]) != goldenNoPlayoutSHA256 {
		t.Fatalf("%s is not the table it is supposed to be.\n  on disk: %s\n  expected: %s\n\n"+
			"This file is the pre-playout decision table, frozen so that admitting a fourth "+
			"candidate can be PROVED additive. It is not a golden file to regenerate: re-cutting "+
			"it from the current code makes this test compare the change against itself and pass "+
			"green, which is worse than no test at all. If you got here by regenerating it, "+
			"restore it (git checkout -- %s) and read the failure it was reporting.",
			path, hex.EncodeToString(sum[:]), goldenNoPlayoutSHA256, path)
	}
	// Same CRLF normalisation as the main table, for the same reason.
	oldLines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n")
	if len(oldLines) != len(lines) {
		t.Fatalf("%s has %d rows, the pre-playout subset has %d", path, len(oldLines), len(lines))
	}

	moved := 0
	for i := range lines {
		if oldLines[i] == lines[i] {
			continue
		}
		moved++
		if moved <= 12 {
			t.Errorf("row %d moved without the playlist being involved:\n  was: %s\n  now: %s",
				i, oldLines[i], lines[i])
		}
	}
	if moved > 12 {
		t.Errorf("... and %d further rows moved", moved-12)
	}
	if moved > 0 {
		t.Errorf("%d of %d decisions that predate the playlist changed. Admitting a "+
			"candidate that is not running must change nothing; do not regenerate "+
			"anything until each of these is explained.", moved, len(lines))
	}
}
