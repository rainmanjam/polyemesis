//go:build !windows

// Unix only: these drive the live command channel, and on Windows the
// supervisor gives a destination no stdin pipe and the engine restarts on every
// level change instead (liveGainsSupported). There is nothing to drive there.

package engine

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The review's reinit reproduction, made a real test against a real FFmpeg and
// the production routing graph: mute a track live, let the decoded format change
// under the running graph, and listen to what comes out after the change.

// formatChangeMedia writes an 8 s stereo AAC transport stream that is 48 kHz for
// its first 4 s and 44.1 kHz after, the shape of a failover between an ingest
// and a slate that encode at different rates.
func formatChangeMedia(t *testing.T, ffmpegBin string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(ffmpegBin, args...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
		}
	}
	a := filepath.Join(dir, "a48.ts")
	b := filepath.Join(dir, "a44.ts")
	run("-v", "error", "-y", "-f", "lavfi", "-i", "sine=f=440:r=48000:d=4", "-ac", "2", "-c:a", "aac", "-f", "mpegts", a)
	// Continuing the first half's timeline, as a relay hop with -output_ts_offset
	// does at a real switch. Without it the second half's timestamps restart at
	// zero and aresample's async drops it, which would read as "still silent"
	// for a reason that has nothing to do with the level.
	run("-v", "error", "-y", "-f", "lavfi", "-i", "sine=f=440:r=44100:d=4", "-ac", "2", "-c:a", "aac",
		"-output_ts_offset", "4", "-f", "mpegts", b)
	mix := filepath.Join(dir, "mix.ts")
	run("-v", "error", "-y", "-i", "concat:"+a+"|"+b, "-c", "copy", "-f", "mpegts", mix)
	return mix
}

var maxVolumeRE = regexp.MustCompile(`max_volume: (-?[0-9.]+|-inf) dB`)

