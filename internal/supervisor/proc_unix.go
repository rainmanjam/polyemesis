//go:build !windows

package supervisor

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in a new process group.
//
// WHAT IT BUYS: FFmpeg can spawn helpers of its own, and signalling the group
// reaches all of them, so stopping a destination stops its whole tree. It also
// means a Ctrl-C in the terminal is delivered only to polyemesis, which gets to
// shut its children down in order rather than having them killed out from under
// it mid-write -- the difference between a finalised recording and a truncated
// one.
//
// WHAT IT COSTS, and this comment used to claim the opposite. It said this "is
// what makes 'all child processes must die with the parent' true". It is what
// makes that FALSE. A new group is detached from every signal aimed at the
// supervisor's group, so an ABRUPT death of polyemesis -- SIGKILL, the OOM
// killer, a cancelled CI job -- signals nothing at all, and each group it
// created keeps running and reparents to init. Thirteen ffmpeg encoders were
// found on a shared host that way, the oldest running for five and a half days,
// each still reading a relay port whose owner no longer existed (#448).
//
// WHAT COVERS IT: systemd. The shipped unit sets KillMode=mixed, so the whole
// cgroup is SIGKILLed when the service stops for any reason. Every other way
// polyemesis is run -- a foreground developer run, the acceptance suites, a
// container with a shell entrypoint -- has no such backstop on Unix. Windows
// does, in job_windows.go.
//
// Pdeathsig is the Linux equivalent and is deliberately NOT set: in Go it fires
// when the creating THREAD exits rather than the process, so a runtime thread
// retirement could SIGKILL a live destination mid-broadcast. Killing a viewer's
// stream at random is a worse failure than leaking a process on a box that is
// about to be torn down anyway.
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
