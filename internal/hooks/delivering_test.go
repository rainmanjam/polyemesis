package hooks

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

type hookAt struct {
	sec    int
	tr     Trigger
	reason string
}

// sweep drives one destination at the engine's 2s cadence. outMS(sec) is its
// output time as FFmpeg last reported it, running(sec) whether its process is
// up, live(sec) whether the ingest is arriving.
func sweep(w *Watcher, until int, running func(int) bool, outMS func(int) int64, live func(int) bool) []hookAt {
	var got []hookAt
	for sec := 0; sec <= until; sec += 2 {
		snap := alerts.Snapshot{
			At: at(0).Add(time.Duration(sec) * time.Second), IngestConfigured: true, IngestLive: live(sec),
			Destinations: []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: running(sec), OutTimeMS: outMS(sec)}},
		}
		for _, ev := range w.Observe(snap) {
			if ev.Destination != nil {
				got = append(got, hookAt{sec, ev.Trigger, ev.Reason})
			}
		}
	}
	return got
}

func always(int) bool { return true }

// "destination.up: delivering" used to fire the moment the process spawned. A
// destination pointed at a closed port spawns, fails to connect, and is
// respawned -- so it announced delivering on every spawn and flapped up/down,
// without one byte ever reaching the platform (exploratory CH-02: an up hook 14s
// before any destination published anything). UP now needs the output time to
// have moved.
func TestADestinationThatNeverDeliversNeverComesUp(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{})
	got := sweep(w, 120,
		// Spawn, fail to connect for 4s, back off 2s, respawn.
		func(sec int) bool { return sec%6 != 4 },
		func(int) int64 { return 0 },
		always)
	if len(got) != 0 {
		t.Fatalf("a destination that never moved a byte produced %+v", got)
	}
}

// A sink that stops reading blocks FFmpeg's write, and the process stays
// "running" for as long as the TCP connection does. Nothing announced it: no
// hook, and the destination read as up for the whole stall (exploratory
// CH-02). The frozen output time is the signal.
func TestAStalledSinkIsADownAndItsRecoveryIsAnUp(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{})
	got := sweep(w, 100, always, func(sec int) int64 {
		switch {
		case sec <= 20:
			return int64(sec+1) * 1000
		case sec < 60:
			return 21000
		default:
			return int64(sec-38) * 1000
		}
	}, always)
	want := []hookAt{
		{0, TriggerDestinationUp, "delivering"},
		{32, TriggerDestinationDown, "stalled"},
		{60, TriggerDestinationUp, "delivering"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

// With the source gone every destination's output time stops. That is
// ingest.disconnected's news; a destination.down per destination on top of it
// would tell a script mirroring "what are we live to" that every platform had
// failed when none had.
func TestAMissingSourceIsNotADestinationStall(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{})
	gone := func(sec int) bool { return sec >= 20 && sec < 80 }
	got := sweep(w, 120, always, func(sec int) int64 {
		switch {
		case sec < 20:
			return int64(sec+1) * 1000
		case sec < 80:
			return 19000
		default:
			return int64(sec-60) * 1000
		}
	}, func(sec int) bool { return !gone(sec) })
	if len(got) != 1 || got[0].tr != TriggerDestinationUp {
		t.Fatalf("got %+v, want only the first destination.up", got)
	}
}
