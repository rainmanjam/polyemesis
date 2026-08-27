package main

// `make check` is the gate you run before you commit, and twice now it has been
// a gate in name only.
//
// The first time, its description said "everything CI would run" while it
// omitted the lint and the gofmt gate that CI fails on -- so CONTRIBUTING.md
// and the PR template both told contributors to run something that could not
// tell them what CI was about to. Somebody fixed that and wrote a comment
// explaining it.
//
// The second time, the comment was still there and still true about the two
// targets it named, and CI had meanwhile grown `npm test` and `npm run build`.
// 657 vitest tests and the production bundle -- the step that catches a type
// error `tsc --noEmit` does not, because `npm run build` runs `tsc -b` for real
// and then bundles -- were gated in CI and nowhere local, for months, and
// nobody noticed, because there is nothing to notice: a `check` that omits a
// gate is byte-identical in its output to one that does not.
//
// A NAME IS NOT A DEVICE, and neither is a comment. This is the device.
//
// WHY IT DOES NOT DIFF THE TWO FILES. The obvious version of this test compares
// ci.yml against the Makefile generically and goes red every time somebody
// edits a cache key or reorders a step. A guard that cries wolf is deleted, or
// worse, ignored while still green-adjacent -- which is how you end up with a
// gate that everybody believes in and nobody has read. So the comparison is
// deliberately narrow: an explicit, named list of the commands this project
// considers check-worthy, and nothing else. Adding a command to that list is a
// decision somebody makes on purpose. So is excluding one.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	parityMakefile = "../Makefile"
	parityCI       = "../.github/workflows/ci.yml"
	// The local gate. Everything reachable from it, transitively, is what a
	// contributor gets when they type `make check`.
	parityGateTarget = "check"
	// The CI job whose steps are compared back against that reachable set.
	// Deliberately one job: it is the one that has grown steps twice, and the
	// one whose every step is cheap enough to belong in a pre-commit gate.
	parityCIJob = "ui"
)

// THE GATE COMMANDS. If CI fails a commit over one of these, `make check` has
// to be able to fail the same commit before it is pushed.
//
// `costs` is not decoration. The failure below quotes it, because "parity check
// failed" tells whoever trips this nothing about whether to care.
type gateCommand struct {
	name  string
	re    *regexp.Regexp
	costs string
}

var gateCommands = []gateCommand{
	{
		name: "gofmt",
		re:   regexp.MustCompile(`\bgofmt\b`),
		costs: "an unformatted file fails CI's `gofmt` step, which runs before anything else " +
			"in the go job, so a stray space costs you the entire job and a round trip.",
	},
	{
		name: "go vet",
		re:   regexp.MustCompile(`\bgo vet\b`),
		costs: "vet catches the cheapest real bugs this project has, and CI runs it for the " +
			"Windows build too.",
	},
	{
		name: "go test",
		re:   regexp.MustCompile(`\bgo test\b`),
		costs: "the whole Go suite. CI runs it with -race under POLYEMESIS_LEDGER=strict; " +
			"if no local target runs it at all, the first person to hear about a break is " +
			"always a red check, hours after the push.",
	},
	{
		name: "tsc",
		re:   regexp.MustCompile(`\btsc\b`),
		costs: "a type error in the UI, which nothing in the Go suite can see and no amount " +
			"of `go test ./...` will find.",
	},
	{
		name: "npm run lint",
		re:   regexp.MustCompile(`\bnpm run lint\b`),
		costs: "the frontend lint gate. CONTRIBUTING.md told contributors to run `make lint` " +
			"for a while before the Makefile had such a target, and this is the half of that " +
			"story that can regress silently.",
	},
	{
		name: "npm test",
		re:   regexp.MustCompile(`\bnpm (run )?test\b`),
		costs: "the vitest suite -- hundreds of tests over the pure UI logic the browser suite " +
			"cannot enumerate, and the cheapest thing in CI. This is one of the two that were " +
			"gated in CI and nowhere local for months.",
	},
	{
		name: "npm run build",
		re:   regexp.MustCompile(`\bnpm run build\b`),
		costs: "the production bundle, and it is not redundant with tsc: `npm run build` runs " +
			"`tsc -b` for real and then bundles, so it fails on things `tsc --noEmit` passes. " +
			"This is the other one.",
	},
}

