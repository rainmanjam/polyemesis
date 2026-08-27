package logtz

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// THE LOG IS WRITTEN IN THE INSTALL'S ZONE.
//
// The server logged UTC while the console rendered in the browser's zone, so
// an operator comparing a log line with the screen that produced it did the
// conversion in their head, at the moment something was going wrong. Two
// people on the same production could not read each other's screenshots.
//
// Mutation: return `a` unchanged from ReplaceAttr. Observed to fail with
// "the log line is not in the configured zone".
func TestLogLinesAreWrittenInTheConfiguredZone(t *testing.T) {
	at := time.Date(2026, 8, 27, 5, 51, 0, 0, time.UTC)

	line := func(zone *time.Location) string {
		Set(zone)
		var buf bytes.Buffer
		h := slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: ReplaceAttr})
		rec := slog.NewRecord(at, slog.LevelInfo, "ingest started", 0)
		if err := h.Handle(t.Context(), rec); err != nil {
			t.Fatalf("handle: %v", err)
		}
		return buf.String()
	}

	utc := line(time.UTC)
	if !strings.Contains(utc, "05:51:00") {
		t.Fatalf("the UTC baseline is not what it claims: %s", utc)
	}

	syd, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Fatalf("Australia/Sydney does not resolve -- internal/scheduler compiles "+
			"the zone database in, so this means that import is gone: %v", err)
	}
	got := line(syd)
	if !strings.Contains(got, "15:51:00") {
		t.Errorf("the log line is not in the configured zone.\n  got:  %s\n"+
			"An operator reading this beside the console it came from is back to "+
			"converting time zones in their head.", strings.TrimSpace(got))
	}
}

// THE CONTROL: an install that never sets a zone reads exactly as it always
// did. A change that put every log in some zone nobody chose would pass the
// test above.
func TestTheDefaultIsUTC(t *testing.T) {
	Set(nil)
	if Location() != time.UTC {
		t.Errorf("a nil zone gave %v, want UTC", Location())
	}
}

// A NESTED ATTRIBUTE NAMED "time" BELONGS TO WHOEVER LOGGED IT. Rewriting it
// would be this package editing somebody else's data.
func TestOnlyTheBuiltInTimestampIsRewritten(t *testing.T) {
	Set(time.UTC)
	a := slog.Time("time", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	got := ReplaceAttr([]string{"process"}, a)
	if !got.Value.Time().Equal(a.Value.Time()) {
		t.Errorf("a grouped attribute called %q was rewritten", a.Key)
	}
}
