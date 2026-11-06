package argvgold_test

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/meters"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// Every relay reader that gained the shared input options and the shared output
// epilogue (rendition, silence synthesiser, loudness monitor, selector feeds)
// runs against a REAL FFmpeg reading a real udp:// input, which is the only
// input the options apply to: an input option a demuxer does not own is fatal
// before a byte is read ("Option scan_all_pmts not found", exit 8), and
// RelayConsumerInput strips scan_all_pmts for anything that is not udp://, so a
// test that hands the builder a file never runs the new option set at all.
//
// "Runs" is: still running after the stream has been flowing, with no error. The
// argv goldens pin the bytes; this pins that FFmpeg accepts them.
func TestRelayReadersWithTheSharedOptionsRunAgainstARealUDPInput(t *testing.T) {
	ffmpegBin := testenv.FFmpegBinary(t, "ffmpeg", "no ffmpeg on PATH: the shared relay options go unrun")

	build := func(in string, out string) map[string][]string {
		return map[string][]string{
			"rendition": ffmpeg.RenditionArgs(ffmpeg.RenditionSpec{
				InRelayURL: in, OutRelayURL: out, Encoder: "libx264", Height: 120, FPS: 30, VideoKbps: 300,
			}),
			"silence": ffmpeg.SilenceArgs(ffmpeg.SilenceSpec{InRelayURL: in, OutRelayURL: out}),
			"loudness": meters.Args(meters.Spec{
				RelayURL: in, FilterComplex: "[0:a:0]anull[aout]", OutLabel: "aout",
			}),
			"ingest meter":   ffmpeg.MetersArgs(ffmpeg.MetersSpec{RelayURL: in, TrackChannels: []int{1}}),
			"selector feed":  ffmpeg.RelayFeedArgs(in, out, 5),
			"conformed feed": ffmpeg.ConformedFeedArgs(in, out, 5, []ffmpeg.AudioStream{{Index: 0, SampleRate: 44100, Channels: 2}}),
		}
	}

	for _, name := range []string{"rendition", "silence", "loudness", "ingest meter", "selector feed", "conformed feed"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in, out := testenv.FreeUDPPort(t), testenv.FreeUDPPort(t)
			args := build("udp://127.0.0.1:"+strconv.Itoa(in), "udp://127.0.0.1:"+strconv.Itoa(out))[name]

			var stderr strings.Builder
			consumer := exec.Command(ffmpegBin, args...)
			consumer.Stderr = &stderr
			if err := consumer.Start(); err != nil {
				t.Fatalf("start the reader: %v", err)
			}
			exited := make(chan error, 1)
			go func() { exited <- consumer.Wait() }()
			t.Cleanup(func() { _ = consumer.Process.Kill(); <-exited })

			// The reader must have bound its input before the head of the stream.
			time.Sleep(750 * time.Millisecond)
			pub := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-re",
				"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=30",
				"-f", "lavfi", "-i", "sine=f=440:r=44100",
				"-t", "10", "-map", "0:v", "-map", "1:a",
				"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "30", "-c:a", "aac",
				"-f", "mpegts", "-flush_packets", "1",
				"udp://127.0.0.1:"+strconv.Itoa(in)+"?pkt_size=1316")
			if err := pub.Start(); err != nil {
				t.Fatalf("start the publisher: %v", err)
			}
			pubDone := make(chan error, 1)
			go func() { pubDone <- pub.Wait() }()
			t.Cleanup(func() { _ = pub.Process.Kill(); <-pubDone })

			select {
			case err := <-exited:
				t.Fatalf("the reader exited while the stream was flowing: %v\n%s", err, stderr.String())
			case <-time.After(5 * time.Second):
			}
			if strings.Contains(stderr.String(), "not found") {
				t.Errorf("FFmpeg did not recognise an option:\n%s", stderr.String())
			}
		})
	}
}
