package rtmpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// THE SPAN NOTHING COVERED, AND THE ONE THE DEFECT LIVES IN. #674.
//
// TestEnhancedRTMPMultitrackSurvivesTheSharedListenerInOrder proves the
// listener presents three correctly configured audio streams to ffprobe. The
// container suite proves a destination fed from the relay produces measurable
// per-track audio. Between them sits the FLV -> MPEG-TS remux that IngestArgs
// actually runs, and NOTHING exercised it -- so when that remux started
// emitting AAC with no channel configuration, both neighbours stayed green.
//
// What that cost: EVERY destination that encodes audio on an enhanced-RTMP
// ingest fails, silently -- file destinations writing matroska and RTMP
// destinations writing flv alike. FFmpeg cannot resolve the audio parameters,
// the filter graph refuses with "Neither number of channels nor channel layout
// specified", the encoder never opens, and nothing is written. The server logs
// a started destination and looks healthy.
//
// "Could not open encoder BEFORE EOF" is the shape: the encoder never opened
// for the entire publish and said so only when the input ended. There is no
// moment at which the destination reports itself broken.
//
// NOT a copy-versus-encode distinction, which is what this comment said first
// and CI disproved: destA/B/C are `kind: file` destinations that encode AAC to
// .mkv, and they fail with the same -22 as the RTMP one. Only the separate
// recorder child, which is `-map 0 -c copy`, is genuinely unaffected.
//
// This test runs the SHIPPED IngestArgs -- not a hand-written approximation of
// it -- against the SHIPPED listener, and asks ffprobe what came out. A
// transcribed argv would be a test of the transcription; the whole point is
// that the deployed command is the thing under suspicion.

// tsOutput swaps the relay URL for a file, leaving every other argument as
// IngestArgs built it.
//
// The relay is a UDP hop and a test that read from it would be measuring the
// relay too. The remux is the subject, so its output is redirected and nothing
// else is touched -- the flags before it, which are what this test exists to
// exercise, arrive exactly as they ship.
func tsOutput(args []string, path string) []string {
	out := append([]string(nil), args...)
	out[len(out)-1] = path
	return out
}

