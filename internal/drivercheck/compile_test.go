package drivercheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

/*
TestEveryAcceptanceDriverStillCompiles.

THE GAP THIS CLOSES, found by breaking it. Every acceptance driver in scripts/
carries `//go:build ignore`, because they are programs run with `go run` rather
than packages of this module. That tag also makes them invisible to
`go build ./...` and `go vet ./...` -- so a change to an internal API compiles
clean, passes the whole unit suite, passes vet, and silently breaks the
drivers.

It is not hypothetical. Changing automod.NewModel to take a spend budget (#502)
left three call sites in acceptance_automod_driver.go behind, and nothing local
said so. The first sign was `acceptance-automod` in CI reporting 0 passed, 12
failed with every field blank -- a run where the driver never started, which
reads exactly like a broken product rather than a broken build.

That is the worst shape a failure can have here: these suites exist to say
whether the product works, so a driver that cannot compile produces a
confident, detailed, entirely false report about the thing they are guarding.

COMPILED IN THE GROUPS THE SHELL SCRIPTS ACTUALLY USE, read from the scripts
rather than listed here. Several drivers share helper files and only compile
alongside them, so a per-file loop reports six false failures; and a hand-kept
list in this file would be one more thing to forget when a driver is added.
*/
func TestEveryAcceptanceDriverStillCompiles(t *testing.T) {
	root := "../.."
	groups := driverGroups(t, root)
	if len(groups) < 12 {
		// A pattern that stops matching would leave this test passing while
		// checking nothing, which is the same silence it exists to break.
		// A floor above what is here today. The first version of this check used
		// 5 and sailed past a pattern that was missing a third of the drivers.
		t.Fatalf("only %d driver groups found in scripts/*.sh; the patterns this "+
			"test reads have probably stopped matching", len(groups))
	}

	for _, g := range groups {
		t.Run(strings.TrimSuffix(filepath.Base(g[0]), ".go"), func(t *testing.T) {
			args := append([]string{"build", "-o", os.DevNull}, g...)
			if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
				t.Fatalf("this driver no longer compiles, so the suite that runs it "+
					"would report a failing product rather than a failing build:\n%s", out)
			}
		})
	}
}

// driverGroups reads every `go run "$SCRIPTS/x.go" "$SCRIPTS/y.go"` invocation
// out of the acceptance shell scripts and returns the file lists, resolved.
func driverGroups(t *testing.T, root string) [][]string {
	t.Helper()
	shells, err := filepath.Glob(filepath.Join(root, "scripts", "*.sh"))
	if err != nil || len(shells) == 0 {
		t.Fatalf("no acceptance scripts found under %s/scripts: %v", root, err)
	}
	// TWO SHAPES, and missing the second is how the first version of this test
	// passed while omitting the exact driver that motivated it. Some suites
	// name their files inline at the `go run`; others assign DRIVER= once at
	// the top and run "$DRIVER" later. acceptance-automod.sh is the second
	// kind, so a pattern that only read `go run` lines checked ten drivers,
	// reported success, and left the broken one out of the list.
	run := regexp.MustCompile(`go run ((?:"\$(?:SCRIPTS|ROOT)[^"]*\.go" ?)+)`)
	assign := regexp.MustCompile(`(?m)^\s*DRIVER=("\$(?:SCRIPTS|ROOT)[^"]*\.go")`)
	file := regexp.MustCompile(`"\$(?:SCRIPTS|ROOT)((?:/scripts)?/[^"]*\.go)"`)

	seen := map[string]bool{}
	var groups [][]string
	for _, sh := range shells {
		body, err := os.ReadFile(sh)
		if err != nil {
			t.Fatalf("read %s: %v", sh, err)
		}
		matches := run.FindAllStringSubmatch(string(body), -1)
		// A DRIVER= assignment is a one-file group; suites that build a binary
		// into $WORK first are compiled by their own script and skipped by the
		// $SCRIPTS/$ROOT anchor in the pattern.
		matches = append(matches, assign.FindAllStringSubmatch(string(body), -1)...)
		for _, m := range matches {
			var files []string
			for _, f := range file.FindAllStringSubmatch(m[1], -1) {
				rel := strings.TrimPrefix(f[1], "/scripts")
				files = append(files, filepath.Join(root, "scripts", rel))
			}
			key := strings.Join(files, " ")
			if len(files) == 0 || seen[key] {
				continue
			}
			seen[key] = true
			groups = append(groups, files)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}
