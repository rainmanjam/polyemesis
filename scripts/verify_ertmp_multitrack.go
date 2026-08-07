//go:build ignore

// Does an E-RTMP multitrack stream survive a round trip through FFmpeg intact?
//
// Run:
//
//	go run scripts/verify_ertmp_multitrack.go                  # both codecs, 3 runs
//	go run scripts/verify_ertmp_multitrack.go -runs 5
//	go run scripts/verify_ertmp_multitrack.go -runs 1 -shuffle # prove it can fail
//
// Builds a six-track FLV (routing.MaxTracks worth of tones is more than anyone
// needs; six is what the docs example uses), publishes it into an
// `ffmpeg -listen 1` receiver, and checks what arrives.
//
// WHAT THIS DOES AND DOES NOT COVER
// It is a conformance check on FFMPEG: can this build mux six E-RTMP tracks, and
// demux them again with the order intact. That is worth knowing on its own — the
// answer is no below 7.1, which includes the stock build on Ubuntu 24.04 — and
// it is why the tone detection is here.
//
// It is NOT polyemesis's ingest path, and it used to claim to be. IngestArgs
// passed `-listen 1` when FFmpeg was the RTMP server; the listener is now
// internal/rtmpserver and the ingest child DIALS it. The path that ships is
// covered by TestEnhancedRTMPMultitrackSurvivesTheSharedListenerInOrder, which
// publishes through the real server and identifies the tracks on arrival.
//
// Keeping that distinction straight matters: while this harness was passing,
// late subscribers to a real multitrack stream were receiving decoder
// configuration for the legacy track only, because rtmpserver did not recognise
// a sequence start wrapped in AudioExMultitrack. Nothing here can see that —
// there is no rtmpserver in it.
//
// WHY IT MEASURES TONES RATHER THAN COUNTING STREAMS
// Six tracks in and six tracks out looks identical whether or not they were
// reordered, and a reordering is the failure that matters — polyemesis routes by
// track index, so a shifted order silently sends the wrong audio to a platform
// while every screen still looks correct. So each track carries a distinct tone
// and is identified on arrival by its content.
//
// To confirm the harness can actually fail, pass -shuffle: it republishes with
// the tracks permuted and must report exactly that permutation.
//
// Requires ffmpeg/ffprobe with libopus. Nothing else: the tone detector is a
// Goertzel filter rather than an FFT, so there is no numeric library to install.
// That was also true of the Python this replaces, which was the repo's only
// Python file.
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// freqs are the six published tones, one per track, far enough apart that a
// Goertzel bin cannot mistake one for its neighbour.
var freqs = []int{300, 500, 700, 1100, 1300, 1700}

// shufflePerm is the permutation -shuffle publishes in. Fixed rather than
// random so a failure is reproducible from the command line alone.
var shufflePerm = []int{3, 0, 5, 1, 4, 2}

const (
	port = 11935
	// sr is the rate the tone detector decodes at, not the rate anything is
	// published at. 8 kHz is comfortably above the highest tone and keeps the
	// Goertzel loop short.
	sr = 8000
)

// ---------------------------------------------------------------- processes

func run(argv ...string) ([]byte, []byte, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	var stdout, stderr []byte
	outPipe, _ := cmd.StdoutPipe()
	errPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() { stderr, _ = readAll(errPipe); close(done) }()
	stdout, _ = readAll(outPipe)
	<-done
	return stdout, stderr, cmd.Wait()
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 1<<16)
	tmp := make([]byte, 1<<15)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

