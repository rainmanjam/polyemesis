package engine

import (
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/playout"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// silenceEngine is the smallest engine the tier's decision function needs: it
// reads only settings, the probe flag and the track list.
// measured, not probed: wantSilence asks whether a probe ever SUCCEEDED for
// this ingest, not whether one is arriving right now. The two differ exactly
// when a stream goes quiet, which is the gap the tier has to survive.
func silenceEngine(measured bool, tracks int) *Engine {
	src := routing.Source{}
	for i := 0; i < tracks; i++ {
		src.Tracks = append(src.Tracks, routing.Track{Index: i, Channels: 2, Codec: "aac"})
	}
	return &Engine{measured: measured, source: src}
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
		name     string
		enabled  bool
		measured bool
		tracks   int
		want     bool
	}{
		{
			name:    "a probed video-only ingest gets a synthetic track",
			enabled: true, measured: true, tracks: 0, want: true,
		},
		{
			// The one failure this product cannot have. -map 1:a:0 in
			// SilenceArgs means a tier started over a real multitrack ingest
			// would publish silence and drop every track the operator selected.
			name:    "an ingest that carries audio never gets one",
			enabled: true, measured: true, tracks: 4, want: false,
		},
		{
			name:    "a single-track ingest never gets one",
			enabled: true, measured: true, tracks: 1, want: false,
		},
		{
			// "We have not looked yet" is not evidence of anything. Acting on it
			// would synthesise for every ingest during the seconds before the
			// first probe lands.
			name:    "an unmeasured ingest gets nothing",
			enabled: true, measured: false, tracks: 0, want: false,
		},
		{
			name:    "an unmeasured ingest gets nothing even with a stale track list",
			enabled: true, measured: false, tracks: 2, want: false,
		},
		{
			// THE ROW THIS TABLE WAS MISSING, and the bug it hid. probeLoop
			// clears `probed` a few rounds after a stream stops but leaves
			// e.source alone, so a video-only ingest that goes quiet is still
			// measured with zero tracks. Reading `probed` here tore the tier
			// down, and reconcileOutputs then compiled every destination against
			// zero tracks -- routing.Compile answers ErrNoAudio, so they were all
			// torn down for as long as the encoder was quiet.
			name:    "a video-only ingest that went idle keeps its tier",
			enabled: true, measured: true, tracks: 0, want: true,
		},
		{
			name:    "the setting off means no tier whatever the probe says",
			enabled: false, measured: true, tracks: 0, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := silenceEngine(tt.measured, tt.tracks)
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

// Same idea for fonts, and the same reason: config spells "fonts" out as a
// string literal because it is a leaf package and cannot import ffmpeg. If the
// two ever disagree, startup writes the embedded fonts into one directory while
// the font picker lists another, and every overlay fails with a font the
// operator can see sitting on disk.
func TestFontsDirMatchesTheFfmpegPackage(t *testing.T) {
	cfg := config.Config{DataDir: "/var/lib/polyemesis"}
	want := filepath.Join(cfg.DataDir, ffmpeg.FontsDirName)
	if got := cfg.FontsDir(); got != want {
		t.Errorf("config.FontsDir() = %q, ffmpeg.FontsDirName gives %q", got, want)
	}
}
