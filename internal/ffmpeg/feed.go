package ffmpeg

import (
	"strconv"
	"strings"
)

// RelayFeedArgs builds the copy hop that carries one ingest into the selector.
//
// `-map 0 -c copy`, exactly like the ingest itself: the selector must never
// become a second place video is degraded or a track is quietly dropped. The
// one thing it adds is -output_ts_offset, which is what makes a switch a
// forward step on a shared timeline instead of a jump into the past.
func RelayFeedArgs(inURL, outURL string, offsetSeconds float64) []string {
	return ConformedFeedArgs(inURL, outURL, offsetSeconds, nil)
}

// conformAudioKbps is the bitrate a conformed track is re-encoded at when the
// primary's probe did not report one it can be held to.
const conformAudioKbps = 128

// ConformedFeedArgs is the copy hop for a STANDBY source -- the backup or the
// playlist -- with its audio re-encoded to the primary's measured format.
//
// WHY A STANDBY MAY NOT KEEP ITS OWN AUDIO FORMAT. A destination reads one relay
// for its whole life, and when the audio on it starts decoding at another
// sample rate or channel count, FFmpeg rebuilds the destination's filter graph
// mid-run. A one-track graph survives that since #931. A multi-track one cannot:
// each leg ends in routing's trackAlign (`aresample=async=1:first_pts=0`, the
// only common origin amix has), a rebuilt leg meets its first frame at the
// stream's runtime T and pads T seconds of silence in front of it, and the
// audio goes T seconds behind the video until the destination restarts. No
// filter inside the graph can stop the rebuild, because the rebuild is decided
// at the graph's INPUT. So the format is fixed here, before the relay: track N
// of the standby is encoded at track N of the primary's rate and channel count,
// and across the switch the destination decodes one format throughout.
//
// The video is still copied, so the selector is still not a place video is
// degraded. The audio costs one AAC generation, and only while a standby is
// on air; a destination re-encodes it again anyway unless it copies audio.
// The primary never comes through here -- it IS the format, and stays a pure
// `-c copy` -- and with no measured primary (nil) this is RelayFeedArgs
// exactly: there is nothing to match, and guessing would be worse than the
// copy.
//
// A standby track beyond the primary's count stays copied; no compiled graph
// maps it. A primary track the probe gave no rate or channel count for is left
// alone rather than encoded at a guess.
//
// LIMIT, stated rather than hidden: AAC is what this produces. The rebuild is
// triggered by the decoded sample format, rate and layout, not the codec name,
// and AAC decodes to planar float, as most lossy codecs do -- but a primary
// whose decoder emits something else (PCM, say) still sees a format change.
// tests: internal/engine/failover_format_conform_test.go.
//
// SILENT PADS. A standby with fewer audio tracks than the primary (a one-track
// backup under a two-track primary) would leave the relay with no track N, and
// a destination compiled for the primary's layout refuses to bind its graph
// against it: FFmpeg exits "Stream specifier ':a:1' ... matches no streams", and
// a restarted destination dies at once. So when every primary track is measured,
// the hop appends one pad per primary track, each `volume=0` of the standby's
// own track 0. The pads are after the standby's real tracks, so a missing
// track N is exactly the pad that lands in slot N, and an extra pad beyond the
// standby's count is only an unused silent track. A pad shares the standby's
// timeline, so it cannot race ahead of a live relay the way a free-running
// anullsrc would. The pads are encoded at the primary's slot format, which is
// why video is copied with -c:v and audio is encoded here, not with -c copy.
// A standby with no audio at all has no track 0 to pad from and fails the
// feed, as it did before, since the destinations need audio from it anyway.
func ConformedFeedArgs(inURL, outURL string, offsetSeconds float64, primary []AudioStream) []string {
	var conform []string
	allMeasured := len(primary) > 0
	for _, a := range primary {
		if a.Index < 0 || a.SampleRate <= 0 || a.Channels <= 0 {
			allMeasured = false
			continue
		}
		kbps := a.Bitrate / 1000
		if kbps < 32 || kbps > 512 {
			kbps = conformAudioKbps
		}
		n := strconv.Itoa(a.Index)
		conform = append(conform,
			"-c:a:"+n, "aac",
			"-ar:a:"+n, strconv.Itoa(a.SampleRate),
			"-ac:a:"+n, strconv.Itoa(a.Channels),
			"-b:a:"+n, strconv.Itoa(kbps)+"k",
		)
	}

	args := CommonArgs(LogWarning, false)
	args = append(args, ProgressArgs()...)
	args = append(args, RelayConsumerInput(inURL, RelayQueue)...)
	args = append(args, "-map", "0")
	switch {
	case len(conform) == 0:
		args = append(args, "-c", "copy")
	case !allMeasured || len(primary) < 2:
		// A per-stream codec has to come after the blanket `-c copy` to override
		// it, and every flag here has to come before the output target to attach
		// to it.
		args = append(args, "-c", "copy")
		args = append(args, conform...)
	default:
		// Two or more primary tracks, all measured: pad to the primary's count.
		// The video stays a copy; the audio, real and pad alike, is encoded.
		var graph []string
		for i := range primary {
			graph = append(graph, "[0:a:0]volume=0[pad"+strconv.Itoa(i)+"]")
		}
		args = append(args, "-c:v", "copy", "-filter_complex", strings.Join(graph, ";"))
		for i := range primary {
			args = append(args, "-map", "[pad"+strconv.Itoa(i)+"]")
		}
		args = append(args, "-c:a", "aac")
		args = append(args, conform...)
	}
	args = append(args, "-output_ts_offset", strconv.FormatFloat(offsetSeconds, 'f', 3, 64))
	return append(args, RelayProducerOutput(outURL)...)
}

