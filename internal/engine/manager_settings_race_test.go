package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The device in engineSettings routes every install-wide setting through ONE
// push, which is what stops a new engine from missing one. It also changed what
// a single SetX does: SetHooks used to push only the dispatcher, and now every
// SetX pushes ALL SIX values, read from the manager AFTER its own write.
//
// That is a lost update waiting to happen, and these tests are the probe for it.
// Two setters running at once interleave like this:
//
//	SetHooks:     write hooks=d ......................... push {d, nil}
//	SetLifecycle:      write lifecycle=o ... push {d, o}
//
// SetHooks took its snapshot before SetLifecycle's write landed, and pushed it
// afterwards, so the engine ends up with lifecycle=nil -- rolled back by a
// setter that was not even about lifecycle. The manager's field still says o, so
// nothing in the manager disagrees with itself and no later push corrects the
// engine. It is the same invisible failure engineSettings exists to prevent, in
// a new place: the operator's coordinator is wired, the manager agrees it is
// wired, and that programme's YouTube broadcasts sit in "testing" all show.
//
// Sequential wiring cannot reach it today -- main pushes the four settings one
// after another -- which is exactly why nothing else catches it. The next caller
// to save two settings from two request handlers reaches it immediately.

// markedLifecycle is a LifecycleObserver with an identity, which stubLifecycle
// -- an empty struct -- cannot have: Go is free to give two pointers to distinct
// zero-size values the SAME address, so `got != want` between two &stubLifecycle{}
// is not a reliable "a different observer arrived". Every assertion in this file
// is an identity comparison, so every observer in it carries a field.
type markedLifecycle struct{ n int }

func (*markedLifecycle) Observe(hooks.Event) {}
func (*markedLifecycle) Wanted() bool        { return false }

// racingSettings is one iteration's worth of distinguishable values, one per
// engineSettings field, so a lost update can be attributed to the setting that
// lost rather than merely detected.
type racingSettings struct {
	tw        *transcribe.Tools
	modelsDir string
	niceMark  string
	attempts  int
	hooks     *hooks.Dispatcher
	lifecycle LifecycleObserver
}

func newRacingSettings(t *testing.T, base string, i int) racingSettings {
	t.Helper()
	return racingSettings{
		tw:        &transcribe.Tools{Binary: fmt.Sprintf("%s/whisper-%d", base, i)},
		modelsDir: filepath.Join(base, fmt.Sprintf("models-%d", i)),
		niceMark:  fmt.Sprintf("nice-%d", i),
		// Positive on every iteration and different on every iteration:
		// Manager.SetAlertRetry refuses a non-positive budget, so this one
		// cannot be reset to zero between rounds the way the pointers can.
		attempts: i + 1,
		hooks: hooks.NewDispatcher(slog.New(slog.NewTextHandler(io.Discard, nil)),
			hooks.SourceFunc(func() ([]hooks.Hook, error) { return nil, nil })),
		lifecycle: &markedLifecycle{n: i},
	}
}

