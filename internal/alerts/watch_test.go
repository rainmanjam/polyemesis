package alerts

import (
	"testing"
	"time"
)

// step is one snapshot in a scenario, with what it should produce.
type step struct {
	after time.Duration
	snap  func(*Snapshot)
	want  []Type
}

func runSteps(t *testing.T, w *Watcher, seed func(*Snapshot), steps []step) {
	t.Helper()
	for i, s := range steps {
		snap := Snapshot{At: base.Add(s.after)}
		if seed != nil {
			seed(&snap)
		}
		if s.snap != nil {
			s.snap(&snap)
		}
		got := w.Observe(snap)
		types := make([]Type, 0, len(got))
		for _, ev := range got {
			types = append(types, ev.Type)
		}
		if len(types) != len(s.want) {
			t.Fatalf("step %d (+%s): events = %v, want %v", i, s.after, types, s.want)
		}
		for j := range types {
			if types[j] != s.want[j] {
				t.Errorf("step %d (+%s): event %d = %q, want %q", i, s.after, j, types[j], s.want[j])
			}
		}
	}
}

func TestWatcherIngestNeedsToStayDownBeforeItIsAnIncident(t *testing.T) {
	w := NewWatcher(WatchConfig{DownFor: 20 * time.Second})
	live := func(v bool) func(*Snapshot) {
		return func(s *Snapshot) { s.IngestConfigured = true; s.IngestLive = v }
	}
	runSteps(t, w, nil, []step{
		{after: 0, snap: live(true)},
		{after: 2 * time.Second, snap: live(false)},
		// A reconnect inside the window is normal operation, not an incident.
		{after: 10 * time.Second, snap: live(true)},
		{after: 12 * time.Second, snap: live(false)},
		{after: 20 * time.Second, snap: live(false)},
		{after: 32 * time.Second, snap: live(false), want: []Type{TypeIngestLost}},
		// Once said, not said again.
		{after: 40 * time.Second, snap: live(false)},
		{after: 45 * time.Second, snap: live(true), want: []Type{TypeIngestRecovered}},
		{after: 50 * time.Second, snap: live(true)},
	})
}

func TestWatcherSaysNothingAboutAnIngestThatIsNotConfigured(t *testing.T) {
	w := NewWatcher(WatchConfig{DownFor: time.Second})
	runSteps(t, w, nil, []step{
		{after: 0},
		{after: time.Minute},
		{after: 2 * time.Minute},
	})
}

func TestWatcherDestinationTransitions(t *testing.T) {
	w := NewWatcher(WatchConfig{DownFor: 20 * time.Second})
	dest := func(enabled, running bool) func(*Snapshot) {
		return func(s *Snapshot) {
			s.Destinations = []DestState{{ID: 7, Name: "Twitch", Enabled: enabled, Running: running}}
		}
	}
	runSteps(t, w, nil, []step{
		{after: 0, snap: dest(true, true)},
		{after: 5 * time.Second, snap: dest(true, false)},
		{after: 30 * time.Second, snap: dest(true, false), want: []Type{TypeDestinationDown}},
		{after: 40 * time.Second, snap: dest(true, true), want: []Type{TypeDestinationRecovered}},
		// Disabled by an operator is not down, and must not restart the clock
		// from the time it spent switched off.
		{after: 45 * time.Second, snap: dest(false, false)},
		{after: 120 * time.Second, snap: dest(false, false)},
		{after: 121 * time.Second, snap: dest(true, false)},
		{after: 130 * time.Second, snap: dest(true, false)},
		{after: 145 * time.Second, snap: dest(true, false), want: []Type{TypeDestinationDown}},
	})
}

