package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// THE WORKFLOW LINTER READS THE SHELL, NOT ONLY THE YAML.
//
// ci.yml's `workflow lint` job (a required check) ran `actionlint
// -shellcheck=`, which turns shellcheck off for every `run:` block. The reason
// recorded beside it was sound when it was written -- 38 findings on its first
// run, nearly all of them shellcheck failing to parse a block -- and stopped
// being true: by staging-readiness row 38 what remained was a handful of real
// info/style findings plus seven PowerShell steps that shellcheck was reading
// as bash, because they ran on Windows by default rather than by a declared
// `shell: pwsh`. With the findings fixed and the shells declared, the flag was
// switching off a linter with nothing left to say, and so nothing would ever
// say anything again: `sha256sum *` meeting a file named `-x`, an unquoted
// expansion, a `sudo` that does not cover its redirect.
//
// Two assertions. The first is the one the row asks for. The second is what
// makes the first sustainable: a PowerShell block with no `shell:` fails the
// lint job with a wall of bash parse errors, which is the exact pressure that
// put the flag there.

type lintStep struct {
	Name  string            `yaml:"name"`
	Run   string            `yaml:"run"`
	Shell string            `yaml:"shell"`
	Env   map[string]string `yaml:"env"`
}

type lintWorkflow struct {
	Jobs map[string]struct {
		Name  string     `yaml:"name"`
		Steps []lintStep `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadLintWorkflow(t *testing.T, name string) lintWorkflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRootFromTest(t), ".github", "workflows", name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	var wf lintWorkflow
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	return wf
}

func TestWorkflowLintRunsShellcheck(t *testing.T) {
	wf := loadLintWorkflow(t, "ci.yml")
	job, ok := wf.Jobs["actionlint"]
	if !ok || job.Name != "workflow lint" {
		t.Fatal("ci.yml has no `actionlint` job named \"workflow lint\" -- the required " +
			"check this test is about. If it moved, move this test with it.")
	}
	invocations := 0
	disable := regexp.MustCompile(`(^|\s)-shellcheck=(\s|$)`)
	pinned := regexp.MustCompile(`(^|\s)-shellcheck=\S`)
	for _, s := range job.Steps {
		for _, line := range strings.Split(s.Run, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "actionlint ") && line != "actionlint" {
				continue
			}
			invocations++
			if disable.MatchString(line) {
				t.Errorf("the workflow lint job runs %q. An empty -shellcheck= turns shellcheck "+
					"off for every run: block in every workflow. Fix what it reports instead "+
					"(or declare `shell: pwsh` on PowerShell steps), and keep it on.", line)
			} else if !pinned.MatchString(line) {
				t.Errorf("the workflow lint job runs %q, which uses whatever shellcheck is on the "+
					"runner's PATH. ubuntu-latest's is 0.9.0, which reports SC2015 findings the "+
					"0.11.0 a developer runs does not, so the required check's verdict would "+
					"depend on the runner image. Pass -shellcheck=<the pinned binary>.", line)
			}
		}
	}
	if invocations == 0 {
		t.Fatal("the workflow lint job never invokes actionlint; this test would pass over nothing")
	}
	assertShellcheckPinned(t, job.Steps)
}

// assertShellcheckPinned requires a step that downloads shellcheck at a named
// version, verifies it against a sha256, and asserts the version it installed.
// The three together are what make the lint result a property of this
// repository rather than of whichever runner image GitHub rolled out that week.
func assertShellcheckPinned(t *testing.T, steps []lintStep) {
	t.Helper()
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, s := range steps {
		if s.Env["SHELLCHECK_VERSION"] == "" {
			continue
		}
		if !sha.MatchString(s.Env["SHELLCHECK_SHA256"]) {
			t.Errorf("step %q installs shellcheck %s without a SHELLCHECK_SHA256 to verify it against",
				s.Name, s.Env["SHELLCHECK_VERSION"])
		}
		if !strings.Contains(s.Run, "sha256sum -c") {
			t.Errorf("step %q never runs `sha256sum -c`; the checksum in its env checks nothing", s.Name)
		}
		if !strings.Contains(s.Run, "shellcheck --version") {
			t.Errorf("step %q never asserts the installed shellcheck's version", s.Name)
		}
		return
	}
	t.Error("the workflow lint job installs no pinned shellcheck (no step sets SHELLCHECK_VERSION). " +
		"Without one it lints with the runner image's shellcheck, and the image decides the verdict.")
}

// PowerShell cmdlets and variables that never appear in a bash block here.
var powershellMarker = regexp.MustCompile(`\$ErrorActionPreference|Write-Host|\[System\.|Test-Path|Get-ChildItem`)

func TestPowerShellStepsDeclareTheirShell(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(repoRootFromTest(t), ".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		wf := loadLintWorkflow(t, e.Name())
		for jobID, job := range wf.Jobs {
			for _, s := range job.Steps {
				if !powershellMarker.MatchString(s.Run) {
					continue
				}
				seen++
				if s.Shell != "pwsh" && s.Shell != "powershell" {
					t.Errorf("%s: job %s, step %q is PowerShell with shell: %q. Without "+
						"`shell: pwsh` it runs as pwsh only because the runner is Windows, and "+
						"actionlint hands it to shellcheck as bash -- the flood of parse errors "+
						"that got shellcheck switched off the first time.",
						e.Name(), jobID, s.Name, s.Shell)
				}
			}
		}
	}
	// POSITIVE CONTROL: ci.yml's Windows leg has at least seven PowerShell
	// steps. Zero means the marker stopped matching, not that they went away.
	if seen < 7 {
		t.Errorf("found only %d PowerShell steps across .github/workflows; expected at "+
			"least the seven in ci.yml's Windows leg. The marker regexp has gone stale.", seen)
	}
}
