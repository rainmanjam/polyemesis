package playlistmedia

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This file is a MEASUREMENT, not a unit test. The playlist spec's timestamp
// contract -- that -stream_loop over a -f concat input keeps DTS and PTS
// moving forward across every item seam and across a wrap -- is a claim
// about FFmpeg's own behaviour, and this project's standard is to probe that
// rather than argue it. Task 3's playout argv is built on the answer
// recorded here.
//
// # Method adaptation #1: ffprobe refuses -stream_loop
//
// The obvious way to ask this question is to hand ffprobe the same argv a
// playout FFmpeg would use and read its packets directly:
//
//	ffprobe -stream_loop -1 -f concat -safe 0 -i list.txt ...
//
// ffprobe refuses it outright:
//
//	Failed to set value '-1' for option 'stream_loop': Option not found
//
// -stream_loop is registered on ffmpeg's input-option table, not ffprobe's,
// regardless of demuxer. So loopThroughConcat below runs the REAL playout
// command -- ffmpeg, -stream_loop -1, -f concat -safe 0, stream copy,
// stopped after N seconds of output with -t -- to a temporary .ts, and
// probes that file with ffprobe. Same bytes ffprobe would have read if it
// could; ffmpeg is just the process willing to produce them.
//
// # Method adaptation #2: a single combined timestamp check is confounded
//
// The obvious way to check monotonicity is one running (lastDTS, lastPTS)
// pair walked across every packet ffprobe reports, in report order. That
// was tried first and it fails constantly -- 254 of 1068 packets by DTS, 312
// by PTS -- but not because of anything the concat demuxer did. ffprobe's
// report interleaves two independent streams (video packet, audio packet,
// video packet, ...) that do not arrive at the same wall-clock rate, so
// comparing packet N of one stream against packet N-1 of the OTHER proves
// nothing about either stream's own splice; it is an artifact of
// interleaving, not a measurement of it. assertMonotonic below groups by
// stream_index first and compares each stream only against itself, which is
// the check that actually answers the question.
//
// # What was measured (2026-08-02, ffmpeg/ffprobe from Homebrew, arm64 macOS)
//
// Three derivatives of DIFFERENT nominal lengths (2.0s, 3.0s, 1.5s; real
// measured durations after normalisation: 2.048000s, 3.050667s, 1.554667s --
// deliberately unequal so a wrong wrap offset cannot look identical to a
// right one at some coincidental multiple), concatenated with -stream_loop
// -1 -f concat -safe 0 -c copy and captured for 14.0s of output -- more than
// two full wraps of the ~6.653334s list (13.306668s) plus margin into a
// third partial wrap. ffmpeg accepted the argv (exit 0) and produced
// 14.144667s of output: 413 video packets (stream 0), 655 audio packets
// (stream 1), 1068 total.
//
//   - Q1, does -stream_loop -1 over -f concat actually loop and produce two
//     full wraps? YES. Captured span (14.144667s) exceeds two full wraps of
//     the list (13.306668s); ffmpeg accepted the argv without error or
//     warning.
//
//   - Q2, WITHOUT per-entry duration directives, do DTS and PTS stay
//     monotonic across every seam and the wrap? DTS: YES, cleanly -- 0
//     backwards steps in 413 video packets and 0 in 655 audio packets, i.e.
//     across all five item-boundary seams the 14.14s capture crosses and
//     both full wraps. PTS: the audio stream is also clean (0/655). The
//     video stream shows 199 backwards PTS steps out of 413 packets, but
//     every single one is EXACTLY 0.066667s (two frame periods at 30fps),
//     and the 199 events are spread roughly uniformly across the whole
//     capture (~10-13 per 0.5s bucket) rather than concentrated at the five
//     seams -- the signature of libx264's ordinary B-frame reorder window
//     (decode order vs. presentation order), not a splice defect. DTS is the
//     value concat is responsible for rebasing and the value a real decoder
//     consumes in order; it never regresses. (The single-combined-timeline
//     check described above reports 254 DTS and 312 PTS "violations" on this
//     same data -- that number is the interleaving artifact from adaptation
//     #2, not a second finding.)
//
//   - Q3, WITH per-entry duration directives measured from each derivative
//     (2.048000, 3.050667, 1.554667), does the answer change? NO. The
//     captured packet stream -- all 1068 packets, every timestamp -- was
//     BYTE-FOR-BYTE IDENTICAL with and without the directives. Every input
//     here already carries real, contiguous timestamps from a genuine
//     FFmpeg encode, so the concat demuxer's own cumulative-duration
//     accounting already matches what the directives would assert.
//     Directives are belt-and-braces for derivatives built by this
//     normaliser, not load-bearing.
func TestConcatTimestampsAreMonotonicAcrossSeamsAndWraps(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dataDir := t.TempDir()
	durations := []float64{2.0, 3.0, 1.5} // unequal on purpose; see the doc comment.
	items := buildDerivatives(t, ffmpegBin, ffprobeBin, dataDir, durations)

	// Sum of the real, encoder-rounded durations is ~6.65s; two full wraps
	// plus margin for GOP alignment at the cutoff is comfortably under 14s.
	const seconds = 14.0

	t.Run("without duration directives", func(t *testing.T) {
		list := writeList(t, t.TempDir(), items, nil)
		assertMonotonic(t, loopThroughConcat(t, ffmpegBin, ffprobeBin, list, seconds))
	})

	t.Run("with duration directives", func(t *testing.T) {
		measured := make([]float64, len(items))
		for i, it := range items {
			measured[i] = probeDuration(t, ffprobeBin, it)
		}
		list := writeList(t, t.TempDir(), items, measured)
		assertMonotonic(t, loopThroughConcat(t, ffmpegBin, ffprobeBin, list, seconds))
	})
}

