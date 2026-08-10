package ffmpeg

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// buildSample muxes one short h264/aac file and returns its path.
//
// ONE SKIP SITE FOR THE WHOLE FILE, which is the point. Every test here that
// needs real media used to carry its own "could not build a sample" skip, and
// internal/testenv's ratchet is right that each of those is a free pass: an
// FFmpeg without libx264 would silently stop exercising the upload gate, four
// tests at a time, and print ok. Now there is one place for that to be noticed
// and one place to fix it.
//
// Extra ffmpeg arguments go between the codec flags and the output path, so a
// caller can ask for a duration or -movflags +faststart.
//
// 320x180 with TWO audio tracks for every caller, rather than a knob per test.
// The track count is the field this whole feature exists to show -- routing is
// per track -- so every fixture carrying two is the right default, and the one
// test that asserts the shape asserts a shape the others also have.
func buildSample(t *testing.T, path string, extra ...string) string {
	t.Helper()
	ffmpegBin := needFFmpeg(t, "ffmpeg")[0]
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac"}
	args = append(args, extra...)
	args = append(args, "-y", path)
	if out, err := exec.Command(ffmpegBin, args...).CombinedOutput(); err != nil {
		t.Skipf("could not build %s with this FFmpeg: %v: %s", filepath.Base(path), err, out)
	}
	return path
}

// emptyMP4 is a valid MPEG-4 file with no tracks in it: an ftyp box and a moov
// holding nothing but an mvhd.
//
// It exists to be the fixture that gets PAST the error check. Every other
// fixture in the refusal table makes ffprobe exit non-zero, so before this was
// added the "no streams were reported" assertion below was executed by zero
// cases -- a check sitting in the file written to prove the gate works, that
// could not itself have failed. The counter at the end of that test is what
// keeps it honest.
func emptyMP4() []byte {
	box := func(kind string, payload []byte) []byte {
		out := make([]byte, 4, 8+len(payload))
		binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
		out = append(out, kind...)
		return append(out, payload...)
	}
	ftyp := box("ftyp", append([]byte("isom"), append([]byte{0, 0, 2, 0}, "isomiso2mp41"...)...))
	mvhd := box("mvhd", make([]byte, 100))
	return append(ftyp, box("moov", mvhd)...)
}

// ffconcatScript is the 44-byte shape that walked straight through this gate.
//
// "ffconcat version 1.0" plus a filename is a complete input as far as the
// concat demuxer is concerned: it opens the file named, and reports THAT file's
// streams, codecs and duration as this file's. So a text file the size of a
// tweet was probed as h264/aac and listed in the Library with a real video's
// metadata, which is the precise failure the whole feature exists to stop.
func ffconcatScript(target string) []byte {
	return []byte("ffconcat version 1.0\nfile " + target + "\n")
}

