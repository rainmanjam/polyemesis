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

// AND WHEN IT FAILS, IT HAS TO SAY WHICH CONDITION.
//
// `sonar.qualitygate.wait=true` bought a red job. It did not buy a reason: the
// scanner's whole failure message is
//
//	ERROR QUALITY GATE STATUS: FAILED - View details on https://sonarcloud.io/...
//
// a verdict and a link. main's gate was red on four consecutive analyses and
// nobody reading those logs could name the metric, because the only place it is
// rendered is a browser -- an unauthenticated read of the gate API returns
// status NONE for a branch, which is indistinguishable from a gate that was
// never computed. A guard that fires without saying what it caught is most of
// the way back to no guard at all: what people do with it is re-run and hope.
//
// So sonar.yml reads the verdict back with the token it already holds and
// prints every condition, passing ones included -- and then FAILS on a verdict
// that is not OK, which the first version did not: ::error annotations do not
// fail a step, so it printed `Quality gate: ERROR` and exited 0.
//
// A correction to what this comment used to say. It claimed continue-on-error
// here could turn a red job green. It could not -- the scan step has already
// failed by then and no later step un-fails a job. The assertion is still worth
// having, for the narrower reason that a broken reporter should fail visibly.
func TestSonarNamesTheConditionThatFailed(t *testing.T) {
	root := repoRootFromTest(t)
	wf := mustReadRepoFile(t, root, ".github/workflows/sonar.yml")

	step := strings.Index(wf, "- name: Which quality gate conditions were evaluated")
	if step < 0 {
		t.Fatal("sonar.yml has no step that reads the quality gate verdict back.\n\n" +
			"Without it a failure prints `QUALITY GATE STATUS: FAILED` and a URL, and the " +
			"log cannot answer which metric, what it measured, or what the threshold was. " +
			"That is a gate people re-run rather than act on.")
	}
	body := wf[step:]
	if end := strings.Index(body, "\n      - name:"); end > 0 {
		body = body[:end]
	}
	// The assertions below must see the SCRIPT, not the prose around it. This
	// file is heavily commented, and every string checked for here appears in
	// those comments -- so matching the whole step would let someone delete the
	// reporting and keep the test green by leaving the explanation behind.
	script := body
	if r := strings.Index(script, "\n        run: |"); r >= 0 {
		script = script[r:]
	}
	var code strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if t := strings.TrimSpace(line); !strings.HasPrefix(t, "#") {
			code.WriteString(line)
			code.WriteString("\n")
		}
	}
	run := code.String()

	// It must run on the failing path.
	if !strings.Contains(body, "!cancelled()") && !strings.Contains(body, "failure()") {
		t.Fatalf("the gate-reporting step does not run when the scan fails:\n\n%s\n\n"+
			"A step that explains failures has to execute on the failing path.", body)
	}

	// It must not be able to mask its own failure -- from the outside...
	if strings.Contains(body, "continue-on-error") {
		t.Fatalf("the gate-reporting step is continue-on-error:\n\n%s\n\n"+
			"A step whose only job is to explain a failure must not be able to hide "+
			"that it could not do so.", body)
	}
	// ...or from the inside. `exit 0` on the error paths is the same masking by
	// another route, and the version this replaced had three of them: an invalid
	// token spun for 150s, blamed a slow analysis, and passed.
	if n := strings.Count(run, "exit 0"); n > 1 {
		t.Fatalf("the gate-reporting script has %d `exit 0` paths:\n\n%s\n\n"+
			"Exactly one is legitimate -- a scan that died before submitting an "+
			"analysis, where the scan step is already red and there is no verdict in "+
			"existence. Every other early return is a gate left unread and reported "+
			"as success.", n, run)
	}

	// The point of the step is the numbers.
	for _, want := range []string{"actualValue", "errorThreshold", "metricKey"} {
		if !strings.Contains(run, want) {
			t.Fatalf("the gate-reporting script never reads %s:\n\n%s\n\n"+
				"Naming the metric without its measurement and its threshold leaves the "+
				"reader in the same place the scanner left them.", want, run)
		}
	}

	// And having read them, it has to act on them. Annotations do not fail a step.
	if !strings.Contains(run, `!= "OK"`) || !strings.Contains(run, "exit 1") {
		t.Fatalf("the gate-reporting script never fails on a bad verdict:\n\n%s\n\n"+
			"::error annotations do not fail a step. Without an explicit exit it "+
			"prints `Quality gate: ERROR` and returns 0 -- a reporter for a gate, "+
			"that does not gate.", run)
	}

	// Every HTTP call has to fail loudly and be bounded. Without -f an HTTP error
	// is a zero exit and a body jq mis-parses; without --max-time a hung
	// connection eats the job ceiling and reports "cancelled", naming nothing.
	for _, want := range []string{"-sSf", "--max-time"} {
		if !strings.Contains(run, want) {
			t.Fatalf("the gate-reporting script's curl lacks %s:\n\n%s", want, run)
		}
	}

	// The endpoint is a constant. Reading it from .scannerwork/report-task.txt
	// let a pull request that edits sonar.host.url redirect this job's live
	// SONAR_TOKEN to a host of its choosing.
	if strings.Contains(run, "serverUrl") {
		t.Fatalf("the gate-reporting script reads its endpoint out of the scanner "+
			"report:\n\n%s\n\nThat file is written from sonar.host.url, which a pull "+
			"request can set. The Authorization header carries a live token; the host "+
			"must be a literal.", run)
	}
	if !strings.Contains(body, "SONAR_API: https://sonarcloud.io") {
		t.Fatalf("the gate-reporting step does not pin its API host:\n\n%s", body)
	}
}