// falling_behind is an EARLY warning, so its whole value is in not firing on
// the dips that a live encoder makes constantly.
func TestWatcherFallingBehindNeedsTheDwell(t *testing.T) {
	w := NewWatcher(WatchConfig{SpeedFloor: 0.95, FallingBehindFor: 30 * time.Second})
	at := func(speed float64) func(*Snapshot) {
		return func(s *Snapshot) {
			s.Destinations = []DestState{{ID: 7, Name: "Twitch", Enabled: true, Running: true, Speed: speed}}
		}
	}
	runSteps(t, w, nil, []step{
		{after: 0, snap: at(1.0)},
		// A dip that returns inside the dwell is a keyframe boundary, not an
		// incident. Firing here is what gets the whole feature muted.
		{after: 5 * time.Second, snap: at(0.80)},
		{after: 20 * time.Second, snap: at(1.0)},
		// Sustained is different.
		{after: 30 * time.Second, snap: at(0.87)},
		{after: 45 * time.Second, snap: at(0.87)},
		{after: 61 * time.Second, snap: at(0.87), want: []Type{TypeDestinationFallingBehind}},
		// And it says so exactly once while it stays bad.
		{after: 75 * time.Second, snap: at(0.85)},
		{after: 90 * time.Second, snap: at(1.01), want: []Type{TypeDestinationCaughtUp}},
	})
}

// Speed 0 is "no progress block yet", not "stopped dead". Treating it as bad
// fires on every destination for the first second of every broadcast.
func TestWatcherTreatsUnknownSpeedAsUnknown(t *testing.T) {
	w := NewWatcher(WatchConfig{SpeedFloor: 0.95, FallingBehindFor: 10 * time.Second})
	zero := func(s *Snapshot) {
		s.Destinations = []DestState{{ID: 1, Name: "YouTube", Enabled: true, Running: true, Speed: 0}}
	}
	runSteps(t, w, nil, []step{
		{after: 0, snap: zero},
		{after: 30 * time.Second, snap: zero},
		{after: 120 * time.Second, snap: zero},
	})
}

// A destination that dies while it is already flagged did not recover. Emitting
// caught_up here would tell an operator the opposite of what happened, at the
// exact moment they are reading the channel to find out.
func TestWatcherDoesNotReportCaughtUpWhenTheDestinationDiedInstead(t *testing.T) {
	w := NewWatcher(WatchConfig{
		DownFor: 20 * time.Second, SpeedFloor: 0.95, FallingBehindFor: 30 * time.Second,
	})
	state := func(running bool, speed float64) func(*Snapshot) {
		return func(s *Snapshot) {
			s.Destinations = []DestState{{ID: 3, Name: "Kick", Enabled: true, Running: running, Speed: speed}}
		}
	}
	runSteps(t, w, nil, []step{
		{after: 0, snap: state(true, 1.0)},
		{after: 10 * time.Second, snap: state(true, 0.6)},
		{after: 45 * time.Second, snap: state(true, 0.6), want: []Type{TypeDestinationFallingBehind}},
		// It gives up. Speed goes to zero because there is no longer a process
		// emitting progress, which must NOT read as a recovery.
		{after: 50 * time.Second, snap: state(false, 0)},
		{after: 75 * time.Second, snap: state(false, 0), want: []Type{TypeDestinationDown}},
		{after: 100 * time.Second, snap: state(true, 1.0), want: []Type{TypeDestinationRecovered}},
	})
}

// A congested uplink degrades every destination at once, and each is its own
// subject. Two events is the correct answer here; suppressing one because
// another fired would hide a destination that was independently sick.
func TestWatcherFallingBehindIsPerDestination(t *testing.T) {
	w := NewWatcher(WatchConfig{SpeedFloor: 0.95, FallingBehindFor: 20 * time.Second})
	both := func(a, b float64) func(*Snapshot) {
		return func(s *Snapshot) {
			s.Destinations = []DestState{
				{ID: 1, Name: "Twitch", Enabled: true, Running: true, Speed: a},
				{ID: 2, Name: "YouTube", Enabled: true, Running: true, Speed: b},
			}
		}
	}
	runSteps(t, w, nil, []step{
		{after: 0, snap: both(1.0, 1.0)},
		{after: 5 * time.Second, snap: both(0.7, 0.7)},
		{after: 30 * time.Second, snap: both(0.7, 0.7), want: []Type{
			TypeDestinationFallingBehind, TypeDestinationFallingBehind,
		}},
		// One recovers, the other does not. The events must not be entangled.
		{after: 40 * time.Second, snap: both(1.0, 0.7), want: []Type{TypeDestinationCaughtUp}},
	})
}

