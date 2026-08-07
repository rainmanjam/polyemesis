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
	defer func() { _ = pub.Process.Kill() }()
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
