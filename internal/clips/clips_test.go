package clips

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testCapturer opens a capturer on an ephemeral port with a clock the test
// drives, and returns a send function that advances it by one tick per
// datagram. A real clock would make the window arithmetic depend on how busy
// the machine running the tests happens to be.
func testCapturer(t *testing.T, cfg Config) (*Capturer, func(dgram []byte), func() time.Time) {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}

	var ticks atomic.Int64
	clock := func() time.Time { return base.Add(time.Duration(ticks.Load()) * time.Second) }

	c, err := Open(testLog(), cfg, "udp://127.0.0.1:0", nil, WithClock(clock))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	conn, err := net.DialUDP("udp", nil, c.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	sent := 0
	send := func(dgram []byte) {
		t.Helper()
		if _, err := conn.Write(dgram); err != nil {
			t.Fatalf("write: %v", err)
		}
		sent++
		// The read loop is a goroutine; wait for it rather than sleeping, so
		// the clock only advances once the datagram has been stamped.
		deadline := time.Now().Add(5 * time.Second)
		for c.Stats().Datagrams < uint64(sent) {
			if time.Now().After(deadline) {
				t.Fatalf("datagram %d never arrived", sent)
			}
			time.Sleep(time.Millisecond)
		}
		ticks.Add(1)
	}
	return c, send, clock
}

// feed writes a PSI datagram followed by n seconds of video, keyed every gop.
func feed(send func([]byte), n, gop int) {
	send(datagram(patPacket(), pmtPacket()))
	for i := 1; i <= n; i++ {
		send(datagram(videoPacket(i%gop == 0), audioPacket()))
	}
}

func TestCapturerWritesAPlayableClipFromTheRollingBuffer(t *testing.T) {
	c, send, clock := testCapturer(t, Config{WindowSeconds: 60})
	feed(send, 20, 5)

	clip, err := c.Capture(10 * time.Second)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	t.Run("the file lands in the clips directory under a recognisable name", func(t *testing.T) {
		if !IsClip(clip.Name) {
			t.Fatalf("name %q is not a clip name this build would list", clip.Name)
		}
		path := filepath.Join(c.Dir(), clip.Name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Size() != clip.Bytes || info.Size() == 0 {
			t.Fatalf("file is %d bytes, clip claims %d", info.Size(), clip.Bytes)
		}
	})

	t.Run("no partial file is left behind", func(t *testing.T) {
		entries, _ := os.ReadDir(c.Dir())
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".partial") {
				t.Fatalf("temporary file %q survived the capture", e.Name())
			}
		}
	})

	t.Run("it opens with the PSI and then a keyframe", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(c.Dir(), clip.Name))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(data) < 3*tsPacketSize {
			t.Fatalf("clip is only %d bytes", len(data))
		}
		if data[0] != tsSyncByte || packetPID(data[:tsPacketSize]) != pidPAT {
			t.Fatal("a clip must begin with a sync byte and the PAT")
		}
		if !clip.KeyframeAligned {
			t.Fatalf("clip is not keyframe aligned: %s", clip.Note)
		}
	})

	t.Run("it is listed, newest first", func(t *testing.T) {
		list, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 1 || list[0].Name != clip.Name {
			t.Fatalf("List = %+v", list)
		}
	})

	t.Run("the capture time and the content time are distinguishable", func(t *testing.T) {
		if !clip.CreatedAt.Equal(clock()) {
			t.Fatalf("CreatedAt = %v, want the capture instant %v", clip.CreatedAt, clock())
		}
		if !clip.StartedAt.Before(clip.CreatedAt) {
			t.Fatalf("StartedAt %v must precede CreatedAt %v", clip.StartedAt, clip.CreatedAt)
		}
	})
}

func TestCaptureClampsARequestLongerThanTheWindow(t *testing.T) {
	// Asking for more than is held is what a person in a hurry does. The honest
	// answer is everything there was, not an error.
	c, send, _ := testCapturer(t, Config{WindowSeconds: MinWindowSeconds})
	feed(send, 20, 2)

	clip, err := c.Capture(time.Hour)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if clip.Seconds > float64(MinWindowSeconds)+1 {
		t.Fatalf("clip is %.1fs, longer than the %ds the buffer holds", clip.Seconds, MinWindowSeconds)
	}
}

func TestCaptureOnAnEmptyBufferExplainsItself(t *testing.T) {
	c, _, _ := testCapturer(t, Config{WindowSeconds: 30})
	if _, err := c.Capture(5 * time.Second); err != ErrEmpty {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
	if list, _ := c.List(); len(list) != 0 {
		t.Fatalf("a failed capture must leave nothing behind, got %+v", list)
	}
}

func TestRetentionKeepsTheNewestClips(t *testing.T) {
	c, send, _ := testCapturer(t, Config{WindowSeconds: 60, MaxClips: 2})
	feed(send, 10, 3)

	var names []string
	for i := 0; i < 4; i++ {
		clip, err := c.Capture(3 * time.Second)
		if err != nil {
			t.Fatalf("Capture %d: %v", i, err)
		}
		names = append(names, clip.Name)
		// Advance the content clock so each clip gets its own filename second.
		send(datagram(videoPacket(true), audioPacket()))
	}

	list, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 clips after retention, got %d: %+v", len(list), list)
	}
	kept := map[string]bool{list[0].Name: true, list[1].Name: true}
	for _, n := range names[:2] {
		if kept[n] {
			t.Fatalf("retention kept the older clip %q", n)
		}
	}
}

