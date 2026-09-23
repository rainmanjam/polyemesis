package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A relay consumer stopped AFTER its publisher has gone must still finalise its
// file.
//
// THE BUG THIS PINS. Every relay consumer reads udp://127.0.0.1 with no read
// timeout, on purpose (see ffmpeg.RelayProbeInputURL): a destination has to ride
// through a quiet patch. The cost was paid at stop time. FFmpeg acts on one
// SIGTERM only between packets, and its I/O interrupt callback does not fire
// until a SECOND signal -- which also aborts the trailer write. So a recorder
// or file destination stopped on a silent feed sat out the whole 8s grace, was
// SIGKILLed, and left a Matroska file with no duration and no cues: the last
// recording segment after an ingest ended, the recording.enabled=false toggle,
// `docker stop` after the publisher left, and stopping a file destination.
//
// Driven through the engine's own reconcileRecorder -- the recorder's real
// argv, Spec and wake wiring, on the engine's real relay hub -- because the
// defect lives in how they meet FFmpeg, and nothing short of FFmpeg
// reproduces it. Measured without the wake: 8.0s, and ffprobe duration N/A.
func TestARecorderStoppedOnAQuietFeedFinalisesItsFileInsteadOfBeingKilled(t *testing.T) {
	ffmpegBin, ffprobeBin := offsetBenchTools(t)
	fixture := quietStopFixture(t, ffmpegBin)

	e, _ := storeEngine(t)
	e.tools.FFmpeg = ffmpegBin
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 1)
	s := db.DefaultSettings()
	s.Recording.Enabled = true
	s.Recording.SegmentSeconds = 3600
	e.reconcileRecorder(s)
	e.mu.RLock()
	started := e.recorder != nil
	e.mu.RUnlock()
	if !started {
		t.Fatal("reconcileRecorder started no recorder")
	}

	publishAndLeave(t, ffmpegBin, fixture, e.hub.Port())

	// recording.enabled=false, exactly: the reconcile that sees it off stops
	// the recorder through stopAux.
	s.Recording.Enabled = false
	began := time.Now()
	e.reconcileRecorder(s)
	assertFinalisedPromptly(t, ffprobeBin, e.cfg.RecordingsDir(), time.Since(began), "the recorder")
}

// The same stop through the destination path: a file destination stopped from
// the dashboard after its publisher has left, reported as a 9s stop and an MKV
// with no duration and no cues. Driven through startDest and teardownDest --
// what POST /destinations/{id}/stop reaches -- so the wake wiring under test is
// the one production runs, not a copy of it.
func TestAFileDestinationStoppedOnAQuietFeedFinalisesItsFile(t *testing.T) {
	ffmpegBin, ffprobeBin := offsetBenchTools(t)
	fixture := quietStopFixture(t, ffmpegBin)

	e, _ := storeEngine(t)
	e.tools.FFmpeg = ffmpegBin
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 1)
	compiled, err := routing.Compile(routing.DefaultProfile(), routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 1, Codec: "aac", Layout: "mono"},
	}})
	if err != nil {
		t.Fatalf("compile the routing graph: %v", err)
	}
	row := &db.Destination{ID: 1, Name: "archive", Kind: db.DestFile, URL: "archive.mkv"}
	if err := e.startDest(destPlan{row: row, compiled: compiled, spec: "spec"}, e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	e.mu.Lock()
	d := e.dests[row.ID]
	e.mu.Unlock()
	if d == nil {
		t.Fatal("the destination was not published")
	}

	publishAndLeave(t, ffmpegBin, fixture, e.hub.Port())

	started := time.Now()
	e.teardownDest(d)
	assertFinalisedPromptly(t, ffprobeBin, e.cfg.RecordingsDir(), time.Since(started), "the file destination")
}

// quietStopFixture is an A/V transport stream LONGER THAN THE PROBE WINDOW, and
// that is the whole reproduction. Inside ffmpeg.RelayProbeWindow (15s) FFmpeg is
// still in stream-info probing, where one SIGTERM DOES interrupt the read, and a
// 6s fixture stops in milliseconds with a finalised file -- a green test over
// the bug. Past it, transcoding has begun and the single signal no longer
// reaches a blocked read.
func quietStopFixture(t *testing.T, ffmpegBin string) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "fixture.ts")
	fix := exec.Command(ffmpegBin,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=15",
		"-f", "lavfi", "-i", "sine=f=440:r=48000",
		"-t", strconv.Itoa(int(ffmpeg.RelayProbeWindow/time.Second)+5),
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-g", "15", "-c:a", "aac",
		"-f", "mpegts", fixture)
	if out, err := fix.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	return fixture
}

// publishAndLeave sends the fixture into a hub -- unpaced, so the probe window
// passes in stream time rather than wall time -- and returns once the publisher
// has LEFT, which is the state every reported case shared.
func publishAndLeave(t *testing.T, ffmpegBin, fixture string, hubPort int) {
	t.Helper()
	// The consumer must have bound its input before the head of the stream --
	// the PAT/PMT -- goes past, or it never learns the layout.
	time.Sleep(750 * time.Millisecond)
	pub := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-readrate", "8", "-i", fixture, "-map", "0", "-c", "copy",
		"-f", "mpegts", "-flush_packets", "1",
		"udp://127.0.0.1:"+strconv.Itoa(hubPort)+"?pkt_size=1316")
	var pubErr strings.Builder
	pub.Stderr = &pubErr
	if err := pub.Run(); err != nil {
		t.Fatalf("publish the fixture: %v\n%s", err, pubErr.String())
	}
	time.Sleep(1500 * time.Millisecond)
}

// assertFinalisedPromptly checks that the one .mkv in dir has its trailer, and
// that the stop which wrote it did not wait out the grace to be killed.
func assertFinalisedPromptly(t *testing.T, ffprobeBin, dir string, took time.Duration, who string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.mkv"))
	if len(files) != 1 {
		t.Fatalf("want one .mkv from %s, found %v", who, files)
	}
	out, err := exec.Command(ffprobeBin, "-v", "error", "-show_entries", "format=duration",
		"-of", "csv=p=0", files[0]).CombinedOutput()
	dur, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	size := int64(-1)
	if st, _ := os.Stat(files[0]); st != nil {
		size = st.Size()
	}
	t.Logf("stop took %v; %s is %d bytes, ffprobe duration %q", took, filepath.Base(files[0]), size,
		strings.TrimSpace(string(out)))

	// The grace is 8s; a finalising FFmpeg answers in well under one. Two
	// seconds leaves CI room without admitting the escalation.
	if took > 2*time.Second {
		t.Errorf("stopping %s on a quiet feed took %v: it ignored SIGTERM and "+
			"waited out the grace to be killed", who, took)
	}
	if err != nil || perr != nil || dur < ffmpeg.RelayProbeWindow.Seconds() {
		t.Errorf("%s did not finalise its file: ffprobe duration %q (err %v). A Matroska "+
			"file with no duration has no trailer, which is what SIGKILL leaves",
			who, strings.TrimSpace(string(out)), err)
	}
}
