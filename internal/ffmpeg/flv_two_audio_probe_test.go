package ffmpeg

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The VOD track is a second audio track on an FLV (RTMP) publish. That only
// reaches a platform if the FFmpeg that writes it really puts two audio streams
// into the FLV, which is not something a version number proves: the repo's own
// evidence says FFmpeg 6.1.1 cannot write multitrack FLV at all, and a version
// floor cannot see a distro build that was patched differently. So the answer is
// a behavioural probe, cached on the Tools, and these tests pin the parts that
// decide it without needing a binary.

// A probe that found one audio stream in its own output is a failure, and the
// reason has to say what was seen so an operator can tell a broken build from a
// probe that read the wrong file.
func TestAFLVWithOneAudioStreamFailsTheTwoAudioProbeAndSaysWhatWasSeen(t *testing.T) {
	got := twoAudioFLVVerdict([]AudioStream{{Index: 0, Codec: "aac", Channels: 2}})
	if got.OK {
		t.Fatalf("one audio stream passed the two-track probe: %+v", got)
	}
	if !strings.Contains(got.Reason, "1 audio") {
		t.Errorf("the reason does not say one audio stream was found: %q", got.Reason)
	}
}

// Two audio streams is the one answer that passes.
func TestAFLVWithTwoAudioStreamsPassesTheTwoAudioProbe(t *testing.T) {
	got := twoAudioFLVVerdict([]AudioStream{
		{Index: 0, Codec: "aac", Channels: 2},
		{Index: 1, Codec: "aac", Channels: 2},
	})
	if !got.OK {
		t.Fatalf("two audio streams failed the probe: %+v", got)
	}
}

// Never probed means the answer is unknown, and unknown must not drop a working
// VOD track. Only a probe that ran and failed may withhold the second mix. This
// is the same "assume the best until measured" rule the encoder and filter
// lists already follow.
func TestAToolsThatWereNeverProbedAssumeTwoAudioFLVWorks(t *testing.T) {
	var tools Tools
	ok, probed, reason := tools.TwoAudioFLV()
	if probed {
		t.Fatalf("a Tools that never ran the probe reports it probed (reason %q)", reason)
	}
	if !ok {
		t.Errorf("an unprobed Tools withholds the second track; unknown must not drop a working VOD mix")
	}
}

// A recorded failure is what the engine reads, with the reason it shows the
// operator. The engine's gate reads only this accessor, so it is the seam the
// planning test fakes through.
func TestARecordedTwoAudioFLVFailureIsReadBackWithItsReason(t *testing.T) {
	var tools Tools
	tools.RecordTwoAudioFLV(FLVTwoAudioResult{OK: false, Reason: "FFmpeg wrote 1 audio stream"})
	ok, probed, reason := tools.TwoAudioFLV()
	if !probed || ok {
		t.Fatalf("recorded failure read back as probed=%v ok=%v", probed, ok)
	}
	if reason != "FFmpeg wrote 1 audio stream" {
		t.Errorf("reason = %q, want the recorded one", reason)
	}
}

// The real binary, end to end: the probe muxes two tones into a temp FLV and
// reads it back. Skipped where no FFmpeg is installed so CI without one still
// runs the verdict tests above. The binary lookup goes through the sanctioned
// gate, so a CI that has undertaken to install FFmpeg fails instead of skipping.
func TestTheRealFFmpegPassesTheTwoAudioFLVProbe(t *testing.T) {
	ff := testenv.FFmpegBinary(t, "ffmpeg", "no ffmpeg on PATH")
	fp := testenv.FFmpegBinary(t, "ffprobe", "no ffprobe on PATH")
	tools := &Tools{FFmpeg: ff, FFprobe: fp}
	res := tools.ProbeTwoAudioFLV(context.Background())
	if !res.OK {
		t.Fatalf("the installed FFmpeg cannot write two audio tracks to FLV: %s", res.Reason)
	}
	ok, probed, _ := tools.TwoAudioFLV()
	if !probed || !ok {
		t.Errorf("the probe ran but the Tools did not keep its answer: probed=%v ok=%v", probed, ok)
	}
}

