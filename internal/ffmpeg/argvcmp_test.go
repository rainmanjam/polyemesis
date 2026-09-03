package ffmpeg

import "testing"

// THE REPRODUCTION MUST DRIVE THE SHIPPED ARGV, MINUS THE TRANSPORT.
//
// internal/rtmpserver's remux reproduction runs IngestArgs and then rewrites
// the LAST argument to a file path (tsOutput), because a test cannot easily
// read back a UDP relay. That is only honest while the output URL is the sole
// difference: the moment IngestArgs grows an option that depends on the output
// being a file or a socket -- or stops putting the URL last -- the test starts
// exercising a command line that is not the one that ships, and a green run
// stops meaning anything about production.
//
// This pins that. It is also the fact the #674 investigation needed and did not
// have: the remux passes to a FILE on ffmpeg 8.1.2 with the interleave flags,
// while the rig writing the same stream to UDP still carries no audio. Knowing
// the argv differ in exactly one place is what makes that a transport question
// rather than a guess.
func TestTheIngestArgvDiffersOnlyInItsOutputURL(t *testing.T) {
	spec := IngestSpec{
		Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live", RTMPAddress: "mt",
		RelayURL: "udp://127.0.0.1:21000",
	}
	rig := IngestArgs(spec)
	if len(rig) == 0 {
		t.Fatal("IngestArgs returned nothing")
	}

	// The URL must be LAST, because that is the position tsOutput rewrites.
	if got := rig[len(rig)-1]; got != RelayOutputURL(spec.RelayURL) {
		t.Fatalf("the relay URL is not the final argument (last arg is %q).\n"+
			"internal/rtmpserver/ingest_remux_test.go rewrites the last argument to a file "+
			"path to read the remux back. If the URL moves, that test silently rewrites some "+
			"OTHER option and stops reproducing anything.", got)
	}

	// And nothing before it may differ between the two.
	test := append([]string(nil), rig...)
	test[len(test)-1] = "/tmp/relay.ts"
	for i := 0; i < len(rig)-1; i++ {
		if rig[i] != test[i] {
			t.Fatalf("argv differ at %d before the output URL: rig=%q test=%q", i, rig[i], test[i])
		}
	}
}
