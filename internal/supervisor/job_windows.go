//go:build windows

package supervisor

import (
	"log/slog"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A single job object owns every FFmpeg that polyemesis spawns.
//
// This is the Windows answer to "no child outlives the parent", and unlike the
// CTRL_BREAK path in proc_windows.go it does not depend on polyemesis getting
// a chance to run any code. If we are killed from Task Manager, panic, or are
// torn down by the service control manager, the OS closes our handles; the
// job's last handle goes with them; KILL_ON_JOB_CLOSE then terminates
// everything still inside.
//
// The consequence of not having this is worse than the phrase "orphaned
// process" implies. An orphaned FFmpeg keeps its ingest port bound and its
// RTMP connection to YouTube or Twitch open, so a restarted polyemesis cannot
// rebind and the platform still believes the old stream is live. That is the
// exact failure the Unix process-group code exists to prevent.
var (
	jobOnce sync.Once
	// jobHandle is deliberately never closed: the handle *is* the guarantee,
	// and it has to outlive every child. The OS closes it when this process
	// exits, which is precisely the moment the children should die.
	jobHandle windows.Handle
)

// ensureJob creates the job and puts polyemesis itself inside it. It is safe
// to call on every spawn; the work happens once.
//
// Self-assignment is what makes the guarantee race-free. Since Windows 8 a
// child inherits its creator's job at CreateProcess time, so every FFmpeg is
// inside the job from the instant it exists. Assigning children individually
// after exec.Cmd.Start would instead leave a window — short, but real — in
// which a freshly spawned encoder and anything it spawned are outside the job
// and would survive a crash. That window is unavoidable from here anyway:
// os/exec offers no post-Start hook and setProcessGroup runs before Start.
//
// Putting ourselves in a KILL_ON_JOB_CLOSE job is safe precisely because the
// only thing that can fire that limit is our own handles closing, which cannot
// happen while we are alive.
func ensureJob() {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			slog.Default().Warn("could not create job object; FFmpeg children will survive a polyemesis crash", "err", err)
			return
		}

		// CreateJobObject with nil attributes yields a non-inheritable handle,
		// which matters: if children held a handle to the job, the job would
		// stay alive after we died and KILL_ON_JOB_CLOSE would never fire.
		info := jobLimits()
		if _, err := windows.SetInformationJobObject(
			h,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			runtime.KeepAlive(&info)
			_ = windows.CloseHandle(h)
			slog.Default().Warn("could not set job object limits; FFmpeg children will survive a polyemesis crash", "err", err)
			return
		}
		// KEPT ALIVE ACROSS THE CALL, and this is not decoration.
		//
		// unsafe's rules allow Pointer -> uintptr only inside the argument list of
		// the syscall itself, because that is the one place the compiler and the
		// runtime treat the value as keeping the object alive.
		// windows.SetInformationJobObject is an ordinary Go function that happens
		// to take a uintptr, so at that boundary the only reference to info is an
		// integer: the compiler may treat info as dead from the conversion onward,
		// and the collector may reclaim it before the wrapper reaches the real
		// syscall. The kernel would then write job limits into memory the runtime
		// has already recycled, and the corruption surfaces later, somewhere with
		// nothing to do with job objects.
		//
		// No tool here catches it. `go vet ./...` runs on Linux, where this file
		// is not built; and GOOS=windows go vet is clean too, because vet's
		// unsafeptr check looks for uintptr -> Pointer, the opposite direction.
		// See #440, which records a "found pointer to free object" on a Windows
		// runner that this may or may not explain -- the violation is real either
		// way.
		runtime.KeepAlive(&info)

		if err := windows.AssignProcessToJobObject(h, windows.CurrentProcess()); err != nil {
			// Nesting one job inside another needs Windows 8. On anything
			// older, or under a harness whose own job forbids nesting, we lose
			// the crash backstop; the graceful CTRL_BREAK path is unaffected,
			// so degrade loudly rather than refusing to start.
			_ = windows.CloseHandle(h)
			slog.Default().Warn("could not assign polyemesis to a job object; FFmpeg children will survive a polyemesis crash", "err", err)
			return
		}

		jobHandle = h
	})
}

// jobLimits is the one limit that matters: when the last handle to the job
// closes, the OS terminates every process still inside it.
//
// Note what is deliberately absent. JOB_OBJECT_LIMIT_BREAKAWAY_OK and
// JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK would let a child opt out of the job
// with CREATE_BREAKAWAY_FROM_JOB, which is the exact escape hatch this exists
// to close. No memory or CPU limits either: the job is a lifetime mechanism
// here, not a resource cage, and a limit that killed a healthy encoder
// mid-stream would be a far worse bug than the one being fixed.
func jobLimits() windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	return info
}
