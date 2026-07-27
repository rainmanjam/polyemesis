package clips

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The synthetic stream in ts_test.go proves the parser. This proves the
// assumption underneath it: that FFmpeg's mpegts muxer really does flag its
// keyframes with random_access_indicator, and that a clip cut on that flag is
// a file a decoder will open. If that assumption ever stops holding, every
// clip this product ships starts with two seconds of grey mush and nothing
// else in the suite would notice.
//
// Skipped without FFmpeg, and in -short: it spends a few seconds encoding.
func TestClipCutFromARealTransportStreamDecodes(t *testing.T) {
	if testing.Short() {
		t.Skip("encodes a real stream")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}

	c, err := Open(testLog(), Config{Dir: t.TempDir(), WindowSeconds: 30}, "udp://127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	target := "udp://127.0.0.1:" + strconv.Itoa(c.Addr().Port) + "?pkt_size=1316"
	// A one-second GOP, so a four-second request has several keyframes to
	// choose between and the backward search is exercised rather than skipped.
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=f=440",
		"-t", "8",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "30", "-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-f", "mpegts", target,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not produce a test stream (%v): %s", err, out)
	}

	// The last datagrams are still in flight when ffmpeg exits.
	deadline := time.Now().Add(3 * time.Second)
	for c.Stats().Seconds < 5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	stats := c.Stats()
	if !stats.VideoFound {
		t.Fatalf("the video PID was never learned from a real PAT/PMT: %+v", stats)
	}

	clip, err := c.Capture(4 * time.Second)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !clip.KeyframeAligned {
		t.Fatalf("a real h264 stream produced no random-access point: %s", clip.Note)
	}

	path, err := c.Resolve(clip.Name)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	probe := exec.Command(ffprobe, "-v", "error",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path)
	out, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe rejected the clip: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{"h264", "aac"} {
		if !strings.Contains(got, want) {
			t.Fatalf("clip is missing its %s stream:\n%s", want, got)
		}
	}

	// The first picture must decode. A cut that lands mid-GOP still probes
	// fine; it is the decode that gives it away.
	dec := exec.Command(ffmpeg, "-hide_banner", "-v", "error",
		"-i", path, "-frames:v", "1", "-f", "rawvideo", "-y", "-", "-loglevel", "error")
	if out, err := dec.CombinedOutput(); err != nil {
		t.Fatalf("the first frame of the clip did not decode: %v\n%s", err, out)
	}
}
