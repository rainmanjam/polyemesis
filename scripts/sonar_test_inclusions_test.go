package main

import (
	"bufio"
	"fmt"
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

// isTestPath reports whether a repo-relative path is test code. A suffix is one
// way; sitting under testdata/ is the other, and it is not a special case --
// internal/api/testdata/faketool/main.go is 117 lines of real Go that the Go
// tool refuses to compile into any coverage profile, because it ignores
// testdata/ entirely. SonarCloud has no such rule, so the file was analysed as
// production source that no test could ever reach. Same class as the two globs
// above: fixture code scored as the thing it is a fixture for.
func isTestPath(rel string) bool {
	if strings.HasPrefix(rel, "testdata/") || strings.Contains(rel, "/testdata/") {
		return true
	}
	for _, suffix := range testFileSuffixes {
		if strings.HasSuffix(rel, suffix) {
			return true
		}
	}
	return false
}

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
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if !isTestPath(rel) {
			return nil
		}
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
		{"**/testdata/**", "internal/api/testdata/faketool/main.go", true, "a fixture binary is test code"},
		{"**/testdata/**", "internal/api/handlers.go", false, "and an ordinary file is not"},
	}
	for _, c := range []struct {
		path string
		want bool
	}{
		{"internal/api/testdata/faketool/main.go", true},
		{"testdata/x.go", true},
		{"internal/db/db_test.go", true},
		{"internal/db/db.go", false},
		{"internal/api/handlers.go", false},
	} {
		if got := isTestPath(c.path); got != c.want {
			t.Errorf("isTestPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	for _, c := range cases {
		if got := matchesGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchesGlob(%q, %q) = %v, want %v -- %s", c.pattern, c.path, got, c.want, c.why)
		}
	}
}

// The second way the coverage plumbing lied, found the same afternoon as the
// first and with the same shape.
//
// web/vitest.config.ts pins coverage.include to an allow-list -- deliberately,
// because the default glob would sweep in every .astro component and report 2%,
// which is the number that makes people stop reading reports. But the list named
// individual files. So when src/scripts/code-copy.ts got its first test, the
// test ran, it passed, and the module still produced no lcov entry.
//
// A source file with no coverage data is not "unmeasured" to SonarCloud. It is
// UNCOVERED: its lines count toward new_lines_to_cover and none of them count as
// covered. Writing the test made the quality gate WORSE, silently, for the second
// time in one afternoon and by a different mechanism.
//
// So this guard is about the class rather than the instance: wherever a vitest
// config pins coverage.include, every module that HAS a sibling test must be
// matched by it. A module somebody bothered to test is a module whose coverage
// has to be reported.

// vitestConfigs are the configs that pin a coverage allow-list, with the
// directory each one's globs are relative to.
var vitestConfigs = []string{"web/vitest.config.ts", "ui/vitest.config.ts"}

// coverageInclude pulls the include: [...] array out of a vitest config's
// coverage block. Returns nil when the config pins no allow-list, which is not
// a finding: the default glob measures everything imported.
func coverageInclude(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := string(raw)
	cov := strings.Index(body, "coverage:")
	if cov < 0 {
		return nil
	}
	inc := strings.Index(body[cov:], "include:")
	if inc < 0 {
		return nil
	}
	rest := body[cov+inc:]
	open := strings.IndexByte(rest, '[')
	close := strings.IndexByte(rest, ']')
	if open < 0 || close < open {
		t.Fatalf("%s: coverage.include is not a bracketed list", path)
	}
	var globs []string
	// Split on commas at brace depth zero. A naive strings.Split cuts
	// "src/**/*.{ts,tsx}" in half and yields two globs that match nothing,
	// which would make this guard report every tested file in the repo -- a
	// check that cries wolf is one people switch off.
	for _, part := range splitTopLevel(rest[open+1 : close]) {
		g := strings.Trim(strings.TrimSpace(part), `"'`)
		if g != "" {
			globs = append(globs, g)
		}
	}
	if len(globs) == 0 {
		t.Fatalf("%s: coverage.include is empty, so nothing is measured at all", path)
	}
	return globs
}

