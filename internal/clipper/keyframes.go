package clipper

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Where the keyframes are is the only fact that decides whether a stream copy
// produces a watchable file, and ffprobe will tell us — but only if it is asked
// the cheap question rather than the obvious one.
//
// The obvious question is `-skip_frame nokey -show_frames`, which DECODES the
// file to find its keyframes. On an hour-long 4K segment that is minutes of CPU
// on a machine whose whole job is not to lose frames on a live stream. The cheap
// question is `-show_packets`, which only demuxes: the container already flags
// every random-access packet, so the answer is a file read and no decode at all.
// Both shapes parse here, because a caller who already has frame output should
// not have to re-probe, but the cheap one is what this package asks.

// Keyframe probing bounds.
const (
	// ProbeLookback and ProbeLookahead bound how much of a segment is read
	// around the in-point. A GOP is normally two to ten seconds; twenty in each
	// direction finds one with room to spare and turns an hour-long file read
	// into a forty-second one.
	ProbeLookback  = 20 * time.Second
	ProbeLookahead = 20 * time.Second

	// ProbeTimeout bounds one ffprobe. A probe that hangs must not hold a job
	// slot; the cut degrades to unsnapped rather than waiting forever.
	ProbeTimeout = 30 * time.Second
)

// Keyframes is a sorted, deduplicated set of random-access points, in timeline
// coordinates.
//
// An EMPTY Keyframes is not "this file has no keyframes" — it is "nobody could
// tell us", which is a different thing and is handled by cutting anyway. See
// PlanCut.
type Keyframes struct {
	times []time.Duration
}

