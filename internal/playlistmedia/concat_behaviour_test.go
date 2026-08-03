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
//     warning. ASSERTED, by assertWrapped, against the list's own measured
//     length rather than the figure quoted here -- without it, deleting
//     -stream_loop -1 from loopThroughConcat gives one ~6.65s pass whose
//     packets are all perfectly monotonic, and a test named "across seams and
//     wraps" passes without a single wrap having happened.
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
//     normaliser, not load-bearing. ASSERTED, in two halves that are only
//     worth anything together: assertDirectives proves the "with" list really
//     carries a directive per item (pass nil to writeList and the two subtests
//     silently become the same run), and assertIdenticalPackets proves the two
//     captures agree packet for packet. This is the measurement the engine
//     cites for building ConcatEntry.DurationMS and never emitting it, so it
//     is the one that must not be able to degenerate into a comparison of
//     something with itself.
//
//     WITH ONE CONDITION THAT WRITING THE ASSERTION FOUND, and that the prose
//     above had silently assumed: identical only while the directive is
//     EXACT. Rendered to three decimals rather than six, the same run is not
//     identical -- the derivatives measure 2.090667 / 3.072000 / 1.600000 on
//     the machine this was last run on, "2.091" is 333 us long, and every
//     packet from the second item onward shifts by that much. So "the
//     directives assert nothing new" is a statement about ACCURATE
//     directives, and the ones production could emit are millisecond-rounded
//     by construction. See writeList.
//
//     Item durations differ between toolchains -- the 2026-08-02 figures
//     above are not the 2026 figures a re-run gives -- so every assertion
//     here computes from the files it just built rather than from any number
//     recorded in this comment.
//
// # Why each Q is a separate assertion and none of them is assertMonotonic
//
// Monotonicity is necessary and nowhere near sufficient. A capture that never
// wrapped is monotonic. A "with directives" capture whose directives were
// never written is monotonic. Both would have left this file green while
// measuring nothing it is named for -- which is how a measurement quietly
// becomes a decoration, and two decisions on this branch rest on the numbers
// above.
func TestConcatTimestampsAreMonotonicAcrossSeamsAndWraps(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dataDir := t.TempDir()
	durations := []float64{2.0, 3.0, 1.5} // unequal on purpose; see the doc comment.
	items := buildDerivatives(t, ffmpegBin, ffprobeBin, dataDir, durations)

	// The real, encoder-rounded lengths. They are both what a `duration`
	// directive would have to carry and what ONE full pass of the list is, so
	// the wrap assertion is measured against the same file the run played
	// rather than against the nominal build target or the figure in the doc
	// comment above.
	measured := make([]float64, len(items))
	var listSecs float64
	for i, it := range items {
		measured[i] = probeDuration(t, ffprobeBin, it)
		listSecs += measured[i]
	}

	// Sum of the real, encoder-rounded durations is ~6.65s; two full wraps
	// plus margin for GOP alignment at the cutoff is comfortably under 14s.
	const seconds = 14.0

	var plain, directed []packet

	t.Run("without duration directives", func(t *testing.T) {
		list := writeList(t, t.TempDir(), items, nil)
		plain = loopThroughConcat(t, ffmpegBin, ffprobeBin, list, seconds)
		assertMonotonic(t, plain)
		assertWrapped(t, plain, listSecs)
	})

	t.Run("with duration directives", func(t *testing.T) {
		list := writeList(t, t.TempDir(), items, measured)
		assertDirectives(t, list, len(items))
		directed = loopThroughConcat(t, ffmpegBin, ffprobeBin, list, seconds)
		assertMonotonic(t, directed)
		assertWrapped(t, directed, listSecs)
	})

	// Q3, and it can only be asked out here: the two subtests each hold half
	// the answer and neither can compare itself to the other.
	if len(plain) == 0 || len(directed) == 0 {
		t.Fatal("one of the two captures produced nothing, so there is nothing to compare " +
			"and Q3 -- do duration directives change the packet stream -- is unanswered")
	}
	assertIdenticalPackets(t, plain, directed)
}

