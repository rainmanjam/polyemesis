package testenv

// A COMMAND NAME IS NOT AN INVOCATION, and `make check`'s parity guard had been
// certifying one as the other.
//
// scripts/check_parity_test.go compares `make check` against ci.yml by matching
// command names: its rule for the Go suite is the regular expression
// `\bgo test\b`. The Makefile said `go test ./...`; CI says
// `POLYEMESIS_LEDGER=strict go test -race -timeout 20m ./...`. Both contain the
// substring, so the guard was green, and its own failure text told anybody who
// tripped it that "CI runs it with -race under POLYEMESIS_LEDGER=strict" -- a
// true sentence about a property the guard did not check.
//
// What that cost, concretely, is two whole classes of test that ran in CI and
// on no developer machine:
//
//   - the ledger's counterpart proofs and the LiveTools shape inspectors, which
//     only execute under POLYEMESIS_LEDGER=strict;
//   - every data race in the tree, because nothing local passed -race.
//
// This is the same species of defect as the one the ledger itself exists to
// catch -- a claim discharged against a NAME rather than against what actually
// ran -- which is why the fix is to compare the real thing.
//
// AND IT COMPARED ONE JOB. check_parity_test.go reads ci.yml's `ui` job only,
// by a documented decision ("deliberately one job"). Since that decision the
// `go` job grew `make coverage-instrument-guard`, the probe for #217, and
// nothing reachable from `check` ran it. A parity guard scoped to the job that
// had gone wrong twice is a guard that cannot see the job going wrong now.
//
// TWO TESTS, because they fail for different reasons and a reader should not
// have to disentangle them:
//
//	TestMakeCheckRunsCIsRealGoTestInvocation   flags and environment
//	TestMakeCheckReachesEveryGateTheGoJobRuns  the go job's step list
//
// Control rung for the first (`make check` cannot now run a weaker command than
// CI's without the guard going red), Detection for the second (a new CI step is
// found on the next run, not prevented).

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	// The CI job the second test compares. The `ui` job is already covered by
	// scripts/check_parity_test.go; this is the half that was not.
	invocationCIJob = "go"
	// The local gate both tests compare against.
	// check-ci, not check. `check` is deliberately the FAST loop -- plain
	// `go test ./...` -- because a twenty-five minute pre-commit target is one
	// nobody runs, and a gate people route around is rung zero. `check-ci` is
	// the target that claims parity, so it is the one this compares. If the
	// split is ever undone, repoint this rather than deleting it.
	invocationGateTarget = "check-ci"
)

// ---------------------------------------------------------------- invocation

