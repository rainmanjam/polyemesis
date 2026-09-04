package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// THE JOB CEILING HAS TO CLEAR THE TEST DEADLINE PLUS EVERYTHING BEFORE IT.
//
// ci.yml runs `go test -race -timeout 20m` inside a job with its own
// `timeout-minutes`. Which of the two fires first decides what an operator gets
// out of a hang: Go's deadline prints a goroutine dump naming the stuck test,
// GitHub's prints "cancelled" and nothing else -- and `gh pr checks` renders
// that as `fail`, indistinguishable at a glance from a real regression.
//
// The old value compared 25 against 20 and read as five minutes of headroom.
// It was not, because `go test` does not start when the job does: gofmt, build,
// vet, the Windows vet, the route-coverage preflight and the internal/api
// coverage run first and take six to seven minutes. The test step therefore had
// ~18.5 minutes against its own 20-minute deadline, so the job ceiling always
// won, the diagnostic could never be produced, and the comment asserting
// otherwise had been false for as long as the preflights had existed. Three
// runs on 2026-09-01 died at 25m16s, 25m17s and 25m20s.
//
// The arithmetic is the part worth pinning, not the number. A future preflight
// pushes the test's start later and silently eats the same margin again, and
// nothing about adding a coverage step suggests reading a timeout on a
// different line. So this asserts the relationship instead.
//
// Warning rung, not Control: CI announces the mistake, it cannot prevent it.
// Control would mean generating the ceiling from the steps, which YAML cannot
// do and a generator would cost more than it saves for two numbers.

// preflightAllowanceMin is what the steps before `go test` are allowed to take.
// Measured at six to seven minutes; the extra covers a slow runner rather than
// a new step, which should raise the ceiling instead.
const preflightAllowanceMin = 7

// runnerHeadroomMin is the gap left after Go's own deadline expires, so the
// runner has time to act on it and upload what it produced. A dump nobody
// collects is the same as no dump.
const runnerHeadroomMin = 5

func TestGoJobCeilingClearsItsTestDeadline(t *testing.T) {
	root := repoRootFromTest(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("reading ci.yml: %v", err)
	}
	src := string(raw)

	// The step this is all about. Anchored on POLYEMESIS_LEDGER=strict because
	// that is the whole-tree race run; other `go test` lines in this file are
	// narrower and live under their own jobs.
	deadline := regexp.MustCompile(`POLYEMESIS_LEDGER=strict go test -race -timeout (\d+)m`)
	dm := deadline.FindStringSubmatch(src)
	if dm == nil {
		t.Fatal("could not find the strict race `go test -race -timeout Nm` line in ci.yml.\n" +
			"If that step was renamed or its flags reordered, update this test in the same " +
			"commit -- it cannot check a relationship it can no longer read, and it would " +
			"otherwise pass by finding nothing.")
	}
	testTimeout, _ := strconv.Atoi(dm[1])

	// The job's own ceiling. Taken from the `go build, vet, test` job, which is
	// the one that runs the line above -- ci.yml has several timeout-minutes.
	jobStart := strings.Index(src, "name: go build, vet, test")
	if jobStart < 0 {
		t.Fatal("could not find the `go build, vet, test` job in ci.yml")
	}
	ceiling := regexp.MustCompile(`(?m)^\s*timeout-minutes: (\d+)`)
	cm := ceiling.FindStringSubmatch(src[jobStart:])
	if cm == nil {
		t.Fatal("the `go build, vet, test` job has no timeout-minutes")
	}
	jobTimeout, _ := strconv.Atoi(cm[1])

	want := testTimeout + preflightAllowanceMin + runnerHeadroomMin
	if jobTimeout < want {
		t.Errorf(
			"ci.yml: the go job's timeout-minutes is %d, but it needs at least %d.\n"+
				"  go test -timeout      %2dm\n"+
				"  steps before it       %2dm  (gofmt, build, vet, windows vet, two coverage preflights)\n"+
				"  runner headroom       %2dm\n"+
				"                        ---\n"+
				"  minimum ceiling       %2dm\n\n"+
				"Below this, the JOB deadline fires before Go's and the run reports a bare "+
				"\"cancelled\" with no goroutine dump -- which `gh pr checks` prints as `fail`, "+
				"so a timeout and a real regression look identical. Raise timeout-minutes, or "+
				"cut the work before the test step. Do not raise `go test -timeout` to close "+
				"the gap: the comment on that line asks for the package to be split first.",
			jobTimeout, want, testTimeout, preflightAllowanceMin, runnerHeadroomMin, want)
	}
}

// repoRootFromTest walks up until it finds go.mod. Hardcoding "../.." breaks
// the moment this file moves.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
