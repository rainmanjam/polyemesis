//go:build !windows

package supervisor

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in a new process group.
//
// This is what makes "all child processes must die with the parent" true:
// FFmpeg can itself spawn helpers, and signalling the group reaches them all.
// It also means a Ctrl-C in the terminal is delivered only to polyemesis, so
// the supervisor gets to shut its children down in order rather than having
// them killed out from under it.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalGroup asks the whole group to terminate. FFmpeg treats SIGTERM as
// "finish up": it flushes buffers and finalises the output file, which is the
// difference between a playable recording and a truncated one.
func signalGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative PID means "the process group with this ID".
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

// killGroup is the escalation: unconditional, unignorable.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
