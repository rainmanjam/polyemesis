package main

import "testing"

/* THE WHOLE JOB OF THIS PROGRAM IS ONE PREDICATE, and getting it wrong in
 * either direction destroys it. Missing a real abort means #440's third
 * occurrence goes unrecorded like the first two nearly did; flagging an
 * ordinary timeout as heap corruption teaches the next reader to ignore the
 * annotation, which costs the same thing more slowly. */
func TestIsRuntimeAbort(t *testing.T) {
	aborts := []string{
		// The three messages #440 has actually produced.
		"fatal error: found pointer to free object",
		"fatal error: s.allocCount != s.nelems && freeIndex == s.nelems",
		"fatal error: unexpected signal during runtime execution",
		// Indented inside `go test` output rather than at column zero.
		"    fatal error: found pointer to free object",
	}
	for _, line := range aborts {
		if !isRuntimeAbort(line) {
			t.Errorf("a runtime abort was not recognised, so it would be read as a "+
				"flaky test and go unrecorded: %q", line)
		}
	}

	ordinary := []string{
		// Go's own test timeout comes through the same channel and is an
		// ordinary failure with an ordinary explanation.
		"fatal error: test timed out after 15m0s",
		"fatal error: all goroutines are asleep - deadlock!",
		"fatal error: stack overflow",
		"fatal error: concurrent map writes",
		// A panicking test is not a runtime throw.
		"panic: runtime error: index out of range [3] with length 2",
		"--- FAIL: TestSomething (0.11s)",
		"ok  	github.com/rainmanjam/polyemesis/internal/engine	41.448s",
		"",
	}
	for _, line := range ordinary {
		if isRuntimeAbort(line) {
			t.Errorf("an ordinary failure was flagged as #440; a few of these and "+
				"the annotation becomes noise the next reader skips: %q", line)
		}
	}
}

/* THE CONTROL CASE. A predicate that answered false to everything would pass
 * the second half above and silently do nothing for ever, which is the exact
 * failure mode -- an instrument that reports nothing looks identical to a
 * codebase with no problem. */
func TestThePredicateCanStillSayYes(t *testing.T) {
	if !isRuntimeAbort("fatal error: found pointer to free object") {
		t.Fatal("the predicate recognises nothing at all")
	}
}