// A disabled destination has no speed worth judging, and re-enabling it must
// start the clock fresh rather than counting the time it spent switched off.
func TestWatcherIgnoresSpeedOnADisabledDestination(t *testing.T) {
	w := NewWatcher(WatchConfig{SpeedFloor: 0.95, FallingBehindFor: 20 * time.Second})
	d := func(enabled bool, speed float64) func(*Snapshot) {
		return func(s *Snapshot) {
			s.Destinations = []DestState{{ID: 9, Name: "File", Enabled: enabled, Running: true, Speed: speed}}
		}
	}
	runSteps(t, w, nil, []step{
		{after: 0, snap: d(false, 0.1)},
		{after: 300 * time.Second, snap: d(false, 0.1)},
		{after: 301 * time.Second, snap: d(true, 0.1)},
		{after: 310 * time.Second, snap: d(true, 0.1)},
		{after: 322 * time.Second, snap: d(true, 0.1), want: []Type{TypeDestinationFallingBehind}},
	})
}

func TestWatcherForgetsADestinationThatWasDeleted(t *testing.T) {
	w := NewWatcher(WatchConfig{DownFor: time.Second})
	w.Observe(Snapshot{At: base, Destinations: []DestState{{ID: 1, Enabled: true}}})
	w.Observe(Snapshot{At: base.Add(time.Minute)})
	if len(w.dest) != 0 {
		t.Errorf("watcher still tracks %d deleted destinations", len(w.dest))
	}
}

func TestWatcherFailoverAdoptsTheCounterBeforeReportingSwitches(t *testing.T) {
	w := NewWatcher(WatchConfig{})
	fail := func(active string, n int) func(*Snapshot) {
		return func(s *Snapshot) {
			s.Failover = &FailoverState{Active: active, Reason: "primary stopped delivering", Switches: n}
		}
	}
	runSteps(t, w, nil, []step{
		// A restarted server must not greet its operator with a history of
		// every switch since the tier came up.
		{after: 0, snap: fail("primary", 4)},
		{after: time.Second, snap: fail("primary", 4)},
		{after: 2 * time.Second, snap: fail("backup", 5), want: []Type{TypeFailoverSwitched}},
		{after: 3 * time.Second, snap: fail("backup", 5)},
		{after: 4 * time.Second, snap: fail("primary", 6), want: []Type{TypeFailoverSwitched}},
	})
}

func TestWatcherFailoverSeveritySaysWhetherWeAreBackOnPrimary(t *testing.T) {
	w := NewWatcher(WatchConfig{})
	w.Observe(Snapshot{At: base, Failover: &FailoverState{Active: "primary", Switches: 0}})

	got := w.Observe(Snapshot{At: base.Add(time.Second),
		Failover: &FailoverState{Active: "slate", Reason: "no source", Switches: 1}})
	if len(got) != 1 || got[0].Severity != SeverityWarning {
		t.Fatalf("switching to slate = %+v, want one warning", got)
	}
	got = w.Observe(Snapshot{At: base.Add(2 * time.Second),
		Failover: &FailoverState{Active: "primary", Reason: "primary returned", Switches: 2}})
	if len(got) != 1 || got[0].Severity != SeverityInfo {
		t.Fatalf("returning to primary = %+v, want one info", got)
	}
}

func TestWatcherClippingNeedsConsecutiveHits(t *testing.T) {
	w := NewWatcher(WatchConfig{ClipDBFS: -0.1, ClipHits: 3})
	peak := func(db float64) func(*Snapshot) {
		return func(s *Snapshot) { s.Peaks = []PeakState{{Track: 1, Channel: 1, PeakDB: db}} }
	}
	runSteps(t, w, nil, []step{
		// One transient on the ceiling is a snare hit.
		{after: 0, snap: peak(0)},
		{after: time.Second, snap: peak(-12)},
		{after: 2 * time.Second, snap: peak(-0.1)},
		{after: 3 * time.Second, snap: peak(0)},
		{after: 4 * time.Second, snap: peak(-0.05), want: []Type{TypeClipping}},
		// The counter resets, so the next alert needs another full run.
		{after: 5 * time.Second, snap: peak(0)},
		{after: 6 * time.Second, snap: peak(0)},
		{after: 7 * time.Second, snap: peak(0), want: []Type{TypeClipping}},
	})
}