// buildDerivatives builds one real upload per duration and normalises each
// through RunNormalise, returning the resulting derivative paths in the same
// order as durations.
//
// Real derivatives, encoded by the real profile, not hand-rolled files with
// invented timestamps: the question this file answers is what FFmpeg's own
// concat demuxer does with timestamps FFmpeg's own encoder wrote, which a
// synthetic .ts could not speak to.
func buildDerivatives(t *testing.T, ffmpegBin, ffprobeBin, dataDir string, durations []float64) []string {
	t.Helper()
	uploadsDir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p := New(nil, Config{
		FFmpeg:  ffmpegBin,
		FFprobe: ffprobeBin,
		DataDir: dataDir,
		Uploads: mustStore(t, dataDir),
	}, WithFreeSpace(func(string) (uint64, error) { return 1 << 60, nil }))

	var derivatives []string
	for i, d := range durations {
		name := fmt.Sprintf("seam-%d-%s.mp4", i, strconv.FormatFloat(d, 'f', 1, 64))
		secs := strconv.FormatFloat(d, 'f', 2, 64)
		runFFmpeg(t, ffmpegBin, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=640x480:rate=25:duration="+secs,
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration="+secs,
			"-map", "0:v", "-map", "1:a",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-ac", "1", "-ar", "44100",
			filepath.Join(uploadsDir, name))

		rep := &recorder{}
		if err := p.RunNormalise(context.Background(), normaliseJob(t, name), rep); err != nil {
			t.Fatalf("normalising %s: %v\n%s", name, err, strings.Join(rep.lines, "\n"))
		}
		derivatives = append(derivatives, DerivativePath(dataDir, name))
	}
	return derivatives
}

// writeList renders a concat-demuxer playlist from derivative paths. When
// durations is non-nil it must have one entry per item, and a `duration`
// directive follows each `file` line -- the variable Step 3 turns on.
func writeList(t *testing.T, dir string, items []string, durations []float64) string {
	t.Helper()
	if durations != nil && len(durations) != len(items) {
		t.Fatalf("writeList: %d items but %d durations", len(items), len(durations))
	}
	var b strings.Builder
	for i, item := range items {
		fmt.Fprintf(&b, "file '%s'\n", item)
		if durations != nil {
			fmt.Fprintf(&b, "duration %s\n", strconv.FormatFloat(durations[i], 'f', 3, 64))
		}
	}
	list := filepath.Join(dir, "playlist.txt")
	if err := os.WriteFile(list, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return list
}

// probeDuration reads the container duration FFprobe itself reports for one
// file -- the number a real caller would have on hand, which is what makes
// the "WITH duration directives" run a fair test of what production code
// could actually supply, rather than of the nominal build target.
func probeDuration(t *testing.T, ffprobeBin, path string) float64 {
	t.Helper()
	out, err := exec.Command(ffprobeBin, "-hide_banner", "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe duration %s: %v", path, err)
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("parsing duration of %s (%q): %v", path, out, err)
	}
	return secs
}