// THE ONE PLACE something CI gates on is allowed to be absent from `make check`.
//
// Excluding a command has to be an act somebody performs and a reviewer sees.
// An omission is what this whole file exists because of, so there is no second
// place to put one and no way to leave one unexplained.
var parityExclusions = []struct {
	re     *regexp.Regexp
	reason string
}{
	{
		re: regexp.MustCompile(`\bnpm audit\b`),
		reason: "`npm audit --audit-level=high` fails on an advisory published overnight " +
			"against code you did not touch. That belongs on a gate you can rerun, not " +
			"between you and a commit.",
	},
}

// Steps CI's job runs to set itself up rather than to check anything. These are
// not gates and there is nothing for `check` to mirror.
var parityCISetupSteps = []*regexp.Regexp{
	regexp.MustCompile(`^\s*npm ci\b`), // installs dependencies
	regexp.MustCompile(`^\s*echo\b`),   // the documentation-gate notice
}

func TestMakeCheckRunsEveryGateCommandCIGatesOn(t *testing.T) {
	reached, recipes := parityReachableFromCheck(t)

	all := strings.Join(recipes, "\n")
	for _, g := range gateCommands {
		if g.re.MatchString(all) {
			continue
		}
		t.Errorf("`make %s` does not run %s anywhere.\n\n"+
			"  What that costs: %s\n\n"+
			"  `%s` currently reaches these targets: %s\n\n"+
			"  Fix it by making some target reachable from `%s` run %s -- add it to an "+
			"existing recipe, or give it a target and put that target in `%s`'s "+
			"prerequisites. If it genuinely does not belong in a pre-commit gate, say so "+
			"in parityExclusions in this file, with the reason, so the omission is "+
			"something a reviewer can see and argue with.",
			parityGateTarget, g.name, g.costs,
			parityGateTarget, strings.Join(reached, " "),
			parityGateTarget, g.name, parityGateTarget)
	}
}

func TestCIUIJobRunsNothingMakeCheckDoesNot(t *testing.T) {
	reached, recipes := parityReachableFromCheck(t)
	all := strings.Join(recipes, "\n")

	steps := parityCIJobRunSteps(t)
	joined := strings.Join(steps, "\n")
	for _, x := range parityExclusions {
		if !x.re.MatchString(joined) {
			t.Errorf("nothing in ci.yml's `%s` job matches the exclusion %q any more.\n\n"+
				"  It was excluded because: %s\n\n"+
				"A carve-out for a step CI no longer runs is a hole waiting for the next "+
				"command that happens to match it. Delete it from parityExclusions.",
				parityCIJob, x.re, x.reason)
		}
	}
	if len(steps) < 4 {
		t.Fatalf("found only %d `run:` steps in ci.yml's `%s` job, which is fewer than it "+
			"has ever had. The job has been renamed or restructured and this test is "+
			"comparing almost nothing -- repoint it rather than deleting it.",
			len(steps), parityCIJob)
	}

next:
	for _, step := range steps {
		for _, x := range parityExclusions {
			if x.re.MatchString(step) {
				continue next
			}
		}
		for _, s := range parityCISetupSteps {
			if s.MatchString(step) {
				continue next
			}
		}
		matched := false
		for _, g := range gateCommands {
			if !g.re.MatchString(step) {
				continue
			}
			matched = true
			if !g.re.MatchString(all) {
				t.Errorf("ci.yml's `%s` job runs %q, which gates on %s, and `make %s` does "+
					"not run %s.\n\n  What that costs: %s\n\n  `%s` currently reaches: %s",
					parityCIJob, step, g.name, parityGateTarget, g.name, g.costs,
					parityGateTarget, strings.Join(reached, " "))
			}
		}
		if matched {
			continue
		}
		t.Errorf("ci.yml's `%s` job has grown a step this repository has no opinion "+
			"about:\n\n    %s\n\nThat is exactly how `make %s` fell behind CI the last two "+
			"times: a step appeared here, gated every pull request, and no local target "+
			"ever ran it.\n\nDecide, out loud, which it is:\n"+
			"  - a gate -> add it to gateCommands in this file AND make `%s` run it;\n"+
			"  - not a gate -> add it to parityExclusions with the reason, or to "+
			"parityCISetupSteps if it only sets the job up.",
			parityCIJob, strings.TrimSpace(step), parityGateTarget, parityGateTarget)
	}
}

