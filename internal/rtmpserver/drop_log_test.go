package rtmpserver

import "testing"

// A DROP COUNTER THAT CANNOT FIRE IS WORSE THAN NO COUNTER.
//
// The gate is `dropped % every == 1 % every`. It used to be `== 1`, which is
// never true when every == 1 -- so the most natural setting for "log every
// drop" logged nothing, and #674 read the resulting silence as proof the RTMP
// server was not dropping messages. It was proof the instrument was mute.
func TestEveryDropIsLoggedWhenTheIntervalIsOne(t *testing.T) {
	fired := 0
	for dropped := 1; dropped <= 10; dropped++ {
		if shouldLogDrop(dropped, 1) {
			fired++
		}
	}
	if fired != 10 {
		t.Fatalf("interval 1 logged %d of 10 drops.\n"+
			"POLYEMESIS_RTMP_DROP_LOG=1 must mean EVERY drop. When it means none, a run\n"+
			"that is shedding audio for its whole life reports a clean zero, which is the\n"+
			"vacuous measurement this repo's whole audit exists to remove.", fired)
	}
}

func TestALargerIntervalStillSamples(t *testing.T) {
	var at []int
	for dropped := 1; dropped <= 30; dropped++ {
		if shouldLogDrop(dropped, 10) {
			at = append(at, dropped)
		}
	}
	if len(at) != 3 || at[0] != 1 || at[1] != 11 || at[2] != 21 {
		t.Fatalf("interval 10 fired at %v, want [1 11 21] -- the every-Nth behaviour must be unchanged", at)
	}
}
