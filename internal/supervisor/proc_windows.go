//go:build windows

package supervisor

import (
	"os/exec"
	"syscall"
)

// Windows has no process groups in the POSIX sense. CREATE_NEW_PROCESS_GROUP
// gives us the closest equivalent; polyemesis is developed and deployed on
// Unix, so this exists to keep `go build ./...` honest across platforms
// rather than as a supported target.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

func signalGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
