//go:build windows

package supervisor

import "golang.org/x/sys/windows"

// alive reports whether the OS still knows about pid.
//
// Windows has no signal 0. Waiting zero milliseconds on the process handle is
// the equivalent: the handle is signalled the moment the process exits, so
// WAIT_TIMEOUT means "still running". GetExitCodeProcess would be the obvious
// alternative but cannot distinguish a live process from one that exited with
// STILL_ACTIVE (259).
//
// Once the supervisor has reaped the child, its last handle is closed and the
// pid stops resolving, so OpenProcess failing also means dead.
func alive(pid int) bool {
	h, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	event, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return event == uint32(windows.WAIT_TIMEOUT)
}
