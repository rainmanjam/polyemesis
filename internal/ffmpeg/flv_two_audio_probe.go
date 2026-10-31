package ffmpeg

import (
	"context"
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

func (t *Tools) probeTwoAudioFLV(ctx context.Context) FLVTwoAudioResult {
	dir, err := os.MkdirTemp("", "polyemesis-flv2a-")
	if err != nil {
		return FLVTwoAudioResult{OK: false, Reason: "could not create a temporary directory for the two-track FLV probe: " + err.Error()}
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "probe.flv")

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	mux := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=0.5:sample_rate=48000",
		"-map", "0:a", "-map", "1:a",
		"-c:a", "aac", "-b:a", "64k", "-ar", "48000",
		"-f", "flv", out,
	}
	if b, err := exec.CommandContext(ctx, t.FFmpeg, mux...).CombinedOutput(); err != nil {
		return FLVTwoAudioResult{OK: false, Reason: fmt.Sprintf("FFmpeg could not write a two-track FLV: %v: %s", err, truncate(string(b), 300))}
	}

	// ProbeFile, not Probe: Probe is for a live relay URL and appends its
	// fifo query string, which a file path cannot carry.
	probe, err := ProbeFile(ctx, Bins{FFprobe: t.FFprobe}, out)
	if err != nil {
		return FLVTwoAudioResult{OK: false, Reason: "ffprobe could not read the two-track FLV the probe wrote: " + err.Error()}
	}
	return twoAudioFLVVerdict(probe.Audio)
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
