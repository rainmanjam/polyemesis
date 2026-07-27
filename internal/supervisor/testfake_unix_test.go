//go:build !windows

package supervisor

import "syscall"

// alive reports whether the OS still knows about pid. Signal 0 performs the
// permission and existence checks without delivering anything.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
