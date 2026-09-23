package alerts

import (
	"testing"
	"time"
)

// feed drives one destination through a timeline at the engine's 2s sweep and
// returns the speed events it produced, keyed by the second they fired at.
//
// out(sec) is the destination's out_time in seconds, and ok(sec) says whether
// FFmpeg emitted a progress block that second. A sink that stops reading blocks
// FFmpeg's write, and a blocked FFmpeg emits NOTHING -- so every progress field,
// the cumulative speed and the output time alike, freezes at its last value.
// (The speed= field is no longer carried; this test failed against it with
// exactly the exploratory run's pattern -- one falling_behind, after the heal.)
func feed(t *testing.T, w *Watcher, until int, out func(sec int) float64, ok func(sec int) bool) map[int]Type {
	t.Helper()
	got := map[int]Type{}
	var lastOut float64
	for sec := 0; sec <= until; sec += 2 {
		if ok(sec) {
			lastOut = out(sec)
		}
		snap := Snapshot{At: base.Add(time.Duration(sec) * time.Second), Destinations: []DestState{{
			ID: 1, Name: "Twitch", Enabled: true, Running: true,
			OutTimeMS: int64(lastOut * 1000),
		}}}
		for _, ev := range w.Observe(snap) {
			if ev.Type == TypeDestinationFallingBehind || ev.Type == TypeDestinationCaughtUp {
				got[sec] = ev.Type
			}
		}
	}
	return got
}

// A stalled sink, healed, then stalled again -- the exploratory run's CH-02.
// Reading FFmpeg's cumulative speed, the first stall said nothing while it was
// happening (the frozen progress block kept reporting ~1.00x), fired only
// AFTER the heal (the cumulative average had been dragged under the floor and
// took the best part of an hour to climb back), never said caught_up, and the
// second stall produced nothing because the latch was still set.
func TestFallingBehindTracksEachStallAndEachRecovery(t *testing.T) {
	w := NewWatcher(WatchConfig{})
	// Delivering 0-60s; stalled 60-150s; delivering 150-300s; stalled 300-360s;
	// delivering after. Output time resumes from where it froze.
	stalled := func(sec int) bool { return (sec > 60 && sec < 150) || (sec > 300 && sec < 360) }
	out := func(sec int) float64 {
		switch {
		case sec <= 60:
			return float64(sec)
		case sec < 150:
			return 60
		case sec <= 300:
			return float64(sec - 90)
		case sec < 360:
			return 210
		default:
			return float64(sec - 150)
		}
	}
	got := feed(t, w, 600, out, func(sec int) bool { return !stalled(sec) })

	var seq []Type
	var at []int
	for sec := 0; sec <= 600; sec += 2 {
		if ev, ok := got[sec]; ok {
			seq, at = append(seq, ev), append(at, sec)
		}
	}
	want := []Type{TypeDestinationFallingBehind, TypeDestinationCaughtUp, TypeDestinationFallingBehind, TypeDestinationCaughtUp}
	if len(seq) != len(want) {
		t.Fatalf("events = %v at %v, want %v", seq, at, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("events = %v at %v, want %v", seq, at, want)
		}
	}
	// Each alert lands DURING its stall, and each recovery after its heal.
	if at[0] <= 60 || at[0] >= 150 || at[1] < 150 || at[2] <= 300 || at[2] >= 360 || at[3] < 360 {
		t.Fatalf("events at %v; want falling_behind inside each stall and caught_up after each heal", at)
	}
}

// Steady delivery at realtime, with FFmpeg's half-second progress granularity
// jittering the sampled output time, never fires.
func TestFallingBehindStaysQuietAtRealtime(t *testing.T) {
	w := NewWatcher(WatchConfig{})
	jitter := []float64{0, -0.4, -0.1, -0.5, -0.2, 0, -0.3}
	got := feed(t, w, 3600, func(sec int) float64 {
		v := float64(sec) + jitter[(sec/2)%len(jitter)]
		if v < 0 {
			return 0
		}
		return v
	}, func(int) bool { return true })
	if len(got) != 0 {
		t.Fatalf("a destination delivering at realtime raised %v", got)
	}
}

