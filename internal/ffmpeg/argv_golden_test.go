package ffmpeg_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
	. "github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// previewDir is a slash path the preview builder joins onto, so Check reads it
// the same on Windows.
const previewDir = "/tmp/preview"

// ffmpegChildren is one argv per kind of FFmpeg child this package builds.
func ffmpegChildren() map[string][]string {
	dest := DestSpec{
		Kind: DestRTMP, Target: "rtmp://live.example/app/KEY", RelayURL: "udp://127.0.0.1:20001",
		FilterComplex: "[0:a:0]anull[aout]", AudioBitrate: 160, SampleRate: 48000, CopyVideo: true,
	}
	destAudioCopy := dest
	destAudioCopy.CopyAudio = true
	destFile := dest
	destFile.Kind, destFile.Target = DestFile, "/var/lib/polyemesis/recordings/out.mkv"
	return map[string][]string{
		"ingest_rtmp": IngestArgs(IngestSpec{Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live", RTMPAddress: "tok-abc", RelayURL: "udp://127.0.0.1:20000"}),
		"ingest_srt":  IngestArgs(IngestSpec{Kind: IngestSRT, SRTPort: 6000, SRTLatencyMS: 200, RelayURL: "udp://127.0.0.1:20000"}),
		"ingest_pull": IngestArgs(IngestSpec{Kind: IngestPull, PullURL: "rtsp://cam.local/stream1", RelayURL: "udp://127.0.0.1:20000"}),
		// The diagnostic second output (POLYEMESIS_INGEST_CAPTURE, startupenv).
		"ingest_capture": IngestArgs(IngestSpec{Kind: IngestSRT, SRTPort: 6000, SRTLatencyMS: 200, RelayURL: "udp://127.0.0.1:20000", CaptureTo: "/tmp/capture.ts"}),

		"destination_rtmp":       DestinationArgs(dest),
		"destination_copy_audio": DestinationArgs(destAudioCopy),
		"destination_file":       DestinationArgs(destFile),

		"recorder":      RecorderArgs(RecorderSpec{RelayURL: "udp://127.0.0.1:20002", SegmentSeconds: 600}),
		"stem_recorder": StemRecorderArgs(StemRecorderSpec{RecorderSpec: RecorderSpec{RelayURL: "udp://127.0.0.1:20002"}, Codec: StemFLAC, Stems: []StemSpec{{Path: "/rec/stem0.flac", Track: 0}}}),
		"preview":       PreviewArgs(PreviewSpec{RelayURL: "udp://127.0.0.1:20003", OutputDir: previewDir}),
		"meters":        MetersArgs(MetersSpec{RelayURL: "udp://127.0.0.1:20004", TrackChannels: []int{2, 2}, SampleRate: 48000}),

		"rendition": RenditionArgs(RenditionSpec{
			InRelayURL: "udp://127.0.0.1:20000", OutRelayURL: "udp://127.0.0.1:20010",
			Height: 1080, FPS: 60, VideoKbps: 6000,
		}),
		"silence":      SilenceArgs(SilenceSpec{InRelayURL: "udp://127.0.0.1:20000", OutRelayURL: "udp://127.0.0.1:20011"}),
		"slate_colour": SlateArgs(SlateSpec{OutRelayURL: "udp://127.0.0.1:20020", Color: "0x112233@0.5"}),
		"slate_offset": SlateArgs(SlateSpec{OutRelayURL: "udp://127.0.0.1:20020", Color: "navy", TimestampOffsetSeconds: 12.5}),

		"probe_ingest": ProbeArgs("udp://127.0.0.1:20000", 3),
	}
}

func TestFFmpegChildArgvIsPinned(t *testing.T) {
	// The goldens are the Linux argv: a destination there takes live commands, so
	// it has no -nostdin. Windows keeps the flag (TestWindowsDestinationKeepsNostdin).
	PinLiveCommandsGOOS(t, "linux")
	for _, lvl := range []string{"", "debug"} {
		argvgold.LogLevel(t, lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		for name, argv := range ffmpegChildren() {
			// ffprobe's -loglevel only shapes its stderr, which is quoted back in an
			// error; the override is for FFmpeg children.
			if lvl != "" && name == "probe_ingest" {
				continue
			}
			t.Run(name+suffix, func(t *testing.T) { argvgold.Check(t, name+suffix, argv, previewDir) })
		}
	}
}

// Every child that READS the relay, and every child that WRITES it, goes through
// one owner (RelayConsumerInput, RelayProducerOutput). The goldens above pin the
// bytes; this pins the reason.
func TestRelayReadersAndWritersCarryTheSharedOptions(t *testing.T) {
	kids := ffmpegChildren()
	for _, name := range []string{"destination_rtmp", "destination_copy_audio", "destination_file", "recorder", "stem_recorder", "preview", "meters", "rendition", "silence"} {
		argvgold.RequireRelayConsumer(t, name, kids[name])
	}
	for _, name := range []string{"ingest_rtmp", "ingest_srt", "ingest_pull", "rendition", "silence", "slate_colour", "slate_offset"} {
		argvgold.RequireRelayProducer(t, name, kids[name])
	}
}

// The one-shot probes honour the override too, each keeping its own default
// when it is unset. (The poster frame in internal/api/playout.go does the same.)
func TestOneShotProbesHonourTheLogLevelOverride(t *testing.T) {
	level := func(argv []string) string {
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == "-loglevel" {
				return argv[i+1]
			}
		}
		return ""
	}
	cases := map[string]struct {
		argv func() []string
		def  string
	}{
		"duration count": {func() []string { return CountDurationArgs("mpegts", "/x.ts") }, "error"},
	}
	for name, c := range cases {
		SetLogLevel("")
		if got := level(c.argv()); got != c.def {
			t.Errorf("%s: default level %q, want %q", name, got, c.def)
		}
		SetLogLevel("debug")
		if got := level(c.argv()); got != "debug" {
			t.Errorf("%s ignores the override: %q", name, got)
		}
	}
	SetLogLevel("")
}

// The other half of the Linux pin above: on a platform without live commands the
// destination keeps -nostdin and runs the command it always did, and the preview
// child, which never takes commands, keeps it everywhere.
func TestWindowsDestinationKeepsNostdin(t *testing.T) {
	PinLiveCommandsGOOS(t, "linux")
	kids := ffmpegChildren()
	if slices.Contains(kids["destination_rtmp"], "-nostdin") {
		t.Fatal("the pinned Linux destination carries -nostdin")
	}
	PinLiveCommandsGOOS(t, "windows")
	if LiveCommandsSupported() {
		t.Fatal("a windows pin still claims live commands")
	}
	for name, argv := range ffmpegChildren() {
		if strings.HasPrefix(name, "destination_") && !slices.Contains(argv, "-nostdin") {
			t.Errorf("%s on windows lost -nostdin: %q", name, argv)
		}
	}
	if !slices.Contains(PreviewArgs(PreviewSpec{RelayURL: "udp://127.0.0.1:20003", OutputDir: previewDir}), "-nostdin") {
		t.Error("the preview child lost -nostdin")
	}
}
