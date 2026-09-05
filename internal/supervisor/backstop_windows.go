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
func EnsureCrashBackstop() { ensureJob() }