// ---------------------------------------------------------------- the parsing
//
// Enough Makefile to follow prerequisites, and enough YAML to read one job's
// `run:` steps. Both are deliberately small: the point is to answer one
// question about each file, not to be a parser anybody has to maintain.

var (
	parityAssignRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*[:?+]?=\s*(.*)$`)
	parityRuleRe   = regexp.MustCompile(`^([^\s#][^:]*):([^=].*)?$`)
	parityVarRe    = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)
	paritySubMake  = regexp.MustCompile(`\$\(MAKE\)\s+([A-Za-z0-9_./-]+)`)
)

// parityReachableFromCheck returns the sorted names of every target reachable
// from `check`, and the recipe lines of those targets.
//
// COMMENTS INSIDE RECIPES ARE DROPPED, and that matters: several targets here
// carry long `@#` explanations that name the very commands they run. A guard
// that accepted a mention would pass on a recipe whose body had been deleted
// and whose comment had not.
func parityReachableFromCheck(t *testing.T) (reached []string, recipes []string) {
	t.Helper()

	raw, err := os.ReadFile(parityMakefile)
	if err != nil {
		t.Fatalf("read %s: %v", parityMakefile, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")

	vars := map[string]string{}
	for _, ln := range lines {
		if strings.HasPrefix(ln, "\t") || strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		if m := parityAssignRe.FindStringSubmatch(ln); m != nil {
			vars[m[1]] = m[2]
		}
	}
	expand := func(s string) string {
		for i := 0; i < 5 && strings.Contains(s, "$("); i++ {
			s = parityVarRe.ReplaceAllStringFunc(s, func(ref string) string {
				name := parityVarRe.FindStringSubmatch(ref)[1]
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
		m := parityRuleRe.FindStringSubmatch(ln)
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

	if _, ok := prereqs[parityGateTarget]; !ok {
		t.Fatalf("no `%s:` rule found in %s. Either the local gate has been renamed or this "+
			"test's Makefile parsing has stopped working -- and in both cases it is now "+
			"comparing nothing at all. Repoint it; do not delete it.",
			parityGateTarget, parityMakefile)
	}

	seen := map[string]bool{}
	queue := []string{parityGateTarget}
	for len(queue) > 0 {
		tgt := queue[0]
		queue = queue[1:]
		if seen[tgt] {
			continue
		}
		seen[tgt] = true
		queue = append(queue, prereqs[tgt]...)
		for _, body := range bodies[tgt] {
			// `$(MAKE) foo` is a prerequisite spelled sideways.
			for _, sm := range paritySubMake.FindAllStringSubmatch(body, -1) {
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
			"gate in it at all.", parityGateTarget, reached)
	}
	return reached, recipes
}

// parityCIJobRunSteps returns the command text of every `run:` step in one job
// of ci.yml, scalar and block form alike. A bare `run:` with a nested mapping
// under it -- `defaults: run: working-directory:` -- is a setting, not a step,
// and is skipped.
func parityCIJobRunSteps(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(parityCI)
	if err != nil {
		t.Fatalf("read %s: %v", parityCI, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")

	start := -1
	for i, ln := range lines {
		if strings.TrimRight(ln, " ") == "  "+parityCIJob+":" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("no `%s:` job in %s. The job has been renamed and this test is now "+
			"comparing `make %s` against nothing -- repoint it at whatever runs the "+
			"frontend gates now.", parityCIJob, parityCI, parityGateTarget)
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
			continue // `defaults:`-style mapping, not a step
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