func trunc(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

// ------------------------------------------------------------ tone detection

// goertzel returns the power at one frequency. Cheaper than an FFT and needs no
// numeric library.
func goertzel(samples []int16, freq int) float64 {
	w := 2 * math.Pi * float64(freq) / float64(sr)
	coeff := 2 * math.Cos(w)
	var s1, s2 float64
	for _, x := range samples {
		s0 := float64(x) + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	return s1*s1 + s2*s2 - coeff*s1*s2
}

// toneOf decodes one second of one audio stream and reports which published
// tone it carries, plus how far ahead that tone was of the runner-up. A margin
// near 1.0 means the detector could not tell them apart and the result should
// not be trusted.
func toneOf(path string, stream int) (int, float64) {
	stdout, _, err := run("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path,
		"-map", fmt.Sprintf("0:a:%d", stream), "-t", "1", "-f", "s16le", "-ac", "1",
		"-ar", strconv.Itoa(sr), "-")
	// Half a second of 16-bit mono is the floor for a usable measurement.
	if err != nil || len(stdout) < sr {
		return 0, 0
	}
	n := len(stdout) / 2
	samples := make([]int16, n)
	for i := 0; i < n; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(stdout[i*2:]))
	}
	best, bestPower, runner := 0, math.Inf(-1), 0.0
	for _, f := range freqs {
		p := goertzel(samples, f)
		if p > bestPower {
			best, bestPower = f, p
		}
	}
	for _, f := range freqs {
		if f == best {
			continue
		}
		if p := goertzel(samples, f); p > runner {
			runner = p
		}
	}
	if runner == 0 {
		runner = 1
	}
	return best, bestPower / runner
}

// ------------------------------------------------------------------ sessions

type result struct {
	err        string
	probeAudio int
	probeVideo int
	order      []int
	minMargin  float64
}

// oneSession publishes the FLV once and reports what came out the other side.
func oneSession(dir string, tag int, flv string) result {
	ts := filepath.Join(dir, fmt.Sprintf("rx-%d.ts", tag))
	// The mpegts muxer and `-map 0 -c copy` that the destination side still
	// uses, with `-listen 1` standing in for an RTMP server. Not IngestArgs:
	// see the package comment.
	listener := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-listen", "1",
		"-fflags", "+genpts", "-i", fmt.Sprintf("rtmp://127.0.0.1:%d/live/test", port),
		"-map", "0", "-c", "copy", "-f", "mpegts", "-flush_packets", "1",
		"-y", ts)
	if err := listener.Start(); err != nil {
		return result{err: "listener: " + err.Error()}
	}
	// The listener has to be accepting before the publisher dials it. Racing
	// this produces "connection refused" and reads as a codec failure.
	time.Sleep(1500 * time.Millisecond)

	_, pubErr, err := run("ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
		"-i", flv, "-map", "0", "-c", "copy", "-f", "flv",
		fmt.Sprintf("rtmp://127.0.0.1:%d/live/test", port))

	waited := make(chan error, 1)
	go func() { waited <- listener.Wait() }()
	select {
	case <-waited:
	case <-time.After(30 * time.Second):
		_ = listener.Process.Kill()
		<-waited
	}
	if err != nil {
		return result{err: trunc(pubErr, 200)}
	}

	// polyemesis's real ProbeArgs (internal/ffmpeg/build.go).
	probeOut, _, perr := run("ffprobe", "-hide_banner", "-loglevel", "error",
		"-print_format", "json", "-show_streams", "-show_format",
		"-analyzeduration", "5000000", "-probesize", "5000000", "-i", ts)
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if perr == nil {
		_ = json.Unmarshal(probeOut, &probe)
	}

	var audio, video int
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "audio":
			audio++
		case "video":
			video++
		}
	}

	out := result{probeAudio: audio, probeVideo: video, minMargin: math.Inf(1)}
	for i := 0; i < audio; i++ {
		f, m := toneOf(ts, i)
		out.order = append(out.order, f)
		if m < out.minMargin {
			out.minMargin = m
		}
	}
	if len(out.order) == 0 {
		out.minMargin = 0
	}
	return out
}

// -------------------------------------------------------------------- fixture