// splitTopLevel splits a comma-separated list, ignoring commas inside braces.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// braceGlob expands one level of {a,b} alternation, which vitest accepts and
// filepath.Match does not, then matches Ant-style.
func braceGlob(pattern, path string) bool {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return matchesGlob(pattern, path)
	}
	close := strings.IndexByte(pattern[open:], '}')
	if close < 0 {
		return matchesGlob(pattern, path)
	}
	close += open
	for _, alt := range strings.Split(pattern[open+1:close], ",") {
		if braceGlob(pattern[:open]+strings.TrimSpace(alt)+pattern[close+1:], path) {
			return true
		}
	}
	return false
}

func TestEveryTestedModuleIsMeasured(t *testing.T) {
	root := repoRoot(t)

	for _, cfg := range vitestConfigs {
		dir := filepath.Dir(filepath.Join(root, cfg))
		globs := coverageInclude(t, filepath.Join(root, cfg))
		if globs == nil {
			continue // no allow-list pinned, so nothing can go stale
		}

		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			// Find the module a test file is the test FOR: foo.test.ts -> foo.ts.
			name := d.Name()
			var stem string
			for _, mid := range []string{".test."} {
				if i := strings.Index(name, mid); i >= 0 {
					stem = name[:i]
				}
			}
			if stem == "" {
				return nil
			}
			ext := filepath.Ext(name)
			module := filepath.Join(filepath.Dir(path), stem+ext)
			if _, serr := os.Stat(module); serr != nil {
				// A test with no same-named sibling tests something else --
				// an .astro page, a whole pipeline. Nothing to measure here.
				return nil
			}
			rel, rerr := filepath.Rel(dir, module)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			for _, g := range globs {
				if braceGlob(g, rel) {
					return nil
				}
			}
			t.Errorf("%s: coverage.include does not match %s, which has a test.\n"+
				"    The test will run and pass, the module will produce no lcov entry, "+
				"and SonarCloud counts a source file with no coverage data as UNCOVERED "+
				"-- so writing that test made new_coverage worse. Widen the glob "+
				"(include is %v).", cfg, rel, globs)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

func TestTheBraceExpanderActuallyDiscriminates(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"src/lib/**/*.{mjs,ts}", "src/lib/hast-mermaid.mjs", true},
		{"src/lib/**/*.{mjs,ts}", "src/lib/features-shots.ts", true},
		{"src/lib/**/*.mjs", "src/lib/features-shots.ts", false},
		{"src/scripts/**/*.ts", "src/scripts/code-copy.ts", true},
		{"src/scripts/mermaid-render.ts", "src/scripts/code-copy.ts", false},
		{"src/**/*.{ts,tsx}", "src/components/signature/AudioMeter.tsx", true},
		{"src/lib/**/*.{mjs,ts}", "src/scripts/code-copy.ts", false},
	}
	// The list splitter is the part that failed first: it cut
	// "src/**/*.{ts,tsx}" at the comma inside the braces and produced two
	// globs that match nothing, so the guard reported every tested file in the
	// repo as unmeasured. A check that cries wolf gets switched off.
	if got := splitTopLevel(`"src/lib/**/*.{mjs,ts}", "src/scripts/**/*.ts"`); len(got) != 2 {
		t.Fatalf("splitTopLevel returned %d globs, want 2: %q", len(got), got)
	}
	for _, c := range cases {
		if got := braceGlob(c.pattern, c.path); got != c.want {
			t.Errorf("braceGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// The fourth way the measurement understated work that had already been done,
// and the only one that is a command-line flag rather than a glob.
//
// `go test ./...` instruments only the package under test, so a function
// exercised solely through ANOTHER package's tests records zero hits. That is
// not a rare shape in this repo -- internal/db is driven almost entirely
// through internal/api's handler tests. db.DeleteAllAPITokens is the case that
// found it: internal/api/password_change_tokens_test.go drives it through the
// password-change handler, which is the only way it is ever called, and the
// profile said 0.0%. With -coverpkg it says 71.4%, and the repo total moves
// 85.6% -> 87.4%.
//
// Dropping the flag would not break anything, fail anything, or look wrong in
// review. It would quietly take back about a point and a half of a gate that
// sits two points above its threshold, and the next person would go looking for
// the missing coverage in the code.
func TestTheGoCoverageProfileIsBuiltAcrossPackages(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github/workflows/sonar.yml"))
	if err != nil {
		t.Fatalf("read sonar.yml: %v", err)
	}
	body := string(raw)

	const step = "Go coverage for the scanner"
	i := strings.Index(body, step)
	if i < 0 {
		t.Fatalf("sonar.yml has no %q step, so nothing produces the Go profile the "+
			"scanner is pointed at and every Go file reads as 0%% covered", step)
	}
	// Just this step, not the file: the point is that THIS command carries the
	// flag, and a mention anywhere else -- a comment, another job -- must not
	// satisfy it.
	end := strings.Index(body[i:], "\n      - name:")
	if end < 0 {
		end = len(body) - i
	}
	block := body[i : i+end]

	if !strings.Contains(block, "go test") || !strings.Contains(block, "-coverprofile=coverage.out") {
		t.Fatalf("the %q step no longer writes coverage.out, which is the path "+
			"sonar.go.coverage.reportPaths names:\n%s", step, block)
	}
	if !strings.Contains(block, "-coverpkg=./...") {
		t.Fatalf("the %q step has lost -coverpkg=./..., so any function exercised "+
			"only through another package's tests will record zero hits and read as "+
			"untested. Worth about 1.5 points of new_coverage, silently.\n%s", step, block)
	}
}

// An informational step that can spend the whole job budget is not
// informational.
//
// "Report what the smoke run executed in cmd/polyemesis" says so three times --
// if: always(), "does not fail the job", "informational only" -- and then hung
// inside `go tool covdata` on windows-latest and took the job to its 35-minute
// ceiling. Twice in one afternoon, on #694 and #696. The runner CANCELS at the
// ceiling, `gh pr checks` renders a cancelled job as a failure naming nothing,
// and the required-check rule refuses the merge. Normal cost of that step on
// the same runner: one to three seconds.
//
// This counts the shape rather than that one step: a step that runs on
// always(), executes a script, and has no timeout-minutes can convert any hang
// into a job-wide cancellation, whatever it says about itself. There is exactly
// one such step today and it is the one being fixed, so this starts life at
// zero and stays there.
func TestNoAlwaysStepCanSpendTheWholeJobBudget(t *testing.T) {
	root := repoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, ".github/workflows/*.yml"))
	if err != nil {
		t.Fatalf("glob workflows: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no workflows found, so this guard is counting nothing")
	}

	var bare []string
	seen := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		lines := strings.Split(string(raw), "\n")
		for i, l := range lines {
			if !strings.HasPrefix(strings.TrimSpace(l), "- name:") {
				continue
			}
			// The step's own block: up to the next `- name:` at the same indent.
			indent := strings.Index(l, "- name:")
			end := len(lines)
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], strings.Repeat(" ", indent)+"- name:") {
					end = j
					break
				}
			}
			block := strings.Join(lines[i:end], "\n")
			if !strings.Contains(block, "always()") || !strings.Contains(block, "run:") {
				continue
			}
			seen++
			if !strings.Contains(block, "timeout-minutes:") {
				bare = append(bare, fmt.Sprintf("  %s:%d  %s",
					filepath.Base(f), i+1, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- name:"))))
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no always() run-steps at all; the block-splitting below has " +
			"stopped matching and this guard is now counting nothing")
	}
	if len(bare) > 0 {
		t.Fatalf("%d always() step(s) run a script with no timeout-minutes:\n%s\n\n"+
			"A step that runs on always() runs after a failure, when the job is "+
			"already short of budget, and an unbounded one converts any hang into a "+
			"CANCELLED job -- which `gh pr checks` shows as a failure naming nothing "+
			"and which the required-check rule then refuses to merge past. Give it a "+
			"timeout generous against its normal cost.", len(bare), strings.Join(bare, "\n"))
	}
}
