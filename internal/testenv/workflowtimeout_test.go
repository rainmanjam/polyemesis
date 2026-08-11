package testenv_test

// THE BACKGROUNDED-STEP RULE. One rule, enforced by a test so it runs on every
// runner in `go test ./...`, with red fixtures that prove it can fail.
//
// The rule: a `run:` block in .github/workflows that BACKGROUNDS A PROCESS must
// carry step-level timeout-minutes.
//
// Why this rule and not a cleverer one. Issue #179: a smoke step started a
// server, decided in two seconds that it served /health, and then blocked in an
// unbounded shell `wait` on the pid before printing the verdict. On Windows the
// `kill` before that wait cannot deliver a real SIGTERM to a native .exe, so the
// wait never returned. The step burned 28 minutes, was cancelled by the JOB
// timeout, and its log came back BlobNotFound -- the partial output, including
// the verdict it had already computed, was unrecoverable. Proving the branch
// innocent afterwards took building two refs, hashing binaries and surveying
// fourteen runs.
//
// A job timeout cancels and names nothing. A step timeout cancels and names the
// step, and GitHub keeps the step's log. That difference is the whole value, and
// it is the discipline ci.yml already documents at :154-158 and :305-311 for its
// `go test` ceilings -- whichever timeout fires first decides how much you learn,
// so the narrowest one has to.
//
// WHY `$!` AND NOTHING ELSE. The general shape "this step can hang" is not
// decidable from a shell script, and a gate that guesses grows an override list
// -- the seventh free pass this repository has invented. `$!` is the syntactic
// mark of a step that holds a pid it may later wait on, which is exactly the
// #179 shape. Enumerated across all seven workflows it matches the two steps
// that #179 fixed and nothing else: zero false positives, no allowlist, nothing
// to rubber-stamp.
//
// WHAT IT DELIBERATELY DOES NOT CATCH, said out loud so nobody mistakes a green
// run for a stronger claim: a step that backgrounds with a bare `&` and never
// captures the pid (it can orphan a process but has nothing to block on); a step
// that hangs on something in the foreground; a `wait` in a script the step
// merely invokes. Those are bounded by the job timeout and by the shell
// libraries' own polls, not by this.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// backgrounding is the mark of a step that captures a child's pid.
const backgrounding = "$!"

