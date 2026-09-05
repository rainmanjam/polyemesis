package main

import (
	"github.com/rainmanjam/polyemesis/internal/childcensus"
	"strings"
	"testing"
	"time"
)

// #631 was found by somebody running `ps` on a production host and noticing an
// ffmpeg whose ppid was the live server -- three weeks and 53 escalations after
// it began. Nothing inside the program could see it, because a child whose
// handle has been lost is absent from every map, every status page and every
// log line, and present only in the process table.
//
// This is the line that would have said it on the first occurrence. What it has
// to get right is small and entirely about the words: name the child, name the
// pid, and say that nothing is coming back for it -- an operator who reads
// "child outlived the shutdown" and no pid has been told there is a problem and
// not told how to find it.

// capture lives in shutdown_warn_test.go, which reports the other half of this
// same moment: that the budget ran out. This says WHICH children it ran out on.

// THIS TEST USED TO ASSERT SILENCE, and the reasoning was sound at the time:
// "the common case by an enormous margin, and a line here on every clean stop
// would teach operators to skim past the one that matters."
//
// #717 is why it changed. The census covered SUPERVISOR CHILDREN ONLY, so the
// silence this test pinned was being produced in two very different situations
// that looked identical from the log: a genuinely clean teardown, and a whisper
// or transcode child still running that this function could not see. A
// detection device that under-reports is worse than none, because its green is
// read as an all-clear.
//
// The scope is now broad -- every spawner that can outlive a call enrols -- and
// TestEverySpawnSiteIsAccountedFor is what keeps it broad. So the clean case
// says so, and says what "clean" covers. One line at the end of a shutdown that
// already logs "goodbye" is not a habit; a green nobody can size is.
func TestAQuietShutdownSaysWhatItCovered(t *testing.T) {
	log, buf := capture(t)
	reportSurvivingChildren(log, nil)

	out := buf.String()
	if out == "" {
		t.Fatal("a clean shutdown said nothing at all. Silence here is produced " +
			"both by a teardown that reaped everything and by one whose survivors " +
			"are outside the census, and an operator cannot tell those apart")
	}
	if strings.Contains(strings.ToLower(out), "warn") || strings.Contains(out, "ERROR") {
		t.Errorf("the clean case is reported at warning level or worse, which is how "+
			"an operator learns to skim the line that matters:\n%s", out)
	}
	if !strings.Contains(out, "scope") {
		t.Errorf("the clean report does not say what it covered, so it can still be "+
			"read as more than it is:\n%s", out)
	}
}

func TestASurvivingChildIsNamedWithItsPID(t *testing.T) {
	log, buf := capture(t)
	reportSurvivingChildren(log, []childcensus.Child{
		{PID: 5216, Name: "meters", Kind: "meters", Since: time.Now().Add(-90 * time.Second)},
		{PID: 5217, Name: "dest:studio-a", Kind: "destination", Since: time.Now().Add(-30 * time.Second)},
	})
	out := buf.String()
	for _, want := range []string{"5216", "meters", "5217", "dest:studio-a", "destination"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q, so an operator cannot find what it "+
				"is telling them about:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "outlived the shutdown"); n != 2 {
		t.Errorf("got %d report lines for two survivors; one line naming a count would "+
			"leave the operator to go and find which:\n%s", n, out)
	}
}
