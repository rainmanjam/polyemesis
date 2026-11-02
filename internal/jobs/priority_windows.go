//go:build windows

package jobs

import (
	"os/exec"
	"syscall"
)

// belowNormalPriorityClass is BELOW_NORMAL_PRIORITY_CLASS from the Win32
// process-creation flags.
const belowNormalPriorityClass = 0x00004000

// LowerPriority asks the OS to run a background child below normal priority.
//
// Windows has no nice for the governor's NiceCommand to put in front of the
// command, so the child is CREATED in the BELOW_NORMAL priority class instead:
// a clip encode or a loudness pass then yields the CPU to the live encoders,
// which run at normal priority.
func LowerPriority(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= belowNormalPriorityClass
}
