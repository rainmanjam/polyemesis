package engine

import "time"

// ONE DEADLINE FOR THE WHOLE SHUTDOWN, BECAUSE systemd ONLY GIVES US ONE.
//
// deploy/polyemesis.service sets TimeoutStopSec=45. What used to sit under
// that number was not one budget but four, each chosen on its own and added
// together by the sequence they run in:
//
//	20s  http server Shutdown            (cmd/polyemesis/main.go)
//	 5s  lifecycle drain                 (internal/api)
//	30s  PER ENGINE, and engines stopped one after another
//	     a captioner wait that took no context at all
//
// A single wedged child on a two-programme install therefore reached past 45s
// without anything in the process believing it had overrun. systemd then
// SIGKILLs the whole cgroup, and internal/supervisor/grace.go states what that
// costs in as many words: a recording killed mid-write is a truncated Matroska
// file that is exactly the right size on disk. Nothing reports it. The
// operator finds out when they play it back.
//
// So the number below is the process's ONLY shutdown budget. Every phase draws
// from the same context, and the phases that used to own a constant now take
// a deadline from their caller.
//
// It is deliberately less than TimeoutStopSec: the margin is the time systemd
// must be left to observe that we exited on our own, plus the time our own
// last log lines take to flush. Exceeding it means systemd kills us, which is
// the outcome this exists to prevent -- so the margin is real headroom, not
// slack to be reclaimed.
//
// internal/testenv/shutdown_budget_test.go asserts this stays under the
// TimeoutStopSec in both the shipped unit and the one install.sh writes. #645.
const ShutdownBudget = 35 * time.Second

// StopMargin is what ShutdownBudget leaves to systemd. Exported so the guard
// test can state the relationship rather than restate the numbers.
const StopMargin = 10 * time.Second