// closedOnce checks the pairing an operator's channel depends on: every
// falling_behind is closed by exactly one caught_up or destination.recovered,
// and nothing closes a falling_behind that is not open.
func closedOnce(t *testing.T, got []timedEvent) {
	t.Helper()
	open := false
	for _, e := range got {
		switch e.typ {
		case TypeDestinationFallingBehind:
			if open {
				t.Fatalf("a second falling_behind at %ds while the first was never closed: %+v", e.sec, got)
			}
			open = true
		case TypeDestinationCaughtUp:
			if !open {
				t.Fatalf("caught_up at %ds closes nothing: %+v", e.sec, got)
			}
			open = false
		case TypeDestinationRecovered:
			open = false
		}
	}
	if open {
		t.Fatalf("a falling_behind was never closed: %+v", got)
	}
}

// The exploratory row-7 timeline as it was measured in Docker. The sink is
// paused for 90s: FFmpeg keeps running with a frozen out_time, so
// falling_behind fires. When the sink is unpaused it resets the connection,
// the child exits, and the supervisor respawns it -- a sweep with no process,
// then a new run whose out_time starts again from zero at realtime.
//
// That sweep with no process used to DROP the falling_behind latch silently,
// and destination.down never fired because the gap was far shorter than its
// dwell. So the stall was announced and never closed: no caught_up, no
// recovered, ever. The second stall, healed in place without a respawn, closed
// normally, which is why the defect hid behind a passing test.
func TestAStallThatEndsInARespawnIsStillClosed(t *testing.T) {
	w := NewWatcher(WatchConfig{})
	var got []timedEvent
	var out float64
	for sec := 0; sec <= 450; sec += 2 {
		d := DestState{ID: 1, Name: "D-default", Enabled: true, Running: true}
		switch {
		case sec > 110 && sec < 200:
			// Paused sink: out_time frozen where it stopped.
		case sec == 200:
			// The sink resets the connection; the child is gone for a sweep,
			// and its successor counts from zero.
			d.Running, out = false, 0
		case sec > 290 && sec < 350:
			// Second pause, healed in place.
		case sec > 0:
			out += 2
		}
		d.OutTimeMS = int64(out * 1000)
		for _, ev := range w.Observe(Snapshot{At: base.Add(time.Duration(sec) * time.Second), Destinations: []DestState{d}}) {
			got = append(got, timedEvent{sec, ev.Type, ev.Key})
		}
	}
	wantTypes(t, got, TypeDestinationFallingBehind, TypeDestinationCaughtUp,
		TypeDestinationFallingBehind, TypeDestinationCaughtUp)
	closedOnce(t, got)
	if got[1].sec <= 200 || got[1].sec > 240 {
		t.Errorf("caught_up at %ds; want it once the respawned run has shown realtime, within a window of 200s", got[1].sec)
	}
}

// A stall that ends in a real outage -- no process for longer than
// destination.down's dwell -- is closed by destination.recovered, once. A
// caught_up as well would close the same incident twice.
func TestAStallThatBecomesAnOutageIsClosedByTheRecovery(t *testing.T) {
	w := NewWatcher(WatchConfig{DownFor: 20 * time.Second})
	got := drive(w, 300, []int64{3}, func(_ int64, sec int) destPlan {
		switch {
		case sec < 20:
			return destPlan{enabled: true, running: true, speed: 1}
		case sec < 80:
			return destPlan{enabled: true, running: true, speed: 0}
		case sec < 140:
			return destPlan{enabled: true, running: false}
		default:
			return destPlan{enabled: true, running: true, speed: 1}
		}
	})
	wantTypes(t, got, TypeDestinationFallingBehind, TypeDestinationDown, TypeDestinationRecovered)
	closedOnce(t, got)
}

// A respawn that comes back still slow has not caught up, and says nothing
// until it does.
func TestARespawnThatIsStillSlowDoesNotCatchUp(t *testing.T) {
	w := NewWatcher(WatchConfig{})
	got := drive(w, 400, []int64{5}, func(_ int64, sec int) destPlan {
		switch {
		case sec < 20:
			return destPlan{enabled: true, running: true, speed: 1}
		case sec == 100:
			return destPlan{enabled: true, running: false}
		case sec < 300:
			return destPlan{enabled: true, running: true, speed: 0.5}
		default:
			return destPlan{enabled: true, running: true, speed: 1}
		}
	})
	wantTypes(t, got, TypeDestinationFallingBehind, TypeDestinationCaughtUp)
	closedOnce(t, got)
	if got[1].sec < 300 {
		t.Errorf("caught_up at %ds, while the respawned run was still at half speed", got[1].sec)
	}
}