// pushConcurrently releases every setter at once and waits for all of them.
// Each setter writes ONE field, so a correct manager leaves the engine holding
// all four values no matter what order they land in.
func pushConcurrently(m *Manager, s racingSettings) {
	var ready sync.WaitGroup
	var done sync.WaitGroup
	start := make(chan struct{})
	setters := []func(){
		func() {
			m.SetTranscriber(s.tw, s.modelsDir, func(name string, args []string) (string, []string) {
				return s.niceMark, args
			})
		},
		func() { m.SetAlertRetry(s.attempts) },
		func() { m.SetHooks(s.hooks) },
		func() { m.SetLifecycle(s.lifecycle) },
	}
	ready.Add(len(setters))
	done.Add(len(setters))
	for _, set := range setters {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			set()
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
}

// TestConcurrentSettingPushesDoNotClobberOneAnother is the 200-iteration probe.
//
// Every iteration first parks the pointer-valued settings at nil, so a stale
// snapshot shows up as a value that went BACKWARDS rather than merely being
// stale, then races one setter per field and asserts that all four arrived.
func TestConcurrentSettingPushesDoNotClobberOneAnother(t *testing.T) {
	m, store := managerFixture(t)
	src := addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng := m.Engine(src.ID)
	if eng == nil {
		t.Fatal("Start brought up no engine for the source")
	}
	base := t.TempDir()

	// The reviewer's probe used 200 and failed around iteration 76 -- but a
	// probe for a lost update is only ever a sampler, and 200 caught the
	// unlocked manager on roughly 7 runs in 10 on this machine. A regression
	// gate that passes three times in ten over a real defect is worse than
	// none, because it reads as a green tick. 2000 costs about a tenth of a
	// second and has not missed it in any run.
	const iterations = 2000
	for i := 0; i < iterations; i++ {
		// Park the pointers. Sequential, so this cannot itself race.
		m.SetTranscriber(nil, filepath.Join(base, "parked"), nil)
		m.SetHooks(nil)
		m.SetLifecycle(nil)

		s := newRacingSettings(t, base, i)
		pushConcurrently(m, s)

		eng.mu.RLock()
		whisper, dir, nice := eng.whisper, eng.whisperDir, eng.whisperNice
		attempts, hookd, lifecycle := eng.alertAttempts, eng.hooks, eng.lifecycle
		eng.mu.RUnlock()

		const lost = "iteration %d: %s = %v, want %v. A concurrent SetX pushed a " +
			"snapshot it took before this setting was written, rolling the engine " +
			"back to a value the manager itself no longer holds -- and nothing " +
			"pushes again to correct it."
		if whisper != s.tw {
			t.Fatalf(lost, i, "engine transcriber", whisper, s.tw)
		}
		if dir != s.modelsDir {
			t.Fatalf(lost, i, "engine models directory", dir, s.modelsDir)
		}
		if nice == nil {
			t.Fatalf(lost, i, "engine nice wrapper", nil, s.niceMark)
		} else if got, _ := nice("whisper", nil); got != s.niceMark {
			t.Fatalf(lost, i, "engine nice wrapper", got, s.niceMark)
		}
		if attempts != s.attempts {
			t.Fatalf(lost, i, "engine alert retry budget", attempts, s.attempts)
		}
		if hookd != s.hooks {
			t.Fatalf(lost, i, "engine hook dispatcher", hookd, s.hooks)
		}
		if lifecycle != s.lifecycle {
			t.Fatalf(lost, i, "engine lifecycle observer", lifecycle, s.lifecycle)
		}
	}
}

// The manager's OWN copy has to survive the same race, because it is what every
// engine created later is built from. A setter that wrote its field and then let
// another setter's stale snapshot win would leave the manager and the engines
// agreeing on a value the operator never saved.
func TestConcurrentSettingPushesLeaveTheManagerHoldingAllOfThem(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	base := t.TempDir()

	for i := 0; i < 50; i++ {
		s := newRacingSettings(t, base, i)
		pushConcurrently(m, s)

		got := m.engineSettingsSnapshot()
		if got.tw != s.tw || got.modelsDir != s.modelsDir || got.alertAttempts != s.attempts ||
			got.hooks != s.hooks || got.lifecycle != s.lifecycle || got.nice == nil {
			t.Fatalf("iteration %d: manager settings = %+v, want every field from %+v", i, got, s)
		}
	}
}

// An engine built by Sync while settings are being saved must end up holding the
// last saved value, and so must every engine that was already running.
//
// Sync configures an engine BEFORE Start -- it has to, Start is what launches
// the loops that read those settings -- and cannot register it until Start has
// returned, because a registered engine that failed to start is one nothing will
// ever stop. Between those two points the engine is configured but INVISIBLE,
// and there are two ways a save gets lost across that gap:
//
//   - the save enumerates the engines while this one is still invisible, so the
//     value reaches every programme except the one the operator just added; or
//   - the save lands after the engine is visible but before Sync's own push, and
//     Sync's push -- carrying a snapshot from before the save -- overwrites it.
//
// The first is the whole width of a reconcile. The second is a couple of
// instructions. A sweep of sleeps finds the first and never the second, so this
// hammers instead: a saver goroutine pushes a fresh observer in a tight loop for
// as long as Sync is running, which puts thousands of saves across both windows.
// The assertion is on the LAST one it pushed, which every engine must be holding
// once both goroutines are done -- a save that arrives after an engine is
// registered reaches it, and a save that arrives before is in the snapshot Sync
// pushes.
func TestASourceAddedWhileSettingsAreBeingSavedGetsThem(t *testing.T) {
	m, store := managerFixture(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const iterations = 60
	for i := 0; i < iterations; i++ {
		src := addSource(t, store, fmt.Sprintf("src-%d", i))

		var stop atomic.Bool
		var pushed atomic.Pointer[markedLifecycle]
		var savers sync.WaitGroup
		savers.Add(1)
		go func() {
			defer savers.Done()
			// At least one save, however fast Sync turns out to be, so the
			// assertion below is never about a value nobody pushed.
			for n := 0; ; n++ {
				o := &markedLifecycle{n: n}
				m.SetLifecycle(o)
				pushed.Store(o)
				if stop.Load() {
					return
				}
			}
		}()

		if err := m.Sync(); err != nil {
			t.Fatalf("iteration %d: Sync: %v", i, err)
		}
		stop.Store(true)
		savers.Wait()

		want := pushed.Load()
		if want == nil {
			t.Fatalf("iteration %d: the saver pushed nothing", i)
		}
		if m.Engine(src.ID) == nil {
			t.Fatalf("iteration %d: Sync started no engine for the source it added", i)
		}
		for _, eng := range m.Engines() {
			eng.mu.RLock()
			lifecycle := eng.lifecycle
			eng.mu.RUnlock()
			if lifecycle != want {
				t.Fatalf("iteration %d: an engine holds lifecycle observer %v, want %v -- "+
					"the last one saved while Sync was building a new engine. Either the "+
					"save missed the engine that was still invisible, or Sync's own push "+
					"carried a snapshot older than the save and rolled it back. Both leave "+
					"the manager agreeing with itself and a programme's YouTube broadcasts "+
					"sitting in \"testing\" for the whole show.", i, lifecycle, want)
			}
		}
	}
}

// The narrow half of the same gap, driven directly.
//
// publishEngine does two things -- make the engine visible, hand it the current
// settings -- and the second reads a snapshot. A save that writes AFTER that
// snapshot is taken and applies BEFORE publishEngine applies is overwritten by a
// state older than itself. That window is a handful of instructions wide, so
// TestASourceAddedWhileSettingsAreBeingSavedGetsThem gets one shot at it per
// Sync and never lands in it; going through Sync also spends a whole engine
// build per attempt.
//
// This calls publishEngine itself, so the same engine can be published tens of
// thousands of times against a save released alongside it. Whichever wins,
// settingsMu makes them a sequence rather than an overlap: a save that got in
// first is in publishEngine's snapshot, and a save that comes second finds the
// engine already registered. The engine must end up holding the saved observer
// either way.
func TestPublishingAnEngineCannotOverwriteAConcurrentSave(t *testing.T) {
	m, store := managerFixture(t)
	src := addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng := m.Engine(src.ID)
	if eng == nil {
		t.Fatal("Start brought up no engine for the source")
	}

	const iterations = 20000
	for i := 0; i < iterations; i++ {
		o := &markedLifecycle{n: i}
		var ready, done sync.WaitGroup
		start := make(chan struct{})
		ready.Add(2)
		done.Add(2)
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			m.publishEngine(src.ID, eng)
		}()
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			m.SetLifecycle(o)
		}()
		ready.Wait()
		close(start)
		done.Wait()

		eng.mu.RLock()
		lifecycle := eng.lifecycle
		eng.mu.RUnlock()
		if lifecycle != o {
			t.Fatalf("iteration %d: the engine holds lifecycle observer %v, want the one "+
				"saved alongside the publish. publishEngine read the settings before the "+
				"save landed and pushed them after it, rolling the engine back to a state "+
				"the manager itself no longer holds.", i, lifecycle)
		}
	}
}
