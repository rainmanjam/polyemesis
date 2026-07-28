//go:build windows

package supervisor

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows has neither POSIX process groups nor signals. It has two mechanisms
// that together cover what proc_unix.go gets from setpgid + SIGTERM/SIGKILL,
// and polyemesis needs both for different reasons:
//
//   - CREATE_NEW_PROCESS_GROUP plus CTRL_BREAK_EVENT is the graceful half.
//     FFmpeg installs a console control handler, so a CTRL_BREAK runs the same
//     "finish up" path SIGTERM does: flush buffers, write the container index,
//     close the RTMP connection politely. That is the difference between a
//     playable recording and a truncated one.
//   - A job object with KILL_ON_JOB_CLOSE is the ungraceful half and the only
//     guarantee that survives polyemesis dying without running code. See
//     job_windows.go.
func setProcessGroup(cmd *exec.Cmd) {
	// The job must exist before the first child is spawned: job membership is
	// inherited at CreateProcess time and there is no retroactive fix for a
	// child that started outside it.
	ensureJob()
	applyProcessGroup(cmd)
}

// applyProcessGroup is split out so the flag arithmetic can be tested without
// touching the OS.
//
// CREATE_NEW_PROCESS_GROUP does two things at once. It gives the child a
// console process group of its own, which is what GenerateConsoleCtrlEvent
// addresses; and it means a Ctrl-C typed in the terminal is delivered only to
// polyemesis, so the supervisor gets to shut its children down in order rather
// than having them killed out from under it. Note that it does *not* detach
// the child from our console — it must keep sharing it, or the CTRL_BREAK
// below can never reach it.
func applyProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags = creationFlags(cmd.SysProcAttr.CreationFlags)
}

func creationFlags(existing uint32) uint32 {
	return existing | windows.CREATE_NEW_PROCESS_GROUP
}

// signalGroup asks the child's process group to finish up, mirroring the
// SIGTERM half of the Unix path. The caller escalates to killGroup after the
// grace period.
//
// The sharp edge: GenerateConsoleCtrlEvent is delivered through the *caller's*
// console, and a process running as a Windows service has no console at all.
// There the call fails immediately (typically ERROR_INVALID_HANDLE) and we
// terminate on the spot instead of waiting out a grace period nobody is going
// to use — a hard kill now is worse than a clean flush, but far better than an
// eight second hang followed by the same hard kill. The practical consequence
// is that recordings made by a console-less service install are truncated;
// closing that gap needs either a console allocated for the process or FFmpeg
// asked to quit over its stdin, and neither belongs in this file.
func signalGroup(cmd *exec.Cmd) {
	gid, ok := processGroupID(cmd)
	if !ok {
		return
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, gid); err != nil {
		killGroup(cmd)
	}
}

// killGroup is the escalation: TerminateProcess, unconditional and unignorable.
//
// Unlike its Unix twin this reaches only the child itself, not its
// descendants. FFmpeg does not fork helpers, and the job object is the backstop
// for anything that does; exact parity would need a nested job per child, which
// cannot be arranged without a post-Start hook in supervisor.go.
func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

// processGroupID returns the console process group to signal. A child created
// with CREATE_NEW_PROCESS_GROUP has a group ID equal to its own PID, so there
// is nothing to look up — but the guard is load-bearing, not defensive
// bookkeeping: GenerateConsoleCtrlEvent with a group of 0 means "every process
// attached to this console", which would take polyemesis itself down along
// with every other supervised child.
func processGroupID(cmd *exec.Cmd) (uint32, bool) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return 0, false
	}
	return uint32(cmd.Process.Pid), true
}
