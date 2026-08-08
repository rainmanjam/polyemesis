package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ProbeFile is the gate on the Library's upload path, so what matters is that
// it says no to the things that used to get in. Before it existed, the only
// check was an extension allowlist that never rejected anything -- an
// unrecognised extension was stored as ".bin" and listed as media, so a PDF, a
// zip or a truncated download all reached the Library looking like a video.
func TestProbeFileRefusesWhatIsNotMedia(t *testing.T) {
	bins := needFFmpeg(t, "ffprobe")
	ffprobe := bins[0]
	dir := t.TempDir()

	write := func(name string, body []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		// A real PDF header. Named .mp4 on purpose: the point is that the
		// extension was never the check.
		{"a PDF renamed to .mp4", write("doc.mp4", []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n"))},
		{"a zip renamed to .mkv", write("archive.mkv", []byte("PK\x03\x04\x14\x00\x00\x00\x08\x00"))},
		{"plain text", write("notes.ts", []byte("this is not a transport stream\n"))},
		{"an empty file", write("empty.mp4", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ProbeFile(context.Background(), ffprobe, tc.path)
			if err != nil {
				return // refused outright, which is the wanted outcome
			}
			// Some of these parse as a container with nothing in it, which is
			// why the caller checks for streams as well as for an error. If
			// this ever returns a stream for one of the above, the upload gate
			// has a hole.
			if res.Video != nil || len(res.Audio) > 0 {
				t.Errorf("accepted as media: video=%v audio=%d", res.Video, len(res.Audio))
			}
		})
	}
}

// The numbers the Library shows come from here, so a real file has to produce
// them -- a gate that also rejected valid media would be worse than no gate.
func TestProbeFileReadsRealMedia(t *testing.T) {
	bins := needFFmpeg(t, "ffmpeg", "ffprobe")
	ffmpegBin, ffprobe := bins[0], bins[1]
	path := filepath.Join(t.TempDir(), "sample.mkv")

	// Two audio tracks, because the count is the field the Library exists to
	// show: routing is per track, so "does this file carry the tracks I am
	// about to select" is the question a name and a size cannot answer.
	mk := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-t", "2", "-y", path)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Skipf("could not build a sample file: %v: %s", err, out)
	}

	res, err := ProbeFile(context.Background(), ffprobe, path)
	if err != nil {
		t.Fatalf("ProbeFile on real media: %v", err)
	}
	if res.Video == nil {
		t.Fatal("no video stream reported")
	}
	if res.Video.Width != 320 || res.Video.Height != 180 {
		t.Errorf("resolution = %dx%d, want 320x180", res.Video.Width, res.Video.Height)
	}
	if len(res.Audio) != 2 {
		t.Errorf("audio tracks = %d, want 2", len(res.Audio))
	}
	// Duration is the field ParseProbe did not read before this change; the
	// -show_format that provides it was already being requested.
	if res.DurationSeconds < 1 || res.DurationSeconds > 4 {
		t.Errorf("DurationSeconds = %v, want about 2", res.DurationSeconds)
	}
}

// A path is not a relay. Probe() runs its input through RelayInputURL, which
// appends "?fifo_size=...&overrun_nonfatal=1" -- options on a UDP URL, part of
// the filename on a path. ProbeFile exists because of that, and this pins the
// distinction so the two are not merged later by someone tidying up.
func TestProbeFileDoesNotUseTheRelayInputWrapper(t *testing.T) {
	bins := needFFmpeg(t, "ffprobe")
	ffprobe := bins[0]
	path := filepath.Join(t.TempDir(), "missing.mp4")

	_, err := ProbeFile(context.Background(), ffprobe, path)
	if err == nil {
		t.Fatal("probing a file that does not exist should fail")
	}
	if strings.Contains(err.Error(), "fifo_size") {
		t.Errorf("the relay wrapper reached a file path: %v", err)
	}
}
