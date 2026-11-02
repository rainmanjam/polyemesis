//go:build windows

package jobs

import (
	"os/exec"
	"testing"
)

// On Windows there is no nice: a background child is created in the
// BELOW_NORMAL priority class instead, which is what makes "clip encodes yield
// to the live stream" true there.
//
// MUTATION: LowerPriority leaves CreationFlags alone. Observed: FAIL.
func TestLowerPriorityCreatesTheChildBelowNormal(t *testing.T) {
	cmd := exec.Command("ffmpeg", "-version")
	LowerPriority(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&belowNormalPriorityClass == 0 {
		t.Fatalf("SysProcAttr %+v carries no BELOW_NORMAL_PRIORITY_CLASS", cmd.SysProcAttr)
	}
}
