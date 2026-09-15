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

// seriesOf reads "major.minor" out of a range like "^19.3.0" or ">=24".
//
// MAJOR ALONE IS NOT ENOUGH, and the mutation test is what proved it. The
// vitest pair that motivated this file drifted across a major -- 5 against 4 --
// so a major comparison caught it. react 19.3 against react-dom 19.2 is the
// same defect one digit to the right, and #808 is exactly that: both are major
// 19, npm installs them happily, and `tsc -b` fails on types that name neither
// package. A guard written from one example measures what that example
// happened to break.
func seriesOf(t *testing.T, spec string) string {
	t.Helper()
	m := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(spec)
	if m != nil {
		return m[1] + "." + m[2]
	}
	// A range with no minor ("^19", ">=24") pins only the major, and two such
	// ranges agree whenever their majors do.
	return strconv.Itoa(majorOf(t, spec)) + ".*"
}

// wildcardAgrees lets a range that pins only a major ("^19") sit beside one
// that pins a minor ("^19.3.0") without being called a mismatch.
func wildcardAgrees(x, y string) bool {
	xs, ys := strings.SplitN(x, ".", 2), strings.SplitN(y, ".", 2)
	if xs[0] != ys[0] {
		return false
	}
	return xs[1] == "*" || ys[1] == "*"
}

// PACKAGES THAT MUST MOVE TOGETHER, and the reason each pair is coupled.
//
// This started as one hardcoded pair because one pair had just cost a day.
// react/react-dom is the second instance in the same week -- dependabot opened
// #808 moving react to 19.3.0 and #806 moving react-dom to 19.3.0, each
// leaving the other behind, and neither could pass alone. A guard written for
// one instance of a recurring shape is a guard that watches one door.
//
// A pair belongs here when the two packages cannot be installed at different
// majors, OR when they can but the result does not type-check or run. The
// second kind is the one dependabot cannot see: npm resolves react 19.3
// against react-dom 19.2 happily, and the failure surfaces as a typecheck
// error naming neither.
var coupledPairs = []struct {
	a, b string
	why  string
}{
	{
		"vitest", "@vitest/coverage-v8",
		"the plugin declares a HARD peer on the exact vitest version, so at " +
			"different majors `npm ci` fails with ERESOLVE and every job in the " +
			"workspace goes red at once",
	},
	{
		"react", "react-dom",
		"react-dom renders react's element types; npm installs a mismatched pair " +
			"without complaint and `tsc -b` then fails on types that name neither " +
			"package. #808 and #806 each moved one half and neither could pass",
	},
	{
		"@types/react", "@types/react-dom",
		"the DOM types extend the core ones, so a split pair produces type errors " +
			"in application code that has not changed",
	},
}

func TestCoupledPackagesAreBumpedTogether(t *testing.T) {
	root := repoRootFromTest(t)

	checked := 0
	for _, ws := range jsWorkspaces {
		p := readPkg(t, filepath.Join(root, ws, "package.json"))

		for _, pair := range coupledPairs {
			av, hasA := p.declared(pair.a)
			bv, hasB := p.declared(pair.b)

			switch {
			case !hasA && !hasB:
				// This workspace uses neither. Nothing to keep in step.
				continue

			case hasA != hasB:
				// One without the other. Sometimes legitimate -- a workspace may
				// run vitest with no coverage plugin -- so it is said out loud
				// rather than failed, and the pair that IS a hard peer fails on
				// the version comparison below instead.
				t.Logf("%s declares %s but not %s; nothing to keep in step", ws, pair.a, pair.b)
				continue
			}

			checked++
			if x, y := seriesOf(t, av), seriesOf(t, bv); x != y && !wildcardAgrees(x, y) {
				t.Errorf("%s has %s %s and %s %s, which are not the same release series.\n%s.\n"+
					"Dependabot files one PR per package and cannot know they are "+
					"coupled, so a split pair arrives as two PRs that each look "+
					"reasonable and neither of which can merge. Bump both in the "+
					"same commit.", ws, pair.a, av, pair.b, bv, pair.why)
			}
		}
	}

	// POSITIVE CONTROL. A renamed workspace, a moved package.json, or a typo in
	// a pair name leaves the loop comparing nothing, and a loop that compares
	// nothing agrees with itself.
	if checked == 0 {
		t.Fatal("no coupled pair was compared in any workspace. jsWorkspaces is " +
			"wrong, a package.json has moved, or every name in coupledPairs is " +
			"misspelled -- so this test is asserting nothing about the coupling it " +
			"exists to protect.")
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
