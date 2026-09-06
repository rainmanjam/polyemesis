//go:build windows

package supervisor

// EnsureCrashBackstop puts this process inside the job object that kills every
// child when polyemesis dies, whether or not it gets to run any code.
//
// WHY IT IS EXPORTED, AND WHY IT IS CALLED FROM main. #723.
//
// The job is the Windows answer to "no child outlives the parent", and it works
// by INHERITANCE: since Windows 8 a child joins its creator's job at
// CreateProcess time, so every FFmpeg, whisper and ffprobe this process spawns
// is inside it from the instant it exists -- whatever package spawned it, and
// whether or not that package knows the job exists.
//
// That inheritance is unconditional. Its PRECONDITION was not. ensureJob ran
// from setProcessGroup, which runs only on a supervisor spawn, so the job did
// not exist until the first supervised child started. A media transcode, a
// transcription worker or an upload probe on a server that had not yet gone
// live was therefore created OUTSIDE the job -- permanently, because membership
// is settled at CreateProcess and there is no retroactive fix for a child that
// started without it.
//
// Nothing had gone wrong yet. The window is real but narrow, and the ordinary
// install spawns an ingest early. It is closed here because "the crash backstop
// covers everything, provided a broadcast happened first" is not a property
// anybody can hold in their head, and because the fix is one call at startup.
//
// Idempotent: the work is behind a sync.Once, so calling it again costs a
// mutex. Safe to call before anything is spawned, which is the point.
//
// WHERE IT IS CALLED FROM, AND WHY THAT MOVED. It is an init() in package main,
// not a line in run(). Membership of the job is INHERITED at CreateProcess and
// nothing here enrols a child afterwards -- AssignProcessToJobObject can add a
// running process to a job, and ensureJob uses it on THIS process, but there is
// no post-Start hook in os/exec to do it for a child at the right moment, and a
// late assignment would still miss whatever that child had already spawned. So
// the call has to precede the first child of any kind -- and while it sat part-way
// down run() the only thing holding it above the first spawn was the comment
// over it. An init runs before main's first statement, so the ordering is now
// structural: nothing can be inserted in front of it by editing run(). This
// function is written to make that placement possible -- no arguments, no
// configuration, no logger it has not already got -- and cmd/polyemesis/
// backstop.go states the whole trade. Its companion guard,
// TestTheCrashBackstopIsEstablishedBeforeMainRuns, parses package main's
// non-test sources and fails if this call is written in any function OTHER than
// an init -- and fails first if package main has stopped calling it at all,
// since a package with no call satisfies "every call is in an init" perfectly.
func EnsureCrashBackstop() { ensureJob() }
