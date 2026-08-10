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
// restores the caller's own selection and does what they asked.
//
// WHAT IS NEUTRALISED, EXACTLY. The previous version of this comment said
// "there is deliberately NO bypass: no flag, no environment variable, no -short
// escape", and that sentence was FALSE when it was written. Go has three
// switches that decide whether a test runs, and TestMain only overrode one:
//
//	-run     forced to ^TestLedgerPreflight$ for the first pass. Was handled.
//	-skip    forced EMPTY for the first pass. Was NOT handled, and
//	         `go test ./internal/api -skip TestLedgerPreflight` reported ok in
//	         41.7s with a registry that failed seven ways.
//	-count   forced to 1 for the first pass. Was NOT handled, and
//	         `go test ./internal/api -count=0` ran nothing at all and printed
//	         ok in 0.2s.
//
// A comment asserting a guarantee the harness does not impose is the exact
// species #150 deleted four of, so the list above enumerates the mechanism
// rather than claiming a property. The one switch NOT neutralised is -list,
// which prints test names and runs nothing; it is short-circuited below instead,
// because forcing a preflight in front of a listing would print the list twice
// and a listing makes no claim about anything passing.
//
// What keeps the whole thing honest in the other direction is its own liveness
// marker -- see ledgerPreflightMarker and `make preflight-guard`, which asserts
// the marker still prints under each of the three switches above. That target
// now runs in CI (.github/workflows/ci.yml, the `go` job) rather than only on a
// developer's machine: a gate nothing invokes is not a gate, and CI invokes
// `go test` directly, never `make test`.
//
// BUDGET, measured in the configuration CI ACTUALLY RUNS rather than the cheap
// one. go1.26.5/darwin-arm64, `go test ./internal/api -run XXXNoSuchTest`, which
// runs the preflight and nothing else:
//
//	plain                                 1.4s   (1.57 / 1.45 / 1.37)
//	POLYEMESIS_LEDGER=strict, -race      22.2s   (22.28 / 22.18 / 22.08)
//
// The strict number is the real one, because ci.yml line 146 is
// `POLYEMESIS_LEDGER=strict go test -race -timeout 15m ./...` and strict mode
// runs the counterpart proofs -- two of which spawn an FFmpeg stand-in -- from
// inside the preflight. The first version of this change reported "+2%" from the
// plain configuration and never measured the strict one, where the preflight ran
// TWICE per unfiltered invocation and cost 22.2s each time.
//
// So it runs ONCE per process now: TestLedgerPreflight returns immediately in
// the second pass, because the first pass already computed every verdict in this
// same process and a second computation cannot reach a different one. Measured
// end to end, `POLYEMESIS_LEDGER=strict go test -race -timeout 15m ./...`,
// median of three:
//
//	origin/main (ae8df24)   301s   (300 / 303 / 301)
//	before this commit      314s   (315 / 314 / 313)   +4.3%
//	after                   299s   (296 / 299 / 299)   -0.7%, inside the spread
//
// It is deliberately NOT a t.Skip: a skip is a test that did not run, and this
// one ran. If TestMain is ever removed, ledgerPreflightDone stays false and the
// second pass runs it in full, which is the fail-closed direction.
func TestMain(m *testing.M) {
	flag.Parse()

	runFlag := flag.Lookup("test.run")
	skipFlag := flag.Lookup("test.skip")
	countFlag := flag.Lookup("test.count")
	listFlag := flag.Lookup("test.list")
	if runFlag == nil || skipFlag == nil || countFlag == nil {
		// No testing flags registered at all. Run once and let the ordinary
		// machinery report whatever it reports; there is nothing to force.
		os.Exit(m.Run())
	}
	if listFlag != nil && listFlag.Value.String() != "" {
		// A listing, not a run. It executes no test and therefore claims
		// nothing; forcing a preflight in front of it would print the names
		// twice and prove nothing about the ledger.
		os.Exit(m.Run())
	}

	set := func(f *flag.Flag, v string) {
		if err := f.Value.Set(v); err != nil {
			fmt.Fprintf(os.Stderr, "preflight: cannot set %s=%q: %v\n", f.Name, v, err)
			os.Exit(1)
		}
	}
	callerRun, callerSkip := runFlag.Value.String(), skipFlag.Value.String()
	callerCount := countFlag.Value.String()

	set(runFlag, "^TestLedgerPreflight$")
	set(skipFlag, "")
	set(countFlag, "1")
	if code := m.Run(); code != 0 {
		fmt.Fprintln(os.Stderr,
			"THE ROUTE COVERAGE PREFLIGHT FAILED, so the tests you asked for did not run.\n"+
				"It runs before every invocation of this package precisely so that a -run, "+
				"-skip or -count filter cannot switch the ledger off. Fix the failure above "+
				"-- each message names the route, the observed status, the byte count and "+
				"the edit.")
		os.Exit(code)
	}
	ledgerPreflightDone = true

	set(runFlag, callerRun)
	set(skipFlag, callerSkip)
	set(countFlag, callerCount)
	os.Exit(m.Run())
}

// ledgerPreflightDone records that the first pass above completed successfully,
// in THIS process. It is the only thing that makes the second pass cheap, and it
// defaults to false so that losing TestMain costs a duplicate run rather than a
// silent hole.
var ledgerPreflightDone bool
