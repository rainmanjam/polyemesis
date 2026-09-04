package main

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
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

func TestAQuietShutdownSaysNothing(t *testing.T) {
	// The common case by an enormous margin, and a line here on every clean
	// stop would teach operators to skim past the one that matters.
	log, buf := capture(t)
	reportSurvivingChildren(log, nil)
	if buf.Len() != 0 {
		t.Fatalf("a shutdown that left nothing behind logged %q", buf.String())
	}
}

func TestASurvivingChildIsNamedWithItsPID(t *testing.T) {
	log, buf := capture(t)
	reportSurvivingChildren(log, []supervisor.Child{
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
