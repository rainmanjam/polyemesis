package oauth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// THE POINT OF LiveStats IS WHAT IT DOES NOT SAY, AND NOTHING TESTED THAT.
//
// Both pointer fields exist so silence survives the trip to the browser: a
// viewer count the platform withheld and a start time we could not read must be
// ABSENT from the JSON, not present with a zero value. Every provider test in
// this package asserts on the Go struct, where nil is obvious. None of them
// asserted on the bytes, where nil is a marshalling detail -- and that gap is
// exactly where the bug lived: `StartedAt time.Time` carried an omitempty tag
// that reads as intentional and does nothing, because encoding/json only
// honours omitempty for empty scalars, maps and slices. A struct is never empty
// to it. Every offline channel shipped "startedAt":"0001-01-01T00:00:00Z" while
// the tag sat there looking like it was handling the case.
func TestAWithheldNumberIsAbsentFromTheJSONRatherThanZero(t *testing.T) {
	t.Run("an offline channel mentions neither field", func(t *testing.T) {
		b, err := json.Marshal(&LiveStats{Source: "/streams"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		got := string(b)
		for _, key := range []string{"viewerCount", "startedAt"} {
			if strings.Contains(got, key) {
				t.Errorf("payload names %q for a platform that reported nothing: %s\n"+
					"A consumer cannot distinguish \"nobody is watching\" from \"we were not told\" "+
					"if the key is present either way.", key, got)
			}
		}
		// The zero time is the specific wrong answer this guards against, and
		// it is worth naming: it renders as a date in the year 1 next to a live
		// stream, which reads as a bug in polyemesis rather than as a gap.
		if strings.Contains(got, "0001-01-01") {
			t.Errorf("payload carries the zero time: %s", got)
		}
	})

	t.Run("a real zero is reported, because it is an answer", func(t *testing.T) {
		zero := 0
		b, err := json.Marshal(&LiveStats{Live: true, ViewerCount: &zero})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		// omitempty on a *int omits nil and keeps a pointer to 0. That is the
		// whole reason the field is a pointer rather than an int with
		// omitempty, which would have dropped a genuine zero -- the opposite
		// lie, told to a streamer whose audience really has left.
		if !strings.Contains(string(b), `"viewerCount":0`) {
			t.Errorf("a genuine count of zero was dropped: %s", b)
		}
	})

	t.Run("a known start time survives", func(t *testing.T) {
		at := time.Date(2026, 8, 16, 20, 0, 0, 0, time.UTC)
		b, err := json.Marshal(&LiveStats{Live: true, StartedAt: &at})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(b), `"startedAt":"2026-08-16T20:00:00Z"`) {
			t.Errorf("start time did not survive marshalling: %s", b)
		}
	})
}
