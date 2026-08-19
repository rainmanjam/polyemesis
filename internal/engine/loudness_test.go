package engine

import (
	"io"
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/meters"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

func loudTestHub(t *testing.T) *relay.Hub {
	t.Helper()
	h, err := relay.New(slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func loudTestProc() *supervisor.Process {
	return supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		supervisor.Spec{Name: "dest:1", Kind: "destination", Bin: "true"})
}

// runningDest is a destination in the state reconcileOutputs leaves a healthy
// one in: a process, a hub it subscribed to, and a compiled graph.
func runningDest(t *testing.T, row *db.Destination) *destination {
	t.Helper()
	return &destination{
		row:      row,
		proc:     loudTestProc(),
		hub:      loudTestHub(t),
		compiled: routing.Result{FilterComplex: "[0:a:0]anull[aout]", OutLabel: routing.OutLabel},
		spec:     "specA",
	}
}

func TestLoudnessWantedOnlyMonitorsDestinationsThatAreOnAir(t *testing.T) {
	on := db.Settings{Meters: db.MeterSettings{Enabled: true}}
	live := func() *db.Destination {
		return &db.Destination{ID: 1, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube}
	}

	tests := []struct {
		name string
		dest func(t *testing.T) *destination
		want bool
	}{
		{
			name: "a running destination earns an analyser",
			dest: func(t *testing.T) *destination { return runningDest(t, live()) },
			want: true,
		},
		{
			name: "a destination that failed to start is not measured",
			dest: func(t *testing.T) *destination {
				d := runningDest(t, live())
				d.err = "rendition 3 is not running"
				return d
			},
		},
		{
			name: "a destination with no process is sending nothing to measure",
			dest: func(t *testing.T) *destination {
				d := runningDest(t, live())
				d.proc = nil
				return d
			},
		},
		{
			name: "a destination with no upstream hub has nothing to subscribe to",
			dest: func(t *testing.T) *destination {
				d := runningDest(t, live())
				d.hub = nil
				return d
			},
		},
		{
			name: "a destination with no compiled graph cannot be measured post-routing",
			dest: func(t *testing.T) *destination {
				d := runningDest(t, live())
				d.compiled = routing.Result{}
				return d
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{dests: map[int64]*destination{1: tc.dest(t)}}
			got := e.loudnessWanted(on)
			if _, ok := got[1]; ok != tc.want {
				t.Fatalf("wanted=%v, want %v", ok, tc.want)
			}
		})
	}
}

func TestLoudnessWantedRespectsTheOffSwitches(t *testing.T) {
	tests := []struct {
		name    string
		meters  bool
		loudOff bool
		stopped bool
		want    bool
	}{
		{name: "on by default when metering is on", meters: true, want: true},
		{name: "off when metering is off", meters: false},
		{name: "off when the operator switched the tier off", meters: true, loudOff: true},
		{name: "off once the engine is shutting down", meters: true, stopped: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{
				dests: map[int64]*destination{1: runningDest(t, &db.Destination{
					ID: 1, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
				})},
				loudOff: tc.loudOff,
				stopped: tc.stopped,
			}
			got := e.loudnessWanted(db.Settings{Meters: db.MeterSettings{Enabled: tc.meters}})
			if (len(got) > 0) != tc.want {
				t.Fatalf("got %d plans, want any=%v", len(got), tc.want)
			}
		})
	}
}

func TestLoudnessPlanTargetsComeFromTheProfileThenThePlatform(t *testing.T) {
	on := db.Settings{Meters: db.MeterSettings{Enabled: true}}

	tests := []struct {
		name       string
		row        *db.Destination
		wantSource meters.TargetSource
		wantLUFS   float64
	}{
		{
			name: "an explicit profile target is what the destination is judged on",
			row: &db.Destination{ID: 1, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
				Profile: routing.Profile{Loudness: &routing.Loudness{TargetLUFS: -18}}},
			wantSource: meters.TargetProfile, wantLUFS: -18,
		},
		{
			name:       "without one, the platform's own normalizer supplies the number",
			row:        &db.Destination{ID: 1, Name: "tw", Kind: db.DestRTMP, Platform: db.PlatformTwitch},
			wantSource: meters.TargetPlatform, wantLUFS: routing.LUFSStreaming,
		},
		{
			name:       "a file destination is an archive and is not judged",
			row:        &db.Destination{ID: 1, Name: "archive", Kind: db.DestFile, Platform: db.PlatformYouTube},
			wantSource: meters.TargetNone,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{dests: map[int64]*destination{1: runningDest(t, tc.row)}}
			p, ok := e.loudnessWanted(on)[1]
			if !ok {
				t.Fatal("expected a plan")
			}
			if p.target.Source != tc.wantSource || p.target.LUFS != tc.wantLUFS {
				t.Fatalf("target = %+v, want %q at %v LUFS", p.target, tc.wantSource, tc.wantLUFS)
			}
		})
	}
}

