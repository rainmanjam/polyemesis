package main

import "github.com/rainmanjam/polyemesis/internal/supervisor"

// THE CRASH BACKSTOP IS ESTABLISHED BEFORE main's FIRST STATEMENT. It is an
// init() rather than a line part-way down run(), because the ORDER is the whole
// of the property and a line in run() had nothing holding it in place except a
// comment. #723.
//
// WHAT MAKES THE ORDER LOAD-BEARING. On Windows the backstop is a job object
// with KILL_ON_JOB_CLOSE, and membership is INHERITED: since Windows 8 a child
// joins its creator's job at the instant it is created, so a child created
// before the job exists is not in it, and nothing here puts it in afterwards.
// AssignProcessToJobObject can enrol a process that is already running --
// ensureJob calls it on THIS process, which is how polyemesis gets inside its
// own job -- but polyemesis keeps no handle to a child it spawned before the
// job existed, os/exec offers no post-Start hook to do the enrolment at the
// right moment (job_windows.go says so), and an assignment after the fact would
// still miss whatever that child had already spawned. So an early child is
// outside the job PERMANENTLY -- not until the next spawn, not until startup
// finishes, permanently. It survives a crash of polyemesis holding its ingest
// port bound and its RTMP connection to the platform open, so the restarted
// server cannot rebind and the platform still believes the old stream is live.
//
// That is the same END STATE as #448's thirteen orphaned encoders, the oldest of
// them five and a half days old -- but not the same cause, and not that
// incident. #448 was Unix, and the mechanism was setpgid: a supervised child
// given a process group of its own is also detached from a signal aimed at
// polyemesis's group, which proc_unix.go records at length. This Windows window
// has orphaned nothing yet; backstop_windows.go says so in as many words. It is
// closed here because "the crash backstop covers everything, provided a
// broadcast happened first" is not a property anybody can hold in their head.
//
// WHY A COMMENT WAS NOT ENOUGH, WHICH IS THE REASON THIS FILE EXISTS. The call
// used to sit in run() under a comment reading "BEFORE THE FIRST CHILD OF ANY
// KIND". Anything inserted above it -- an FFmpeg probe, a codec detection, a
// version check, a capability sniff that looked too small to think about -- was
// silently outside the job. Nothing could report it: the Unix implementation of
// EnsureCrashBackstop is a documented no-op, so on the machines this is
// developed and tested on the violation has no symptom whatsoever. Moving the
// call below ffmpeg.Detect, or adding a spawn above it, broke no test. The
// device that comment was standing in for is this init function.
//
// WHY init() IS SAFE THIS EARLY, which had to be answered before the ordering
// could be bought at all, because init runs before flag.Parse and before run()
// builds a logger. EnsureCrashBackstop takes no arguments and reads no
// configuration: on Windows it creates a handle and assigns this process to it,
// and on Unix it does nothing. So there is no flag it could have wanted. Nor is
// there a logger it loses: ensureJob reports a failure through slog.Default(),
// and nothing in this program calls slog.SetDefault, so a warning from here
// lands in exactly the same place it landed when the call was in run().
//
// WHAT IT COSTS, STATED AGAINST WHAT run() ACTUALLY DID. The old call sat below
// flag.Parse, below the config load and below cfg.Validate/EnsureDirs, but ABOVE
// the -verify-backup and -reset-admin branches -- so those two already created
// the job and are not new. What is new is everything that used to return or fail
// above the call: `polyemesis -version`, which returns immediately after
// flag.Parse; any run that dies in flag parsing or config validation; and every
// test binary built from this package. A job object is one handle and one limit
// structure; the exit that immediately follows closes it and terminates nothing,
// because nothing was ever spawned into it. That is a fair price for an ordering
// that no edit to main() or run() can get in front of.
//
// THE ONE HOLE LEFT IS GUARDED, NOT DESCRIBED. Go initialises an imported
// package fully before the importing one, and within any package runs the
// package-level variable initializers before that package's init functions --
// so a spawn from either place, in an imported package or in package main
// itself, would precede this init even now.
// TestNothingSpawnsFromAPackageInitializer in backstop_order_test.go refuses
// that, and its reach is worth stating exactly rather than as "everywhere": it
// walks every non-test .go file under the module root apart from dot-directories
// and node_modules/web/ui/dist/vendor/testdata, and it recognises a spawn as a
// selector call on `exec` whose name starts with "Command" -- the same match
// internal/childcensus uses, and no wider. Its companion,
// TestTheCrashBackstopIsEstablishedBeforeMainRuns, fails if this call is written
// anywhere in package main other than an init, and fails first if package main
// stops calling it at all.
//
// Both are tests, so both are Warning rather than Control: they fire when the
// suite runs, not when the line is typed. Control for the second one is what
// this init() already gives inside main() and run(); Control for the first would
// need Go to let a package say "no initializer in my import graph may spawn",
// which the language cannot express.
func init() { supervisor.EnsureCrashBackstop() }
