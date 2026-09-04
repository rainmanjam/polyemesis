package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sonar.test.inclusions decides which files SonarCloud measures as tests rather
// than as production source. A test file the list misses is not merely
// uncounted: it is analysed as source with no coverage, so it counts AGAINST
// new_coverage. Adding one test file under web/src/scripts took a pull
// request's new_coverage to 4.5% and turned the gate red, because the
// TypeScript patterns were anchored to ui/ and the docs site lives in web/.
//
// That is the worst failure mode a coverage gate can have. It punished the
// exact action it exists to encourage, and it did so silently -- the gate said
// "new_coverage is 4.5, and the gate requires lt 80", which is true, and names
// nothing an author would connect to a properties file they did not touch.
//
// The device is this test rather than a comment, because the property is a
// glob list nobody re-reads and the failure surfaces two CI jobs and one code
// review later. It is a Warning-rung poka-yoke: the mistake stays possible --
// nothing stops somebody writing a narrower glob -- but it is announced here,
// in seconds, naming the file, instead of arriving as an unexplained
// percentage on somebody else's pull request. Control would mean deriving the
// property from the tree at scan time, which the scanner has no hook for.

// testFileSuffixes are the endings that make a file a test in this repo. A new
// language belongs in both this list and sonar.test.inclusions.
var testFileSuffixes = []string{"_test.go", ".test.ts", ".test.tsx"}

// skipDirs are trees SonarCloud never sees, so a test file inside one is not
// evidence of a hole in the property.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	".claude": true, "coverage": true, "vendor": true, "test-results": true,
	"playwright-report": true, ".next": true, ".svelte-kit": true,
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// This package sits one level down, in scripts/.
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// readInclusions returns sonar.test.inclusions as a slice of globs.
func readInclusions(t *testing.T, root string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "sonar-project.properties"))
	if err != nil {
		t.Fatalf("open sonar-project.properties: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "sonar.test.inclusions=") {
			continue
		}
		return strings.Split(strings.TrimPrefix(line, "sonar.test.inclusions="), ",")
	}
	t.Fatal("sonar-project.properties declares no sonar.test.inclusions, so every " +
		"test file in the repo is being measured as production source")
	return nil
}

// matchesGlob applies one SonarCloud path pattern to a repo-relative path.
// Sonar's syntax is Ant-like: ** spans directories, * stays inside one.
func matchesGlob(pattern, path string) bool {
	return globParts(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func globParts(pat, seg []string) bool {
	switch {
	case len(pat) == 0:
		return len(seg) == 0
	case pat[0] == "**":
		// ** matches zero or more path segments, so try every split point.
		for i := 0; i <= len(seg); i++ {
			if globParts(pat[1:], seg[i:]) {
				return true
			}
		}
		return false
	case len(seg) == 0:
		return false
	default:
		ok, err := filepath.Match(pat[0], seg[0])
		return err == nil && ok && globParts(pat[1:], seg[1:])
	}
}

func TestEveryTestFileIsDeclaredAsATest(t *testing.T) {
	root := repoRoot(t)
	globs := readInclusions(t, root)

	var missed []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		isTest := false
		for _, suffix := range testFileSuffixes {
			if strings.HasSuffix(d.Name(), suffix) {
				isTest = true
				break
			}
		}
		if !isTest {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		for _, g := range globs {
			if matchesGlob(strings.TrimSpace(g), rel) {
				return nil
			}
		}
		missed = append(missed, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(missed) > 0 {
		t.Fatalf("sonar.test.inclusions does not match %d test file(s):\n  %s\n\n"+
			"SonarCloud will analyse each of these as production source with no "+
			"coverage, so they count AGAINST new_coverage and the gate gets angrier "+
			"the more tests are written. Widen the pattern in "+
			"sonar-project.properties -- anchor it to the extension rather than to "+
			"a directory, the way **/*_test.go already is.",
			len(missed), strings.Join(missed, "\n  "))
	}
}

// The guard above is only as good as its matcher, and a matcher that said yes
// to everything would pass forever while checking nothing. These are its
// positive controls.
func TestTheInclusionMatcherActuallyDiscriminates(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
		why           string
	}{
		{"**/*_test.go", "internal/db/db_test.go", true, "** spans directories"},
		{"**/*_test.go", "db_test.go", true, "** also matches zero directories"},
		{"**/*_test.go", "internal/db/db.go", false, "a non-test file is not a test"},
		{"**/*.test.ts", "web/src/scripts/code-copy.test.ts", true, "the case that was missed"},
		{"ui/**/*.test.ts", "web/src/scripts/code-copy.test.ts", false, "the pattern that missed it"},
		{"ui/**/*.test.ts", "ui/src/lib/api.test.ts", true, "and still matched what it was written for"},
		{"ui/e2e/**", "ui/e2e/smoke.spec.ts", true, "a trailing ** matches below it"},
		{"scripts/**", "internal/scripts/x_test.go", false, "a leading segment is anchored"},
		{"*.test.ts", "web/src/x.test.ts", false, "a single * does not span directories"},
	}
	for _, c := range cases {
		if got := matchesGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchesGlob(%q, %q) = %v, want %v -- %s", c.pattern, c.path, got, c.want, c.why)
		}
	}
}
