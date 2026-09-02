package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/engine"
)

// THE PROCESS'S SHUTDOWN BUDGET MUST STAY UNDER WHAT systemd WAITS FOR.
//
// Two numbers in two file formats decide whether a recording is finalised or
// truncated, and nothing connects them: engine.ShutdownBudget in Go, and
// TimeoutStopSec in the unit file -- which exists TWICE, once as the shipped
// deploy/polyemesis.service and once inside the heredoc scripts/install.sh
// writes. Raise the Go constant, or lower either unit, and the process starts
// being SIGKILLed mid-teardown.
//
// The failure is silent and it is the worst shape available: a killed recorder
// leaves a Matroska file with no trailer, at exactly the size a reader would
// call plausible, and nothing logs it as an error. That is why this is pinned
// rather than commented. #645.
//
// Warning rung. Control would mean generating the unit from the constant,
// which is a real design -- and a larger change than two numbers warrant while
// they can be checked in a second.

// unitsWithStopTimeout are every file declaring TimeoutStopSec for this
// service. A check that covers some of the units is worse than none, because
// it reads as coverage: the one it misses is the one that ships.
var unitsWithStopTimeout = []string{
	filepath.Join("deploy", "polyemesis.service"),
	filepath.Join("scripts", "install.sh"),
}

func TestShutdownBudgetStaysUnderEveryTimeoutStopSec(t *testing.T) {
	root := repoRootFromTest(t)
	re := regexp.MustCompile(`(?m)^\s*TimeoutStopSec=(\d+)`)

	for _, rel := range unitsWithStopTimeout {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("reading %s: %v", rel, err)
			continue
		}
		matches := re.FindAllStringSubmatch(string(b), -1)
		if len(matches) == 0 {
			t.Errorf("%s declares no TimeoutStopSec.\n"+
				"If the unit stopped setting one, systemd's 90s default applies and this "+
				"check is measuring nothing -- update unitsWithStopTimeout in the same commit.", rel)
			continue
		}
		for _, m := range matches {
			secs, err := strconv.Atoi(m[1])
			if err != nil {
				t.Errorf("%s: TimeoutStopSec=%q is not a number", rel, m[1])
				continue
			}
			unit := time.Duration(secs) * time.Second
			if engine.ShutdownBudget >= unit {
				t.Errorf("%s: TimeoutStopSec=%ds, but engine.ShutdownBudget is %s.\n"+
					"The process would still be tearing down when systemd SIGKILLs the "+
					"cgroup, and a recorder killed mid-write leaves a truncated file at "+
					"exactly the right size on disk, with nothing in the log. Lower the "+
					"budget or raise the unit.", rel, secs, engine.ShutdownBudget)
			}
			if got := unit - engine.ShutdownBudget; got < engine.StopMargin {
				t.Errorf("%s: only %s between engine.ShutdownBudget (%s) and "+
					"TimeoutStopSec=%ds; StopMargin asks for %s.\n"+
					"That margin is the time systemd needs to observe a clean exit and the "+
					"time our own last log lines take to flush -- it is headroom, not slack.",
					rel, got, engine.ShutdownBudget, secs, engine.StopMargin)
			}
		}
	}
}

func TestTheShutdownBudgetIsNotAbsurd(t *testing.T) {
	// A budget of a few milliseconds would satisfy the comparison above and
	// kill every child instantly. internal/engine/manager.go records the
	// measurement this floor comes from: SIGTERM with the input still flowing
	// exits in 0.105s, and a recorder finalising a large file needs more.
	if engine.ShutdownBudget < 10*time.Second {
		t.Errorf("engine.ShutdownBudget is %s, which is not long enough for a "+
			"recorder to write its trailer", engine.ShutdownBudget)
	}
}
