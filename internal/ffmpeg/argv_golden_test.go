package ffmpeg

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
)

// withLogLevel runs the argv builders under POLYEMESIS_FFMPEG_LOGLEVEL=lvl
// ("" means unset). See argvgold for how the goldens are regenerated.
func withLogLevel(t *testing.T, lvl string) {
	t.Helper()
	t.Setenv("POLYEMESIS_FFMPEG_LOGLEVEL", lvl)
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
		withLogLevel(t, lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		for name, argv := range ffmpegChildren() {
			t.Run(name+suffix, func(t *testing.T) { argvgold.Check(t, name+suffix, argv) })
		}
	}
}