func TestTheIngestRemuxKeepsEachAACTracksChannelConfiguration(t *testing.T) {
	ffmpegBin, ffprobeBin := requireShippedFFmpeg(t)

	// THE SHIPPED FFmpeg, OR THIS PROVES NOTHING.
	//
	// #674 needs BOTH the live listener path and FFmpeg 8.x. Measured while
	// writing this test:
	//
	//   - a multi-track FLV FILE remuxed to TS keeps its channel configuration
	//     on 8.1.2 AND on 9.0.1, so this is not a generic remux bug;
	//   - the same live path through this listener on 9.0.1 reports
	//     "aac (LC), 48000 Hz, stereo, fltp" for all three tracks -- correct.
	//
	// So a developer machine on 9.x runs this test green and learns nothing.
	// Skipping is the honest outcome: a pass on the wrong FFmpeg would be a
	// green check asserting something it never tested, which is the failure
	// this whole audit exists to remove.

	tg := Target{SourceID: 1, Name: "Main", Enabled: true, Ready: true}
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(map[string]Target{"mt": tg}))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()

	_, portStr, ok := strings.Cut(addr, ":")
	if !ok {
		t.Fatalf("listener address %q has no port", addr)
	}
	port, convErr := strconv.Atoi(portStr)
	if convErr != nil {
		t.Fatalf("listener port %q: %v", portStr, convErr)
	}

	// Three tracks, stereo, at distinct sample rates -- the same shape the
	// container suite publishes and the same shape an enhanced-RTMP encoder
	// sends. Stereo matters here: the assertion is that CHANNELS survive.
	pubCtx, stopPub := context.WithCancel(context.Background())
	defer stopPub()
	pub := exec.CommandContext(pubCtx, ffmpegBin, "-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=1700:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-b:v", "500k",
		"-c:a", "aac", "-ac", "2",
		"-f", "flv", "rtmp://"+addr+"/live/mt")
	var pubErr strings.Builder
	pub.Stderr = &pubErr
	if err := pub.Start(); err != nil {
		t.Fatalf("publisher: %v", err)
	}
	// Kill AND Wait: Kill only asks, and an unreaped child holds a slot in this
	// process's table. #197.
	defer func() { _ = pub.Process.Kill(); _ = pub.Wait() }()
	waitPublishing(t, s, tg.SourceID, 25*time.Second)

	tsPath := filepath.Join(t.TempDir(), "relay.ts")
	args := tsOutput(ffmpeg.IngestArgs(ffmpeg.IngestSpec{
		Kind:        ffmpeg.IngestRTMP,
		RTMPPort:    port,
		RTMPApp:     "live",
		RTMPAddress: "mt",
		RelayURL:    "udp://127.0.0.1:1", // replaced by tsOutput; never dialled
	}), tsPath)

	ingCtx, stopIngest := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopIngest()
	ing := exec.CommandContext(ingCtx, ffmpegBin, args...)
	var ingErr, ingOut strings.Builder
	ing.Stderr = &ingErr
	ing.Stdout = &ingOut
	if err := ing.Start(); err != nil {
		t.Fatalf("ingest remux: %v", err)
	}
	// Long enough for the muxer to emit its tables and several seconds of
	// media; the assertion is about what a DESTINATION would find on opening
	// this, and a destination opens a stream already in flight.
	time.Sleep(8 * time.Second)
	_ = ing.Process.Kill()
	_ = ing.Wait()

	st, statErr := os.Stat(tsPath)
	if statErr != nil || st.Size() == 0 {
		t.Fatalf("the remux wrote nothing (%v).\n\nargv: ffmpeg %s\n\nstderr:\n%s\n\npublisher stderr:\n%s",
			statErr, strings.Join(args, " "), ingErr.String()+"\n--- stdout ---\n"+ingOut.String(), pubErr.String())
	}

	// JSON, NOT CSV LINES. On an MPEG-TS ffprobe emits every stream TWICE --
	// once under the program it belongs to and once under the top-level stream
	// list, the second copy padded with empty fields. Counting csv lines
	// therefore reported 7 for a file with 3 audio streams, and the test failed
	// on a correct remux while claiming the stream count was wrong. #674.
	out, probeErr := exec.Command(ffprobeBin, "-hide_banner", "-loglevel", "error",
		"-select_streams", "a", "-show_streams",
		"-of", "json", tsPath).Output()
	if probeErr != nil {
		t.Fatalf("ffprobe: %v", probeErr)
	}
	var probed struct {
		Streams []struct {
			Channels   int    `json:"channels"`
			SampleRate string `json:"sample_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probed); err != nil {
		t.Fatalf("ffprobe json: %v\n%s", err, out)
	}
	if len(probed.Streams) != 3 {
		t.Fatalf("expected 3 audio streams in the remuxed TS, got %d:\n%s\n\ningest stderr:\n%s",
			len(probed.Streams), out, ingErr.String())
	}
	for i, st := range probed.Streams {
		if st.Channels == 0 || st.SampleRate == "" {
			t.Errorf("audio stream %d came out of the ingest remux without channel "+
				"configuration (%q).\n\nEvery destination that must ENCODE this audio "+
				"then fails with \"Neither number of channels nor channel layout "+
				"specified\", writes nothing, and never connects -- while the server "+
				"logs a started destination. Recording destinations are unaffected "+
				"because -c copy needs no layout, which is why routing still measures "+
				"correct. #674.\n\nfull probe output:\n%s\n\ningest stderr:\n%s",
				i, fmt.Sprintf("channels=%d sample_rate=%q", st.Channels, st.SampleRate),
				out, ingErr.String())
		}
	}
}

// ffmpegMajor reports the major version of the binary under test.
func ffmpegMajor(t *testing.T, bin string) int {
	t.Helper()
	out, err := exec.Command(bin, "-version").Output()
	if err != nil {
		t.Fatalf("ffmpeg -version: %v", err)
	}
	f := strings.Fields(string(out))
	if len(f) < 3 {
		t.Fatalf("unreadable ffmpeg -version output: %q", string(out))
	}
	// A GIT BUILD DOES NOT START WITH A DIGIT. The shipped artefact reports
	// "n8.1.2-34-g9b6c8969e0-20260812", so cutting at the first "." yielded
	// "n8" and this fataled on every CI runner -- the test never ran the path
	// it exists to cover, and said so as a failure rather than a skip. Take the
	// first run of digits instead, which reads a release ("8.1.2") and a git
	// description identically.
	n, ok := leadingInt(f[2])
	if !ok {
		t.Fatalf("unreadable ffmpeg version %q", f[2])
	}
	return n
}

// leadingInt returns the first run of digits in s.
func leadingInt(s string) (int, bool) {
	start := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if start < 0 {
		return 0, false
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[start:end])
	return n, err == nil
}