func TestLoudnessSignatureMovesWithTheGraphAndTheTarget(t *testing.T) {
	on := db.Settings{Meters: db.MeterSettings{Enabled: true}}
	sigFor := func(t *testing.T, mutate func(*destination)) string {
		t.Helper()
		d := runningDest(t, &db.Destination{ID: 1, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube})
		mutate(d)
		e := &Engine{dests: map[int64]*destination{1: d}}
		return e.loudnessWanted(on)[1].sig
	}

	baseSig := sigFor(t, func(*destination) {})

	tests := []struct {
		name   string
		mutate func(*destination)
		same   bool
	}{
		{
			name:   "renaming a destination does not cycle its meter",
			mutate: func(d *destination) { d.row.Name = "YouTube Main" },
			same:   true,
		},
		{
			name:   "changing the routing graph does",
			mutate: func(d *destination) { d.spec = "specB" },
		},
		{
			name: "changing the loudness target does",
			mutate: func(d *destination) {
				d.row.Profile.Loudness = &routing.Loudness{TargetLUFS: -23}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sigFor(t, tc.mutate)
			if (got == baseSig) != tc.same {
				t.Fatalf("sig %q vs base %q, want same=%v", got, baseSig, tc.same)
			}
		})
	}
}

func TestLoudnessAndClipStatusAreSafeOnAnEngineNewNeverBuilt(t *testing.T) {
	// Status is assembled on paths that have nothing to do with either tier,
	// and a nil store there would panic the whole dashboard.
	e := &Engine{}
	if got := e.Loudness(); len(got) != 0 {
		t.Fatalf("Loudness = %+v, want empty", got)
	}
	st := e.ClipBuffer()
	if st.Enabled || st.Running || st.Buffer != nil {
		t.Fatalf("ClipBuffer = %+v, want off", st)
	}
	e.reconcileLoudness(db.Settings{Meters: db.MeterSettings{Enabled: true}})
}

