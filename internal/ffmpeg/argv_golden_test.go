package ffmpeg_test

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
	. "github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// withLogLevel runs the argv builders under POLYEMESIS_FFMPEG_LOGLEVEL=lvl
// ("" means unset). See argvgold for how the goldens are regenerated.
func withLogLevel(t *testing.T, lvl string) {
	t.Helper()
	argvgold.LogLevel(t, lvl)
}

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
		"preview":       PreviewArgs(PreviewSpec{RelayURL: "udp://127.0.0.1:20003", OutputDir: "/tmp/preview"}),
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
			t.Run(name+suffix, func(t *testing.T) { argvgold.Check(t, name+suffix, argv) })
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
