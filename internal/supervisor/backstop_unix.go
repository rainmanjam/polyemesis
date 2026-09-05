//go:build !windows

package supervisor

// EnsureCrashBackstop is a no-op on Unix, and the reason is worth stating
// rather than leaving as an empty function. #723.
//
// Windows needs an explicit object -- a job with KILL_ON_JOB_CLOSE -- because
// it has no notion of a process tree dying with its root. Unix gets the
// equivalent for free in the case that matters here: a child that has NOT been
// given a process group of its own stays in this process's group, so a signal
// aimed at the group reaches it, and a terminal's Ctrl-C reaches it directly.
//
// That is exactly why the spawners outside internal/supervisor do not call
// setProcessGroup, and it is a decision rather than an omission. See
// spawnPolicy in internal/childcensus, which is where every spawn site states
// which side of it they are on and why.
//
// The one case Unix does not cover is SIGKILL to polyemesis alone, which
// reaches nothing else. systemd's KillMode=mixed covers it in production;
// proc_unix.go records that, and records that Pdeathsig is deliberately not the
// answer.
func EnsureCrashBackstop() {}
