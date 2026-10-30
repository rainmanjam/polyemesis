package routing

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// A FORMAT-CHANGING FAILOVER MUST NOT PUT THE STREAM'S RUNTIME IN FRONT OF THE
// AUDIO.
//
// A destination that rides a switch keeps reading the same relay, and the relay
// keeps the timeline moving forward. When the new feed decodes at another
// sample rate or channel count -- a 44.1 kHz mono backup behind a 48 kHz stereo
// primary, or the 48 kHz stereo slate behind a 44.1 kHz primary -- FFmpeg
// throws the destination's filter graph away and builds a new one from the
// argv, in the middle of the run. Every filter in it starts again.
//
// The final resample used to be `aresample=<rate>:async=1:first_pts=0`. A fresh
// instance of that filter meets its first frame at T seconds -- however long
// the stream has been up -- and, told the first sample belongs at zero, pads T
// seconds of silence in front of it. Those samples have timestamps the muxer
// has already passed, so it clamps them onto the current one: on the wire the
// audio is suddenly T seconds behind the video, and stays there until the
// destination restarts. Measured in Docker on main: 96 s of drift and 97.7 s
// of silence after a failover 98 s in (scripts/acceptance-audio-drift.sh).
//
// "Hidden audio" is the measurement: how much longer the decoded samples last
// than the audio timestamps say. Every padded second is a second of samples
// squeezed in at one timestamp, so the fault reads as roughly the runtime at
// the first change. A correct graph reads zero, or a little BELOW zero: a
// rebuilt instance has no previous timestamp to fill an outage from, so the
// outage stays a timestamp gap -- the same gap the copied video carries.

// formatChangeSegment is one stretch of the relay as a destination reads it:
// an audio format, a length, and where on the shared timeline it starts.
type formatChangeSegment struct {
	rate, channels int
	seconds        float64
	at             float64
}

// formatChangeRelay builds an MPEG-TS that changes audio format between
// segments and carries a little video throughout, so the output has a picture
// timeline to be measured against. Each segment is written at its own offset
// (as the selector's -output_ts_offset does), and a space between one
// segment's end and the next one's start is an outage.
func formatChangeRelay(t *testing.T, bin string, segs []formatChangeSegment) string {
	t.Helper()
	dir := t.TempDir()
	var parts []string
	for i, s := range segs {
		p := filepath.Join(dir, fmt.Sprintf("seg%d.ts", i))
		d := strconv.FormatFloat(s.seconds, 'f', 3, 64)
		out, err := exec.Command(bin, "-nostdin", "-v", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=10:duration="+d,
			"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:sample_rate=%d:duration=%s", s.rate, d),
			"-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-g", "10", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-ac", strconv.Itoa(s.channels),
			"-output_ts_offset", strconv.FormatFloat(s.at, 'f', 3, 64),
			"-f", "mpegts", p).CombinedOutput()
		if err != nil {
			t.Fatalf("building segment %d: %v\n%s", i, err, out)
		}
		parts = append(parts, p)
	}
	relay := filepath.Join(dir, "relay.ts")
	if out, err := exec.Command(bin, "-nostdin", "-v", "error", "-y",
		"-i", "concat:"+strings.Join(parts, "|"), "-map", "0", "-c", "copy",
		"-f", "mpegts", relay).CombinedOutput(); err != nil {
		t.Fatalf("joining segments: %v\n%s", err, out)
	}
	return relay
}

// destinationOutput runs the compiled graph the way a transcoding destination
// does -- video copied, routed audio encoded -- and returns the FLV it wrote.
func destinationOutput(t *testing.T, bin, relay string, res Result) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "dest.flv")
	if b, err := exec.Command(bin, "-nostdin", "-v", "error", "-y", "-i", relay,
		"-filter_complex", res.FilterComplex,
		"-map", "0:v", "-map", "["+res.OutLabel+"]",
		"-c:v", "copy", "-c:a", "aac", "-ar", "48000", "-f", "flv", out).CombinedOutput(); err != nil {
		t.Fatalf("the destination failed: %v\ngraph: %s\n%s", err, res.FilterComplex, b)
	}
	return out
}

