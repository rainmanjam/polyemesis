package supervisor

import (
	"regexp"
	"time"
)

// DECODER WARM-UP. Every child that decodes video from the relay -- the
// preview, the renditions, anything that re-encodes -- joins the stream
// wherever the relay happens to be, and that is almost never a keyframe. Until
// the next one arrives, FFmpeg's video decoder has no reference picture and says
// so once a frame: "non-existing PPS 0 referenced", "decode_slice_header error",
// "Missing reference picture". classify() files several of those as errors, so a
// perfectly healthy install showed "189 ERRORS", all in the first second, on
// whichever process had started last. No publisher setting removes it short of
// an all-intra stream.
//
// So a video decoder's complaints are logged as info while the process is still
// warming up, and counted as what they are once it is past that.
//
// THE WINDOW is tight on purpose, because a decode error after warm-up is real
// (corrupt input, a publisher fault) and must still count. It closes at the
// FIRST of:
//
//   - the first output frame. FFmpeg's decoder emits nothing until it has
//     decoded a keyframe, so a frame out means the keyframe has arrived and
//     anything the decoder says after it is about the stream, not the join.
//   - decoderWarmup after the spawn, for a child whose stdout is not the
//     -progress parser (the frame count then stays 0) or whose output is not
//     video at all. 8s is two of the longest keyframe intervals a supported
//     platform accepts (YouTube's ceiling is 4s; Twitch requires 2s), which
//     covers a join one frame after a keyframe plus the relay subscribe and
//     FFmpeg's input probe ahead of the first decode.
//
// The window restarts with every spawn, because every respawn joins again.
const decoderWarmup = 8 * time.Second

// videoDecoderLine matches a line logged by a video decoder's context:
// "[h264 @ 0x...]" and, from FFmpeg 7, the decoder-stream wrapper
// "[vist#0:0/h264 @ 0x...]". Audio decoders and muxers are left out: they do
// not join a GOP, so nothing they say at the start is warm-up.
var videoDecoderLine = regexp.MustCompile(
	`^\[(?:vist#\d+:\d+/)?(?:h264|hevc|av1|vp8|vp9|mpeg2video|mpeg4) @ 0x[0-9a-fA-F]+\]`)

// inDecoderWarmup reports whether line is the join-time decoder burst: a video
// decoder's line, from a process `since` its spawn that has produced `frames`
// output frames, inside a window of `window`.
func inDecoderWarmup(line string, since time.Duration, frames int64, window time.Duration) bool {
	return frames == 0 && since < window && videoDecoderLine.MatchString(line)
}

// levelOf is classify() with the decoder warm-up taken out: a warm-up line that
// would have been an error or a warning is info. Called from the stderr scan
// loop; the lock is taken only for a line that would otherwise be counted.
func (p *Process) levelOf(line string) string {
	level := classify(line)
	if level == "info" || level == "fatal" {
		return level
	}
	p.mu.RLock()
	since, frames, window := time.Since(p.startedAt), p.progress.Frame, p.warmup
	p.mu.RUnlock()
	if inDecoderWarmup(line, since, frames, window) {
		return "info"
	}
	return level
}