// build writes six tones plus video into one FLV.
//
// AAC gives track 0 a legacy tag; Opus has no legacy FLV SoundFormat, so track 0
// goes out as ExHeader+fourCC — which is the shape OBS writes track 0 in
// (plugins/obs-outputs/flv-mux.c). Testing both covers both.
func build(dir, codec string, shuffled bool) (string, []int, error) {
	name := fmt.Sprintf("six-%s.flv", codec)
	if shuffled {
		name = fmt.Sprintf("six-%s-shuf.flv", codec)
	}
	out := filepath.Join(dir, name)

	order := append([]int(nil), freqs...)
	if shuffled {
		order = order[:0]
		for _, i := range shufflePerm {
			order = append(order, freqs[i])
		}
	}

	argv := []string{"ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15:duration=3"}
	for _, f := range order {
		argv = append(argv, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=3", f))
	}
	argv = append(argv, "-map", "0:v")
	for i := range order {
		argv = append(argv, "-map", fmt.Sprintf("%d:a", i+1))
	}
	encoder := "aac"
	if codec == "opus" {
		encoder = "libopus"
	}
	argv = append(argv, "-c:v", "libx264", "-preset", "ultrafast",
		"-c:a", encoder, "-b:a", "96k", "-f", "flv", out)

	if _, stderr, err := run(argv...); err != nil {
		return "", nil, fmt.Errorf("could not build %s: %s", out, trunc(stderr, 300))
	}
	return out, order, nil
}

// tags walks the FLV and reports what audio tag headers it contains, which is
// how you tell a genuine E-RTMP multitrack stream from a legacy FLV that merely
// has the right number of streams.
func tags(path string) (map[string]int, error) {
	d, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seen := map[string]int{}
	off := 9 // past the FLV header
	for off+11 <= len(d) {
		off += 4 // PreviousTagSize
		if off+11 > len(d) {
			break
		}
		typ := d[off] & 0x1f
		size := int(d[off+1])<<16 | int(d[off+2])<<8 | int(d[off+3])
		end := off + 11 + size
		if end > len(d) {
			break
		}
		body := d[off+11 : end]
		if typ == 8 && len(body) > 0 {
			b := body[0]
			hi, lo := b>>4, b&0xf
			var kind string
			switch {
			case hi != 9:
				kind = "legacy"
			case lo == 5:
				kind = "ExHeader Multitrack"
			default:
				kind = fmt.Sprintf("ExHeader pkt=%d", lo)
			}
			seen[fmt.Sprintf("0x%02x %s", b, kind)]++
		}
		off = end
	}
	return seen, nil
}

// ----------------------------------------------------------------------- main

func main() {
	runs := flag.Int("runs", 3, "publish this many times per codec")
	codec := flag.String("codec", "both", "aac, opus, or both")
	shuffle := flag.Bool("shuffle", false, "publish permuted; the harness must report the permutation")
	keep := flag.Bool("keep", false, "keep the working directory instead of removing it")
	flag.Parse()
	// A bare number still works, because the docs and the shell history are
	// full of `... 5`.
	if n := flag.Arg(0); n != "" {
		if v, err := strconv.Atoi(n); err == nil {
			*runs = v
		}
	}

	codecs := []string{"aac", "opus"}
	if *codec != "both" {
		codecs = []string{*codec}
	}

	dir, err := os.MkdirTemp("", "ertmp-verify-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create a working directory:", err)
		os.Exit(1)
	}
	if *keep {
		fmt.Println("working directory:", dir)
	} else {
		defer func() { _ = os.RemoveAll(dir) }()
	}

	ok := true
	for _, c := range codecs {
		flv, published, err := build(dir, c, *shuffle)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("\n=== %s === published order %v\n", c, published)

		seen, err := tags(flv)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot read the built FLV:", err)
			os.Exit(1)
		}
		keys := make([]string, 0, len(seen))
		for k := range seen {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("    %s  x%d\n", k, seen[k])
		}

		for i := 0; i < *runs; i++ {
			r := oneSession(dir, i, flv)
			if r.err != "" {
				fmt.Printf("  run %d: FAILED %s\n", i, r.err)
				ok = false
				continue
			}
			match := equal(r.order, published)
			if !match || r.probeAudio != len(freqs) {
				ok = false
			}
			verdict := "MATCH"
			if !match {
				verdict = "*** MISMATCH ***"
			}
			fmt.Printf("  run %d: probe %dv+%da  order %v  %s\n",
				i, r.probeVideo, r.probeAudio, r.order, verdict)
		}
	}

	if *shuffle {
		fmt.Println("\n-shuffle: MATCH lines above are correct — the harness tracked the permutation.")
	}
	if ok {
		fmt.Println("\nVERDICT: six tracks, order preserved, every run")
		return
	}
	fmt.Println("\nVERDICT: FAILED")
	os.Exit(1)
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
