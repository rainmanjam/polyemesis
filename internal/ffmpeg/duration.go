package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// DurationSource says HOW ProbeResult.DurationSeconds was arrived at, because
// the two ways of arriving at it are not the same kind of statement and the
// difference is not recoverable from the number.
//
// THE POINT IS THAT THE ZERO VALUE IS "UNKNOWN". A ProbeResult built by hand --
// by a test, by Probe for a live relay, by any future caller -- says "I do not
// know where this came from" until something sets it, and can never be mistaken
// for a container's own statement by a reader who forgot the field exists. The
// alternative, making "declared" the zero value, would have made every
// unpopulated struct claim the strongest of the three.
type DurationSource string

const (
	// DurationUnknown is no duration established: a live relay, or a file whose
	// length neither the container nor a counting pass would state.
	DurationUnknown DurationSource = ""
	// DurationDeclared is the container's own field. A muxer wrote the length
	// down when it wrote the file, and for most containers it can be checked
	// against the index -- it is a second, independent statement about the same
	// bytes.
	DurationDeclared DurationSource = "declared"
	// DurationCounted is derived by decoding every frame and reading where
	// FFmpeg's output clock finished. See CountDurationSeconds for exactly what
	// that is and is not.
	DurationCounted DurationSource = "counted"
)

// CountDurationSeconds decodes a whole file and reports where FFmpeg's output
// clock ended up.
//
// WHAT THIS NUMBER IS: the count of coded frames actually decoded, times the
// frame interval the codec's own parser inferred from the bitstream. The count
// is a fact -- every frame was read. The interval is the ENCODER'S DECLARED
// INTENT, carried in the SPS VUI for H.264 or the VPS/SPS timing info for
// HEVC, and there is nothing else in a raw elementary stream to check it
// against. A container's duration can be cross-checked against its index; this
// cannot be cross-checked against anything. That asymmetry is the whole reason
// DurationSource exists and why this result is tagged DurationCounted rather
// than being laundered into the same field with the same standing.
//
// WHY A FULL DECODE, WHEN THE ISSUE HOPED FOR SOMETHING CHEAPER. Every cheaper
// route was tried against real raw dumps muxed for the purpose and every one
// is wrong or empty:
//
//   - nb_read_packets / avg_frame_rate. Cheap -- 0.15s over a 192 MB file --
//     and WRONG, because ffprobe reports avg_frame_rate=25/1 for EVERY raw
//     H.264 and HEVC stream regardless of the real rate. It is the demuxer's
//     hardcoded fallback, not a reading. Measured on 17-second fixtures: 30 fps
//     derived 20.400s, 50 fps derived 34.000s, 60 fps derived 40.800s.
//   - r_frame_rate. Right to a factor: exactly 2x the true rate for H.264
//     (field ticks) and exactly 1x for HEVC. A halving rule that depends on the
//     codec and on which FFmpeg deprecated ticks_per_frame is not a rule this
//     product should carry.
//   - packet or frame timestamps, at any level. ffprobe reports pts_time,
//     dts_time and best_effort_timestamp_time as N/A for every raw H.264 and
//     HEVC fixture. There is no timestamp to read.
//   - ffmpeg -c copy -f null -. Reports no time at all for H.264 and HEVC,
//     for the same reason: stream copy never builds the timestamps.
//   - ffprobe -count_frames. Accurate on the COUNT and gives no rate, so it
//     cannot produce a duration on its own -- and it is 7.5x SLOWER than the
//     full decode below (22.4s vs 3.0s over the same 192 MB file), because it
//     prints a record per frame.
//
// So the decode pass is not the expensive option that was settled for, it is
// the only one that answers, and it happens to be the fastest thing that does.
//
// WHAT IT COSTS, measured on this machine with FFmpeg 8.1.2, three runs each:
//
//	720p25,  600s, 192 MB  ->  2.77-2.97s   (~210x realtime)
//	1080p25, 300s, 375 MB  ->  4.17-4.28s   (~71x realtime)
//
// The caller supplies the deadline. At the upload handler that is the probe
// budget #216 already established, so ~35 minutes of 1080p fits inside it and
// anything past it gets the answer it gets today. No new budget is invented and
// no new concurrency bound is needed: this runs inside the slot the probe
// already holds.
//
// THE DEMUXER IS PINNED with -f, from the format name ffprobe just reported and
// the allowlist just admitted. Without it this is a SECOND, unguarded format
// detection over bytes the gate has already ruled on, and FFmpeg is free to
// reach a different verdict than ffprobe did -- which is exactly the hole
// ProbeFile's allowlist closes. -protocol_whitelist file is here for the same
// reason it is on ProbeFile and on build.go's pull input.
func CountDurationSeconds(ctx context.Context, ffmpegBin, format, path string) (float64, error) {
	if ffmpegBin == "" {
		return 0, fmt.Errorf("no ffmpeg binary to count frames with")
	}
	if format == "" {
		return 0, fmt.Errorf("no demuxer name to pin the count to")
	}
	cmd := exec.CommandContext(ctx, ffmpegBin, CountDurationArgs(format, path)...)
	// The same reasoning as probeFile's: killing the child is a different event
	// from its stdout pipe closing, and a wrapper script leaves a grandchild
	// holding the write end. Without this a cancelled upload sits in Wait.
	cmd.WaitDelay = 5 * time.Second
	// -progress prints one small block per second, so this is far smaller than
	// a probe's JSON -- but it is unbounded in the file's LENGTH rather than in
	// its stream count, and the cap costs nothing.
	stdout := &cappedBuffer{max: probeStdoutCap}
	stderrBuf := &cappedBuffer{max: probeStderrCap}
	cmd.Stdout = stdout
	cmd.Stderr = stderrBuf
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(stderrBuf.buf.String()); s != "" {
			return 0, fmt.Errorf("%s: %w", truncate(s, 300), err)
		}
		return 0, err
	}
	ms, err := furthestOutTimeMS(&stdout.buf)
	if err != nil {
		return 0, fmt.Errorf("read ffmpeg progress: %w", err)
	}
	if ms <= 0 {
		return 0, fmt.Errorf("ffmpeg decoded %s without reaching a positive output time", path)
	}
	return float64(ms) / 1000, nil
}

