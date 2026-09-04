package testenv

import (
	"strings"
	"testing"
)

// A GUARD ON THE GUARD. #661 asks for this in as many words: "whatever is built
// must fail when the comparison stops happening -- the audit's recurring finding
// is checks that report and gate nothing."
//
// db.RenditionConcerns can be perfectly correct and perfectly useless: if
// nothing calls it, every unit test for it still passes and no operator is ever
// warned. That is the exact shape the readiness audit kept finding -- a
// protective device that exists and is not connected to the thing it protects,
// which is what #661 IS. A guard that only tested the comparison would repeat it.
//
// Detection rung, and it cannot be more from inside the repository: it reads the
// source for the call. Deleting the call while leaving the function makes this
// fail, which is the case worth catching, because it is the one that leaves
// every other test green.
func TestTheRenditionGuidanceComparisonIsActuallyCalled(t *testing.T) {
	root := repoRootFromTest(t)

	engine := mustReadRepoFile(t, root, "internal/engine/videocodec.go")
	if !strings.Contains(engine, "db.RenditionConcerns(") {
		t.Fatal("nothing in internal/engine calls db.RenditionConcerns.\n\n" +
			"The catalogue in platforms.go was researched, dated and sourced precisely " +
			"so figures would not be guesses -- and #661 is that nothing read it. A " +
			"comparison nobody calls reproduces the bug it was written to fix, with a " +
			"full set of passing tests on top.")
	}

	dests := mustReadRepoFile(t, root, "internal/engine/destinations.go")
	if !strings.Contains(dests, "warnRenditionAgainstPlatform(") {
		t.Fatal("startDest no longer calls warnRenditionAgainstPlatform.\n\n" +
			"Destination start is where a rendition is attached to a platform, and so " +
			"the last moment an operator can be told before the platform tells them -- " +
			"by dropping the stream mid-broadcast.")
	}
}

// The same, for #627: the codec warning is only worth anything if the probe path
// still calls it.
func TestTheVideoCodecWarningIsActuallyCalled(t *testing.T) {
	root := repoRootFromTest(t)
	src := mustReadRepoFile(t, root, "internal/engine/engine.go")
	if !strings.Contains(src, "warnVideoCodec(") {
		t.Fatal("nothing calls warnVideoCodec.\n\n" +
			"The video codec is the ENCODER's choice and is only knowable at probe " +
			"time, so that call is the single moment an HEVC or AV1 ingest can be " +
			"reported before the platform rejects it. Without it the stream muxes " +
			"cleanly, uploads cleanly, and is rejected -- looking correct everywhere " +
			"the operator can see. #627.")
	}
}
