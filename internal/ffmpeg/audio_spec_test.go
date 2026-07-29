package ffmpeg

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func audioDest(kind DestKind, target string, a AudioSpec) []string {
	return DestinationArgs(DestSpec{
		Kind: kind, Target: target, RelayURL: "udp://127.0.0.1:20001",
		FilterComplex: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		AudioBitrate:  160, CopyVideo: true, Audio: a,
	})
}

// The zero value is AAC stereo, which is what every destination emitted before
// this existed.
func TestAudioZeroValueIsUnchanged(t *testing.T) {
	for _, k := range []struct {
		kind   DestKind
		target string
	}{
		{DestRTMP, "rtmp://a.example/live/key"},
		{DestSRT, "srt://a.example:9000"},
		{DestFile, "/data/out.mkv"},
	} {
		line := join(audioDest(k.kind, k.target, AudioSpec{}))
		if !strings.Contains(line, "-c:a aac") {
			t.Errorf("%s: default codec is not aac: %s", k.target, line)
		}
		if !strings.Contains(line, "-ac 2") {
			t.Errorf("%s: default is not stereo: %s", k.target, line)
		}
		if strings.Contains(line, "libopus") {
			t.Errorf("%s: opus appeared without being asked for: %s", k.target, line)
		}
	}
}

func TestMonoFoldsTheMixToOneChannel(t *testing.T) {
	for _, k := range []struct {
		kind   DestKind
		target string
	}{
		{DestRTMP, "rtmp://a.example/live/key"},
		{DestSRT, "srt://a.example:9000"},
		{DestAudio, "icecast://source:pw@a.example:8000/live.mp3"},
	} {
		line := join(audioDest(k.kind, k.target, AudioSpec{Mono: true}))
		if !strings.Contains(line, "-ac 1") {
			t.Errorf("%s: mono was requested and the output is not one channel: %s", k.target, line)
		}
		if strings.Contains(line, "-ac 2") {
			t.Errorf("%s: emitted both -ac 1 and -ac 2: %s", k.target, line)
		}
	}
}

// FFmpeg will happily WRITE Opus into FLV -- Enhanced RTMP defines a mapping,
// and a probe produced a valid 8.6 KB file. No mainstream ingest accepts it.
// A stream that muxes cleanly, uploads cleanly and is rejected by the platform
// is the worst failure available: it looks correct everywhere the operator can
// see.
func TestOpusIsRefusedOnRTMPAndAllowedOnSRT(t *testing.T) {
	rtmp := join(audioDest(DestRTMP, "rtmp://a.example/live/key", AudioSpec{Codec: AudioCodecOpus}))
	if strings.Contains(rtmp, "libopus") {
		t.Errorf("opus reached an RTMP destination: %s", rtmp)
	}
	if !strings.Contains(rtmp, "-c:a aac") {
		t.Errorf("RTMP did not fall back to aac: %s", rtmp)
	}

	srt := join(audioDest(DestSRT, "srt://a.example:9000", AudioSpec{Codec: AudioCodecOpus}))
	if !strings.Contains(srt, "-c:a libopus") {
		t.Errorf("opus did NOT reach an SRT destination, so the option does nothing: %s", srt)
	}
}

// Measured, not asserted from a string: the point of mono is that the delivered
// audio really has one channel, and a flag that FFmpeg silently ignored would
// pass every check above.
func TestMonoActuallyProducesOneChannel(t *testing.T) {
	bins := needFFmpeg(t, "ffmpeg", "ffprobe")
	ffmpeg, ffprobe := bins[0], bins[1]

	for _, tc := range []struct {
		name string
		mono bool
		want string
	}{
		{"stereo", false, "2"},
		{"mono", true, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir() + "/out.ts"
			args := []string{"-hide_banner", "-v", "error", "-nostdin",
				"-f", "lavfi", "-i", "sine=f=300:d=0.5",
				"-f", "lavfi", "-i", "sine=f=900:d=0.5",
				"-filter_complex", "[0:a][1:a]amerge=inputs=2,pan=stereo|c0=c0|c1=c1[aout]",
				"-map", "[aout]"}
			args = append(args, audioCodecArgs(DestSpec{
				Kind: DestSRT, AudioBitrate: 96, SampleRate: 48000,
				Audio: AudioSpec{Mono: tc.mono},
			})...)
			args = append(args, "-f", "mpegts", "-y", out)

			cmd := exec.Command(ffmpeg, args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("ffmpeg: %v\n%s", err, stderr.String())
			}

			probe := exec.Command(ffprobe, "-v", "error",
				"-show_entries", "stream=channels", "-of", "default=nw=1:nk=1", out)
			got, err := probe.Output()
			if err != nil {
				t.Fatalf("ffprobe: %v", err)
			}
			if ch := strings.Fields(string(got)); len(ch) == 0 || ch[0] != tc.want {
				t.Errorf("delivered %v channels, want %s", ch, tc.want)
			}
		})
	}
}