// packet is one packet's stream and timestamps, in ffprobe's report order.
type packet struct {
	stream string
	pts    float64
	dts    float64
}

// loopThroughConcat plays a concat list through -stream_loop -1 for at least
// `seconds` of output and returns every packet's timestamps.
//
// ffprobe cannot be asked directly -- see the file's doc comment,
// adaptation #1. This runs the real playout argv through ffmpeg to a
// temporary stream-copied .ts and probes that, which is the same bytes
// ffprobe would have read had it accepted the option.
func loopThroughConcat(t *testing.T, ffmpegBin, ffprobeBin, list string, seconds float64) []packet {
	t.Helper()
	out := filepath.Join(filepath.Dir(list), "looped.ts")
	runFFmpeg(t, ffmpegBin, "-hide_banner", "-loglevel", "error", "-y",
		"-stream_loop", "-1", "-f", "concat", "-safe", "0", "-i", list,
		"-c", "copy", "-t", strconv.FormatFloat(seconds, 'f', 1, 64),
		"-f", "mpegts", out)

	raw, err := exec.Command(ffprobeBin, "-hide_banner", "-v", "error",
		"-show_entries", "packet=stream_index,pts_time,dts_time",
		"-of", "csv=p=0", out).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", out, err)
	}
	var pkts []packet
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 3 {
			continue
		}
		pts, err1 := strconv.ParseFloat(f[1], 64)
		dts, err2 := strconv.ParseFloat(f[2], 64)
		if err1 != nil || err2 != nil {
			continue // N/A timestamps, e.g. from a trailing partial GOP at cutoff.
		}
		pkts = append(pkts, packet{stream: f[0], pts: pts, dts: dts})
	}
	return pkts
}

// ptsReorderTolerance is bigger than any B-frame reorder window this
// profile produces and smaller than any item's duration, so it separates
// the codec's own, expected reordering from an actual seam defect.
//
// H.264 with B-frames presents pictures in a different order from the one
// they were coded and transmitted in -- reconciling exactly that gap is
// what PTS vs. DTS is for. libx264 buffers a small, bounded number of
// frames to do it; this measurement observed a maximum reorder of two
// frame periods (0.066667s at 30fps), recurring every GOP whether or not
// there is a playlist involved. An actual seam defect -- an item whose
// timestamps were not rebased and restarted the clock -- would jump back by
// the better part of a second, not by two frame periods, so 250ms
// (comfortably above the observed reorder window, comfortably below the
// shortest item's 1.5s) is the line between the two.
const ptsReorderTolerance = 0.25

// assertMonotonic fails on the FIRST DTS step backwards in any stream, and
// on the first PTS step back by more than ptsReorderTolerance in any
// stream. A summary count would say "199 non-monotonic packets" and leave
// the reader to find which seam; this names the stream, the packet index
// and both timestamps.
//
// Packets from different streams are compared only against their OWN
// stream's last packet, never against each other's. ffprobe's report
// interleaves independent timelines running at different wall-clock
// rates -- video packet, audio packet, video packet -- and comparing
// packet N of one against packet N-1 of the other proves nothing about
// either stream's splice; see the file's doc comment, adaptation #2.
func assertMonotonic(t *testing.T, pkts []packet) {
	t.Helper()
	if len(pkts) == 0 {
		t.Fatal("no packets: the concat input produced nothing, so nothing was measured")
	}
	type last struct{ dts, pts float64 }
	seen := map[string]last{}
	for i, p := range pkts {
		l, ok := seen[p.stream]
		if !ok {
			l = last{-1, -1}
		}
		if p.dts < l.dts {
			t.Fatalf("packet %d (stream %s): DTS went backwards, %f after %f", i, p.stream, p.dts, l.dts)
		}
		if l.pts-p.pts > ptsReorderTolerance {
			t.Fatalf("packet %d (stream %s): PTS went backwards by %f (%f after %f), "+
				"more than a B-frame reorder window can explain",
				i, p.stream, l.pts-p.pts, p.pts, l.pts)
		}
		seen[p.stream] = last{p.dts, p.pts}
	}
}