// hiddenAudio is decoded audio duration minus the span its timestamps cover,
// in seconds. See the block comment above.
func hiddenAudio(t *testing.T, bin, probe, flv string) float64 {
	t.Helper()
	pcm, err := exec.Command(bin, "-nostdin", "-v", "error", "-i", flv,
		"-map", "0:a:0", "-ac", "1", "-ar", "48000", "-f", "s16le", "-").Output()
	if err != nil {
		t.Fatalf("decoding %s: %v", flv, err)
	}
	decoded := float64(len(pcm)) / (2 * 48000)

	pts, err := exec.Command(probe, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "packet=pts_time", "-of", "csv=p=0", flv).Output()
	if err != nil {
		t.Fatalf("probing %s: %v", flv, err)
	}
	lines := strings.Fields(string(pts))
	if len(lines) < 2 {
		t.Fatalf("%s carries %d audio packets", flv, len(lines))
	}
	first, _ := strconv.ParseFloat(lines[0], 64)
	last, _ := strconv.ParseFloat(lines[len(lines)-1], 64)
	return decoded - (last - first + 1024.0/48000)
}

// TWO changes in a row, because a failover is a round trip: 48 kHz stereo for
// 20 s, a 3 s outage, 10 s of a 44.1 kHz mono backup, then the 48 kHz stereo
// primary back. Each change is a rebuild, and the return is the one an
// operator meets after the longer runtime.
//
// MUTATION: put first_pts=0 back on the final resample in compile.
// Observed: FAIL, 52.86 s hidden: each rebuild pads from zero to where it
// starts (23 s, then 33 s), less the 3 s outage.
func TestAFormatChangingFailoverDoesNotPadTheRuntimeInFrontOfTheAudio(t *testing.T) {
	bin := testenv.FFmpegBinary(t, "ffmpeg",
		"ffmpeg not on PATH: lost the real-FFmpeg check that a filter-graph rebuild keeps audio on the video's timeline")
	probe := testenv.FFmpegBinary(t, "ffprobe",
		"ffprobe not on PATH: lost the real-FFmpeg check that a filter-graph rebuild keeps audio on the video's timeline")
	relay := formatChangeRelay(t, bin, []formatChangeSegment{
		{rate: 48000, channels: 2, seconds: 20, at: 0},
		{rate: 44100, channels: 1, seconds: 10, at: 23},
		{rate: 48000, channels: 2, seconds: 10, at: 33},
	})
	src := Source{Tracks: []Track{{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"}}}
	for _, compiled := range []struct {
		name string
		fn   func(Profile, Source) (Result, error)
	}{{"Compile", Compile}, {"CompileProvisional", CompileProvisional}} {
		res, err := compiled.fn(simple(NormOff, 0), src)
		if err != nil {
			t.Fatalf("%s: %v", compiled.name, err)
		}
		h := hiddenAudio(t, bin, probe, destinationOutput(t, bin, relay, res))
		t.Logf("%s: hidden audio %.3fs", compiled.name, h)
		// 0.1 s is lip-sync territory and two orders of magnitude under the
		// fault, which is the runtime. Below zero is the outage left as a
		// timestamp gap, and is bounded by it: more than its 3 s would be audio
		// lost rather than unpadded.
		if h > 0.1 {
			t.Errorf("%s: %.2fs of audio is hidden behind the timestamps after two format changes: "+
				"the rebuilt graph padded from zero instead of continuing\ngraph: %s",
				compiled.name, h, res.FilterComplex)
		}
		if h < -3.2 {
			t.Errorf("%s: the audio is %.2fs SHORT of its timestamps, more than the 3 s outage\ngraph: %s",
				compiled.name, -h, res.FilterComplex)
		}
	}
}