// assertWrapped fails unless the capture spans more than two full passes of
// the list, which is the only evidence that -stream_loop -1 looped at all.
//
// DTS, not the container duration ffprobe reports: these are the same
// timestamps every other assertion in this file reads, so a wrap that the
// demuxer produced but the muxer's header did not describe still counts, and
// there is no second notion of "how long the output is" to drift.
//
// Two passes rather than one plus a bit, because a single wrap crossed at the
// very end of the capture would leave the interesting region -- the packets
// AFTER the wrap -- barely populated. The 14s capture against a ~6.65s list
// clears this with margin; a failure here means the loop did not happen, not
// that it was marginal.
func assertWrapped(t *testing.T, pkts []packet, listSecs float64) {
	t.Helper()
	if len(pkts) == 0 {
		t.Fatal("no packets: nothing was captured, so nothing wrapped")
	}
	lo, hi := pkts[0].dts, pkts[0].dts
	for _, p := range pkts {
		if p.dts < lo {
			lo = p.dts
		}
		if p.dts > hi {
			hi = p.dts
		}
	}
	if span, want := hi-lo, 2*listSecs; span <= want {
		t.Fatalf("captured %.6fs of output from a %.6fs list, which does not exceed two full "+
			"passes (%.6fs): the input did not loop, so no wrap was crossed and the "+
			"monotonicity above speaks only for the seams inside one pass",
			span, listSecs, want)
	}
}

// assertDirectives fails unless the list really carries one `duration`
// directive per item.
//
// Without it the "with duration directives" subtest is one argument away from
// being a second copy of the "without" subtest -- pass nil to writeList and it
// writes a bare file list, every assertion still passes, and Q3 becomes a
// comparison of a run against itself. The subtest's NAME is the claim; this is
// what makes the name checkable.
func assertDirectives(t *testing.T, list string, items int) {
	t.Helper()
	raw, err := os.ReadFile(list)
	if err != nil {
		t.Fatalf("reading the concat list back: %v", err)
	}
	var got int
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "duration ") {
			got++
		}
	}
	if got != items {
		t.Fatalf("the list carries %d duration directives for %d items, so this subtest is not "+
			"measuring what it is named for:\n%s", got, items, raw)
	}
}

// assertIdenticalPackets fails on the first packet where the two captures
// differ, which is Q3 stated as an assertion rather than as a paragraph.
//
// Element by element rather than reflect.DeepEqual on the slices: the whole
// value of this comparison when it ever goes red is knowing WHICH packet moved
// and by how much, and "not deeply equal" would send the next reader back to
// ffprobe to find out.
func assertIdenticalPackets(t *testing.T, without, with []packet) {
	t.Helper()
	if len(without) != len(with) {
		t.Fatalf("duration directives changed the packet COUNT: %d without, %d with. "+
			"The engine builds ConcatEntry.DurationMS and deliberately emits nothing; "+
			"that decision rests on these two runs being identical",
			len(without), len(with))
	}
	for i := range without {
		if without[i] != with[i] {
			t.Fatalf("packet %d differs with duration directives: stream %s dts %f pts %f, "+
				"against stream %s dts %f pts %f without. The directives are not the "+
				"belt-and-braces this file records them as, and the engine's decision to "+
				"emit none needs re-taking",
				i, with[i].stream, with[i].dts, with[i].pts,
				without[i].stream, without[i].dts, without[i].pts)
		}
	}
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
//
// SIX DECIMAL PLACES, which is not cosmetic and cost a measurement to find.
// This wrote three, and at three the "with directives" capture is NOT
// identical to the plain one: ffprobe reports these derivatives at 2.090667s,
// 3.072000s, 1.600000s on the machine this was last run on, "2.091" is 333 us
// long, the demuxer starts the next item at the directive rather than at the
// file's real end, and every timestamp from there on is shifted by that much.
// Six places round-trips exactly what ffprobe printed, so the directive
// asserts what the file already says and the two captures agree.
//
// The finding is worth more than the fix: a directive is only harmless while
// it is EXACT, and a millisecond is not enough resolution to be exact.
// ConcatEntry.DurationMS is milliseconds, so the directives production could
// actually emit are the inaccurate kind -- which is a second, sharper reason
// the engine builds that field and emits nothing (see engine.go's concat list
// and ffmpeg/concat.go, where a zero duration emits no directive at all).
func writeList(t *testing.T, dir string, items []string, durations []float64) string {
	t.Helper()
	if durations != nil && len(durations) != len(items) {
		t.Fatalf("writeList: %d items but %d durations", len(items), len(durations))
	}
	var b strings.Builder
	for i, item := range items {
		fmt.Fprintf(&b, "file '%s'\n", item)
		if durations != nil {
			fmt.Fprintf(&b, "duration %s\n", strconv.FormatFloat(durations[i], 'f', 6, 64))
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
//
// Their sum is also what assertWrapped calls one pass of the list. The nominal
// 2.0/3.0/1.5 would be ~0.65% short of the encoder's real output, which is far
// too small to change that assertion's verdict -- but taking the length of the
// list from the files the run actually played, rather than from the numbers it
// asked for, is the difference between measuring the output and restating the
// input.
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