// TestALateStartFindsTheDestinationGoneAndPublishesNothing is #453.
//
// THE OBSERVED FAILURE. Adding one destination to a running engine and then
// deleting it left exactly one ffmpeg behind, on a Mac and on the OVH host
// alike: 5 processes before the add, 8 after, 6 after the delete. The survivor
// was not the destination's own encoder -- nothing matched its name -- it was
// the ebur128 loudness analyser started for it, holding a relay-hub port and
// subscribed to a hub that was about to be closed under it. Closing a hub stops
// UDP delivery and does not end a process, so it sat there receiving nothing.
//
// THE RACE. reconcileLoudness computes its wanted-set, releases the lock, and
// only then starts each analyser. A destination deleted inside that window is
// still in the plan. startLoudness guarded its publish with `if e.stopped`
// alone -- shutdown, but not deletion -- so the monitor was stored and started
// for a destination that no longer existed.
//
// Driven by calling startLoudness with a stale plan rather than by racing two
// goroutines: the window is real but narrow, and a test that has to win a race
// to fail is a test that passes for the wrong reason most of the time.
func TestALateStartFindsTheDestinationGoneAndPublishesNothing(t *testing.T) {
	row := &db.Destination{ID: 1, Name: "sibling", Kind: db.DestRTMP, Platform: db.PlatformYouTube}
	d := runningDest(t, row)

	e := &Engine{
		dests:     map[int64]*destination{1: d},
		loud:      map[int64]*loudnessMon{},
		loudStore: meters.NewStore(),
		alloc:     relay.NewPortAllocator(freeUDPPort(t), 4),
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		tools:     &ffmpeg.Tools{FFmpeg: "true"},
	}

	// The plan this reconcile is carrying, taken while the destination was live.
	plan, ok := loudnessPlanFor(1, d)
	if !ok {
		t.Fatal("fixture: a running destination must earn an analyser, or this proves nothing")
	}

	// THE DELETE LANDS HERE, between the plan and the start.
	delete(e.dests, 1)

	e.startLoudness(plan)

	if m := e.loud[1]; m != nil {
		t.Errorf("a monitor was published for a destination that had already been deleted "+
			"(port %d, sub %q). It holds a relay port and is subscribed to a hub that is "+
			"about to be closed under it, so it receives nothing and never exits -- which is "+
			"the process left behind by every add-then-delete in #453.", m.port, m.subName)
	}
}

// The guard must not OVER-reject, which would be worse and much quieter than the
// leak: a meter that never starts reads as a destination with no loudness at all.
//
// Asserted through the predicate rather than by calling startLoudness. The happy
// path starts a real supervised process, whose state callback reaches
// Engine.Status and from there the hub, the rendition store and the database --
// so a test of the guard would end up standing up most of an engine, and would
// fail for reasons that have nothing to do with the guard.
func TestTheLateStartGuardAcceptsADestinationThatHasNotChanged(t *testing.T) {
	row := &db.Destination{ID: 1, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube}
	d := runningDest(t, row)

	plan, ok := loudnessPlanFor(1, d)
	if !ok {
		t.Fatal("a running destination must earn an analyser")
	}
	again, ok := loudnessPlanFor(1, d)
	if !ok {
		t.Fatal("the same running destination stopped earning an analyser when asked twice")
	}
	if again.sig != plan.sig {
		t.Errorf("the signature moved for an unchanged destination (%q -> %q); the late-start "+
			"guard compares these, so an unstable signature would reject every start and "+
			"leave destinations silently unmetered", plan.sig, again.sig)
	}
}

// A destination DELETED AND RE-CREATED arrives back under the same id, and is not
// the thing the in-flight plan was made for. This is why the guard compares
// signatures rather than testing that the id is still present.
func TestALateStartRejectsADestinationReplacedUnderTheSameID(t *testing.T) {
	row := &db.Destination{ID: 1, Name: "sibling", Kind: db.DestRTMP, Platform: db.PlatformYouTube}
	d := runningDest(t, row)

	e := &Engine{
		dests:     map[int64]*destination{1: d},
		loud:      map[int64]*loudnessMon{},
		loudStore: meters.NewStore(),
		alloc:     relay.NewPortAllocator(freeUDPPort(t), 4),
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		tools:     &ffmpeg.Tools{FFmpeg: "true"},
	}
	plan, ok := loudnessPlanFor(1, d)
	if !ok {
		t.Fatal("fixture: a running destination must earn an analyser")
	}

	// Same id, different destination: a new graph means a new command line, so
	// the analyser this plan describes would be measuring the wrong thing.
	replacement := runningDest(t, row)
	replacement.spec = "specB"
	e.dests[1] = replacement

	e.startLoudness(plan)

	if m := e.loud[1]; m != nil {
		t.Errorf("a monitor from a stale plan was published against a destination that had "+
			"been replaced under the same id (port %d): it would measure a graph that is no "+
			"longer running", m.port)
	}
}
