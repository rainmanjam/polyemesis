package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// FLVTwoAudioResult is the answer to "can this FFmpeg write two audio tracks into
// an FLV that the second one survives?". It is what the VOD track needs from the
// publisher, measured rather than inferred from a version number.
type FLVTwoAudioResult struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
	// Inconclusive is set when the probe could not ask its question at all: no
	// temporary directory, FFmpeg or ffprobe would not start or ran out of time.
	// Such a probe is recorded with OK true, so the second mix is KEPT. Only an
	// answer, a real refusal by this FFmpeg, withholds it. See probeTwoAudioFLV.
	Inconclusive bool `json:"inconclusive,omitempty"`
}

// flvProbeTimeout bounds the whole probe: writing the tones, then reading them
// back. A variable so a test can exercise the expiry without waiting for it.
var flvProbeTimeout = 20 * time.Second

// flvInconclusive is the record for a probe that could not finish. OK is true on
// purpose: an unknown answer must not withhold a working VOD mix. The reason
// carries the cause so the record says what was not checked.
func flvInconclusive(cause string) FLVTwoAudioResult {
	return FLVTwoAudioResult{
		OK:           true,
		Inconclusive: true,
		Reason:       "probe inconclusive: " + cause + "; the second (VOD) mix is kept",
	}
}

// twoAudioFLVVerdict decides the probe from the audio streams ffprobe read back
// out of the probe's own output. Two streams is the only passing shape: one is
// the failure the repo's evidence describes (a build that writes the FLV but
// drops the second track), and zero means the file was not written at all.
//
// It is pure so the decision can be tested without a binary.
func twoAudioFLVVerdict(audio []AudioStream) FLVTwoAudioResult {
	if len(audio) == 2 {
		return FLVTwoAudioResult{OK: true}
	}
	return FLVTwoAudioResult{
		OK: false,
		Reason: fmt.Sprintf("this FFmpeg wrote %d audio stream(s) into a two-track FLV, not 2, so a second "+
			"audio track cannot reach an RTMP ingest through it; install an FFmpeg 8.1 or newer", len(audio)),
	}
}

// ProbeTwoAudioFLV muxes two short tones into a temporary FLV with this
// binary's own FFmpeg, reads the file back with ffprobe, and records the verdict
// on the Tools. It is the behavioural check the VOD track's gate reads: the same
// muxer and the same two-audio map that the destination argv uses, on a file
// that is thrown away.
//
// Any failure to run the probe is itself a recorded answer, not a panic and not
// a silent pass. The caller decides what a failed probe means; the engine drops
// only the second mix and never the broadcast.
func (t *Tools) ProbeTwoAudioFLV(ctx context.Context) FLVTwoAudioResult {
	res := t.probeTwoAudioFLV(ctx)
	t.RecordTwoAudioFLV(res)
	return res
}

// probeTwoAudioFLV asks the question, and sorts every way of not getting an
// answer apart from a refusal.
//
// FAIL OPEN, FAIL CLOSED. A refusal is an answer: this FFmpeg ran to its own end
// and either exited with an error about its muxer, or wrote a file whose audio
// streams are not two. Only a refusal withholds the second mix. Everything else
// is INCONCLUSIVE and keeps it: no temporary directory, a binary that will not
// start, a deadline that passes (the child is killed, which also reads as an
// *exec.ExitError, so the deadline is checked first), an ffprobe that cannot read
// its own output. Treating those as refusals made a busy or stripped machine lose
// a VOD track it had never been shown to lack.
func (t *Tools) probeTwoAudioFLV(ctx context.Context) FLVTwoAudioResult {
	dir, err := os.MkdirTemp("", "polyemesis-flv2a-")
	if err != nil {
		return flvInconclusive("could not create a temporary directory for the two-track FLV probe: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "probe.flv")

	ctx, cancel := context.WithTimeout(ctx, flvProbeTimeout)
	defer cancel()
	mux := flvProbeMuxArgs(out)
	if b, err := exec.CommandContext(ctx, t.FFmpeg, mux...).CombinedOutput(); err != nil {
		return classifyMuxFailure(ctx, err, b)
	}

	// ProbeFile, not Probe: Probe is for a live relay URL and appends its
	// fifo query string, which a file path cannot carry.
	probe, err := ProbeFile(ctx, Bins{FFprobe: t.FFprobe}, out)
	if err != nil {
		return flvInconclusive("ffprobe could not read the two-track FLV the probe wrote: " + err.Error())
	}
	return twoAudioFLVVerdict(probe.Audio)
}

// classifyMuxFailure decides what a failed FFmpeg run means for the verdict.
// Pure in its inputs, so the sort is testable without a binary.
//
// The order is the point. A context that has expired is checked first, because
// exec kills the child at the deadline and the kill arrives as an *exec.ExitError
// with no positive exit code, which would otherwise look like a refusal. Only a
// child that exited on its own with a non-zero code has refused.
func classifyMuxFailure(ctx context.Context, err error, output []byte) FLVTwoAudioResult {
	if ctx.Err() != nil {
		return flvInconclusive("FFmpeg timed out before it finished writing the probe")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() <= 0 {
		// Not an exit at all: the binary is missing or not executable, or it was
		// killed by a signal (ExitCode -1), which says nothing about its muxer.
		return flvInconclusive("FFmpeg did not run to completion: " + err.Error())
	}
	return FLVTwoAudioResult{OK: false, Reason: fmt.Sprintf("FFmpeg could not write a two-track FLV: %v: %s", err, truncate(string(output), 300))}
}

// RecordTwoAudioFLV stores a verdict on the Tools. ProbeTwoAudioFLV is the only
// production caller; it exists separately so the engine's gate can be exercised
// with a known answer.
func (t *Tools) RecordTwoAudioFLV(r FLVTwoAudioResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rr := r
	t.flvTwoAudio = &rr
}

// TwoAudioFLV reports the recorded verdict. probed is false when the probe has
// never run, in which case ok is true: an unknown answer must not withhold a VOD
// mix that would work. reason is set only when a probe ran and failed.
func (t *Tools) TwoAudioFLV() (ok, probed bool, reason string) {
	if t == nil {
		return true, false, ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.flvTwoAudio == nil {
		return true, false, ""
	}
	return t.flvTwoAudio.OK, true, t.flvTwoAudio.Reason
}

// flvProbeMuxArgs is the command that writes the two-audio-track FLV the probe
// then reads back.
func flvProbeMuxArgs(out string) []string {
	return []string{
		flagHideBanner, "-loglevel", LogLevel(LogError), "-nostdin",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=0.5:sample_rate=48000",
		"-map", "0:a", "-map", "1:a",
		"-c:a", "aac", "-b:a", "64k", "-ar", "48000",
		"-f", "flv", out,
	}
}
