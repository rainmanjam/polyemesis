package engine

import (
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A destination that copies the ingest's video sends whatever bitstream the
// encoder chose, and RTMP is where that can go wrong silently.
//
// THE HAZARD. Selecting HEVC or AV1 in OBS produces Enhanced RTMP, which
// polyemesis ingests (docs/INSTALL.md). Video is `-c:v copy` for every
// destination with no rendition (internal/ffmpeg/build.go:1121), so that
// bitstream reaches the platform verbatim. FFmpeg will mux HEVC into FLV
// happily -- Enhanced RTMP defines the mapping -- and the platform drops the
// stream. internal/ffmpeg/build.go names this failure class already: "muxes
// cleanly, uploads cleanly and is rejected by the platform ... looks correct
// everywhere the operator can see."
//
// THE SAME FACT IS ALREADY IN THE PRODUCT, aimed at the other direction.
// db.VideoEncoder.Codec's doc says "RTMP ingests are overwhelmingly H.264-only,
// so callers use this to warn", and rend.hevcWarning says it to the operator in
// sixteen languages -- but only about a codec POLYEMESIS chose for a rendition,
// never about one the INGEST brought in. A destination with no rendition on an
// HEVC ingest had no equivalent. This is that equivalent.
//
// WHY THIS WARNS AND DOES NOT REFUSE, which is not a matter of taste here.
// internal/db/platforms.go records that the opposite was already decided:
// datarhei's allowCopy filter, "which suppresses the passthrough option when
// the source codec is not in a platform's accepted list", was "considered for
// this codebase and rejected: suggesting is honest, hiding on the strength of a
// third-party number is not." VideoGuidance carries the same contract --
// "ADVISORY, ALWAYS. It must never hide an option, refuse a value, or block a
// save." A custom RTMP endpoint that does take HEVC is a real setup, and
// refusing a stream that would have worked is worse than the hazard.
//
// So: Warning rung. Control was unaffordable because the fact that decides it
// -- whether THIS platform accepts THIS codec -- is not knowable at save time
// (nothing has connected) and is not published for 29 of the 33 presets.

// h264Codec is the one value that does not warn. Matching on it rather than
// blocklisting {hevc, av1} is deliberate: a blocklist silently blesses whatever
// Enhanced RTMP maps next, and the probe reports ffprobe's codec_name, which is
// a larger vocabulary than the two values db.VideoEncoder.Codec knows about.
const h264Codec = "h264"

// passthroughCodecWarning returns the operator-facing warning for a destination
// that copies the ingest's video to a transport that may refuse it, or "" when
// there is nothing to say.
//
// IT FAILS OPEN, in three separate ways, because every one of them is a case
// where warning would be a guess: a video-less or unprobed ingest says nothing,
// a destination with a rendition says nothing (the rendition re-encodes, so the
// ingest's codec never reaches the platform), and a non-RTMP destination says
// nothing (SRT, file and HLS carry HEVC without complaint).
func passthroughCodecWarning(kind db.DestKind, platform db.Platform, renditionID *int64, video *ffmpeg.VideoStream) string {
	if kind != db.DestRTMP || renditionID != nil {
		return ""
	}
	// No probe, no claim. `measured` gates destination start so this is
	// normally populated by the time anything runs, but the probe can give up
	// and the silence tier starts destinations unmeasured -- and "unknown" must
	// never render as "H.264".
	if video == nil || video.Codec == "" || video.Codec == h264Codec {
		return ""
	}

	// Kick is the only platform in the tree with a SOURCED refusal:
	// internal/db/platforms.go cites Kick's own help page, checked 2026-08-06,
	// for "H.264 only -- Kick refuses H.265". That page says nothing about AV1,
	// so the certain wording is spent only where the evidence reaches.
	if platform == db.PlatformKick && video.Codec == "hevc" {
		return "The ingest is sending HEVC and this destination copies it. " +
			"Kick documents that it accepts H.264 only and refuses H.265, so " +
			"this stream will be rejected. Switch the encoder to H.264, or give " +
			"this destination an H.264 rendition."
	}

	// Everything else is "may", and the hedge is the honest part: no evidence
	// in this repository establishes what the other platforms accept, and
	// asserting a refusal from a third-party codec list is the failure
	// docs/evidence/platform-lifecycle-apis-2026-08-16.md records at length.
	return fmt.Sprintf(
		"The ingest is sending %s and this destination copies it. Most RTMP "+
			"destinations accept H.264 only, so this may be rejected even though "+
			"it uploads cleanly. If this endpoint has not told you it takes %s, "+
			"switch the encoder to H.264 or give this destination an H.264 rendition.",
		video.Codec, video.Codec)
}
