package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A required CI gate greps this log line by hand, and nothing connects them.
//
// scripts/acceptance-recording-stop.sh asserts that a full server stop kills no
// child, and it does it by grepping the server log for the exact words
// "did not exit after grace period". That is the only link: no shared constant,
// no import, nothing a compiler or a refactoring tool would follow.
//
// So rewording the log line -- tidying it, translating it, adding a field --
// silently turns a required check into one that can never fail. The suite goes
// green because it finds nothing, which is indistinguishable from finding
// nothing wrong. That is the "check wrong in the restrictive direction"
// failure inverted: a check wrong in the permissive direction, which is worse,
// because the first is noticed the moment it fires and the second never fires.
//
// This is the missing link, made mechanical. It is Warning rung, not Control:
// the string still lives in two places and nothing stops a third copy
// appearing. Control would mean the script consuming a constant this package
// exports -- worth doing if a second script ever greps a second line, and not
// worth the plumbing for one.

// graceEscalationLogLine is the text the acceptance script greps for. Changing
// it means changing scripts/acceptance-recording-stop.sh in the same commit.
const graceEscalationLogLine = "did not exit after grace period"

// greppingScripts are the acceptance suites that assert on that text.
//
// Only acceptance-recording-stop today, covering a whole-server SIGTERM. A suite
// for the reconcile paths -- where production's escalations actually came from --
// is written and held back until the wedge it detects is fixed; see the follow-up
// issue. Adding it, or any other consumer, means adding it here: a pin that
// covers some of the consumers is worse than none, because it reads as coverage.
var greppingScripts = []string{
	"acceptance-recording-stop.sh",
}

func TestTheAcceptanceScriptStillGrepsTheLineWeStillLog(t *testing.T) {
	root := repoRootForTest(t)

	// Half one: the supervisor still emits it.
	src, err := os.ReadFile(filepath.Join(root, "internal", "supervisor", "supervisor.go"))
	if err != nil {
		t.Fatalf("reading supervisor.go: %v", err)
	}
	if !strings.Contains(string(src), graceEscalationLogLine) {
		t.Fatalf("supervisor.go no longer logs %q.\n"+
			"If that was deliberate, update scripts/acceptance-recording-stop.sh in the "+
			"same commit -- it greps this text and will otherwise pass by finding nothing.",
			graceEscalationLogLine)
	}

	// Half two: EVERY script that greps it still does. One consumer was the
	// original hole; two consumers and a partial pin would be the same hole
	// with a test standing next to it looking reassuring.
	for _, name := range greppingScripts {
		script, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Errorf("reading %s: %v", name, err)
			continue
		}
		if !strings.Contains(string(script), graceEscalationLogLine) {
			t.Errorf("%s no longer greps %q.\n"+
				"If that gate moved, remove it from greppingScripts in the same commit; "+
				"if the text was reworded, the supervisor's line must change to match, "+
				"or the gate is now unfalsifiable -- it passes by finding nothing.",
				name, graceEscalationLogLine)
		}
	}
}

// repoRootForTest walks up from the package directory until it finds go.mod.
// Tests run with the package dir as cwd, and hardcoding "../.." breaks the
// moment this file moves.
func repoRootForTest(t *testing.T) string {
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
