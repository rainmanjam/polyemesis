//go:build !windows

package supervisor

import "syscall"

// alive reports whether the OS still knows about pid. Signal 0 performs the
// permission and existence checks without delivering anything.
//
// LEAK-CHECK ONLY. Never a postcondition of Stop; the name promises more than
// the call delivers, and there are two ways it lies:
//
//   - A ZOMBIE ANSWERS TRUE. A child that has exited but not been reaped still
//     has a pid the kernel knows about, so this reports "alive" for a process
//     that is dead in every sense the caller cares about.
//   - PID REUSE ANSWERS TRUE ABOUT SOMEBODY ELSE. Once the child is reaped the
//     kernel may hand that number to an unrelated process at any moment, and
//     this cannot tell the difference.
//
// Its one honest use is the failing direction: if it still says true well
// inside a bound that only a real kill could meet, something leaked. When the
// question is "was the child reaped", the answer is a nil error from Stop --
// which entails cmd.Wait() returned -- plus the pipe-EOF timing assertion in
// TestStopReapsTheChildWhenItHasTimeTo, which observes the same fact without
// naming a pid and so cannot be told either lie above. Never this. (If this is
// ever renamed, pidExists is the accurate name: it reports existence, not
// identity.)
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