// NewKeyframes builds an index from raw positions, in any order.
func NewKeyframes(times []time.Duration) Keyframes {
	out := make([]time.Duration, 0, len(times))
	for _, t := range times {
		if t < 0 {
			// A negative timestamp survives no arithmetic worth doing here, and
			// containers do emit them at the head of a file.
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	// Dedupe: several video streams, or a probe window overlapping a previous
	// one, will report the same instant twice.
	uniq := out[:0]
	for i, t := range out {
		if i > 0 && t == uniq[len(uniq)-1] {
			continue
		}
		uniq = append(uniq, t)
	}
	return Keyframes{times: uniq}
}

// Times returns a copy of the indexed positions.
func (k Keyframes) Times() []time.Duration { return append([]time.Duration(nil), k.times...) }

// Len is how many random-access points are known.
func (k Keyframes) Len() int { return len(k.times) }

// Known reports whether the index has anything to say. False means the probe
// failed, was skipped, or found nothing — never that the file is unseekable.
func (k Keyframes) Known() bool { return len(k.times) > 0 }

// Shift moves every position by d, which is how a per-file probe becomes part
// of a timeline that starts somewhere else.
func (k Keyframes) Shift(d time.Duration) Keyframes {
	out := make([]time.Duration, 0, len(k.times))
	for _, t := range k.times {
		out = append(out, t+d)
	}
	return Keyframes{times: out}
}

// Merge folds another index into this one.
func (k Keyframes) Merge(other Keyframes) Keyframes {
	return NewKeyframes(append(k.Times(), other.times...))
}

// AtOrBefore returns the latest keyframe at or before t.
//
// This is the one a FAST cut snaps to: starting there guarantees every frame in
// the clip decodes, at the cost of a clip that begins earlier than asked.
func (k Keyframes) AtOrBefore(t time.Duration) (time.Duration, bool) {
	i := sort.Search(len(k.times), func(i int) bool { return k.times[i] > t })
	if i == 0 {
		return 0, false
	}
	return k.times[i-1], true
}

// After returns the first keyframe strictly after t.
//
// This is where a PRECISE cut stops re-encoding and starts copying. Strictly
// after, not at-or-after: a t that is already a keyframe needs no head at all,
// and returning t itself would produce a zero-length re-encode.
func (k Keyframes) After(t time.Duration) (time.Duration, bool) {
	i := sort.Search(len(k.times), func(i int) bool { return k.times[i] > t })
	if i >= len(k.times) {
		return 0, false
	}
	return k.times[i], true
}

// Contains reports whether t is itself a keyframe, within tolerance.
//
// Tolerance is not optional. The caller's in-point comes from a scrubber in
// milliseconds while the index comes from a container in fractional seconds,
// and demanding exact equality would re-encode a head that is already aligned.
func (k Keyframes) Contains(t, tolerance time.Duration) bool {
	if tolerance < 0 {
		tolerance = 0
	}
	i := sort.Search(len(k.times), func(i int) bool { return k.times[i] >= t-tolerance })
	return i < len(k.times) && k.times[i] <= t+tolerance
}

// AlignTolerance is how close to a keyframe counts as on it. One frame at 60fps
// is 16.6ms; half of that is under any real container's timestamp precision and
// still comfortably inside the rounding a UI does.
const AlignTolerance = 8 * time.Millisecond

// KeyframeArgs builds the ffprobe command that reports one file's random-access
// points.
//
// from/window restrict the read to the interesting part of the file. A zero
// window reads all of it, which is the honest fallback when a bounded read
// found nothing.
func KeyframeArgs(path string, from, window time.Duration) []string {
	if from < 0 {
		from = 0
	}
	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		// format.start_time comes back in the same call because it is needed to
		// interpret every packet timestamp below. See ParseKeyframes.
		"-show_entries", "format=start_time:packet=pts_time,dts_time,flags",
		"-of", "json",
	}
	if window > 0 {
		args = append(args, "-read_intervals", secs(from)+"%+"+secs(window))
	}
	// Deliberately last: -read_intervals applies to the input that follows it.
	return append(args, path)
}

// probeJSON is the shape of both ffprobe outputs this package accepts.
type probeJSON struct {
	Format struct {
		StartTime string `json:"start_time"`
	} `json:"format"`
	Packets []struct {
		PTSTime string `json:"pts_time"`
		DTSTime string `json:"dts_time"`
		Flags   string `json:"flags"`
	} `json:"packets"`
	Frames []struct {
		PTSTime  string `json:"pts_time"`
		BestTime string `json:"best_effort_timestamp_time"`
		// ffprobe has emitted this as both a number and a string across the 6.x
		// and 7.x lines, so it is decoded loosely rather than as an int.
		KeyFrame json.RawMessage `json:"key_frame"`
		PictType string          `json:"pict_type"`
	} `json:"frames"`
}

// ParseKeyframes turns ffprobe JSON into an index, in seek coordinates.
//
// SEEK COORDINATES, not container coordinates, and the difference has teeth. An
// MPEG-TS recording routinely starts at pts 1.4 rather than 0, and FFmpeg's -ss
// counts from the start of the FILE — so a keyframe reported at 1.4 is what
// `-ss 0` reaches. Handing the raw 1.4 back to -ss would seek 1.4 seconds past
// where the caller pointed. format.start_time is subtracted here so that never
// happens; MKV, where it is 0, is unaffected.
func ParseKeyframes(raw []byte) (Keyframes, error) {
	var p probeJSON
	if err := json.Unmarshal(raw, &p); err != nil {
		return Keyframes{}, fmt.Errorf("clipper: parse ffprobe output: %w", err)
	}

	base := parseSeconds(p.Format.StartTime)
	if base < 0 {
		// A negative container start is real (TS wraparound) but subtracting it
		// would push every keyframe later than it is. Ignore it instead.
		base = 0
	}

	var times []time.Duration
	for _, pkt := range p.Packets {
		// Position 0 of the flags field is the keyframe flag; the rest are
		// discard and corrupt markers. Prefix, never Contains: this repo has
		// already paid for a substring match against FFmpeg's output once.
		if !strings.HasPrefix(pkt.Flags, "K") {
			continue
		}
		// A packet with no PTS still has a DTS, and on a keyframe the two are
		// the same. Falling back beats leaving a hole in the index.
		if t, ok := firstTime(pkt.PTSTime, pkt.DTSTime); ok {
			times = append(times, seekTime(t, base))
		}
	}
	for _, f := range p.Frames {
		if !isKeyFrame(f.KeyFrame) && f.PictType != "I" {
			continue
		}
		if t, ok := firstTime(f.PTSTime, f.BestTime); ok {
			times = append(times, seekTime(t, base))
		}
	}
	return NewKeyframes(times), nil
}

// firstTime returns the first of several timestamp spellings that parses.
func firstTime(candidates ...string) (float64, bool) {
	for _, c := range candidates {
		if t := parseSeconds(c); t >= 0 {
			return t, true
		}
	}
	return 0, false
}

// seekTime rebases a container timestamp onto the file's own start.
func seekTime(t, base float64) time.Duration {
	d := time.Duration((t - base) * float64(time.Second))
	if d < 0 {
		// Packets before format.start_time exist at the head of some files.
		// Clamping beats discarding: the first keyframe is the most useful entry
		// in the whole index.
		return 0
	}
	return d
}

// isKeyFrame reads the several spellings of true ffprobe has used for this.
func isKeyFrame(raw json.RawMessage) bool {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	switch s {
	case "1", "true":
		return true
	}
	return false
}

// parseSeconds returns -1 for "N/A", "" and anything else unparseable, which is
// how every caller above distinguishes "no timestamp" from "timestamp zero".
func parseSeconds(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return -1
	}
	return v
}

