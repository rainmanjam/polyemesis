package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE DOCS-ONLY CI PATH RUNS THE DRIFT GUARDS, AND THIS KEEPS IT ABLE TO.
//
// ci.yml's `go build, vet, test` job gates every Go step on `code == 'true'`,
// so a pull request touching only documentation used to run nothing -- the one
// class of change that can drift the docs was the class the drift guards never
// saw (#651). The docs-only branch now discovers the packages whose tests read
// `docs/*.md` and runs them with a `-run` pattern.
//
// Two things about that step can rot without anyone noticing, and both fail
// SILENTLY -- towards green:
//
//   - the discovery grep stops matching (a test moves, the path shape changes)
//   - a guard is renamed out of the `-run` pattern
//
// `go test -run` matching nothing exits 0. The step counts what actually ran
// and refuses a low count, which covers the wholesale case. This covers the
// retail one: the specific guards we know about must still be reachable by the
// pattern the workflow actually uses. Warning rung -- CI announces it; Control
// would mean generating the pattern, which is a worse trade for two regexes.

// canonicalDriftTests are guards that MUST run on a docs-only pull request.
// Each reads a document and compares it to code. Adding one here is how a new
// guard gets protected; removing one means saying why in the same commit.
var canonicalDriftTests = []string{
	"TestPlatformsDocMatrixMatchesTheCapabilityMatrix",
	"TestEveryRouteIsInTheAPIDocument",
	"TestTheAPIDocumentDescribesNoRouteThatIsGone",
	"TestEveryCapabilityCellAgreesWithTheCodeThatImplementsIt",
	"TestThePagesHeadersRestateEverySecurityHeaderTheNginxConfigDeclares",
}

func TestTheDocsOnlyPathStillRunsTheDriftGuards(t *testing.T) {
	root := repoRootFromTest(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("reading ci.yml: %v", err)
	}
	ci := string(raw)

	// The step itself. If it is gone, the docs-only path is a no-op again and
	// every assertion below would pass by describing nothing.
	if !strings.Contains(ci, "Documentation-only change, so only the doc-drift guards ran") {
		t.Fatal("ci.yml no longer has the docs-only doc-drift step.\n" +
			"If that was deliberate, delete this test in the same commit -- otherwise " +
			"a documentation-only pull request runs no Go test at all, which is #651.")
	}

	pattern := extractRunPattern(t, ci)
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("the -run pattern in ci.yml does not compile as a regexp: %q: %v", pattern, err)
	}

	for _, name := range canonicalDriftTests {
		if !re.MatchString(name) {
			t.Errorf("ci.yml's -run pattern %q does not match %s.\n"+
				"That guard would stop running on documentation-only pull requests, "+
				"silently. Widen the pattern, or rename the test back into it.",
				pattern, name)
		}
	}
}

func TestTheCanonicalDriftGuardsStillExist(t *testing.T) {
	// An entry naming a test that has been deleted or renamed is an entry that
	// protects nothing, and it would keep the assertion above passing.
	root := repoRootFromTest(t)
	found := map[string]bool{}

	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(b)
		for _, name := range canonicalDriftTests {
			if strings.Contains(src, "func "+name+"(") {
				found[name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}

	for _, name := range canonicalDriftTests {
		if !found[name] {
			t.Errorf("canonicalDriftTests names %s, which no longer exists.\n"+
				"Update the list in the same commit as the rename, or it covers nothing.", name)
		}
	}
}

// extractRunPattern pulls the `-run '...'` argument out of the docs-only step.
func extractRunPattern(t *testing.T, ci string) string {
	t.Helper()
	re := regexp.MustCompile(`go test \$\{pkgs\} -run '([^']+)'`)
	m := re.FindStringSubmatch(ci)
	if m == nil {
		t.Fatal("could not find the `go test ${pkgs} -run '...'` line in ci.yml's docs-only step.\n" +
			"If it was reworded, update this test in the same commit -- it cannot check a " +
			"pattern it can no longer read, and would otherwise pass by finding nothing.")
	}
	return m[1]
}
