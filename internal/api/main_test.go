package api

import (
	"flag"
	"fmt"
	"os"
	"testing"
)

// THE PREFLIGHT. #161, for this package.
//
// The route coverage ledger is a ratchet that only ratchets while it RUNS, and
// `go test ./internal/api -run TestSomethingElse` ran none of it. That is the
// same shape as everything else this ledger exists to catch: a guard that is
// thorough over a set which excludes the moment it is needed. A `-run` filter
// is not malice; it is what everybody types while debugging one handler, and it
// silently turned every ledger obligation off.
//
// So the package's tests run TWICE. The first m.Run is forced to
// ^TestLedgerPreflight$ regardless of what the caller asked for; if it fails,
// the package fails and the caller's selection never runs. The second m.Run
// restores the caller's own -run and does what they asked.
//
// There is deliberately NO bypass: no flag, no environment variable, no -short
// escape. A preflight with an off switch is a preflight that is off. What keeps
// it honest in the other direction is its own liveness marker -- see
// ledgerPreflightMarker and the Makefile target that asserts the marker still
// prints under a -run that matches no test at all. The preflight's existence is
// proven by running something that fails if it stopped, which is the single
// rule this whole change is built on, turned back on the guard itself.
//
// Budget: measured at ~1.4s on go1.26.5/darwin-arm64 against a 42s package. The
// two FFmpeg counterpart proofs stay outside it and keep their own row in
// deferredWithReasons rather than being an implicit residual.
func TestMain(m *testing.M) {
	flag.Parse()

	runFlag := flag.Lookup("test.run")
	if runFlag == nil {
		// No testing flags registered at all. Run once and let the ordinary
		// machinery report whatever it reports; there is nothing to force.
		os.Exit(m.Run())
	}
	callerSelection := runFlag.Value.String()

	if err := runFlag.Value.Set("^TestLedgerPreflight$"); err != nil {
		fmt.Fprintf(os.Stderr, "preflight: cannot set -run: %v\n", err)
		os.Exit(1)
	}
	if code := m.Run(); code != 0 {
		fmt.Fprintln(os.Stderr,
			"THE ROUTE COVERAGE PREFLIGHT FAILED, so the tests you asked for did not run.\n"+
				"It runs before every invocation of this package precisely so that a -run "+
				"filter cannot switch the ledger off. Fix the failure above -- each message "+
				"names the route, the observed status, the byte count and the edit.")
		os.Exit(code)
	}
	if err := runFlag.Value.Set(callerSelection); err != nil {
		fmt.Fprintf(os.Stderr, "preflight: cannot restore -run %q: %v\n", callerSelection, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
