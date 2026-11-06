package playout

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The HLS variant reads the relay with the same input options as every other
// consumer (ffmpeg.RelayConsumerInput), which it used to write by hand without
// the probe limits and scan_all_pmts. The argv goldens pin the bytes; this runs
// them against a real FFmpeg, because an input option a demuxer does not own is
// fatal before a byte is read ("Option scan_all_pmts not found", exit 8) and no
// test of the argv can see that.
func TestAHLSVariantWithTheSharedRelayInputOptionsStartsAndWritesAPlaylist(t *testing.T) {
	ffmpegBin := testenv.FFmpegBinary(t, "ffmpeg", "no ffmpeg on PATH: the variant's input options go unrun")
	port := testenv.FreeUDPPort(t)
	dir := t.TempDir()

	args := VariantArgs(VariantSpec{
		Name: "main", RelayURL: "udp://127.0.0.1:" + strconv.Itoa(port), Dir: dir, SegmentSeconds: 1,
	})
	var consumerErr lockedBuffer
	consumer := exec.Command(ffmpegBin, args...)
	consumer.Stderr = &consumerErr
	if err := consumer.Start(); err != nil {
		t.Fatalf("start the variant: %v", err)
	}
	t.Cleanup(func() { _ = consumer.Process.Kill(); _ = consumer.Wait() })

	// The consumer must have bound its input before the head of the stream.
	time.Sleep(750 * time.Millisecond)
	pub := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=30",
		"-f", "lavfi", "-i", "sine=f=440:r=48000",
		"-t", "8", "-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "30", "-c:a", "aac",
		"-f", "mpegts", "-flush_packets", "1",
		"udp://127.0.0.1:"+strconv.Itoa(port)+"?pkt_size=1316")
	if err := pub.Start(); err != nil {
		t.Fatalf("start the publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Process.Kill(); _ = pub.Wait() })

	playlist := filepath.Join(dir, MediaPlaylist)
	deadline := time.Now().Add(25 * time.Second)
	for {
		if b, err := os.ReadFile(playlist); err == nil && strings.Contains(string(b), "#EXTINF") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the variant wrote no media playlist within 25s; its stderr:\n%s", consumerErr.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// lockedBuffer is a stderr sink the test may read while FFmpeg is still writing.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