func TestRetentionRemovesClipsOlderThanTheWindow(t *testing.T) {
	dir := t.TempDir()
	c, send, _ := testCapturer(t, Config{Dir: dir, WindowSeconds: 60, MaxAgeDays: 1})
	feed(send, 5, 2)

	// A clip from long before the retention window, written by hand: capture
	// cannot produce one, and age retention has to be provable without waiting.
	old := filepath.Join(dir, Prefix+"20200101-000000"+Ext)
	if err := os.WriteFile(old, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := c.Capture(2 * time.Second); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the stale clip survived retention (err=%v)", err)
	}
}

func TestRetentionOnlyEverTouchesFilesThisBuildWrote(t *testing.T) {
	dir := t.TempDir()
	c, send, _ := testCapturer(t, Config{Dir: dir, WindowSeconds: 60, MaxClips: 1, MaxAgeDays: 1})
	feed(send, 5, 2)

	stranger := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(stranger, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := c.Capture(2 * time.Second); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, err := os.Stat(stranger); err != nil {
		t.Fatalf("retention deleted a file it did not write: %v", err)
	}
}

func TestResolveRefusesAnythingOutsideTheClipsDirectory(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{"a clip this build wrote", Prefix + "20260727-203000" + Ext, true},
		{"a parent traversal", "../" + Prefix + "x" + Ext, false},
		{"an absolute path", "/etc/" + Prefix + "passwd" + Ext, false},
		{"a recording", "rec-20260727-203000.mkv", false},
		{"an empty name", "", false},
		{"the prefix and extension with nothing between", Prefix + Ext, false},
		// Shaped to pass IsClip so the SEPARATOR is the only thing rejecting
		// it -- otherwise the case would pass for the wrong reason and keep
		// passing after the separator check was removed.
		//
		// A backslash rather than a forward slash because that is what makes
		// the invariant testable here. This check tests '/' AND
		// os.PathSeparator, which is correct; the identical copy in
		// internal/recording had drifted to the separator alone, and on Linux
		// every forward-slash case still passed while Windows let "a/b"
		// through.
		{"a windows separator in an otherwise valid clip name", Prefix + `a\b` + Ext, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(dir, tc.input)
			if (err == nil) != tc.ok {
				t.Fatalf("Resolve(%q) err = %v, want ok=%v", tc.input, err, tc.ok)
			}
		})
	}
}

func TestDeleteRemovesOneClipAndNothingElse(t *testing.T) {
	c, send, _ := testCapturer(t, Config{WindowSeconds: 60})
	feed(send, 10, 3)

	first, err := c.Capture(3 * time.Second)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	send(datagram(videoPacket(true), audioPacket()))
	second, err := c.Capture(3 * time.Second)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	if err := c.Delete(first.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ := c.List()
	if len(list) != 1 || list[0].Name != second.Name {
		t.Fatalf("after delete: %+v", list)
	}
	if err := c.Delete("../escape.ts"); err == nil {
		t.Fatal("Delete must refuse a name that escapes the directory")
	}
}

func TestConfigNormalizedClampsRatherThanRefuses(t *testing.T) {
	tests := []struct {
		name       string
		in         Config
		wantWindow int
		wantBytes  int64
	}{
		{"zero takes the defaults", Config{}, DefaultWindowSeconds, DefaultMaxRingBytes},
		{"an absurd window is clamped down", Config{WindowSeconds: 100000}, MaxWindowSeconds, DefaultMaxRingBytes},
		{"a tiny window is clamped up", Config{WindowSeconds: 1}, MinWindowSeconds, DefaultMaxRingBytes},
		{"a tiny ceiling is clamped up", Config{MaxRingBytes: 1}, DefaultWindowSeconds, MinMaxRingBytes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Normalized()
			if got.WindowSeconds != tc.wantWindow || got.MaxRingBytes != tc.wantBytes {
				t.Fatalf("Normalized = %+v, want window %d ceiling %d", got, tc.wantWindow, tc.wantBytes)
			}
			if got.MaxClips <= 0 || got.MaxAgeDays <= 0 || got.MaxDiskMB <= 0 {
				t.Fatalf("retention must never normalize to zero: %+v", got)
			}
		})
	}
}

func TestUsageTotalsTheClipsOnDisk(t *testing.T) {
	c, send, _ := testCapturer(t, Config{WindowSeconds: 60})
	feed(send, 10, 3)
	clip, err := c.Capture(3 * time.Second)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	u, err := c.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if u.Count != 1 || u.UsedBytes != clip.Bytes {
		t.Fatalf("Usage = %+v, want one clip of %d bytes", u, clip.Bytes)
	}
}

func TestCloseGivesTheMemoryBack(t *testing.T) {
	c, send, _ := testCapturer(t, Config{WindowSeconds: 60})
	feed(send, 5, 2)
	if c.Stats().Bytes == 0 {
		t.Fatal("nothing was buffered")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent: the engine closes on teardown and again on shutdown.
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
