package delayline

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/delayline/delaytest"
	"github.com/rainmanjam/polyemesis/internal/mpegts"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The dump against real encoder output: libx264 with B-frames and AAC, muxed
// by FFmpeg exactly as the relay's ingest muxes it, dumped mid-stream, then
// handed to FFmpeg's own decoders and to the remux every RTMP destination
// runs. The synthetic tests in dump_test.go say what the splice does; these
// say a real decoder agrees.

// tsFixture encodes seconds of test pattern and tone to MPEG-TS.
func tsFixture(t *testing.T, ffmpeg string, seconds int, extra ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "src.ts")
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", strconv.Itoa(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-bf", "2", "-g", "60", "-keyint_min", "60",
		"-sc_threshold", "0", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k"}
	args = append(args, extra...)
	args = append(args, "-f", "mpegts", out)
	if msg, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("encoding the fixture: %v\n%s", err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pcrStream cuts a TS file into 1316-byte datagrams, each timed by the last
// PCR before it: the pace a live relay would deliver it at.
func pcrStream(t *testing.T, ts []byte) *delaytest.Stream {
	t.Helper()
	s := &delaytest.Stream{}
	first, last := int64(-1), int64(0)
	for off := 0; off+mpegts.PacketSize <= len(ts); off += mpegts.PacketSize {
		p := ts[off : off+mpegts.PacketSize]
		if at := pcrAt(p); at >= 0 {
			v := readPCRBase(p[at:])
			if first < 0 {
				first = v
			}
			last = wrap(v - first)
		}
		s.Pkts = append(s.Pkts, p)
		s.Times = append(s.Times, delaytest.Ticks(last))
	}
	// Datagrams of seven, all stamped with their first packet's time.
	for i := 0; i < len(s.Times); i += chunkPackets {
		for j := i; j < i+chunkPackets && j < len(s.Times); j++ {
			s.Times[j] = s.Times[i]
		}
	}
	return s
}

type probePacket struct {
	CodecType string `json:"codec_type"`
	PTS       *int64 `json:"pts"`
	DTS       *int64 `json:"dts"`
	Flags     string `json:"flags"`
}

func probePackets(t *testing.T, ffprobe, path string) (video, audio []probePacket) {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-show_entries",
		"packet=codec_type,pts,dts,flags", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var doc struct {
		Packets []probePacket `json:"packets"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range doc.Packets {
		switch p.CodecType {
		case "video":
			video = append(video, p)
		case "audio":
			audio = append(audio, p)
		}
	}
	return video, audio
}

// decodeErrors runs FFmpeg's decoders over a file and returns what they said.
func decodeErrors(t *testing.T, ffmpeg, path string) string {
	t.Helper()
	cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-i", path, "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("decoding %s: %v\n%s", path, err, stderr.String())
	}
	return stderr.String()
}

// checkMonotonic fails on any DTS that does not increase, and on any jump
// bigger than maxStep, in the stream's own time base.
func checkMonotonic(t *testing.T, what string, pkts []probePacket, maxStep int64) {
	t.Helper()
	var prev int64
	have := false
	for i, p := range pkts {
		if p.DTS == nil {
			continue
		}
		if have {
			if *p.DTS <= prev {
				t.Fatalf("%s packet %d: DTS %d after %d -- not monotonic", what, i, *p.DTS, prev)
			}
			if *p.DTS-prev > maxStep {
				t.Fatalf("%s packet %d: DTS jumps %d (max %d) -- the dumped time is still on the timeline",
					what, i, *p.DTS-prev, maxStep)
			}
		}
		prev, have = *p.DTS, true
	}
}

func TestADumpedLibx264StreamDecodesCleanlyAndRemuxesToFLVWithMonotonicTimestamps(t *testing.T) {
	ffmpeg := testenv.FFmpegBinary(t, "ffmpeg", "ffmpeg is needed to encode and decode a dumped stream")
	ffprobe := testenv.FFmpegBinary(t, "ffprobe", "ffprobe is needed to read a dumped stream's timestamps")
	src := tsFixture(t, ffmpeg, 30)
	dir := t.TempDir()

	r := newRig(t, 12*time.Second, memLimits(), pcrStream(t, src))
	r.run(18 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(18*time.Second), 3*time.Second, DumpMargin)
	if err != nil {
		t.Fatalf("Dump on libx264 output: %v", err)
	}
	r.run(40 * time.Second)
	res := ticket.Result()
	if !res.Complete || res.Dumped < 3*time.Second {
		t.Fatalf("result %+v: want a complete dump of at least 3 s", res)
	}

	out := filepath.Join(dir, "dumped.ts")
	if err := os.WriteFile(out, r.out, 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := decodeErrors(t, ffmpeg, out); msg != "" {
		t.Errorf("FFmpeg's decoders reported errors on the dumped stream:\n%s", msg)
	}
	srcPath := filepath.Join(dir, "src.ts")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	sv, _ := probePackets(t, ffprobe, srcPath)
	v, a := probePackets(t, ffprobe, out)
	// 90 kHz: one frame is 3000; audio frames are 1920.
	checkMonotonic(t, "video", v, 2*delaytest.FrameTicks)
	checkMonotonic(t, "audio", a, 2*delaytest.AudioTicks)
	dropped := len(sv) - len(v)
	if want := int(res.Dumped * 30 / time.Second); dropped < want-2 || dropped > want+2 {
		t.Errorf("%d pictures missing from the output; a %s dump is %d", dropped, res.Dumped, want)
	}
	// The first picture after the splice is a keyframe: find the DTS where
	// the source's numbering would jump, via ffprobe's K flag.
	keys := 0
	for _, p := range v {
		if bytes.Contains([]byte(p.Flags), []byte("K")) {
			keys++
		}
	}
	if keys == 0 {
		t.Fatal("no keyframes in the output")
	}

	// THE DESTINATION'S OWN REMUX. Video copied, audio re-encoded, to FLV: what
	// every RTMP destination runs on what the delay releases.
	flv := filepath.Join(dir, "dumped.flv")
	remux := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-y",
		"-dts_delta_threshold", "31536000", "-i", out,
		"-map", "0:v:0", "-c:v", "copy", "-map", "0:a:0", "-c:a", "aac", "-f", "flv", flv)
	if msg, err := remux.CombinedOutput(); err != nil || len(bytes.TrimSpace(msg)) > 0 {
		t.Fatalf("the destination remux of the dumped stream: %v\n%s", err, msg)
	}
	if msg := decodeErrors(t, ffmpeg, flv); msg != "" {
		t.Errorf("decoding the remuxed FLV:\n%s", msg)
	}
	fv, fa := probePackets(t, ffprobe, flv)
	// FLV is milliseconds: a frame is 33, an AAC frame 21.
	checkMonotonic(t, "flv video", fv, 2*34)
	checkMonotonic(t, "flv audio", fa, 2*22)
}

func TestADumpRefusesLibx264OpenGOPWhoseKeyframesAreNotIDR(t *testing.T) {
	ffmpeg := testenv.FFmpegBinary(t, "ffmpeg", "ffmpeg is needed to encode an open-GOP stream")
	src := tsFixture(t, ffmpeg, 20, "-x264-params", "open-gop=1")
	r := newRig(t, 8*time.Second, memLimits(), pcrStream(t, src))
	r.run(14 * time.Second)
	_, err := r.b.Dump(r.epoch.Add(14*time.Second), 2*time.Second, DumpMargin)
	if err == nil {
		t.Fatal("dumped an open-GOP stream at a picture that is not IDR")
	}
	if !errors.Is(err, ErrNoCutPoint) && !errors.Is(err, ErrNotSpliceable) {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

// remuxClean runs the destination's own remux (video copied, audio
// re-encoded, FLV) over a dumped stream and returns what FFmpeg said at
// warning level, and the FLV's packets.
func remuxClean(t *testing.T, ffmpeg, ffprobe, ts string) (string, []probePacket, []probePacket) {
	t.Helper()
	flv := ts + ".flv"
	cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "warning", "-y",
		"-dts_delta_threshold", "31536000", "-i", ts,
		"-map", "0:v:0", "-c:v", "copy", "-map", "0:a:0", "-c:a", "aac", "-f", "flv", flv)
	msg, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the destination remux failed: %v\n%s", err, msg)
	}
	v, a := probePackets(t, ffprobe, flv)
	return string(bytes.TrimSpace(msg)), v, a
}

// THE REAL-ENCODER SWEEP. GOP sizes that do and do not divide the audio
// framing, three audio bitrates, x264 with B-frames, HEVC with RADL pictures,
// and Opus: each dumped mid-stream and put through the destination's own FLV
// remux. FFmpeg's mpegts muxer groups several audio frames per PES, so the
// first audio after a resume overlaps the last audio kept unless it is cut at
// a frame boundary -- and an overlap makes the remux log a timestamp
// discontinuity and shift its whole input, video included.
//
// Mutation: never set r.trimming in decideResumeLocked. Observed: remux logs
// "timestamp discontinuity" and "Non-monotonic DTS" on most cases.
func TestTheRealEncoderSweepDumpsWithoutADiscontinuityInTheDestinationRemux(t *testing.T) {
	ffmpeg := testenv.FFmpegBinary(t, "ffmpeg", "ffmpeg is needed to encode and remux the sweep")
	ffprobe := testenv.FFmpegBinary(t, "ffprobe", "ffprobe is needed to read the sweep's timestamps")
	type source struct {
		name string
		args []string
	}
	var sources []source
	for _, gop := range []int{37, 47, 53, 60, 61} {
		for _, kbps := range []string{"96k", "128k", "192k"} {
			g := strconv.Itoa(gop)
			sources = append(sources, source{"x264-g" + g + "-a" + kbps, []string{
				"-c:v", "libx264", "-preset", "ultrafast", "-bf", "2", "-g", g, "-keyint_min", g,
				"-sc_threshold", "0", "-c:a", "aac", "-b:a", kbps}})
		}
	}
	sources = append(sources,
		source{"hevc-radl2", []string{"-c:v", "libx265", "-preset", "ultrafast", "-x265-params",
			"keyint=60:min-keyint=60:scenecut=0:open-gop=0:bframes=3:radl=2:log-level=error", "-c:a", "aac"}},
		source{"x264-bf2-opus", []string{"-c:v", "libx264", "-preset", "ultrafast", "-bf", "2", "-g", "60",
			"-keyint_min", "60", "-sc_threshold", "0", "-c:a", "libopus"}},
	)
	for _, src := range sources {
		t.Run(src.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "src.ts")
			args := []string{"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30",
				"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
				"-t", "30", "-pix_fmt", "yuv420p"}
			args = append(append(args, src.args...), "-f", "mpegts", in)
			if msg, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
				t.Fatalf("encoding: %v\n%s", err, msg)
			}
			raw, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			r := newRig(t, 12*time.Second, memLimits(), pcrStream(t, raw))
			r.run(18 * time.Second)
			ticket, err := r.b.Dump(r.epoch.Add(18*time.Second), 3*time.Second, DumpMargin)
			if err != nil {
				t.Fatalf("Dump: %v", err)
			}
			r.run(40 * time.Second)
			if res := ticket.Result(); !res.Complete || res.Dumped < 3*time.Second {
				t.Fatalf("result %+v", res)
			}
			out := filepath.Join(dir, "dumped.ts")
			if err := os.WriteFile(out, r.out, 0o600); err != nil {
				t.Fatal(err)
			}
			if msg := decodeErrors(t, ffmpeg, out); msg != "" {
				t.Errorf("decode errors:\n%s", msg)
			}
			log, fv, fa := remuxClean(t, ffmpeg, ffprobe, out)
			for _, bad := range []string{"discontinuity", "Non-monotonic", "backward", "corrupt", "Invalid"} {
				if strings.Contains(log, bad) {
					t.Fatalf("the destination remux logged %q:\n%s", bad, log)
				}
			}
			checkMonotonic(t, "flv video", fv, 2*34)
			checkMonotonic(t, "flv audio", fa, 2*22)
		})
	}
}

// craAfterIDR encodes HEVC that has IDR keyframes for its first `closed`
// seconds and only CRA keyframes (x265's open GOP) after -- a failover to an
// open-GOP source -- joined by FFmpeg's concat demuxer into one MPEG-TS with a
// continuous timeline.
func craAfterIDR(t *testing.T, ffmpeg string, closed, open int) []byte {
	t.Helper()
	dir := t.TempDir()
	part := func(name string, seconds int, openGOP string) {
		args := []string{"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-t", strconv.Itoa(seconds), "-pix_fmt", "yuv420p",
			"-c:v", "libx265", "-preset", "ultrafast", "-x265-params",
			"keyint=60:min-keyint=60:scenecut=0:bframes=3:log-level=error:open-gop=" + openGOP,
			"-c:a", "aac", "-f", "mpegts", filepath.Join(dir, name)}
		if msg, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("encoding %s: %v\n%s", name, err, msg)
		}
	}
	part("a.ts", closed, "0")
	part("b.ts", open, "1")
	list := filepath.Join(dir, "list.txt")
	if err := os.WriteFile(list, []byte("file a.ts\nfile b.ts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "src.ts")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "concat", "-i", list,
		"-c", "copy", "-f", "mpegts", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("joining: %v\n%s", err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// R-d WITH A REAL DECODER. The cut is at an IDR; after it the source sends
// only CRA keyframes, so the dump recovers at a CRA. FFmpeg's HEVC decoder
// must decode the result with no error -- the CRA rewritten to BLA, its RASL
// pictures (which reference what was dumped) gone -- and the destination's
// remux must log nothing.
func TestARecoveryAtARealHEVCCRADecodesCleanly(t *testing.T) {
	ffmpeg := testenv.FFmpegBinary(t, "ffmpeg", "ffmpeg is needed to encode and decode the CRA recovery")
	ffprobe := testenv.FFmpegBinary(t, "ffprobe", "ffprobe is needed to read the CRA recovery's timestamps")
	src := craAfterIDR(t, ffmpeg, 20, 30)
	r := newRig(t, 14*time.Second, memLimits(), pcrStream(t, src))
	r.run(23 * time.Second)
	tk, err := r.b.Dump(r.epoch.Add(23*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	r.run(60 * time.Second)
	res := tk.Result()
	if !res.Complete || !res.Recovery {
		t.Fatalf("result %+v: want a dump completed by recovery at a CRA", res)
	}
	out := filepath.Join(t.TempDir(), "dumped.ts")
	if err := os.WriteFile(out, r.out, 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := decodeErrors(t, ffmpeg, out); msg != "" {
		t.Fatalf("FFmpeg's decoder reported errors on the recovered stream:\n%s", msg)
	}
	log, fv, fa := remuxClean(t, ffmpeg, ffprobe, out)
	for _, bad := range []string{"discontinuity", "Non-monotonic", "backward", "corrupt", "Invalid"} {
		if strings.Contains(log, bad) {
			t.Fatalf("the destination remux logged %q:\n%s", bad, log)
		}
	}
	checkMonotonic(t, "flv video", fv, 2*34)
	checkMonotonic(t, "flv audio", fa, 2*22)
}