// ProbeFile is the gate on the Library's upload path, so what matters is that
// it says no to the things that used to get in. Before it existed, the only
// check was an extension allowlist that never rejected anything -- an
// unrecognised extension was stored as ".bin" and listed as media, so a PDF or
// a zip reached the Library looking like a video.
//
// TWO KINDS OF NO are covered here and the distinction is load-bearing, because
// only one of them is ProbeFile's to give. A file ffprobe cannot read at all
// comes back as an error. A file ffprobe reads fine and finds nothing playable
// in comes back as a result with no streams, and it is the CALLER that refuses
// that -- see internal/api's probeUpload. A table that only ever produced the
// first kind would leave the second untested, which is what it did.
func TestProbeFileRefusesWhatIsNotMedia(t *testing.T) {
	ffprobe := needFFmpeg(t, "ffmpeg", "ffprobe")[1]
	dir := t.TempDir()

	write := func(name string, body []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	// A real file for the ffconcat script to point at, so the bypass has
	// something worth stealing the metadata of. Built rather than faked: the
	// whole point is that the script inherits a REAL probe result. The scripts
	// below name it by its bare filename, which is how the concat demuxer
	// resolves a sibling, so nothing needs the returned path.
	buildSample(t, filepath.Join(dir, "victim.mp4"), "-t", "1")

	// reachedStreamCheck counts the cases that got a result rather than an
	// error, which is the only way the no-streams assertion runs at all.
	var reachedStreamCheck atomic.Int64

	for _, tc := range []struct {
		name string
		path string
		// wantIndirect marks the cases that must be refused specifically as a
		// file naming other files, not merely "refused somehow". A generic
		// error would satisfy "err != nil" while meaning ffprobe simply could
		// not read the script, which is not what is being proven.
		wantIndirect bool
	}{
		// A real PDF header. Named .mp4 on purpose: the point is that the
		// extension was never the check.
		{name: "a PDF renamed to .mp4", path: write("doc.mp4", []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n"))},
		{name: "a zip renamed to .mkv", path: write("archive.mkv", []byte("PK\x03\x04\x14\x00\x00\x00\x08\x00"))},
		{name: "plain text", path: write("notes.ts", []byte("this is not a transport stream\n"))},
		{name: "an empty file", path: write("empty.mp4", nil)},
		// Reads clean, reports nothing. This is the case that makes the
		// no-streams assertion below a live check rather than dead code.
		{name: "an MP4 with no tracks", path: write("notracks.mp4", emptyMP4())},
		// Reads clean, reports somebody else's streams.
		{name: "an ffconcat script naming a real file", wantIndirect: true,
			path: write("script.mp4", ffconcatScript("victim.mp4"))},
		{name: "an ffconcat script under its own extension", wantIndirect: true,
			path: write("script.ffconcat", ffconcatScript("victim.mp4"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ProbeFile(context.Background(), ffprobe, tc.path)
			if tc.wantIndirect {
				if !errors.Is(err, ErrIndirectContainer) {
					t.Fatalf("want ErrIndirectContainer, got err=%v res=%+v", err, res)
				}
				// And it does not hand back the victim's numbers on the way
				// out. A caller that logged or stored a partial result would
				// re-create the exact bug.
				if res != nil {
					t.Errorf("a refused script still produced a result: %+v", res)
				}
				return
			}
			if err != nil {
				return // refused outright, which is the wanted outcome
			}
			reachedStreamCheck.Add(1)
			// Some of these parse as a container with nothing in it, which is
			// why the caller checks for streams as well as for an error. If
			// this ever returns a stream for one of the above, the upload gate
			// has a hole.
			if res.Video != nil || len(res.Audio) > 0 {
				t.Errorf("accepted as media: video=%v audio=%d", res.Video, len(res.Audio))
			}
		})
	}

	// THE ASSERTION ABOVE MUST HAVE RUN. Counted, not assumed: it was dead for
	// the entire life of this file, because all four original fixtures errored
	// out before reaching it. If a future ffprobe starts refusing the no-tracks
	// MP4 outright, this fails and says so instead of quietly going dead again.
	if n := reachedStreamCheck.Load(); n == 0 {
		t.Error("no fixture reached the no-streams assertion; it is dead code again")
	} else {
		t.Logf("the no-streams assertion executed for %d of the fixtures", n)
	}
}

// A file that names other files is not media, however real the media it names.
//
// Split out from the table because the interesting assertion is not "refused"
// but "refused WITHOUT inheriting the victim's identity". Before the format
// allowlist, this exact input produced a ProbeResult carrying the referenced
// file's h264 video stream, its aac audio stream and its duration -- and the
// handler stored all of it as the 44-byte script's own metadata.
func TestProbeFileRefusesAScriptThatNamesOtherFiles(t *testing.T) {
	ffprobe := needFFmpeg(t, "ffmpeg", "ffprobe")[1]
	dir := t.TempDir()

	victim := buildSample(t, filepath.Join(dir, "victim.mp4"), "-t", "2")

	// The victim really is readable media, so a refusal below cannot be
	// explained by the referenced file being broken.
	if res, err := ProbeFile(context.Background(), ffprobe, victim); err != nil {
		t.Fatalf("the referenced file is not readable, so this proves nothing: %v", err)
	} else if res.Video == nil {
		t.Fatal("the referenced file has no video stream, so this proves nothing")
	}

	script := filepath.Join(dir, "innocent.mp4")
	if err := os.WriteFile(script, ffconcatScript("victim.mp4"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if n := len(ffconcatScript("victim.mp4")); n > 64 {
		t.Fatalf("the script fixture grew to %d bytes; it is meant to be tiny", n)
	}

	res, err := ProbeFile(context.Background(), ffprobe, script)
	if !errors.Is(err, ErrIndirectContainer) {
		t.Fatalf("an ffconcat script was not refused as indirect: err=%v res=%+v", err, res)
	}
	// The format name is named in the message so an operator and a log reader
	// can both tell this apart from "your file is corrupt".
	if !strings.Contains(err.Error(), "concat") {
		t.Errorf("the refusal does not say what it read the file as: %v", err)
	}
}

// selfContained is the admission rule, so it is asserted directly rather than
// only through ffprobe. The table is the point: an allowlist that accidentally
// accepted a substring, or that matched the whole comma-joined name instead of
// its elements, would pass every end-to-end test above by rejecting everything.
func TestSelfContainedIsElementWiseAndClosed(t *testing.T) {
	for _, tc := range []struct {
		formatName string
		want       bool
	}{
		{"mov,mp4,m4a,3gp,3g2,mj2", true}, // an MP4
		{"matroska,webm", true},           // an MKV
		{"mpegts", true},
		{"wav", true},
		{"h264", true},
		{"concat", false}, // the bypass
		{"hls", false},    // a playlist by another name
		{"dash", false},   // and another
		{"sdp", false},    // and another
		{"image2", false}, // resolves a name pattern to files
		{"", false},       // ffprobe naming no format is not a yes
		{"mp", false},     // not a prefix match
		{"mp4x", false},   // not a suffix match
		{"concat,mp4", true},
		{" mp4 , junk", true}, // whitespace around elements is tolerated
	} {
		if got := selfContained(tc.formatName); got != tc.want {
			t.Errorf("selfContained(%q) = %v, want %v", tc.formatName, got, tc.want)
		}
	}
}

// TRUNCATION IS MOSTLY NOT DETECTED, and this pins where the boundary actually
// is because three places in this change claimed otherwise.
//
// "moov atom not found means a truncated download" is true and is the whole of
// what is true. It only happens for an MP4 whose index sits at the END of the
// file, which is the default layout. Cut a -movflags +faststart MP4, whose
// index is at the front, or cut a Matroska file, and ffprobe reads the header
// it still has, reports every stream, and reports THE ORIGINAL DURATION of the
// whole file -- so the Library shows ten minutes for a file holding one.
//
// This test asserts the accepting behaviour on purpose. It is not an
// endorsement; it is the boundary written down, so that a later change that
// starts catching these fails here and gets to update the docs deliberately.
func TestProbeFileAcceptsMostTruncatedMedia(t *testing.T) {
	ffprobe := needFFmpeg(t, "ffmpeg", "ffprobe")[1]
	dir := t.TempDir()

	// Eight seconds, so a tenth of the bytes is unambiguously less than the
	// duration the header claims.
	build := func(name string, extra ...string) string {
		t.Helper()
		return buildSample(t, filepath.Join(dir, name), append([]string{"-t", "8"}, extra...)...)
	}
	cut := func(src string, keep float64) string {
		t.Helper()
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		p := src + ".cut" + filepath.Ext(src)
		if err := os.WriteFile(p, b[:int(float64(len(b))*keep)], 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}

	t.Run("a truncated faststart MP4 is accepted with the whole file's duration", func(t *testing.T) {
		whole := build("fast.mp4", "-movflags", "+faststart")
		res, err := ProbeFile(context.Background(), ffprobe, cut(whole, 0.1))
		if err != nil {
			t.Fatalf("this is documented as ACCEPTED; if it now fails, "+
				"docs/TROUBLESHOOTING.md and probeUpload's comment need updating: %v", err)
		}
		if res.Video == nil {
			t.Fatal("no video stream reported for a truncated faststart MP4")
		}
		// The lie the operator is shown: nine tenths of the file is missing and
		// the duration is the original.
		if res.DurationSeconds < 7 {
			t.Errorf("DurationSeconds = %v; the documented behaviour is that the "+
				"ORIGINAL duration survives truncation", res.DurationSeconds)
		}
	})

	t.Run("a truncated Matroska file is accepted with the whole file's duration", func(t *testing.T) {
		res, err := ProbeFile(context.Background(), ffprobe, cut(build("whole.mkv"), 0.1))
		if err != nil {
			t.Fatalf("this is documented as ACCEPTED: %v", err)
		}
		if res.Video == nil {
			t.Fatal("no video stream reported for a truncated MKV")
		}
		if res.DurationSeconds < 7 {
			t.Errorf("DurationSeconds = %v, want the original ~8", res.DurationSeconds)
		}
	})

	t.Run("a truncated plain MP4 is the one case that IS caught", func(t *testing.T) {
		_, err := ProbeFile(context.Background(), ffprobe, cut(build("plain.mp4"), 0.1))
		if err == nil {
			t.Fatal("a truncated non-faststart MP4 should fail: its moov box is at the end")
		}
		if !strings.Contains(err.Error(), "moov atom not found") {
			t.Errorf("the message docs/TROUBLESHOOTING.md promises is not the one given: %v", err)
		}
	})
}

// The numbers the Library shows come from here, so a real file has to produce
// them -- a gate that also rejected valid media would be worse than no gate.
func TestProbeFileReadsRealMedia(t *testing.T) {
	ffprobe := needFFmpeg(t, "ffmpeg", "ffprobe")[1]

	// Two audio tracks, because the count is the field the Library exists to
	// show: routing is per track, so "does this file carry the tracks I am
	// about to select" is the question a name and a size cannot answer.
	// buildSample produces two for every caller.
	path := buildSample(t, filepath.Join(t.TempDir(), "sample.mkv"), "-t", "2")

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

// Cancelling the context must actually end the probe, and killing the process
// is not enough to guarantee that.
//
// cmd.Output() waits for the stdout pipe to close, which is a different event
// from the child exiting. A probe binary that is a wrapper script leaves the
// real work as a grandchild holding that pipe, and the grandchild never gets
// the signal -- so Wait blocks until it finishes on its own, however long that
// is. Not hypothetical: it is exactly what this test sets up, and it is how the
// need for cmd.WaitDelay was found. Without that line this hangs for the full
// sixty seconds rather than returning.
//
// It matters because internal/api treats an interrupted probe as "could not
// check" and keeps the upload -- see probeUpload. A cancellation that does not
// return leaves the request goroutine and the staged file pinned instead, which
// is the failure the client-disconnect fix was supposed to remove.
func TestProbeFileReturnsWhenItsContextIsCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the wrapper-script shape this pins is a POSIX one")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "slow-probe")
	// No `exec` in the script, deliberately: the sleep is a GRANDCHILD of the
	// process CommandContext kills, and it inherits the stdout pipe.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatalf("write fake probe: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := ProbeFile(ctx, fake, filepath.Join(dir, "anything.mp4"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cancelled probe reported success")
	}
	// Generous on purpose: what is being pinned is "bounded by WaitDelay", not
	// the exact number, and the failure this catches is unbounded.
	if elapsed > 30*time.Second {
		t.Fatalf("ProbeFile took %v to notice its context was cancelled; "+
			"cmd.WaitDelay is what bounds this", elapsed)
	}
	t.Logf("returned after %v with %v", elapsed, err)
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