// TestMakeCheckRunsCIsRealGoTestInvocation reads CI's whole-tree `go test`
// command -- environment assignments and flags, not just the words `go test` --
// and requires something reachable from `make check` to run the same thing.
func TestMakeCheckRunsCIsRealGoTestInvocation(t *testing.T) {
	root := repoRootFromTest(t)

	ciSteps := invocationCIJobRunSteps(t, root, invocationCIJob)
	ciCmd := invocationFindGoTest(ciSteps)
	if ciCmd == "" {
		t.Fatalf("no whole-tree `go test ... ./...` step in ci.yml's `%s` job.\n"+
			"That step is the reason this test exists, so its absence means the job was "+
			"restructured and this guard is now comparing nothing. Repoint it; do not "+
			"delete it.", invocationCIJob)
	}

	reached, recipes := invocationReachableFromCheck(t, root)
	makeCmd := invocationFindGoTest(recipes)
	if makeCmd == "" {
		t.Fatalf("nothing reachable from `make %s` runs a whole-tree `go test ./...`.\n"+
			"  `%s` reaches: %s\n\nCI runs:\n    %s",
			invocationGateTarget, invocationGateTarget, strings.Join(reached, " "), ciCmd)
	}

	wantEnv, wantFlags := invocationParts(ciCmd)
	gotEnv, gotFlags := invocationParts(makeCmd)

	var missing []string
	for _, e := range wantEnv {
		if !invocationHas(gotEnv, e) {
			missing = append(missing, e)
		}
	}
	for _, f := range wantFlags {
		if !invocationHas(gotFlags, f) {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return
	}

	// The costs are spelled per token, because "parity check failed" tells the
	// person who tripped this nothing about whether to care.
	costs := map[string]string{
		"POLYEMESIS_LEDGER=strict": "internal/api's route-coverage ledger runs its counterpart proofs, and the " +
			"LiveTools shape inspectors with them, ONLY under this variable. Without it those " +
			"tests exist in the tree and execute nowhere but CI.",
		"-race": "the engine reconciles from several goroutines; a data race here is a stream " +
			"that stops for one viewer. Nothing local looks for one if this is missing.",
	}
	var detail strings.Builder
	for _, m := range missing {
		why := costs[m]
		if why == "" {
			if strings.HasPrefix(m, "-timeout") {
				why = "CI's per-run deadline. A local run without it uses Go's default of ten " +
					"minutes PER PACKAGE, so a package that would fail CI's wall can pass here."
			} else {
				why = "CI gates on it and nothing local does."
			}
		}
		fmt.Fprintf(&detail, "\n  %-26s %s\n", m, why)
	}

	t.Errorf("`make %s` runs a WEAKER `go test` than CI does.\n\n"+
		"  CI:    %s\n  local: %s\n\nMissing:\n%s\n"+
		"A parity guard that matches the substring `go test` calls these two the same "+
		"command, which is how the strict ledger and the race detector came to run in CI "+
		"and on no developer machine. Make the local invocation match, or -- if a flag "+
		"genuinely does not belong in a pre-commit gate -- say so here, in this file, "+
		"with the reason, so the omission is something a reviewer can argue with.\n\n"+
		"  `%s` currently reaches: %s",
		invocationGateTarget, ciCmd, makeCmd, detail.String(),
		invocationGateTarget, strings.Join(reached, " "))
}

// invocationFindGoTest returns the first whole-tree `go test ... ./...` command
// found in the given command texts, with its leading environment assignments.
func invocationFindGoTest(cmds []string) string {
	re := regexp.MustCompile(`(?m)^\s*((?:[A-Z][A-Z0-9_]*=\S+\s+)*go test\b[^\n|;&]*\./\.\.\.)`)
	for _, c := range cmds {
		if m := re.FindStringSubmatch(c); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// invocationParts splits a command into its leading NAME=VALUE assignments and
// its flags. Flag VALUES are kept attached (`-timeout 20m` is one token), so a
// local run at a longer deadline than CI's is a difference this can see.
func invocationParts(cmd string) (env, flags []string) {
	fields := strings.Fields(cmd)
	i := 0
	for ; i < len(fields) && strings.Contains(fields[i], "=") && !strings.HasPrefix(fields[i], "-"); i++ {
		env = append(env, fields[i])
	}
	for ; i < len(fields); i++ {
		f := fields[i]
		if !strings.HasPrefix(f, "-") {
			continue
		}
		// `-timeout 20m` -- a flag whose value is the next field.
		if !strings.Contains(f, "=") && i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "-") && !strings.HasPrefix(fields[i+1], "./") {
			f += " " + fields[i+1]
			i++
		}
		flags = append(flags, f)
	}
	return env, flags
}

func invocationHas(have []string, want string) bool {
	for _, h := range have {
		if h == want {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ the job

// invocationGoJobGate is a command CI's `go` job runs that `make check` has to
// be able to run too. `re` matches the CI step; `local` matches the Makefile
// recipe line that discharges it, and the two differ because CI says
// `make preflight-guard` while the Makefile says the target's body.
type invocationGoJobGate struct {
	name  string
	re    *regexp.Regexp
	local *regexp.Regexp
	costs string
}

var invocationGoJobGates = []invocationGoJobGate{
	{
		name:  "gofmt",
		re:    regexp.MustCompile(`\bgofmt\b`),
		local: regexp.MustCompile(`\bgofmt\b`),
		costs: "an unformatted file fails CI's first step and costs the whole job.",
	},
	{
		name:  "go vet",
		re:    regexp.MustCompile(`(?m)^\s*go vet\b`),
		local: regexp.MustCompile(`(?m)^\s*go vet\b`),
		costs: "vet catches the cheapest real bugs this project has.",
	},
	{
		name:  "GOOS=windows go vet",
		re:    regexp.MustCompile(`GOOS=windows go vet\b`),
		local: regexp.MustCompile(`GOOS=windows go vet\b`),
		costs: "internal/supervisor has _windows.go sources that no build on Linux or macOS " +
			"compiles at all. A vet failure in one is invisible locally and costs the whole " +
			"CI job. It is seconds: vet type-checks, it does not build.",
	},
	{
		name:  "make preflight-guard",
		re:    regexp.MustCompile(`\bmake preflight-guard\b`),
		local: regexp.MustCompile(`\bgo test -count=1 \./internal/api\b`),
		costs: "the proof that internal/api's forced route-coverage preflight still survives " +
			"-run, -skip and -count=0. Without it a deleted TestMain is a silent hole.",
	},
	{
		name:  "make coverage-instrument-guard",
		re:    regexp.MustCompile(`\bmake coverage-instrument-guard\b`),
		local: regexp.MustCompile(`\bcoverage-instrument-guard\.sh\b`),
		costs: "the probe for #217, where `go test -cover ./internal/api` reported 22.0% for " +
			"zero tests, one test and the whole suite alike. Reordering the forced preflight " +
			"is a one-line change and reverting it is a one-line change; only this notices. " +
			"~60s. THIS IS THE ONE THE `ui`-only parity guard could not see.",
	},
	{
		name:  "make sh-syntax",
		re:    regexp.MustCompile(`\bmake sh-syntax\b`),
		local: regexp.MustCompile(`\bbash -n\b`),
		costs: "`bash -n` over every scripts/*.sh, including the 25 that run only in the " +
			"acceptance matrix, on dispatch, or nowhere at all. Without it a syntax error in " +
			"an unrun script is found by whoever next runs it by hand.",
	},
	{
		name:  "go test",
		re:    regexp.MustCompile(`\bgo test\b.*\./\.\.\.`),
		local: regexp.MustCompile(`\bgo test\b.*\./\.\.\.`),
		costs: "the whole Go suite. TestMakeCheckRunsCIsRealGoTestInvocation above compares " +
			"the flags and the environment as well, which is the part that was missing.",
	},
}

// THE ONE PLACE a step CI's `go` job runs is allowed to be absent from
// `make check`. Same discipline as scripts/check_parity_test.go: excluding
// something is an act somebody performs and a reviewer sees.
var invocationGoJobExclusions = []struct {
	re     *regexp.Regexp
	reason string
}{
	{
		re: regexp.MustCompile(`(?m)^\s*go build \./\.\.\.\s*$`),
		reason: "`go test ./...` compiles every package in the tree before it runs anything, " +
			"so a build failure fails `check` already. CI runs it separately because a " +
			"compile error should not be reported as a test failure.",
	},
	{
		re: regexp.MustCompile(`\./scripts/(test-lib-observe|test-lib-watchdog|test-sbom-guard|test-release-gates|termination-guard|test-termination-guard|test-obs-stop|test-lib-cleanup)\.sh`),
		reason: "the shell harnesses. Each starts and kills processes -- test-lib-cleanup.sh " +
			"needs lsof installed and kills by port -- and running them from a pre-commit " +
			"gate on a shared developer machine kills whatever else that machine is doing. " +
			"They are also the steps in this job with real step timeouts, for that reason.",
	},
	{
		re: regexp.MustCompile(`\./scripts/acceptance-hooks\.sh`),
		reason: "binds a loopback port and drives a real http.Server for ~20s. Same argument " +
			"as `check-browser`: the gate you run before every commit must not need a port " +
			"to be free.",
	},
	{
		re:     regexp.MustCompile(`apt-get install`),
		reason: "runner provisioning. There is nothing for a local gate to mirror.",
	},
	{
		re: regexp.MustCompile(`::notice title=go build, vet, test|doc-drift packages`),
		reason: "the documentation-only path. It runs when the code paths did NOT change, so " +
			"it is the complement of everything above rather than a gate `check` skips.",
	},
	{
		re:     regexp.MustCompile(`ffmpeg -version|\bffmpeg\b.*cache|^\s*sudo\b`),
		reason: "FFmpeg provisioning for the suites below. Setup, not a gate.",
	},
}

// TestMakeCheckReachesEveryGateTheGoJobRuns is the second half of the parity
// claim: not only must `check` run CI's commands, CI must run nothing `check`
// has no opinion about.
func TestMakeCheckReachesEveryGateTheGoJobRuns(t *testing.T) {
	root := repoRootFromTest(t)
	reached, recipes := invocationReachableFromCheck(t, root)
	local := strings.Join(recipes, "\n")

	steps := invocationCIJobRunSteps(t, root, invocationCIJob)
	if len(steps) < 10 {
		t.Fatalf("found only %d `run:` steps in ci.yml's `%s` job, which is fewer than it has "+
			"ever had. The job has been renamed or restructured and this test is comparing "+
			"almost nothing -- repoint it rather than deleting it.", len(steps), invocationCIJob)
	}

next:
	for _, step := range steps {
		for _, x := range invocationGoJobExclusions {
			if x.re.MatchString(step) {
				continue next
			}
		}
		matched := false
		for _, g := range invocationGoJobGates {
			if !g.re.MatchString(step) {
				continue
			}
			matched = true
			if !g.local.MatchString(local) {
				t.Errorf("ci.yml's `%s` job runs %q, which gates on %s, and nothing reachable "+
					"from `make %s` runs it.\n\n  What that costs: %s\n\n  `%s` reaches: %s",
					invocationCIJob, strings.TrimSpace(step), g.name, invocationGateTarget,
					g.costs, invocationGateTarget, strings.Join(reached, " "))
			}
		}
		if matched {
			continue
		}
		t.Errorf("ci.yml's `%s` job has grown a step this repository has no opinion about:\n\n"+
			"    %s\n\nThat is exactly how `make coverage-instrument-guard` came to gate every "+
			"pull request while no local target ran it: the parity guard in scripts/ reads the "+
			"`ui` job only, so a step appearing HERE was invisible to it.\n\nDecide, out loud, "+
			"which it is:\n  - a gate -> add it to invocationGoJobGates in this file AND make "+
			"`make %s` run it;\n  - not a gate -> add it to invocationGoJobExclusions with the "+
			"reason.",
			invocationCIJob, strings.TrimSpace(step), invocationGateTarget)
	}
}

// ---------------------------------------------------------------- the parsing

var (
	invocationAssignRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*[:?+]?=\s*(.*)$`)
	invocationRuleRe   = regexp.MustCompile(`^([^\s#][^:]*):([^=].*)?$`)
	invocationVarRe    = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)
	invocationSubMake  = regexp.MustCompile(`\$\(MAKE\)\s+([A-Za-z0-9_./-]+)`)
)

// invocationReachableFromCheck returns every target reachable from `check` and
// the recipe lines of those targets. Recipe COMMENTS are dropped: several
// targets here carry long `@#` explanations naming the very commands they run,
// and a guard that accepted a mention would pass on a recipe whose body had been
// deleted and whose comment had not.
func invocationReachableFromCheck(t *testing.T, root string) (reached, recipes []string) {
	t.Helper()

	path := filepath.Join(root, "Makefile")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")

	vars := map[string]string{}
	for _, ln := range lines {
		if strings.HasPrefix(ln, "\t") || strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		if m := invocationAssignRe.FindStringSubmatch(ln); m != nil {
			vars[m[1]] = m[2]
		}
	}
	expand := func(s string) string {
		for i := 0; i < 5 && strings.Contains(s, "$("); i++ {
			s = invocationVarRe.ReplaceAllStringFunc(s, func(ref string) string {
				name := invocationVarRe.FindStringSubmatch(ref)[1]
				if v, ok := vars[name]; ok {
					return v
				}
				return ref
			})
		}
		return s
	}

	prereqs := map[string][]string{}
	bodies := map[string][]string{}
	var current []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "\t") {
			body := strings.TrimPrefix(ln, "\t")
			if strings.HasPrefix(body, "@#") || strings.HasPrefix(body, "#") {
				continue
			}
			for _, tgt := range current {
				bodies[tgt] = append(bodies[tgt], body)
			}
			continue
		}
		if strings.TrimSpace(ln) == "" {
			continue
		}
		current = nil
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		m := invocationRuleRe.FindStringSubmatch(ln)
		if m == nil || strings.HasPrefix(m[1], ".PHONY") {
			continue
		}
		rest := m[2]
		if i := strings.Index(rest, "##"); i >= 0 {
			rest = rest[:i]
		}
		for _, tgt := range strings.Fields(expand(m[1])) {
			current = append(current, tgt)
			prereqs[tgt] = append(prereqs[tgt], strings.Fields(expand(rest))...)
		}
	}

	if _, ok := prereqs[invocationGateTarget]; !ok {
		t.Fatalf("no `%s:` rule found in %s. Either the local gate has been renamed or this "+
			"test's Makefile parsing has stopped working -- and in both cases it is now "+
			"comparing nothing at all. Repoint it; do not delete it.", invocationGateTarget, path)
	}

	seen := map[string]bool{}
	queue := []string{invocationGateTarget}
	for len(queue) > 0 {
		tgt := queue[0]
		queue = queue[1:]
		if seen[tgt] {
			continue
		}
		seen[tgt] = true
		queue = append(queue, prereqs[tgt]...)
		for _, body := range bodies[tgt] {
			for _, sm := range invocationSubMake.FindAllStringSubmatch(body, -1) {
				queue = append(queue, sm[1])
			}
		}
	}
	for tgt := range seen {
		reached = append(reached, tgt)
		recipes = append(recipes, bodies[tgt]...)
	}
	sort.Strings(reached)

	if len(reached) < 4 {
		t.Fatalf("`%s` reaches only %v. That is far short of what it has ever gated, so the "+
			"parsing above has broken and this test would pass against a Makefile with no "+
			"gate in it at all.", invocationGateTarget, reached)
	}
	return reached, recipes
}

// invocationCIJobRunSteps returns the command text of every `run:` step in one
// job of ci.yml, scalar and block form alike.
func invocationCIJobRunSteps(t *testing.T, root, job string) []string {
	t.Helper()

	path := filepath.Join(root, ".github", "workflows", "ci.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")

	start := -1
	for i, ln := range lines {
		if strings.TrimRight(ln, " ") == "  "+job+":" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("no `%s:` job in %s. The job has been renamed and this test is now comparing "+
			"`make %s` against nothing -- repoint it.", job, path, invocationGateTarget)
	}

	indentOf := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

	var steps []string
	for i := start; i < len(lines); i++ {
		ln := lines[i]
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if indentOf(ln) <= 2 { // the next job
			break
		}
		trimmed := strings.TrimSpace(ln)
		body := strings.TrimPrefix(trimmed, "- ")
		if !strings.HasPrefix(body, "run:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(body, "run:"))
		if value == "" {
			continue // a `defaults:`-style mapping, not a step
		}
		if value != "|" && !strings.HasPrefix(value, "|") && !strings.HasPrefix(value, ">") {
			steps = append(steps, value)
			continue
		}
		keyIndent := indentOf(ln) + strings.Index(trimmed, "run:")
		var block []string
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			if indentOf(lines[j]) <= keyIndent {
				break
			}
			block = append(block, strings.TrimSpace(lines[j]))
		}
		steps = append(steps, strings.Join(block, "\n"))
	}
	return steps
}
