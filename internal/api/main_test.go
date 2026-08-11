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
// So the package's tests run TWICE. The FIRST m.Run is the caller's own
// selection, exactly as they typed it. The SECOND is forced to
// ^TestLedgerPreflight$ with -skip emptied and -count pinned to 1, regardless of
// what the caller asked for, and the process exits non-zero if EITHER pass did.
// The ledger therefore runs on every invocation of this package and no filter
// can switch it off -- which is the whole of #161 -- but it no longer runs
// FIRST, and that ordering is load-bearing for a reason that has nothing to do
// with the ledger. See THE ORDER, below.
//
// THE ORDER, and why it is this way round. #217/#223. Coverage counters are
// process-global, but the coverage PROFILE is written by testing.M.after, which
// is guarded by m.afterOnce (go1.26.5, src/testing/testing.go:2284, :2683,
// coverReport at :2751). after runs on a `defer` inside the first m.Run to
// return, so the profile is written when PASS ONE ends and the second pass's
// counters are never in it. With the preflight first, `go test -cover
// ./internal/api` reported the preflight's own execution and nothing else:
//
//	-run XXXNoSuchTestAtAll  -covermode=set  ->  22.0% of statements
//	-run TestUpload...Origin -covermode=set  ->  22.0% of statements
//	(unfiltered)             -covermode=set  ->  22.0% of statements
//
// Zero tests, one test and the whole suite, the same number -- which is not a
// coverage figure at all, it is a constant wearing one. Running the caller's
// selection first puts the caller's selection in the profile, which is what
// -cover is asked for; the forced preflight's own coverage is then the part that
// falls outside it. `make coverage-instrument-guard` (scripts/coverage-instrument-guard.sh)
// fails if those two numbers ever collapse back into each other.
//
// WHAT THE REORDER COSTS, stated rather than glossed. Under the old order a
// failing preflight meant the caller's tests never ran. It cannot mean that any
// more: by the time the preflight runs, they have. The package still goes red --
// the exit code is the first non-zero of the two passes -- but a developer whose
// ledger is broken now pays for their own test run before being told. That is
// the price of a measurable -cover on this package, and it is the only thing the
// old order bought that this one does not.
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
// So it runs ONCE per process: whichever pass drives TestLedgerPreflight's body
// records that in ledgerPreflightDone, and the other pass returns immediately,
// because the verdict was computed in this same process and a second computation
// cannot reach a different one. Under an unfiltered invocation that is now the
// FIRST pass doing the work and the forced second pass returning; under `-run
// TestSomethingElse` the first pass never selects it and the forced pass does the
// work. Either way, once. That once-per-process property is what the measurement
// below bought when it was introduced, and the #217 reorder moves which pass pays
// rather than how many do; the figures are kept as the record of that earlier
// change, `POLYEMESIS_LEDGER=strict go test -race -timeout 15m ./...`, median of
// three, and are NOT a measurement of the reorder:
//
//	origin/main (ae8df24)                301s   (300 / 303 / 301)
//	the run-it-twice version             314s   (315 / 314 / 313)   +4.3%
//	with ledgerPreflightDone             299s   (296 / 299 / 299)   -0.7%, inside the spread
//
// It is deliberately NOT a t.Skip: a skip is a test that did not run, and this
// one ran. If TestMain is ever removed there is only one pass, ledgerPreflightDone
// stays false through it, and the body runs in full there -- the fail-closed
// direction, at the cost of the -cover reorder going with it.
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
		// nothing; forcing a preflight alongside it would print the names
		// twice and prove nothing about the ledger.
		os.Exit(m.Run())
	}

	set := func(f *flag.Flag, v string) {
		if err := f.Value.Set(v); err != nil {
			fmt.Fprintf(os.Stderr, "preflight: cannot set %s=%q: %v\n", f.Name, v, err)
			os.Exit(1)
		}
	}
	// PASS ONE: the caller's own selection, with their flags untouched. It goes
	// first so that testing.M.after -- which writes the coverage profile once,
	// under m.afterOnce, on the way out of whichever m.Run returns first --
	// writes a profile of the tests the caller asked for. See THE ORDER above.
	code := m.Run()

	// PASS TWO: the preflight, forced, whatever the caller typed and whatever
	// pass one reported. An unfiltered pass one has already driven it and set
	// ledgerPreflightDone, so this pass costs nothing there; a filtered pass one
	// has not, and this is where the ledger actually runs.
	set(runFlag, "^TestLedgerPreflight$")
	set(skipFlag, "")
	set(countFlag, "1")
	if pre := m.Run(); pre != 0 {
		fmt.Fprintln(os.Stderr,
			"THE ROUTE COVERAGE PREFLIGHT FAILED. It is forced after every invocation of\n"+
				"this package, with -run, -skip and -count set aside, precisely so that a "+
				"filter cannot switch the ledger off -- so the tests you asked for DID run, "+
				"above, and this verdict is in addition to whatever they reported. Fix the "+
				"failure above -- each message names the route, the observed status, the "+
				"byte count and the edit.")
		if code == 0 {
			code = pre
		}
	}
	os.Exit(code)
}

// ledgerPreflightDone records that TestLedgerPreflight's body has already been
// DRIVEN in this process -- by either pass, and whatever verdict it reached. It
// is the only thing that keeps the other pass cheap.
//
// It is set by the test itself rather than by TestMain, because after the #217
// reorder TestMain cannot know whether pass one selected the preflight: that
// depends on the caller's own -run and -skip. It is set on the way out including
// the failing way out, so a red ledger is reported once rather than twice.
//
// It defaults to false, so losing TestMain costs a run of the full body in the
// caller's own pass rather than a silent hole.
var ledgerPreflightDone bool
