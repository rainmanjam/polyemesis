package engine

import (
	"io"
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
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
