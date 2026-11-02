//go:build !windows

package jobs

import (
	"os/exec"
	"testing"
)

// Off Windows the priority comes from the nice/ionice wrapper the governor
// puts in front of the command, so LowerPriority leaves the process attributes
// untouched.
func TestLowerPriorityLeavesTheWrapperToDoItOffWindows(t *testing.T) {
	cmd := exec.Command("ffmpeg", "-version")
	LowerPriority(cmd)
	if cmd.SysProcAttr != nil {
		t.Errorf("LowerPriority set %+v off Windows", cmd.SysProcAttr)
	}
}
