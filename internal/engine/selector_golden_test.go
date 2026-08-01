package engine

import (
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
// so its input space is 1024 wide: 4 current x 4 pinned x 2^6 flags. That is
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
// moved", not "the selector is correct". And 1024 is exhaustive over the fields
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
func allSourceChoices() []sourceChoice {
	kinds := []sourceKind{sourceNone, sourcePrimary, sourceBackup, sourceSlate}
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
										now:           goldenNow,
										cur:           cur,
										pinned:        pinned,
										primary:       p,
										backup:        b,
										backupEnabled: backupEnabled,
										slateEnabled:  slateEnabled,
										grace:         goldenGrace,
										autoReturn:    autoReturn,
										returnStable:  goldenReturnStable,
									})
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
	const want = 4 * 4 * 2 * 2 * 2 * 2 * 2 * 2
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
	// "1024 of 1024 rows changed" -- every row differing only by an invisible
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

	// Name the rows that moved rather than dumping 1024 lines. The diff is the
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
