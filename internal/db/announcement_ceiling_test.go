package db

import (
	"strconv"
	"testing"
	"time"
)

// The ceiling must never evict a marker that names a live broadcast.
//
// The first version of this sorted by occurrence and kept the tail, so the
// entry it dropped was the show NEAREST IN TIME. That is not lost bookkeeping:
// the next sweep finds no marker for that schedule, creates a SECOND Facebook
// event page, and evicts another marker to make room for it. People are already
// subscribed to the first. It thrashes, one orphaned event page per sweep, and
// it starts with the broadcast about to go out.
//
// Mutation proving it can fail: in Announce, replace
// `f.Announcements = capIntents(kept)` with the old rule --
//
//	if len(kept) > maxAnnouncements {
//	    kept = kept[len(kept)-maxAnnouncements:]
//	}
//	f.Announcements = kept
//
// Measured: FAIL, "the soonest show's marker was evicted".
func TestTheCeilingNeverEvictsALiveBroadcast(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var f FacebookSettings

	// One more than the ceiling, every one of them a real broadcast, every one
	// still ahead of us -- which is what maxAnnouncements + 1 enabled start
	// schedules targeting one destination produces.
	total := maxAnnouncements + 1
	for i := 0; i < total; i++ {
		f.Announce(int64(i+1), now.Add(time.Duration(i+1)*time.Hour),
			"bcast-"+strconv.Itoa(i+1), now)
	}

	if len(f.Announcements) != total {
		t.Errorf("kept %d markers of %d, so at least one live broadcast was "+
			"evicted; the next sweep creates a duplicate event page for it and "+
			"orphans the one people subscribed to", len(f.Announcements), total)
	}
	// Name the specific victim the old rule chose, because "some marker went
	// missing" and "the imminent show's marker went missing" are different bugs.
	if !f.AnnouncedFor(now.Add(time.Hour)) {
		t.Error("the soonest show's marker was evicted -- the broadcast nearest " +
			"to going out is the one that loses its event page")
	}
}

// The ceiling still has to DO something, or it is not a bound at all. Intents
// are the markers it is safe to drop: one records a create whose outcome never
// came back, and losing it costs a retry, which is the behaviour without any
// marker at all.
//
// Mutation proving it can fail: in capIntents, change
// `if in[i].BroadcastID == ""` to `if false`. Measured: FAIL, kept 40 of 40.
func TestTheCeilingStillDropsIntents(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var f FacebookSettings

	total := maxAnnouncements + 8
	for i := 0; i < total; i++ {
		// Every one an INTENT: a create recorded, no broadcast id back.
		f.Announce(int64(i+1), now.Add(time.Duration(i+1)*time.Hour), "", now)
	}

	if len(f.Announcements) > maxAnnouncements {
		t.Errorf("kept %d intents, want at most %d: the ceiling is not bounding "+
			"anything and a row of them grows without limit",
			len(f.Announcements), maxAnnouncements)
	}
}

// Which intent goes matters. One close in time is about to be resolved by the
// very next sweep; one seven days out has the longest to sit there being wrong,
// suppressing a create that should have been retried.
//
// Mutation proving it can fail: in capIntents, change the eviction walk
// `for i := len(in) - 1; i >= 0 && over > 0; i--` to
// `for i := 0; i < len(in) && over > 0; i++`. Measured: FAIL, the soonest
// intent was dropped and the furthest kept.
func TestTheFurthestIntentIsTheOneDropped(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	var f FacebookSettings

	soonest := now.Add(time.Hour)
	f.Announce(1, soonest, "", now)
	for i := 1; i < maxAnnouncements+1; i++ {
		f.Announce(int64(i+1), now.Add(time.Duration(i+1)*24*time.Hour), "", now)
	}

	for _, a := range f.Announcements {
		if a.ScheduleID == 1 && a.Occurrence.Equal(soonest) {
			return
		}
	}
	t.Error("the SOONEST intent was evicted. It is the one the next sweep was " +
		"about to resolve; the seven-day-out one it kept instead is the one with " +
		"the longest to sit there suppressing a retry")
}