// workflow is the slice of the schema this rule needs. Everything else in a
// workflow file is left to yaml.v3 to discard, so an unrelated schema change
// cannot break this test.
type workflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Name    string  `yaml:"name"`
			Run     string  `yaml:"run"`
			Timeout *int    `yaml:"timeout-minutes"`
			Uses    string  `yaml:"uses"`
			ID      string  `yaml:"id"`
			Shell   *string `yaml:"shell"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// finding is one offending step, named the way a reader would look for it.
type finding struct {
	file, job, step string
}

func (f finding) String() string {
	return f.file + " :: job " + f.job + " :: step " + f.step
}

// auditWorkflows returns every backgrounding step in dir that has no
// step-level timeout-minutes, sorted for a stable failure message.
func auditWorkflows(t *testing.T, dir string) []finding {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var out []finding
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
		var wf workflow
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			// A workflow this cannot parse is a failure, not a pass. The
			// alternative is a gate that goes quiet the day somebody
			// restructures a file.
			t.Fatalf("parse %s: %v", path, err)
		}

		for job, j := range wf.Jobs {
			for i, s := range j.Steps {
				if !strings.Contains(s.Run, backgrounding) {
					continue
				}
				if s.Timeout != nil {
					continue
				}
				name := s.Name
				if name == "" {
					name = s.ID
				}
				if name == "" {
					name = "#" + itoa(i+1) + " (unnamed)"
				}
				out = append(out, finding{file: e.Name(), job: job, step: name})
			}
		}
	}

	// A directory that matched no files would make every assertion below
	// vacuous, which is the failure mode this whole round is about.
	if files == 0 {
		t.Fatalf("no workflow files found under %s; this check would pass by "+
			"examining nothing", dir)
	}

	sort.Slice(out, func(a, b int) bool { return out[a].String() < out[b].String() })
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestBackgroundedWorkflowStepsCarryAStepTimeout is the rule, applied to the
// real workflows.
func TestBackgroundedWorkflowStepsCarryAStepTimeout(t *testing.T) {
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	for _, f := range auditWorkflows(t, dir) {
		t.Errorf("%s backgrounds a process (%s) and has no step-level "+
			"timeout-minutes.\n"+
			"        A step that holds a pid can block on it for ever. Without a step\n"+
			"        timeout the job timeout cancels the whole runner, names no step,\n"+
			"        and GitHub returns BlobNotFound for the partial log -- see #179,\n"+
			"        where that cost 28 minutes and a day of investigation to recover\n"+
			"        an answer the step had already computed in two seconds.\n"+
			"        Add `timeout-minutes:` to the step, sized from a measurement and\n"+
			"        kept below the job's.", f, backgrounding)
	}
}

// TestTheBackgroundedStepRuleFlagsItsRedFixtures is the guard on the guard.
//
// Eight tests have shipped in this repository that passed for the wrong reason,
// and scripts/sbom-guard.sh taught the lesson directly: a check nobody has
// watched FAIL is a check nobody has evidence works. The fixtures below are the
// evidence. `red` holds workflows this rule must flag and `green` holds ones it
// must not, and the count and identity of the findings are both asserted -- a
// rule that flagged everything would satisfy a count-only test.
func TestTheBackgroundedStepRuleFlagsItsRedFixtures(t *testing.T) {
	base := filepath.Join("testdata", "workflowguard")

	t.Run("red fixtures are all flagged", func(t *testing.T) {
		want := []string{
			"backgrounded-no-timeout.yml :: job smoke :: step Start a server and check it serves",
			"job-timeout-is-not-a-step-timeout.yml :: job smoke :: step Start a server behind a job-level timeout only",
			"second-step-of-two.yml :: job smoke :: step The one that backgrounds",
			"unnamed-step.yml :: job smoke :: step #2 (unnamed)",
		}
		var got []string
		for _, f := range auditWorkflows(t, filepath.Join(base, "red")) {
			got = append(got, f.String())
		}
		if len(got) != len(want) {
			t.Fatalf("flagged %d red steps, want %d\n got: %v\nwant: %v",
				len(got), len(want), got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("finding %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("green fixtures are not flagged", func(t *testing.T) {
		if got := auditWorkflows(t, filepath.Join(base, "green")); len(got) != 0 {
			t.Errorf("flagged %v; these fixtures either carry a step timeout or "+
				"background nothing, and flagging them is the false-positive rate "+
				"that would make this rule grow an allowlist", got)
		}
	})
}

// ---------------------------------------------------------------- rule two

// THE TERMINATION-ORDER RULE, and it exists because the rule above is not
// enough. #210 measured that: a fixture whose `run:` was #179's body VERBATIM --
// `kill "$pid" ... ; wait "$pid" ... ; if [ "$ok" != yes ]` -- was dropped into
// testdata/workflowguard/green/ carrying `timeout-minutes: 10`, and
// TestTheBackgroundedStepRuleFlagsItsRedFixtures passed, both subtests,
// unflagged. The gate obliges bounding the blast radius. It does not oblige
// emitting the verdict before the cleanup, and the defect it was built for could
// be reintroduced verbatim and merge green.
//
// So: in a step that backgrounds a process, the VERDICT MUST NOT COME AFTER THE
// TEARDOWN. That is the property #179 actually violated. Its step decided in two
// seconds that the server was healthy, then blocked in `wait` on a kill Git-Bash
// could not deliver, and the answer it had already computed died with the step.
// A step that reports first can hang afterwards and still have told you what it
// learned.
//
// A `trap` is NOT a teardown for this purpose, and the distinction is the whole
// reason the real workflows are green: `trap 'kill $pid' EXIT` REGISTERS cleanup
// to run when the step ends, which is necessarily after the verdict. That is the
// shape #179 was fixed into, and a rule that could not tell it from an inline
// kill would flag the fix.
//
// WHAT IT DELIBERATELY DOES NOT CATCH: a verdict computed in a script the step
// invokes; a teardown hidden behind a variable or a function defined elsewhere;
// an `exit 0` that is really a verdict. It is syntactic and line-local, a net
// under the class rather than a proof about it. scripts/termination-guard.sh is
// the same net over scripts/*.sh, with rules of its own.

// inlineKill reports a kill in command position that is EXECUTED here.
//
// A `trap 'kill ...' EXIT` is excluded, and the distinction is the whole reason
// the real workflows are green: a trap REGISTERS cleanup to run when the step
// ends, necessarily after the verdict. That is the shape #179 was fixed into,
// and a rule that could not tell it from an inline kill would flag the fix.
func inlineKill(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "trap ") {
		return false
	}
	if strings.HasPrefix(t, "kill -0") || strings.Contains(t, "! kill -0") {
		return false // an observation, not a kill
	}
	return strings.HasPrefix(t, "kill ") || strings.HasPrefix(t, "pkill ")
}

// observation reports a line that RE-OBSERVES whether the process is actually
// gone. `kill -0` and pgrep are the portable spellings; a poll loop around
// either is what turns a request into an answer.
func observation(s string) bool {
	t := strings.TrimSpace(s)
	return strings.Contains(t, "kill -0") || strings.Contains(t, "pgrep") ||
		strings.Contains(t, "poly_free_port") || strings.Contains(t, "tasklist")
}

// assumption reports a line that ACTS on the belief that the process is gone --
// including `wait`, which in a shell has no ceiling at all and is #179's
// mechanism exactly: it blocks on a death that a kill Git-Bash cannot deliver to
// a native .exe may never produce.
func assumption(s string) bool {
	t := strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "#", 2)[0])
	if t == "" {
		return false
	}
	for _, w := range []string{"wait", "sleep ", "cat ", "grep ", "ls ", "if ", "["} {
		if t == w || strings.HasPrefix(t, w) {
			return true
		}
	}
	return false
}

// bareWait reports a `wait` with no pid: it blocks on every background job at
// once, with no pid and no bound.
func bareWait(s string) bool {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "trap ") {
		return false
	}
	t = strings.TrimSuffix(strings.TrimSpace(strings.SplitN(t, "#", 2)[0]), ";")
	for _, form := range []string{"wait", "wait || true", "wait 2>/dev/null",
		"wait 2>/dev/null || true"} {
		if t == form {
			return true
		}
	}
	return false
}

// auditTerminationOrder returns every backgrounding step whose verdict is
// computed after its teardown, or which contains a bare wait.
func auditTerminationOrder(t *testing.T, dir string) []finding {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var out []finding
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
		var wf workflow
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		for job, j := range wf.Jobs {
			for i, s := range j.Steps {
				if !strings.Contains(s.Run, backgrounding) {
					continue
				}
				name := s.Name
				if name == "" {
					name = s.ID
				}
				if name == "" {
					name = "#" + itoa(i+1) + " (unnamed)"
				}

				// WHICHEVER COMES FIRST DECIDES. After an inline kill, scan
				// forward: a re-observation means the step earned whatever it
				// does next, and an assumption reached first means it did not.
				// Same adjudication as scripts/termination-guard.sh, so the two
				// jurisdictions cannot drift into disagreeing about a shape.
				lines := strings.Split(s.Run, "\n")
				flagged := false
				for n, ln := range lines {
					if bareWait(ln) {
						flagged = true
						break
					}
					if !inlineKill(ln) {
						continue
					}
					for _, after := range lines[n+1:] {
						if observation(after) {
							break
						}
						if assumption(after) {
							flagged = true
							break
						}
					}
					if flagged {
						break
					}
				}
				if flagged {
					out = append(out, finding{file: e.Name(), job: job, step: name})
				}
			}
		}
	}

	if files == 0 {
		t.Fatalf("no workflow files found under %s; this check would pass by "+
			"examining nothing", dir)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].String() < out[b].String() })
	return out
}

// TestBackgroundedWorkflowStepsReportBeforeTheyTearDown is the rule, applied to
// the real workflows.
func TestBackgroundedWorkflowStepsReportBeforeTheyTearDown(t *testing.T) {
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	for _, f := range auditTerminationOrder(t, dir) {
		t.Errorf("%s kills a process and then ACTS on the belief that it is gone, with "+
			"nothing in between that observed it.\n"+
			"        A signal is a request. Between asking a process to die and observing\n"+
			"        it dead there is an interval, and everything the step does in that\n"+
			"        interval is measured against a machine in a state nobody looked at.\n"+
			"        A shell `wait` is not an observation with a ceiling: on Windows the\n"+
			"        kill above it cannot deliver a real SIGTERM to a native .exe, so it\n"+
			"        waits for a death that never comes. That is #179 -- 28 minutes, and a\n"+
			"        log that came back BlobNotFound carrying a verdict already computed.\n"+
			"        Poll `kill -0` with a bound, or register cleanup with `trap ... EXIT`,\n"+
			"        which runs after the step has already said what it learned.", f)
	}
}

// TestTheTerminationOrderRuleFlagsItsRedFixtures is the guard on rule two --
// including, by name, the fixture #210 proved the first rule admits.
func TestTheTerminationOrderRuleFlagsItsRedFixtures(t *testing.T) {
	base := filepath.Join("testdata", "workflowguard")

	t.Run("red fixtures are all flagged", func(t *testing.T) {
		want := []string{
			"issue-179-body-verbatim.yml :: job smoke :: step Start a server and check it serves",
			"kill-then-unbounded-wait.yml :: job smoke :: step Stop the server and collect it",
			"kill-then-verdict.yml :: job smoke :: step Verdict after the kill",
		}
		var got []string
		for _, f := range auditTerminationOrder(t, filepath.Join(base, "termination-red")) {
			got = append(got, f.String())
		}
		if len(got) != len(want) {
			t.Fatalf("flagged %d red steps, want %d\n got: %v\nwant: %v",
				len(got), len(want), got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("finding %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("green fixtures are not flagged", func(t *testing.T) {
		for _, dir := range []string{"termination-green", "green", "red"} {
			if got := auditTerminationOrder(t, filepath.Join(base, dir)); len(got) != 0 {
				t.Errorf("%s: flagged %v. These report before they tear down, or use a "+
					"trap -- which runs after the verdict by construction and is the "+
					"shape #179 was fixed into. Flagging the fix is how a rule grows "+
					"an allowlist.", dir, got)
			}
		}
	})
}
