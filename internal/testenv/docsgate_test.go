package testenv_test

// THE DOCUMENTATION-GATE RULE. #378, and the outage it is made of is #351.
//
// ci.yml's `changes` job computes one boolean -- `needs.changes.outputs.code`,
// false when every path a pull request touches is documentation -- and five jobs
// read it to decide whether to do any work. Every one of those jobs publishes a
// REQUIRED status context. That combination has exactly one safe spelling, and
// the unsafe one is not merely slower or noisier: it makes a pull request
// permanently unmergeable.
//
// THE MECHANISM, because "put it on the steps" is a rule nobody can check
// against a workflow they are editing:
//
//	a skipped ORDINARY job reports a `skipped` conclusion, and branch protection
//	accepts a skip as satisfied. Job-level `if:` is harmless there.
//
//	a skipped MATRIX job never expands its matrix. `acceptance: ${{ matrix.suite }}`
//	is not one context -- it is thirteen, one per leg, and a job that never
//	expanded produces NONE of them. They are not skipped. They do not exist, and
//	a required context that does not exist can never be satisfied.
//
// That shipped in #351 and #349 was the casualty: a documentation-only pull
// request -- the exact case the gate was built for -- sat at fifteen green
// checks with no way in, and had to be repaired in #357 by moving the condition
// onto every step.
//
// SO THE RULE IS FLAT: this output may be read from a step's `if:` and never
// from a job's. It is flat rather than "matrix jobs only" on purpose. Three of
// the five gated jobs have no matrix today, and a job-level `if:` on them would
// be correct today -- but the distance between the correct spelling and the
// outage is one `strategy:` block added by somebody thinking about Go versions,
// with no reason at all to be thinking about branch protection. A rule with an
// exception is a rule you have to re-derive at the moment you are least likely
// to. This one has none.
//
// WHAT IT DELIBERATELY DOES NOT CATCH, said out loud so a green run is not read
// as a stronger claim: it knows nothing about which contexts the ruleset
// actually requires -- that lives in GitHub's settings, not in this repository,
// and a test cannot read it without a token. It reasons about `changes` alone,
// so a second gate job invented later gets none of this protection until its
// name is added below. And it cannot tell a step that SHOULD be gated from one
// that legitimately runs on every event; rule two below takes the position that
// inside a job which asked for the gate, there are no such steps.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// docsGate is the expression whose placement this rule is about.
const docsGate = "needs.changes.outputs.code"

// docsGateJob is the job that produces it. Named rather than inferred: a job
// this rule has never heard of is a job it silently permits, and saying which
// one it watches is the difference between "no findings" and "no opinion".
const docsGateJob = "changes"

