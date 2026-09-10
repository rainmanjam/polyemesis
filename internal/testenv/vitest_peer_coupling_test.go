package testenv

// DEPENDABOT CANNOT FILE A COUPLED BUMP, AND NOTHING SAID THESE WERE COUPLED.
//
// Three PRs were opened to move to vitest 5 -- #760 (vitest, ui), #762
// (@vitest/coverage-v8, ui) and #764 (vitest, web) -- and not one of them
// could ever have gone green, individually or all three together:
//
//   - @vitest/coverage-v8 declares a HARD peer on the exact vitest version
//     ("5.0.0" wants "5.0.0"). Bumping either alone is an unresolvable tree,
//     so #760 and #762 could only ever pass as one commit. Dependabot files
//     one PR per package and has no way to know that.
//   - web declares @vitest/coverage-v8 too, and NO PR was ever opened for it.
//     #764 was unmergeable for a reason not visible anywhere in #764.
//   - vitest 5 requires @types/node "^22 || >=24"; ui declared ^20.19.43 while
//     CI has run Node 24 for months. The types described a runtime the project
//     stopped using, which is a defect on its own -- type-checking against a
//     standard library that is two majors from the one in production.
//
// The shape is the one lockfile_coverage_test.go is aimed at, arriving again:
// each file was correct when written, nothing recorded the relationship
// between them, and there was no artefact that went red when it broke. The
// evidence was three PRs sitting red for weeks with no single failure
// explaining why.
//
// Control rung for the pairing: the versions cannot drift, because a
// disagreement fails here. Warning rung for @types/node: this asserts the
// major matches what CI runs, which is checkable; whether the TYPES are right
// is not.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type nodePkg struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (p nodePkg) declared(name string) (string, bool) {
	if v, ok := p.DevDependencies[name]; ok {
		return v, true
	}
	v, ok := p.Dependencies[name]
	return v, ok
}

func readPkg(t *testing.T, path string) nodePkg {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var p nodePkg
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return p
}

// majorOf reads the leading number out of a range like "^5.0.0" or ">=24".
func majorOf(t *testing.T, spec string) int {
	t.Helper()
	m := regexp.MustCompile(`(\d+)`).FindStringSubmatch(spec)
	if m == nil {
		t.Fatalf("no version number in %q", spec)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("unreadable major in %q: %v", spec, err)
	}
	return n
}

// The workspaces that have a package.json of their own.
var jsWorkspaces = []string{"ui", "web"}

func TestVitestAndItsCoveragePluginAreBumpedTogether(t *testing.T) {
	root := repoRootFromTest(t)

	checked := 0
	for _, ws := range jsWorkspaces {
		p := readPkg(t, filepath.Join(root, ws, "package.json"))
		vitest, hasVitest := p.declared("vitest")
		cov, hasCov := p.declared("@vitest/coverage-v8")

		if !hasVitest {
			if hasCov {
				t.Errorf("%s declares @vitest/coverage-v8 (%s) and no vitest. The plugin "+
					"peers on an exact vitest version, so on its own it can only ever "+
					"resolve by accident.", ws, cov)
			}
			continue
		}
		checked++

		if !hasCov {
			// Not an error: a workspace may legitimately run tests without a
			// coverage report. Said out loud so it is a decision, not a gap.
			t.Logf("%s runs vitest with no coverage plugin; nothing to keep in step", ws)
			continue
		}

		if a, b := majorOf(t, vitest), majorOf(t, cov); a != b {
			t.Errorf("%s has vitest %s and @vitest/coverage-v8 %s.\n"+
				"@vitest/coverage-v8 declares a HARD peer on the exact vitest version, so "+
				"these two cannot be installed at different majors -- `npm ci` fails with "+
				"ERESOLVE and every job in the workspace goes red at once.\n"+
				"Dependabot files one PR per package and cannot know they are coupled: it "+
				"opened #760 and #762 separately and neither could pass. Bump both in the "+
				"same commit.", ws, vitest, cov)
		}
	}

	// POSITIVE CONTROL. A renamed workspace, a moved package.json, or a typo in
	// jsWorkspaces leaves the loop above comparing nothing, and a loop that
	// compares nothing agrees with itself.
	if checked == 0 {
		t.Fatal("no workspace was found declaring vitest. jsWorkspaces is wrong or a " +
			"package.json has moved, so this test is asserting nothing about the coupling " +
			"it exists to protect.")
	}
}

func TestTypesNodeMatchesTheNodeCIRunsOn(t *testing.T) {
	root := repoRootFromTest(t)

	// The Node major every workflow pins. They must agree with each other
	// first, or "the version CI runs" is not a single answer.
	pinned := map[int][]string{}
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read workflows: %v", err)
	}
	re := regexp.MustCompile(`node-version:\s*'?"?(\d+)`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			n, _ := strconv.Atoi(m[1])
			pinned[n] = append(pinned[n], e.Name())
		}
	}

	// POSITIVE CONTROL, before any comparison: no pins found means the regex or
	// the directory is wrong, and every assertion below would pass over an
	// empty map.
	if len(pinned) == 0 {
		t.Fatal("no `node-version:` pin was found in any workflow. The walk is broken, " +
			"so this test cannot be said to have checked anything.")
	}
	if len(pinned) > 1 {
		t.Fatalf("workflows pin more than one Node major: %v. There is no single version "+
			"for the types to describe until they agree.", pinned)
	}
	var ci int
	for n := range pinned {
		ci = n
	}

	checked := 0
	for _, ws := range jsWorkspaces {
		p := readPkg(t, filepath.Join(root, ws, "package.json"))
		spec, ok := p.declared("@types/node")
		if !ok {
			// Resolved transitively. Nothing declared it, so nothing here has
			// asserted a version that could be wrong.
			continue
		}
		checked++
		if got := majorOf(t, spec); got != ci {
			t.Errorf("%s declares @types/node %s and CI runs Node %d.\n"+
				"The types then describe a standard library the project does not run on: "+
				"code type-checks against APIs production may not have, and against "+
				"signatures that have since changed. It also blocks upgrades for reasons "+
				"that look unrelated -- vitest 5 requires @types/node ^22 || >=24, and ui "+
				"sat on ^20 while CI had run Node 24 for months, so the vitest bump failed "+
				"with an ERESOLVE naming neither vitest nor Node.",
				ws, spec, ci)
		}
	}

	if checked == 0 {
		t.Fatal("no workspace declares @types/node, so this test compared nothing. If that " +
			"is now true on purpose, delete it rather than leaving it green over nothing.")
	}
}
