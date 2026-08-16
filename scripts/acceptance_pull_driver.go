//go:build ignore

// Driver for scripts/acceptance-pull.sh.
//
// Pull inverts the ingest: polyemesis dials a source rather than waiting for an
// encoder. Everything downstream -- probing, the relay, per-destination routing
// -- is supposed to be identical once the bytes arrive, and this driver exists
// to prove that rather than assume it.
//
//	(no subcommand)  set up, switch to pull, create two destinations
//	tracks           print how many audio tracks were probed
//	stopall          stop every destination so its file finalises
//	escape           try a file:// source outside the data directory
//
// The session, the shared subcommands and the escape check itself live in
// pullsynthhelpers.go, named on the `go run` line. What is left here is what
// makes this suite the PULL suite rather than the synth one.
package main

const (
	pass = "PullAcceptance!9x"

	// A real programme: two audio tracks, so track 1 below has something to
	// select and the per-destination routing is actually under test.
	pullURL = "file://recordings/loop.ts"

	pullModeDone = "PULL_MODE_OK"
	destsDone    = "DESTS_OK"
)

// Two destinations off one ingest, each selecting a DIFFERENT track. One would
// prove the pull path delivers bytes; two prove it still routes per
// destination, which is the thing pull could plausibly break.
var destFixtures = []destSpec{
	{"pullA", "pullA.mkv", 0},
	{"pullB", "pullB.mkv", 1},
}

func main() {
	run("acceptance_pull_driver.go", func(cmd string) bool {
		if cmd != "escape" {
			return false
		}
		login()
		escape()
		return true
	})
}
