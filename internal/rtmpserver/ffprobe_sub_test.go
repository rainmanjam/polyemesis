package rtmpserver

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// End to end with the real tools on both ends: FFmpeg publishes, ffprobe
// subscribes, and the assertion is POSITIVE — ffprobe must report the actual
// codecs. An earlier version of this test asserted only the ABSENCE of error
// strings and passed on empty output, which proved nothing at all.
//
// This is also the only test that exercises what a real encoder puts on the
// wire, which is where gortmplib's chunk-stream handling either copes or does
// not.
func TestRealFFmpegPublishesAndRealFFprobeSubscribes(t *testing.T) {
	if testing.Short() {
		t.Skip("runs two FFmpeg processes")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}

	tg := Target{SourceID: 1, Name: "Main", Enabled: true, Ready: true}
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(map[string]Target{"k": tg}))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()
	target := "rtmp://" + addr + "/live/k"

	pubCtx, stopPub := context.WithCancel(context.Background())
	defer stopPub()
	pub := exec.CommandContext(pubCtx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-b:v", "500k",
		"-c:a", "aac", "-f", "flv", target)
	if err := pub.Start(); err != nil {
		t.Fatalf("publisher: %v", err)
	}
	// Kill AND Wait. Kill only asks; until something reaps the child it is a
	// zombie holding a slot in this process's table, and on a driver that
	// starts several children in sequence that is a leak with a name (#197).
	defer func() { _ = pub.Process.Kill(); _ = pub.Wait() }()
	time.Sleep(2 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobe, "-hide_banner", "-loglevel", "error",
		"-rw_timeout", "10000000",
		"-show_entries", "stream=codec_type,codec_name", "-of", "csv=p=0", target).CombinedOutput()

	got := strings.TrimSpace(string(out))
	t.Logf("ffprobe reported: %q (err=%v)", got, err)

	if !strings.Contains(got, "video") || !strings.Contains(got, "audio") {
		t.Fatalf("ffprobe did not see a video and an audio stream through the relay; got %q", got)
	}
	if !strings.Contains(got, "h264") || !strings.Contains(got, "aac") {
		t.Errorf("the codecs did not survive the relay untouched; got %q", got)
	}
}

// E-RTMP MULTITRACK, through the shared listener, with the tracks identified on
// arrival rather than counted.
//
// Multitrack is the reason this project exists: a destination selects which
// ingest tracks it sends, BY INDEX. So "six went in and six came out" is not
// the property that matters — a reordering passes that check and silently ships
// the wrong audio to a platform while every screen still looks right.
//
// Each track therefore carries a distinct sample rate, and the assertion is on
// the SEQUENCE. Sample rate rather than a tone because it survives `-c copy`
// untouched and needs no DSP in the test: rtmpserver relays messages and never
// decodes, so if it reorders or drops anything the sequence changes.
//
// scripts/verify_ertmp_multitrack.go does the deeper version of this — six
// tracks identified by Goertzel tone detection, with a -shuffle mode to prove
// the harness can fail. What it does NOT do any more is exercise this path: it
// publishes into `ffmpeg -listen 1`, which is what IngestArgs used to build
// before the listener became polyemesis's own. This test covers the path that
// actually ships.
func TestEnhancedRTMPMultitrackSurvivesTheSharedListenerInOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("runs two FFmpeg processes")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}

	tg := Target{SourceID: 1, Name: "Main", Enabled: true, Ready: true}
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(map[string]Target{"mt": tg}))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()
	target := "rtmp://" + addr + "/live/mt"

	pubCtx, stopPub := context.WithCancel(context.Background())
	defer stopPub()
	pub := exec.CommandContext(pubCtx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=300",
		"-f", "lavfi", "-i", "sine=frequency=900",
		"-f", "lavfi", "-i", "sine=frequency=1700",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-b:v", "500k",
		"-c:a", "aac",
		// The identity of each track, carried in a field `-c copy` cannot alter.
		"-ar:a:0", "48000", "-ar:a:1", "44100", "-ar:a:2", "32000",
		"-f", "flv", target)
	var pubErr strings.Builder
	pub.Stderr = &pubErr
	if err := pub.Start(); err != nil {
		t.Fatalf("publisher: %v", err)
	}
	// Kill AND Wait. Kill only asks; until something reaps the child it is a
	// zombie holding a slot in this process's table, and on a driver that
	// starts several children in sequence that is a leak with a name (#197).
	defer func() { _ = pub.Process.Kill(); _ = pub.Wait() }()
	time.Sleep(3 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobe, "-hide_banner", "-loglevel", "error",
		"-rw_timeout", "15000000",
		"-show_entries", "stream=codec_type,sample_rate", "-of", "csv=p=0", target).CombinedOutput()

	got := strings.TrimSpace(string(out))
	t.Logf("ffprobe reported:\n%s\n(err=%v)", got, err)

	// A publisher that never connected is the failure most likely to be
	// mistaken for "multitrack is unsupported", so it is named separately.
	// FFmpeg below 7.1 cannot mux multitrack FLV at all.
	if got == "" {
		t.Fatalf("nothing arrived through the listener. publisher said: %s", pubErr.String())
	}

	var rates []string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "audio,") {
			rates = append(rates, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "audio,")))
		}
	}
	want := []string{"48000", "44100", "32000"}
	if len(rates) != len(want) {
		t.Fatalf("expected %d audio tracks through the relay, got %d: %q", len(want), len(rates), got)
	}
	for i := range want {
		if rates[i] != want[i] {
			t.Fatalf("track order changed across the relay: got %v, want %v. "+
				"Destinations select ingest tracks by index, so a reordering sends "+
				"the wrong audio to a platform with nothing on screen to show it", rates, want)
		}
	}
	if !strings.Contains(got, "video") {
		t.Error("the video track did not survive alongside the audio tracks")
	}
}
