package testenv

// THE SONAR JOB WAS GREEN WHETHER OR NOT THE QUALITY GATE PASSED.
//
// The scanner's default is fire-and-forget: it uploads the analysis and exits
// 0. The gate is computed afterwards, on SonarCloud, and its verdict never
// travels back to the run. A pull request whose new code failed
// new_reliability_rating therefore showed a passing `sonar` check, and there is
// nothing in the checks list to notice -- a passing scan and a passing gate
// render identically.
//
// sonar-project.properties now sets `sonar.qualitygate.wait=true`, which makes
// the scanner poll for the verdict and exit non-zero on a failure. This test
// keeps it set, and keeps its poll deadline below the job's own ceiling for the
// reason ci.yml gives everywhere about Go's -timeout: the scanner's timeout
// exits with a message naming the gate, and the job ceiling prints "cancelled"
// and names nothing.
//
// Warning rung, not Control, and the file says so in full: a failing job is not
// a blocked merge until `sonar` is a REQUIRED status context in the branch
// protection rule, which is a repository setting and cannot be committed. This
// test guards the half that lives in the repository.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestSonarWaitsForItsQualityGate(t *testing.T) {
	root := repoRootFromTest(t)
	props := mustReadRepoFile(t, root, "sonar-project.properties")

	if !regexp.MustCompile(`(?m)^sonar\.qualitygate\.wait\s*=\s*true\s*$`).MatchString(props) {
		t.Fatalf("sonar-project.properties does not set `sonar.qualitygate.wait=true`.\n\n" +
			"Without it the scanner uploads the analysis and exits 0, so the `sonar` check " +
			"is green on every pull request whose quality gate is red -- and a passing scan " +
			"looks exactly like a passing gate in the checks list. Removing this line makes " +
			"the job stop gating without making it stop appearing to gate, which is the " +
			"failure this whole audit is about.\n\n" +
			"If it has to come off, say why in the properties file where the next reader of " +
			"a green check will find it.")
	}

	m := regexp.MustCompile(`(?m)^sonar\.qualitygate\.timeout\s*=\s*(\d+)\s*$`).FindStringSubmatch(props)
	if m == nil {
		t.Fatal("sonar-project.properties sets `wait` but no `sonar.qualitygate.timeout`.\n\n" +
			"The scanner's default poll deadline is 300s. Set it explicitly so the " +
			"relationship with the job's own ceiling is a stated one rather than a " +
			"coincidence -- this test checks that relationship and cannot check an implicit " +
			"value.")
	}
	pollSec, _ := strconv.Atoi(m[1])

	// The `sonar` job's own ceiling, read out of the workflow.
	wf := mustReadRepoFile(t, root, ".github", "workflows", "sonar.yml")
	start := strings.Index(wf, "\n  sonar:\n")
	if start < 0 {
		t.Fatal("no `sonar:` job in .github/workflows/sonar.yml. It has been renamed and this " +
			"test is now comparing the poll deadline against nothing -- repoint it.")
	}
	cm := regexp.MustCompile(`(?m)^    timeout-minutes:\s*(\d+)\s*$`).FindStringSubmatch(wf[start:])
	if cm == nil {
		t.Fatal("the `sonar` job has no timeout-minutes of its own")
	}
	jobMin, _ := strconv.Atoi(cm[1])
	jobSec := jobMin * 60

	// Headroom for the steps BEFORE the scan -- checkout, Go coverage, two npm
	// coverage runs -- plus the scanner's own upload. The job does real work
	// before it starts waiting, exactly like ci.yml's go job does before
	// `go test` starts, and that is what made the old 25-vs-20 comparison in
	// ci.yml wrong for as long as it existed.
	const preScanAllowanceSec = 8 * 60

	if pollSec+preScanAllowanceSec >= jobSec {
		t.Errorf("the quality-gate poll can outlive the job that runs it.\n\n"+
			"  sonar.qualitygate.timeout   %4ds\n"+
			"  steps before the scan       %4ds  (checkout, Go coverage, ui and web coverage)\n"+
			"                              ----\n"+
			"  needs                       %4ds\n"+
			"  sonar job timeout-minutes   %4ds\n\n"+
			"Below this, the JOB deadline fires first and the run reports a bare "+
			"\"cancelled\" -- which `gh pr checks` prints as `fail`, indistinguishable from a "+
			"real gate failure. Raise the job's timeout-minutes, or lower the poll.",
			pollSec, preScanAllowanceSec, pollSec+preScanAllowanceSec, jobSec)
	}
}
