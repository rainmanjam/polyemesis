// Package meters is the measurement tier: what a destination is actually
// sending, expressed in the unit the platform on the other end judges it in.
//
// The per-channel peak/RMS sidecar (ffmpeg.MetersArgs) measures the INGEST,
// which is the number an operator uses to set gain. This package measures the
// other end of the pipe: one EBU R128 analyser per destination, downstream of
// that destination's own routing graph, because a per-destination mix is
// exactly the thing an ingest-side meter cannot see. Two destinations summing
// different tracks land on different loudness, and only one of them can be the
// one the platform re-levels.
package meters

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Spec describes one destination's loudness analyser.
type Spec struct {
	// RelayURL is the hub the destination itself reads, so the analyser is fed
	// byte-identical input rather than something merely similar.
	RelayURL string
	// FilterComplex is that destination's COMPILED routing graph, reused
	// verbatim. Anything reconstructed here would measure a different mix the
	// day the compiler changed, and be believed anyway.
	FilterComplex string
	// OutLabel is the label in that graph carrying the finished stereo mix.
	// Empty means routing's default.
	OutLabel string
}

// Graph labels the analyser splices on. Deliberately unlikely names: the graph
// they attach to is user-influenced, and a collision would fail the whole
// analyser rather than the stage that caused it.
const (
	analyserIn  = "l_r128"
	analyserOut = "l_meta"
)

// Args builds the loudness analyser's command line.
//
// WHY A SEPARATE PROCESS, and what it costs. The alternative is `-af ebur128`
// inside the destination's own FFmpeg, which measures the same samples for
// almost no extra CPU. It was rejected on three counts:
//
//   - It would alter the destination's filter graph. Every saved profile is
//     guaranteed to compile to the same filter string it always did, and that
//     guarantee is worth more than a spare core.
//   - It puts a measurement stage inside the one process that must not stop.
//     An ebur128 that refuses to negotiate a format takes the stream off air;
//     here it takes a number off a dashboard.
//   - Its output would arrive on the same stderr the log ring reads, at 10 Hz,
//     which is a log nobody can use any more.
//
// The price is one process per destination. It decodes audio only — the video
// is never mapped, so it is never decoded — applies the routing graph a second
// time and runs ebur128, then throws the samples away into `-f null`. On a
// stereo 48 kHz mix that is low single-digit percent of one core, roughly what
// the destination's own AAC encode already costs, and it is the whole extra
// bill for the feature.
//
// Measurements come back on STDOUT via ametadata, exactly as the ingest meter
// does, so stderr stays a pure human log and the frame log stays suppressed by
// the inherited `-loglevel warning`.
func Args(s Spec) []string {
	out := s.OutLabel
	if out == "" {
		out = routing.OutLabel
	}
	// direct=1 is load-bearing, not tidiness. ebur128 prints about 150 bytes a
	// frame, so a buffered avio holds twenty seconds of readings before the
	// first flush and then delivers them in a burst — a meter that appears
	// dead for twenty seconds and then jumps. It has been an ametadata option
	// since FFmpeg 4.0; a build old enough to reject it loses this meter and
	// nothing else, which is the failure this tier is shaped to survive.
	graph := fmt.Sprintf("%s;[%s]ebur128=metadata=1:peak=true[%s];[%s]ametadata=mode=print:file=-:direct=1[%s]",
		s.FilterComplex, out, analyserIn, analyserIn, analyserOut)

	return []string{
		"-hide_banner", "-nostdin", "-nostats", "-loglevel", "warning",
		"-fflags", "+genpts",
		"-thread_queue_size", "512",
		"-i", ffmpeg.RelayInputURL(s.RelayURL),
		"-filter_complex", graph,
		"-map", "[" + analyserOut + "]",
		"-f", "null", "-",
	}
}

// LUFSFloor is what ebur128 reports while the programme has not yet risen above
// its absolute gate. It is the absence of a measurement, not a very quiet one,
// and reporting it as "-70 LUFS, 56 LU under target" would be a lie told to
// three decimal places.
const LUFSFloor = -70.0

// TruePeakFloor bounds the dBTP conversion. true_peak arrives as a linear
// amplitude and digital silence is exactly zero, whose logarithm is not a
// number anyone can put on a meter.
const TruePeakFloor = -120.0

// Frame is one measurement the analyser printed.
type Frame struct {
	// Seconds is the programme time this frame measured up to, taken from the
	// filter's own pts rather than the wall clock: a stream that stalls must
	// not appear to have been measured through the gap.
	Seconds float64 `json:"seconds"`
	// MomentaryLUFS is the 400 ms window, ShortTermLUFS the 3 s window.
	MomentaryLUFS float64 `json:"momentaryLufs"`
	ShortTermLUFS float64 `json:"shortTermLufs"`
	// IntegratedLUFS is the gated programme loudness — the number a platform
	// normalizes against, and the only one a compliance verdict is worth
	// forming on.
	IntegratedLUFS float64 `json:"integratedLufs"`
	RangeLU        float64 `json:"rangeLu"`
	// TruePeakDBTP is the inter-sample peak, converted from the linear
	// amplitude the filter emits.
	TruePeakDBTP float64 `json:"truePeakDbtp"`
	// Integrated reports whether IntegratedLUFS is a reading at all.
	Integrated bool `json:"integrated"`
}

// Parse reads the analyser's stdout and calls fn once per printed frame.
//
// ametadata prints a `frame:` header followed by one `key=value` line per
// metadata entry, so a frame is only complete when the next header arrives —
// or when the stream ends, which is why the flush is repeated after the loop.
func Parse(r io.Reader, fn func(Frame)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 16*1024), 256*1024)

	var cur Frame
	var have bool
	flush := func() {
		if have && fn != nil {
			fn(cur)
		}
		cur, have = Frame{}, false
	}

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "frame:"):
			flush()
			cur.Seconds = ptsTime(line)
		case strings.HasPrefix(line, "lavfi.r128."):
			key, raw, ok := strings.Cut(strings.TrimPrefix(line, "lavfi.r128."), "=")
			if !ok {
				continue
			}
			v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				continue
			}
			switch key {
			case "M":
				cur.MomentaryLUFS, have = v, true
			case "S":
				cur.ShortTermLUFS, have = v, true
			case "I":
				cur.IntegratedLUFS, have = v, true
				cur.Integrated = v > LUFSFloor
			case "LRA":
				cur.RangeLU, have = v, true
			case "true_peak":
				cur.TruePeakDBTP, have = DBTP(v), true
			}
		}
	}
	flush()
	return sc.Err()
}

// ptsTime pulls pts_time out of an ametadata frame header. A header we cannot
// read yields zero rather than dropping the frame: the levels in it are still
// true, and only the "how much programme so far" gate is affected.
func ptsTime(line string) float64 {
	for _, f := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(f, "pts_time:"); ok {
			if s, err := strconv.ParseFloat(v, 64); err == nil {
				return s
			}
		}
	}
	return 0
}

// DBTP converts ebur128's linear true-peak amplitude to dBTP.
func DBTP(linear float64) float64 {
	if linear <= 0 {
		return TruePeakFloor
	}
	db := 20 * math.Log10(linear)
	if db < TruePeakFloor {
		return TruePeakFloor
	}
	return db
}