// maxVolume is the loudest sample of wav in [from, from+dur) seconds, in dB. A
// negative from counts back from the end of the file.
//
// The END, for "after the format change", rather than a fixed time: the
// production graph's aresample=...:async=1:first_pts=0 pads silence when the
// graph is rebuilt, so the post-change audio lands later in the file than it
// sits in the input, and a fixed window would measure that padding.
func maxVolume(t *testing.T, ffmpegBin, wav string, from, dur float64) float64 {
	t.Helper()
	seek := []string{"-ss", strconv.FormatFloat(from, 'f', 2, 64)}
	if from < 0 {
		seek = []string{"-sseof", strconv.FormatFloat(from, 'f', 2, 64)}
	}
	args := append([]string{"-hide_banner"}, seek...)
	args = append(args, "-t", strconv.FormatFloat(dur, 'f', 2, 64),
		"-i", wav, "-af", "volumedetect", "-f", "null", "-")
	out, err := exec.Command(ffmpegBin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("volumedetect %s: %v\n%s", wav, err, out)
	}
	m := maxVolumeRE.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("no max_volume for %s (is it empty?):\n%s", wav, out)
	}
	if m[1] == "-inf" {
		return -200
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

// reinitDest runs FFmpeg as a destination does for this purpose: the production
// graph for the profile, taken from the holder at every spawn, the live command
// channel on, and each spawn writing its own file so the test can hear the last.
func reinitDest(t *testing.T, ffmpegBin, input string, holder *graphHolder) (*supervisor.Process, func() string) {
	t.Helper()
	dir := t.TempDir()
	var spawn atomic.Int32
	outPath := func(n int32) string { return filepath.Join(dir, fmt.Sprintf("out-%d.wav", n)) }
	argv := func() []string {
		n := spawn.Add(1)
		g := holder.get()
		// -progress so the test can tell when media is flowing through a built
		// graph (muteLive); the supervisor parses it off stdout as it does for
		// every destination.
		return []string{"-hide_banner", "-loglevel", "warning", "-nostats",
			"-progress", "pipe:1", "-stats_period", "0.1", "-re", "-i", input,
			"-filter_complex", g.FilterComplex, "-map", "[" + g.OutLabel + "]",
			"-c:a", "pcm_s16le", "-y", outPath(n)}
	}
	p := supervisor.New(testLogger(), supervisor.Spec{
		Name: "dest-reinit", Kind: "destination", Bin: ffmpegBin, Args: nil, NextArgs: argv,
		LiveCommands: true,
	})
	p.Start()
	t.Cleanup(func() { _ = p.Stop(t.Context()) })
	waitRunning(t, p)
	return p, func() string { return outPath(spawn.Load()) }
}

// waitExited waits for the finite input to end the last spawn.
func waitExited(t *testing.T, p *supervisor.Process) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for p.Status().State == supervisor.StateRunning || p.Status().State == supervisor.StateStarting {
		if time.Now().After(deadline) {
			t.Fatal("the destination never reached the end of its 8 s input")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// muteLive applies a live mute of track 0 exactly as stopDestinations does.
func muteLive(t *testing.T, e *Engine, p *supervisor.Process, holder *graphHolder) {
	t.Helper()
	_, store := storeEngine(t)
	row := liveRow(t, store, true)
	muted := planFor(t, e, row).compiled
	// The graph is built when the first frame arrives, so a command before then
	// has no graph to reach, and the mute must land well before the format
	// changes at 4 s of media. Waiting on the process's own progress rather
	// than a fixed sleep keeps both true on a loaded -race runner.
	deadline := time.Now().Add(20 * time.Second)
	for p.Status().Progress.OutTimeMS < 300 {
		if time.Now().After(deadline) {
			t.Fatal("no media progress from the destination; the graph never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ms := p.Status().Progress.OutTimeMS; ms > 2000 {
		t.Fatalf("media is already at %d ms; too close to the 4 s format change to measure", ms)
	}
	if err := liveApplyTo(p, holder, muted); err != nil {
		t.Fatalf("the live mute was refused: %v", err)
	}
}

func profileGraph(t *testing.T, e *Engine, muted bool) routing.Result {
	t.Helper()
	_, store := storeEngine(t)
	return planFor(t, e, liveRow(t, store, muted)).compiled
}

// The premise, kept as a test so that a future FFmpeg that preserved command
// state across a reinit would say so here rather than leaving the respawn below
// to look necessary forever. Measured on 7.1.5 and 9.0.2: muted to -91 dB, then
// back to about -18 dB once the 44.1 kHz frames arrive.
func TestFFmpegRevertsALiveLevelAtAFormatChange(t *testing.T) {
	bin := testenv.FFmpegBinary(t, "ffmpeg", "no ffmpeg on PATH: the reinit premise of the stale-level respawn goes unmeasured")
	input := formatChangeMedia(t, bin)
	e, _ := storeEngine(t)
	holder := newGraphHolder(profileGraph(t, e, false))
	p, last := reinitDest(t, bin, input, holder)

	muteLive(t, e, p, holder)
	waitExited(t, p)

	// The mute is sent once media flows (0.3 to 2 s in); the format changes at 4 s.
	if v := maxVolume(t, bin, last(), 2.5, 1.0); v > -60 {
		t.Fatalf("the track is audible between the live mute and the format change (%.1f dB): "+
			"the mute never applied, so this measures nothing", v)
	}
	if v := maxVolume(t, bin, last(), -1.5, 1.5); v < -40 {
		t.Errorf("after the 48 -> 44.1 kHz change the muted track is still silent (%.1f dB); "+
			"FFmpeg now keeps a live level across a reinit, and the respawn in "+
			"respawnStaleLevels may no longer be needed", v)
	}
}

// THE FIX, end to end. The same live mute, then the format event the engine
// reacts to; the process is respawned with the stored level in its argv, and
// what it sends after the format change is still silent.
//
// MUTATION: make respawnStaleLevels return without restarting anything.
// Observed: FAIL, "the muted track is audible after the format change".
func TestALiveMuteSurvivesAFormatChange(t *testing.T) {
	bin := testenv.FFmpegBinary(t, "ffmpeg", "no ffmpeg on PATH: a live mute is not proven to survive a format change")
	input := formatChangeMedia(t, bin)
	e, store := storeEngine(t)
	holder := newGraphHolder(profileGraph(t, e, false))
	p, last := reinitDest(t, bin, input, holder)
	row := liveRow(t, store, false)
	e.mu.Lock()
	e.dests[row.ID] = &destination{row: row, proc: p, graph: holder}
	e.mu.Unlock()

	muteLive(t, e, p, holder)
	e.respawnStaleLevels("test: the source feed changed")
	waitArgvMuted(t, p, "a format event")
	waitExited(t, p)

	if !strings.HasSuffix(last(), "out-2.wav") {
		t.Fatalf("the last spawn wrote %s; want the respawn's out-2.wav", last())
	}
	if v := maxVolume(t, bin, last(), -1.5, 1.5); v > -60 {
		t.Errorf("the muted track is audible after the format change: %.1f dB", v)
	}
}