func TestWatcherClippingKeysByTrackSoOneMessageCoversAStereoPair(t *testing.T) {
	w := NewWatcher(WatchConfig{ClipHits: 1})
	got := w.Observe(Snapshot{At: base, Peaks: []PeakState{
		{Track: 2, Channel: 2, PeakDB: 0},
		{Track: 2, Channel: 1, PeakDB: 0},
	}})
	if len(got) != 2 {
		t.Fatalf("events = %d, want one per channel", len(got))
	}
	// Sorted, so the coalescer sees a stable order and the pair folds into one
	// message rather than two.
	if got[0].Fields[1].Value != "1" || got[1].Fields[1].Value != "2" {
		t.Errorf("channels came out unsorted: %v then %v", got[0].Fields, got[1].Fields)
	}
	if got[0].Key != got[1].Key {
		t.Errorf("keys %q and %q differ; both channels of one track should coalesce",
			got[0].Key, got[1].Key)
	}
}

func TestWatcherDiskAlertsImmediatelyAndOnlyOnce(t *testing.T) {
	w := NewWatcher(WatchConfig{DiskFloorBytes: 2 << 30, DiskFloorPercent: 5})
	disk := func(free uint64, halted bool) func(*Snapshot) {
		return func(s *Snapshot) {
			s.Disk = DiskState{FreeBytes: free, TotalBytes: 100 << 30, Halted: halted}
		}
	}
	runSteps(t, w, nil, []step{
		{after: 0, snap: disk(50<<30, false)},
		{after: time.Second, snap: disk(1<<30, false), want: []Type{TypeDiskLow}},
		{after: 2 * time.Second, snap: disk(1<<30, false)},
		{after: 3 * time.Second, snap: disk(50<<30, false), want: []Type{TypeDiskRecovered}},
		// The percentage floor catches a large volume long before the byte one.
		{after: 4 * time.Second, snap: disk(4<<30, false), want: []Type{TypeDiskLow}},
	})
}

func TestWatcherDiskSaysNothingWhenTheVolumeCouldNotBeRead(t *testing.T) {
	w := NewWatcher(WatchConfig{})
	// Zero free of zero total is an unreadable volume, not a full one.
	if got := w.Observe(Snapshot{At: base}); len(got) != 0 {
		t.Errorf("events = %v, want none: an unreadable volume is not a full one", got)
	}
}

func TestWatcherHaltedRecorderIsCriticalWhateverTheNumbersSay(t *testing.T) {
	w := NewWatcher(WatchConfig{})
	got := w.Observe(Snapshot{At: base, Disk: DiskState{
		FreeBytes: 900 << 30, TotalBytes: 1000 << 30, Halted: true, Reason: "free space below floor",
	}})
	if len(got) != 1 || got[0].Severity != SeverityCritical {
		t.Fatalf("events = %+v, want one critical", got)
	}
}

func TestWatcherLoudnessNeedsToStayOutOfToleranceBeforeItIsAMixProblem(t *testing.T) {
	w := NewWatcher(WatchConfig{LoudnessFor: 90 * time.Second})
	loud := func(failed bool) func(*Snapshot) {
		return func(s *Snapshot) {
			s.Loudness = []LoudnessState{{
				ID: 3, Name: "YouTube", Failed: failed,
				Reason: "-9.2 LUFS is 4.8 LU over target", LUFS: -9.2, Target: -14,
			}}
		}
	}
	runSteps(t, w, nil, []step{
		{after: 0, snap: loud(false)},
		{after: 10 * time.Second, snap: loud(true)},
		// A loud passage is not a mix problem.
		{after: 60 * time.Second, snap: loud(false)},
		{after: 70 * time.Second, snap: loud(true)},
		{after: 150 * time.Second, snap: loud(true)},
		{after: 165 * time.Second, snap: loud(true), want: []Type{TypeLoudnessOut}},
		{after: 200 * time.Second, snap: loud(false), want: []Type{TypeLoudnessRecovered}},
	})
}

func TestShortRendersDurationsTheWayAnOperatorReadsThem(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{in: 20*time.Second + 481*time.Microsecond, want: "20s"},
		{in: 59 * time.Second, want: "59s"},
		{in: 60 * time.Second, want: "1m"},
		{in: 80 * time.Second, want: "1m20s"},
		{in: 3600 * time.Second, want: "60m"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := short(tt.in); got != tt.want {
				t.Errorf("short(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
