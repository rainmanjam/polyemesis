package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/playout"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// silenceEngine is the smallest engine the tier's decision function needs: it
// reads only settings, the probe flag and the track list.
func silenceEngine(probed bool, tracks int) *Engine {
	src := routing.Source{}
	for i := 0; i < tracks; i++ {
		src.Tracks = append(src.Tracks, routing.Track{Index: i, Channels: 2, Codec: "aac"})
	}
	return &Engine{probed: probed, source: src}
}

func silenceSettings(on bool) db.Settings {
	s := db.DefaultSettings()
	s.Synth.SilenceOnVideoOnly = on
	return s
}

// The tier is the only thing in the pipeline that can invent an audio track, so
// what it refuses to do matters more than what it does. Every "want: false" row
// here is a case where synthesising would either drop the operator's real audio
// or act on evidence the engine does not have.
func TestWantSilenceOnlyOnAProbeThatFoundNoAudio(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		probed  bool
		tracks  int
		want    bool
	}{
		{
			name:    "a probed video-only ingest gets a synthetic track",
			enabled: true, probed: true, tracks: 0, want: true,
		},
		{
			// The one failure this product cannot have. -map 1:a:0 in
			// SilenceArgs means a tier started over a real multitrack ingest
			// would publish silence and drop every track the operator selected.
			name:    "an ingest that carries audio never gets one",
			enabled: true, probed: true, tracks: 4, want: false,
		},
		{
			name:    "a single-track ingest never gets one",
			enabled: true, probed: true, tracks: 1, want: false,
		},
		{
			// "We have not looked yet" is not evidence of anything. Acting on it
			// would synthesise for every ingest during the seconds before the
			// first probe lands.
			name:    "an unprobed ingest gets nothing",
			enabled: true, probed: false, tracks: 0, want: false,
		},
		{
			name:    "an unprobed ingest gets nothing even with a stale track list",
			enabled: true, probed: false, tracks: 2, want: false,
		},
		{
			name:    "the setting off means no tier whatever the probe says",
			enabled: false, probed: true, tracks: 0, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := silenceEngine(tt.probed, tt.tracks)
			got := e.wantSilence(silenceSettings(tt.enabled)) != ""
			if got != tt.want {
				t.Errorf("wantSilence() != \"\" = %v, want %v", got, tt.want)
			}
		})
	}
}

// The tier publishes exactly one stereo track, and every routing graph below it
// is compiled against that. If this ever describes more tracks than the tier
// actually produces, a profile would select a track that does not exist.
func TestSynthTrackIsOneStereoTrack(t *testing.T) {
	src := synthTrack()
	if len(src.Tracks) != 1 {
		t.Fatalf("synthTrack() has %d tracks, want 1", len(src.Tracks))
	}
	if got := src.Tracks[0].Channels; got != 2 {
		t.Errorf("channels = %d, want 2", got)
	}
	if got := src.Tracks[0].Index; got != 0 {
		t.Errorf("index = %d, want 0", got)
	}
	// A profile selecting the one track must compile, or a video-only ingest
	// would gain a silence tier and still refuse to start its destinations.
	if _, err := routing.Compile(routing.DefaultProfile(), src); err != nil {
		t.Errorf("the default profile does not compile against the synthetic source: %v", err)
	}
}

// Switching synthesis on or off moves every consumer onto a different relay.
// Anything that does not restart keeps reading a hub that has closed.
func TestSilenceRestartsEveryConsumer(t *testing.T) {
	row := testDestination(1, nil)
	compiled := routing.Result{FilterComplex: "[0:a:0]anull[out]", OutLabel: "[out]"}

	if destSpec(row, compiled, "") == destSpec(row, compiled, "silence-sig") {
		t.Error("a passthrough destination does not restart when the silence tier appears")
	}

	r := testRendition(1, "1080p60")
	if renditionSig(r, 60, "", "") == renditionSig(r, 60, "silence-sig", "") {
		t.Error("a rendition does not restart when the silence tier appears")
	}
}

// The engine's own accessor has to agree with the playout package about where
// the directory is, or the handler serves one path while the muxers write to
// another. config cannot import playout, so this is where the two meet.
func TestPlayoutDirMatchesThePlayoutPackage(t *testing.T) {
	cfg := config.Config{DataDir: "/var/lib/polyemesis"}
	if got, want := cfg.PlayoutDir(), playout.DirIn(cfg.DataDir); got != want {
		t.Errorf("config.PlayoutDir() = %q, playout.DirIn() = %q", got, want)
	}
}