// furthestOutTimeMS reads a -progress stream and reports the highest output
// time any complete block reported.
//
// THE PROPERTY THAT MATTERS, AND IS TESTED, is that it reads the WHOLE stream.
// FFmpeg's -progress counters are cumulative -- out_time_us is the total so far,
// not a delta -- so the first block of a long decode reports a fraction of the
// file and taking it would report a fraction of the length. This is a function
// of its own with its own test because a closure inside the call above could not
// be shown that: FFmpeg emits a block about twice a second of WALL time, so no
// fixture short enough for a unit test produces more than one, and against a
// single block every reading of the stream is the same reading. Measured -- with
// a real two-second fixture, a first-block rule changed no test in this package.
//
// HIGHEST RATHER THAN LAST IS NOT SEPARATELY FALSIFIABLE, and saying so is more
// honest than a comment claiming a defence. The two differ only if a later block
// reports a SMALLER time, and ParseProgress cannot produce that: it reuses one
// Progress across blocks, so a block missing an out_time line inherits the
// previous block's value rather than resetting to zero, and cappedBuffer drops
// bytes from the END, which removes whole blocks rather than corrupting earlier
// ones. `>` is kept because it costs one character and cannot be wrong in the
// direction that hurts -- a length read SHORT becomes estimateBytes' -fs cap and
// truncates an operator's media -- but it is a belt, not a mechanism, and no
// test in this package asserts it.
func furthestOutTimeMS(r io.Reader) (int64, error) {
	var furthest int64
	if err := ParseProgress(r, func(p Progress) {
		if p.OutTimeMS > furthest {
			furthest = p.OutTimeMS
		}
	}); err != nil {
		return 0, err
	}
	return furthest, nil
}

// CountDurationArgs is the argv CountDurationSeconds runs, split out because
// two of its tokens are SAFETY PINS and a pin nobody can assert is a comment.
//
// Same shape as ProbeArgs, streamArgs and normaliseArgs, and for the same
// reason those are separate.
//
//   - `-f format` before `-i` pins the DEMUXER to the one ffprobe reported and
//     the allowlist admitted. Without it this is a second, unguarded format
//     detection over bytes the gate has already ruled on, and FFmpeg may reach
//     a different verdict than ffprobe did -- which is the hole ProbeFile's
//     allowlist exists to close. Order matters: after `-i` it is an OUTPUT
//     format and pins nothing.
//   - `-protocol_whitelist file` bounds what the demuxer may open, the same pin
//     ProbeFile and build.go's pull input carry.
//
// `-f null -` is the output: decode everything, write nothing. `-nostats` with
// `-progress pipe:1` puts the machine-readable clock on stdout instead of the
// human one on stderr, so the answer is parsed by ParseProgress rather than
// scraped from a status line whose format is not a contract.
func CountDurationArgs(format, path string) []string {
	return []string{
		"-hide_banner",
		"-nostdin",
		"-loglevel", "error",
		"-protocol_whitelist", "file",
		"-nostats",
		"-progress", "pipe:1",
		"-f", format,
		"-i", path,
		"-f", "null", "-",
	}
}
