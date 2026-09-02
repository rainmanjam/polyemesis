package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// EVERY REQUIRED CHECK MUST BE A NAME SOME JOB CAN ACTUALLY EMIT.
//
// A job's NAME is part of its contract with branch protection, and nothing in
// the workflow files says so. Rename a job, split it across a matrix, or add a
// rule that skips it, and a required check stops existing -- with no failing
// test, no red check, and no line in any diff to notice.
//
// On 2026-09-02 that had happened three times over and #650, #656 and #659 were
// all unmergeable at once. #659 was forty of forty green. Nothing was red
// anywhere, because a check that never reports is not a failure; it is an
// absence, and GitHub renders an absence as "waiting". The only symptom was
// that merge did nothing.
//
// This is the detection rung and cannot be more. It reads .github/required-checks.txt,
// which is a MIRROR of a setting stored on GitHub rather than the setting
// itself -- so it cannot see that someone edited protection without editing the
// file. What it can see is the direction all three failures actually came from:
// a workflow change that made a required name unproducible. Control would mean
// generating protection from the repository, which is a real design and a much
// larger one than this.

func TestEveryRequiredCheckIsProducibleByAWorkflow(t *testing.T) {
	root := repoRootFromTest(t)

	required := readRequiredChecks(t, root)
	if len(required) == 0 {
		t.Fatal(".github/required-checks.txt lists no contexts. An empty list makes " +
			"this test pass by checking nothing, which is the failure mode it is here to " +
			"prevent -- repopulate it from the live setting (the command is in the file).")
	}

	patterns, jobsSeen := producibleCheckNames(t, root)
	if jobsSeen == 0 {
		t.Fatal("no jobs found in .github/workflows. The parse is wrong, not the workflows.")
	}

	var missing, unreliable []string
	for _, want := range required {
		var static, dyn []string
		for _, p := range patterns {
			if !p.re.MatchString(want) {
				continue
			}
			if p.dynamic {
				dyn = append(dyn, p.job)
			} else {
				static = append(static, p.job)
			}
		}
		switch {
		case len(static) > 0:
			// A job that always runs under this name. Nothing to prove.
		case len(dyn) >= 2:
			// The partition pattern: one job runs the suite, a sibling reports
			// the same name when it does not. Between them the name is always
			// emitted, which is the property being asserted.
		case len(dyn) == 1:
			unreliable = append(unreliable, want+"  (only "+dyn[0]+", whose matrix is decided at run time)")
		default:
			missing = append(missing, want)
		}
	}

	if len(unreliable) > 0 {
		t.Errorf("%d required check(s) are produced only by a job that may decide not to "+
			"run them:\n  %s\n\nA job whose matrix is computed at run time emits these names "+
			"sometimes. When it does not, the check does not fail -- it never appears, and "+
			"branch protection waits on it forever with nothing red to point at.\n\n"+
			"Pair it with a shim job carrying the SAME name that reports when the real one "+
			"does not run, so the context is always answered. ci.yml's "+
			"container-not-applicable is that pattern.",
			len(unreliable), strings.Join(unreliable, "\n  "))
	}

	if len(missing) > 0 {
		t.Errorf("%d required check(s) name a job no workflow can emit:\n  %s\n\n"+
			"Branch protection waits on these names. A name nothing produces is not a "+
			"failing check -- it is an absent one, so every pull request blocks with "+
			"nothing red to point at.\n\n"+
			"Either restore the name (a job whose `name:` is exactly this, or a shim that "+
			"reports it when the real job does not run), or remove it from branch "+
			"protection AND from .github/required-checks.txt in the same commit.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

func readRequiredChecks(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".github", "required-checks.txt"))
	if err != nil {
		t.Fatalf("reading .github/required-checks.txt: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// matrixRef matches a ${{ matrix.whatever }} expression inside a job name.
var matrixRef = regexp.MustCompile(`\$\{\{\s*matrix\.[A-Za-z0-9_.-]+\s*\}\}`)

// producibleCheckNames turns every job in every workflow into a pattern for the
// check names it can report, following GitHub's own naming rules:
//
//   - an explicit `name:` is the check name;
//   - without one, the job id is;
//   - a matrix job with no matrix reference in its name gets " (values)"
//     appended, so it can never produce the bare name;
//   - a ${{ matrix.x }} inside the name is replaced by whatever the matrix
//     holds, which is frequently computed at run time and cannot be resolved
//     here -- so it becomes a wildcard rather than a guess.
//
// producer is one job's claim on a set of check names.
type producer struct {
	re *regexp.Regexp
	// dynamic marks a job whose matrix is computed at run time -- typically
	// ${{ fromJSON(needs.x.outputs.y) }}. Such a job MIGHT emit the name and
	// might not, depending on a decision made during the run, so on its own it
	// is not evidence the name will ever appear. That distinction is the whole
	// point: `container: acceptance-docker` was "produced" by a job that had
	// already decided not to run it.
	dynamic bool
	job     string
}

func producibleCheckNames(t *testing.T, root string) ([]producer, int) {
	t.Helper()
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	var out []producer
	jobs := 0
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		var wf struct {
			Jobs map[string]struct {
				Name     string `yaml:"name"`
				Strategy struct {
					Matrix map[string]any `yaml:"matrix"`
				} `yaml:"strategy"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(b, &wf); err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		for id, j := range wf.Jobs {
			jobs++
			name := j.Name
			if name == "" {
				name = id
			}
			hasMatrix := len(j.Strategy.Matrix) > 0
			usesMatrixInName := matrixRef.MatchString(name)

			// Build the pattern by escaping the literal parts and turning each
			// matrix reference into a wildcard.
			var b strings.Builder
			b.WriteString("^")
			last := 0
			for _, loc := range matrixRef.FindAllStringIndex(name, -1) {
				b.WriteString(regexp.QuoteMeta(name[last:loc[0]]))
				b.WriteString(".+")
				last = loc[1]
			}
			b.WriteString(regexp.QuoteMeta(name[last:]))
			if hasMatrix && !usesMatrixInName {
				// GitHub appends the matrix values, so the bare name is NOT
				// produced. This is precisely what broke `npm-audit`.
				b.WriteString(` \(.+\)`)
			}
			b.WriteString("$")
			dynamic := false
			for _, v := range j.Strategy.Matrix {
				if str, ok := v.(string); ok && strings.Contains(str, "${{") {
					dynamic = true
				}
			}
			out = append(out, producer{re: regexp.MustCompile(b.String()), dynamic: dynamic, job: id})
		}
	}
	return out, jobs
}