// PlaylistFeedArgs builds the loop that publishes the WHOLE list into the
// playlist's own hub. It is the backup's command with a concat list where the
// socket was: `-map 0 -c copy`, so a programme that was encoded once is not
// re-encoded here.
//
// -f concat over every item's derivative, looped as a whole rather than per
// item, so the list plays in order and then starts again from the top.
//
// THE LIST NAMES DERIVATIVES, which is the point of B1: every entry shares
// codec, timebase, geometry and channel layout by construction, so `-c copy`
// across a seam is a copy and not a codec change.
//
// -safe 0 because the list holds absolute paths. They are paths this process
// built through playlistmedia.DerivativePath, from a name uploads.SafeName had
// already sanitised and DerivativePath then reduces to its base -- never
// operator text reaching FFmpeg as written, which is the whole reason items
// reference uploads rather than paths.
//
// ALWAYS CONCAT, EVEN FOR ONE ITEM. A single-file special case would mean two
// argv shapes, two sets of seam behaviour, and a branch that is wrong in a way
// nobody notices until the one-item playlist is the one on air.
//
// NO `duration` DIRECTIVES ARE EMITTED, and that is a measured result rather
// than a preference: three real derivatives were concatenated, looped past two
// full wraps and probed over 1068 packets with and without the per-entry
// directives. See internal/playlistmedia/concat_behaviour_test.go.
//
// The packet streams are identical ONLY WHEN THE DIRECTIVE IS EXACT. An earlier
// version of this comment said "byte-identical either way" without that clause,
// and the measurement does not support it: at three decimals the directive is
// 333 microseconds short of the file and every packet after the first item
// shifts. ffmpeg.ConcatList renders `%.3f` because ConcatEntry.DurationMS is
// MILLISECONDS, so the only directives this product could emit are the
// inaccurate kind -- which is an argument for leaving the field zero, not
// against it. The field survives for a profile that drifts far enough to need
// it, and whoever turns it on needs sub-millisecond precision first.
//
// The two input flags are the whole difference from every other feed, and
// neither is optional. -stream_loop -1 is what makes a file that ends look like
// a source that does not, so the tier is still delivering an hour later instead
// of exiting once and leaving the selector with a candidate that vanished.
// Without -re FFmpeg reads a file as fast as the disk allows: an hour of
// programme arrives at the relay in seconds, the hub's consumers are handed a
// timeline racing away from wall clock, and what an operator sees is a playlist
// that "played" and disappeared. The same pair, for the same reason, is what
// pullFile emits for a file:// ingest.
//
// The "file:" prefix pins the LIST to the file protocol, as B1 pinned the
// upload path and as pullFile and pullSource pin theirs.
//
// WHAT IT ACTUALLY PROTECTS IS NARROWER THAN IT LOOKS, and the boundary was
// measured against FFmpeg 8.1.2 rather than reasoned about. FFmpeg infers a
// protocol from the characters before the first ":" ONLY while no "/" has
// appeared, so:
//
//   - "2026:01/data/list.txt" -- a RELATIVE data directory whose first segment
//     carries a colon -- fails with "Protocol not found" unprefixed, and opens
//     with the prefix. This is the case the prefix buys.
//   - "/mnt/2026:01/data/list.txt" is fine either way. An ABSOLUTE path can
//     never be misread, because the leading "/" ends protocol detection before
//     the colon is reached.
//   - "data/a:b/list.txt" is fine either way, for the same reason.
//
// So the widely-repeated worry -- an operator's data directory containing a
// colon -- is NOT a failure for any absolute DataDir, which is the ordinary
// case. The prefix is kept because it makes the guarantee unconditional instead
// of resting on that argument, it costs one string concatenation, and it keeps
// every file input in this package spelled the same way. It is not load bearing
// for a deployment that configures an absolute data directory.
//
// THE FILE'S CODEC PARAMETERS MUST MATCH THE INGEST'S, AND NOTHING CHECKS.
// `-c copy` here and a copy hop in startFeed mean the file's codec, resolution,
// frame rate and pixel format reach every destination exactly as they were
// encoded. A destination that is also copying passes them straight to the
// platform, so switching to a file that differs is a mid-stream codec change --
// and platforms answer that by dropping the connection, which is the one thing
// this whole tier exists to prevent. The slate re-encodes to the ingest's
// PROBED geometry for precisely this reason; the playlist cannot, because
// re-encoding a programme that was already encoded once is a cost an operator
// did not ask for. scripts/acceptance-failover.sh builds its filler clip to
// match the publisher deliberately, and says so.
//
// It is not validated here because validation would mean probing the operator's
// file at settings-save time and comparing it against an ingest that may not be
// connected yet -- a feature with its own failure modes (an unprobeable file, a
// geometry that changes when the encoder reconnects), not a check this function
// can make from an argv. Until that exists the constraint is documented where
// an operator meets it, in docs/SCHEDULED-BROADCAST.md.
func PlaylistFeedArgs(listPath, outURL string) []string {
	args := CommonArgs(LogWarning, false)
	args = append(args, ProgressArgs()...)
	args = append(args,
		"-stream_loop", "-1",
		"-re",
		// Each entry's own timestamps restart at every seam and at every loop
		// boundary; genpts gives the relay a monotonic base without touching the
		// payload.
		"-fflags", "+genpts",
		"-f", "concat", "-safe", "0", "-i", "file:"+listPath,
		"-map", "0",
		"-c", "copy",
	)
	return append(args, RelayProducerOutput(outURL)...)
}