// THE PROBE FAILS OPEN WHEN IT CANNOT RUN. A tempdir that does not exist, a
// binary that will not start and a deadline that passes are all reasons the
// question could not be asked, not answers to it. Each must keep the second
// mix, because a broken probe must not silently withhold a VOD track that would
// have worked. The verdict says it was inconclusive so the record is honest.
//
// MUTATION: in probeTwoAudioFLV, return OK false from the MkdirTemp error arm.
// Observed: FAIL, "withheld the second mix when the tempdir was missing".
func TestAFLVProbeFailsOpenWhenTheTemporaryDirectoryCannotBeCreated(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "no-such-dir"))
	tools := &Tools{FFmpeg: "/bin/sh", FFprobe: "/bin/sh"}
	got := tools.ProbeTwoAudioFLV(context.Background())
	if !got.OK || !got.Inconclusive {
		t.Fatalf("a missing tempdir was read as a refusal: %+v", got)
	}
	if !strings.Contains(got.Reason, "probe inconclusive") {
		t.Errorf("the record does not say the probe was inconclusive: %q", got.Reason)
	}
}

// MUTATION: in probeTwoAudioFLV, return OK false from the start-failure arm.
// Observed: FAIL, "withheld the second mix when FFmpeg would not start".
func TestAFLVProbeFailsOpenWhenFFmpegCannotBeStarted(t *testing.T) {
	tools := &Tools{FFmpeg: filepath.Join(t.TempDir(), "no-such-ffmpeg"), FFprobe: "/bin/sh"}
	got := tools.ProbeTwoAudioFLV(context.Background())
	if !got.OK || !got.Inconclusive {
		t.Fatalf("a binary that would not start was read as a refusal: %+v", got)
	}
}

// A deadline that passes while FFmpeg is still running is a timeout, not a
// refusal. The fake FFmpeg sleeps far longer than the caller allows; exec
// replaces the shell so the kill reaches the sleep itself.
//
// MUTATION: in probeTwoAudioFLV, drop the ctx.Err() check before the exit-code
// classification. Observed: FAIL, the killed child read as "FFmpeg refused".
func TestAFLVProbeFailsOpenWhenTheDeadlinePassesBeforeFFmpegFinishes(t *testing.T) {
	ff := fakeFFmpeg(t, `"") exec sleep 30 ;;`)
	tools := &Tools{FFmpeg: ff, FFprobe: "/bin/sh"}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got := tools.ProbeTwoAudioFLV(ctx)
	if !got.OK || !got.Inconclusive {
		t.Fatalf("a timed-out probe was read as a refusal: %+v", got)
	}
	if !strings.Contains(got.Reason, "timed out") {
		t.Errorf("the reason does not say the probe timed out: %q", got.Reason)
	}
}

// THE PROBE FAILS CLOSED ON A REAL REFUSAL. FFmpeg that ran to its own end and
// exited with an error about its muxer is the failure the VOD track must not
// walk into: the second stream would be silently lost. That still withholds the
// second mix, and the reason is the exit, not a guess.
//
// MUTATION: in probeTwoAudioFLV, classify every mux failure as inconclusive.
// Observed: FAIL, "a refusal passed the probe".
func TestAFLVProbeFailsClosedWhenFFmpegRefusesToWriteTheFile(t *testing.T) {
	ff := fakeFFmpeg(t, `"") echo 'muxer refused a second audio stream' >&2; exit 1 ;;`)
	tools := &Tools{FFmpeg: ff, FFprobe: "/bin/sh"}
	got := tools.ProbeTwoAudioFLV(context.Background())
	if got.OK || got.Inconclusive {
		t.Fatalf("a real refusal did not withhold the second mix: %+v", got)
	}
	if !strings.Contains(got.Reason, "could not write a two-track FLV") {
		t.Errorf("the reason does not say FFmpeg refused: %q", got.Reason)
	}
}

// An inconclusive verdict is what the engine reads back. It must not be a
// refusal, or the planning gate would drop the mix the probe could not judge.
func TestARecordedInconclusiveProbeIsNotARefusal(t *testing.T) {
	var tools Tools
	tools.RecordTwoAudioFLV(FLVTwoAudioResult{OK: true, Inconclusive: true, Reason: "probe inconclusive: timed out"})
	ok, probed, _ := tools.TwoAudioFLV()
	if !probed || !ok {
		t.Fatalf("an inconclusive probe read back as a refusal: probed=%v ok=%v", probed, ok)
	}
}
