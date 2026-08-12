// Package uploadprobe turns ONE ffmpeg.ProbeFile call into exactly one
// uploads.Verdict.
//
// WHY THIS IS A PACKAGE AND NOT A THIRD COPY. The classification existed twice
// before #202 -- api.probeUpload, which had to answer an HTTP request, and
// playlistmedia.verifySource, which had to fail a job -- and the re-verify
// worker needed it a third time. Three copies of "which of these ffprobe
// failures is a statement about the FILE and which is a statement about this
// SERVER" is three chances to get the fail-open default backwards, and getting
// it backwards is how a GIF gets stored, 201, unchecked (measured; see
// api/media.go). So the rule lives here once and api.probeUpload calls it.
//
// playlistmedia.verifySource is deliberately NOT converted. It answers a
// different question -- "may this file be transcoded for air", with its own
// per-arm remedies and its own jobs.Permanent wrapping -- and folding two
// different questions into one function to save a switch is how the answers
// start disagreeing. What is shared here is the part that is genuinely one
// rule: which outcome an inspection had.
//
// THE THREE OUTCOMES, AND WHY THE THIRD IS NOT AN ERROR. Classify never returns
// an error, because every outcome of an inspection is a fact worth naming:
//
//   - OutcomeVerified: ffprobe read it and it is media.
//   - OutcomeRefused: ffprobe read it and it is not media this server takes.
//     A fact about the FILE. Permanent -- the same bytes earn the same answer.
//   - OutcomeUnverified: no inspection produced a verdict. A fact about this
//     SERVER or about the request that carried the bytes -- no ffprobe, a fork
//     that failed, a probe cut short. NOT a statement about the file, and a
//     caller that treats it as one is the defect this whole tier exists to
//     stop.
//
// The third one is the one callers must handle deliberately, and they handle it
// DIFFERENTLY. The upload handler records it, because "stored without being
// inspected" is the honest description of a file that has just arrived. The
// re-verify worker records NOTHING, because a file it was asked to re-inspect
// already has a record and replacing an established verdict with "nobody read
// this" would destroy knowledge the server had. See uploadverify.RunVerify.
package uploadprobe

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// Classify reports what one inspection established.
//
// res and probeErr are ffmpeg.ProbeFile's two returns. probeCtxErr is the error
// of the context the probe RAN UNDER, and passing it is not optional
// bookkeeping: exec.CommandContext kills its child when the context ends and
// what comes back is a plain *exec.ExitError carrying no context error at all,
// so errors.Is finds nothing and a cancelled probe is otherwise indistinguishable
// from ffprobe disagreeing with the file. Measured, in the shape that answered
// 400 "signal: killed" and deleted the operator's upload. Pass ctx.Err().
func Classify(res *ffmpeg.ProbeResult, probeErr error, probeCtxErr error) uploads.Verdict {
	if probeErr != nil {
		// Interruption first, because it is not a verdict about the file.
		if errors.Is(probeErr, context.Canceled) ||
			errors.Is(probeErr, context.DeadlineExceeded) ||
			probeCtxErr != nil {
			return uploads.UnverifiedVerdict(uploads.ReasonInterrupted)
		}
		// ASKED AS ONE QUESTION, not as a sequence of arms. ffmpeg.Refused owns
		// the list of "ffprobe ran and this is a verdict about the file",
		// because the default below is FAIL-OPEN and no caller has a way to
		// know when it has missed a case. Measured: with the
		// ErrUnsupportedContainer arm disabled, a GIF was not refused with a
		// worse message -- it was stored, 201, unchecked.
		if ffmpeg.Refused(probeErr) {
			switch {
			case errors.Is(probeErr, ffmpeg.ErrIndirectContainer):
				// Not "could not read": ffprobe read it fine and reported
				// another file's streams. Saying "could not be read as media"
				// here would be the false diagnosis all over again.
				return uploads.RefusedVerdict(
					"this file is a playlist or script naming other files, not media itself")
			case errors.Is(probeErr, ffmpeg.ErrUnsupportedContainer):
				// A REAL FILE IN A FORMAT WE DO NOT TAKE, which used to get the
				// sentence above. AIFF, DV, y4m, IVF, CAF and GIF were all
				// measured being told they were "a playlist or script naming
				// other files" -- untrue about every one of them, and
				// unactionable. If an operator is refused here for something
				// they should be able to upload, the fix is an entry in
				// ffmpeg.selfContainedFormats, not a widening of the gate.
				return uploads.RefusedVerdict(
					fmt.Sprintf("%s; re-save it as MP4 or MPEG-TS", probeErr))
			}
			// A refusal no arm here has a sentence of its own for. ffprobe's
			// words, and still a REFUSAL -- the point of routing through
			// Refused is that a new refusal shape lands here rather than in the
			// fail-open arm below.
			return uploads.RefusedVerdict(
				fmt.Sprintf("this file could not be read as media: %s", probeErr))
		}
		var exitErr *exec.ExitError
		if !errors.As(probeErr, &exitErr) {
			// FFPROBE NEVER RAN, OR RAN AND SAID SOMETHING THIS PROCESS CANNOT
			// READ. Both are facts about this server, and neither is a verdict
			// about the operator's bytes.
			//
			// It was reachable and it destroyed data: with the ffprobe path
			// pointing at something that does not exist, is not executable, or
			// is a directory, every upload got 400 "this file could not be read
			// as media: fork/exec ...: no such file or directory" and the
			// uploads directory was empty afterwards. exec's start failures are
			// *exec.Error and fs.ErrPermission, never *exec.ExitError, and
			// ctx.Err() is nil for them, so both of the other guards missed. A
			// fork that fails with EAGAIN on a box that is also encoding live
			// video reaches this without any misconfiguration at all.
			return uploads.UnverifiedVerdict(uploads.ReasonProbeUnusable)
		}
		// ffprobe's own words, because "could not read this file" tells the
		// operator nothing they can act on and ffprobe's message usually does.
		return uploads.RefusedVerdict(
			fmt.Sprintf("this file could not be read as media: %s", probeErr))
	}
	if res == nil {
		// A nil result with a nil error is a broken prober, not a broken file.
		// It cannot happen through ffmpeg.ProbeFile and it is reachable through
		// a seam a test supplies, so it is answered rather than dereferenced.
		return uploads.UnverifiedVerdict(uploads.ReasonProbeUnusable)
	}
	if res.Video == nil && len(res.Audio) == 0 {
		// ffprobe parsed it and found nothing playable. A container with no
		// streams is the shape a renamed archive or document arrives in.
		return uploads.RefusedVerdict("this file carries no video or audio stream")
	}
	return uploads.VerifiedVerdict(MediaInfo(res))
}

// MediaInfo is the listing's view of a probe result.
func MediaInfo(res *ffmpeg.ProbeResult) uploads.MediaInfo {
	info := uploads.MediaInfo{
		DurationSeconds: res.DurationSeconds,
		// Carried through rather than dropped, because this is the field an
		// OPERATOR sees. A counted duration and a declared one look identical
		// as numbers and are not the same claim -- see ffmpeg.DurationSource --
		// and the Library is where somebody deciding whether to schedule this
		// item is looking at it.
		DurationSource: string(res.DurationSource),
		AudioTracks:    len(res.Audio),
		ProbedAt:       time.Now().UTC(),
	}
	if res.Video != nil {
		info.VideoCodec = res.Video.Codec
		info.Width = res.Video.Width
		info.Height = res.Video.Height
		info.FrameRate = res.Video.FrameRate
	}
	if len(res.Audio) > 0 {
		// The first track's shape, which is what a listing has room for. The
		// count above is the number that matters for routing; per-track detail
		// belongs on a detail view, not in a table row.
		info.AudioCodec = res.Audio[0].Codec
		info.AudioChannels = res.Audio[0].Channels
		info.AudioLayout = res.Audio[0].Layout
	}
	return info
}