// Prober runs ffprobe. It is an interface at the call site so planning can be
// tested against a known GOP structure without a file on disk.
type Prober interface {
	// Keyframes reports the random-access points of one file, in seek
	// coordinates, restricted to a window when one is given.
	//
	// It must not return an error for a file it merely could not understand:
	// an empty index means "unknown" and the caller degrades gracefully.
	Keyframes(ctx context.Context, path string, from, window time.Duration) (Keyframes, error)
}

// FFprobe is the real Prober.
type FFprobe struct {
	// Bin is the ffprobe binary. Empty means "ffprobe", found on PATH.
	Bin string
	// Run is overridable so tests can supply captured output. Nil runs the real
	// process.
	Run func(ctx context.Context, name string, args []string) ([]byte, error)
}

// Keyframes implements Prober.
func (f FFprobe) Keyframes(ctx context.Context, path string, from, window time.Duration) (Keyframes, error) {
	bin := f.Bin
	if bin == "" {
		bin = "ffprobe"
	}
	run := f.Run
	if run == nil {
		run = runCommand
	}

	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	out, err := run(ctx, bin, KeyframeArgs(path, from, window))
	if err != nil {
		return Keyframes{}, fmt.Errorf("clipper: ffprobe %s: %w", path, err)
	}
	kf, err := ParseKeyframes(out)
	if err != nil {
		return Keyframes{}, err
	}
	if kf.Known() || window == 0 {
		return kf, nil
	}
	// A bounded read that found nothing means the window missed — a very long
	// GOP, or a segment shorter than the lookback. Pay for the whole file once
	// rather than returning "unknown" and giving up on snapping.
	out, err = run(ctx, bin, KeyframeArgs(path, 0, 0))
	if err != nil {
		return Keyframes{}, fmt.Errorf("clipper: ffprobe %s: %w", path, err)
	}
	return ParseKeyframes(out)
}

// runCommand is the default process runner, shared by the prober and the cutter.
func runCommand(ctx context.Context, name string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, truncate(strings.TrimSpace(string(out)), 400))
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// indexFor probes for the keyframes near an in-point and returns them in
// timeline coordinates.
//
// Only the segments overlapping the probe window are read. Every keyframe this
// package uses — the one to snap back to, the one to resume copying at — lives
// within a GOP or two of the in-point, so reading the far end of a
// boundary-spanning cut would cost minutes to learn nothing.
//
// It never fails the cut. A per-file error becomes a warning and the surviving
// keyframes are used, because a cut snapped against a partial index is better
// than no cut at all.
func indexFor(ctx context.Context, p Prober, segs []Segment, around time.Duration) (Keyframes, []string) {
	if p == nil {
		return Keyframes{}, nil
	}
	lo, hi := around-ProbeLookback, around+ProbeLookahead
	var (
		kf    Keyframes
		warns []string
	)
	for _, s := range segs {
		if s.End() <= lo || s.Start >= hi {
			continue
		}
		// The window is relative to the file, so the in-point has to leave
		// timeline coordinates first. A segment the in-point falls after is
		// probed from its own start.
		from := around - s.Start - ProbeLookback
		if from < 0 {
			from = 0
		}
		one, err := p.Keyframes(ctx, s.Path, from, ProbeLookback+ProbeLookahead)
		if err != nil {
			warns = append(warns, fmt.Sprintf("could not read the keyframes of %s: %v", s.Path, err))
			continue
		}
		kf = kf.Merge(one.Shift(s.Start))
	}
	return kf, warns
}