// gatedWorkflow is the slice of the schema this rule needs. Everything else is
// left to yaml.v3 to discard, so an unrelated schema change cannot break it.
//
// Needs is a yaml.Node because GitHub accepts both `needs: changes` and
// `needs: [changes, other]`, and a rule that understood only one of them would
// stop applying the day somebody added a second dependency -- quietly, which is
// the failure mode this whole file is about.
type gatedWorkflow struct {
	Jobs map[string]struct {
		If    string    `yaml:"if"`
		Needs yaml.Node `yaml:"needs"`
		Steps []struct {
			Name string `yaml:"name"`
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
			If   string `yaml:"if"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// gateFinding is one offending place, named the way a reader would look for it.
type gateFinding struct {
	file, job, where, why string
}

func (f gateFinding) String() string {
	at := "job " + f.job
	if f.where != "" {
		at += " :: step " + f.where
	}
	return f.file + " :: " + at + " :: " + f.why
}

// dependsOnGate reports whether a job's `needs` names the gate job, in either
// of the two spellings GitHub accepts.
func dependsOnGate(n yaml.Node) bool {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value == docsGateJob
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Value == docsGateJob {
				return true
			}
		}
	}
	return false
}

// auditDocsGate returns every violation of both rules in dir, sorted for a
// stable failure message.
func auditDocsGate(t *testing.T, dir string) []gateFinding {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var out []gateFinding
	var files int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext != ".yml" && ext != ".yaml" {
			continue
		}
		files++

		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var w gatedWorkflow
		if err := yaml.Unmarshal(raw, &w); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		for name, job := range w.Jobs {
			// RULE ONE: the gate may not be read from a job-level `if:`.
			if strings.Contains(job.If, docsGate) {
				out = append(out, gateFinding{e.Name(), name, "",
					"reads " + docsGate + " from a job-level `if:`"})
			}
			// RULE TWO: a job that asked for the gate must apply it to every
			// step. A half-gated job is a job that still pays for most of
			// itself on a documentation-only run while looking gated.
			if !dependsOnGate(job.Needs) {
				continue
			}
			for i, s := range job.Steps {
				if strings.Contains(s.If, docsGate) {
					continue
				}
				out = append(out, gateFinding{e.Name(), name, stepLabel(i, s.Name, s.Uses),
					"has no " + docsGate + " in its `if:`"})
			}
		}
	}
	if files == 0 {
		t.Fatalf("no workflow files in %s; this rule would report nothing and "+
			"look like a pass", dir)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// stepLabel names a step the way somebody scrolling the file would find it.
func stepLabel(i int, name, uses string) string {
	switch {
	case name != "":
		return name
	case uses != "":
		return "uses: " + uses
	default:
		return "#" + strconv.Itoa(i+1) + " (unnamed)"
	}
}

// TestTheDocumentationGateIsNeverReadFromAJobLevelIf is the rule, applied to the
// real workflows.
func TestTheDocumentationGateIsNeverReadFromAJobLevelIf(t *testing.T) {
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	for _, f := range auditDocsGate(t, dir) {
		t.Errorf("%s\n"+
			"        The documentation gate belongs on every STEP of a job and never\n"+
			"        on the job itself. A skipped matrix job does not expand its\n"+
			"        matrix, so its per-leg required contexts are never created --\n"+
			"        not skipped, absent -- and the pull request can never satisfy\n"+
			"        branch protection. #351 shipped exactly that and #349 was\n"+
			"        unmergeable at fifteen green checks until #357 undid it.\n"+
			"        Move the condition onto the steps, and add a step that says out\n"+
			"        loud why the check did no work.", f)
	}
}

// TestTheDocumentationGateRuleFlagsItsRedFixtures is the guard on the guard.
//
// Eight tests have shipped in this repository that passed for the wrong reason.
// A rule about a failure nobody can reproduce locally -- this one needs a
// required-context ruleset and a documentation-only pull request to demonstrate
// -- is the kind most likely to join them, so the fixtures are the evidence that
// it can fail. Both the count and the identity of the findings are asserted: a
// rule that flagged everything would satisfy a count-only test.
func TestTheDocumentationGateRuleFlagsItsRedFixtures(t *testing.T) {
	base := filepath.Join("testdata", "docsgate")

	t.Run("red fixtures are all flagged", func(t *testing.T) {
		want := []string{
			"half-gated-job.yml :: job build :: step #3 (unnamed) :: has no needs.changes.outputs.code in its `if:`",
			"job-level-if-on-a-matrix.yml :: job build :: reads needs.changes.outputs.code from a job-level `if:`",
			"job-level-if-on-an-ordinary-job.yml :: job build :: reads needs.changes.outputs.code from a job-level `if:`",
			"needs-a-list.yml :: job build :: step uses: actions/checkout@v5 :: has no needs.changes.outputs.code in its `if:`",
		}
		var got []string
		for _, f := range auditDocsGate(t, filepath.Join(base, "red")) {
			got = append(got, f.String())
		}
		if len(got) != len(want) {
			t.Fatalf("flagged %d red places, want %d\n got: %v\nwant: %v",
				len(got), len(want), got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("finding %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("green fixtures are not flagged", func(t *testing.T) {
		if got := auditDocsGate(t, filepath.Join(base, "green")); len(got) != 0 {
			t.Errorf("flagged %v; these fixtures either gate every step or do not "+
				"read the gate at all, and flagging them is the false-positive rate "+
				"that would make this rule grow an allowlist", got)
		}
	})
}
