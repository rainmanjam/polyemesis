package uploadprobe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// TestHelperExitsNonZero is not a test. It is the child process exitError runs,
// and it returns immediately unless it is the one being spawned.
//
// THE TEST BINARY RE-EXECUTING ITSELF is os/exec's own idiom for this, and the
// reason it is used here rather than a command name is portability: there is no
// `false` on Windows, so `exec.Command("false")` fails to START -- an
// *exec.Error, which is the OPPOSITE of the case this helper exists to produce,
// and the guard below would have turned the whole table red on that platform
// for a reason having nothing to do with the code under test.
func TestHelperExitsNonZero(t *testing.T) {
	if os.Getenv("POLYEMESIS_HELPER_EXIT") != "1" {
		return
	}
	os.Exit(3)
}

// exitError produces a real *exec.ExitError, which is the shape "ffprobe ran and
// exited non-zero" arrives in. Constructed by running a process that really does
// exit non-zero rather than by faking one, because the arm under test is
// errors.As against a concrete type and a hand-rolled stand-in would not
// exercise it.
func exitError(t *testing.T) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperExitsNonZero$")
	cmd.Env = append(os.Environ(), "POLYEMESIS_HELPER_EXIT=1")
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("the helper process did not produce an *exec.ExitError: %T %v", err, err)
	}
	return err
}

// wantExitError marks a table row that needs the real *exec.ExitError, which
// cannot be built at table-literal time because it needs a *testing.T.
var wantExitError = errors.New("substitute the real exec.ExitError here")

func playable() *ffmpeg.ProbeResult {
	return &ffmpeg.ProbeResult{
		Video:           &ffmpeg.VideoStream{Codec: "h264", Width: 1920, Height: 1080, FrameRate: 30},
		Audio:           []ffmpeg.AudioStream{{Codec: "aac", Channels: 2, Layout: "stereo"}},
		DurationSeconds: 12.5,
		DurationSource:  ffmpeg.DurationSource("declared"),
	}
}

// THE WHOLE TABLE, and the property each row pins is which of the three
// OUTCOMES an inspection had -- not the wording. The distinction that matters is
// refused (a fact about the FILE, permanent) versus unverified (a fact about
// this SERVER, and never a reason to overwrite a real verdict). Getting one of
// these backwards is how a GIF gets stored 201 unchecked, measured, and it is
// the reason this classification is one function instead of three copies.
func TestClassifySeparatesFactsAboutTheFileFromFactsAboutTheServer(t *testing.T) {
	cases := []struct {
		name    string
		res     *ffmpeg.ProbeResult
		err     error
		ctxErr  error
		want    uploads.Outcome
		because string
	}{
		{
			name: "a playable file is verified",
			res:  playable(), want: uploads.OutcomeVerified,
			because: "ffprobe read it and found streams",
		},
		{
			name: "a container naming other files is refused",
			err:  fmt.Errorf("probe: %w", ffmpeg.ErrIndirectContainer),
			want: uploads.OutcomeRefused,
			because: "ffprobe read it fine and reported another file's streams; " +
				"this is the ffconcat amplification shape",
		},
		{
			name: "a container we do not take is refused",
			err:  fmt.Errorf("probe: %w", ffmpeg.ErrUnsupportedContainer),
			want: uploads.OutcomeRefused,
			because: "a real file in a format the allowlist does not carry -- " +
				"AIFF, DV, y4m, IVF, CAF, GIF",
		},
		{
			name: "a file whose length cannot be established is refused",
			err:  fmt.Errorf("probe: %w", ffmpeg.ErrNoDuration),
			want: uploads.OutcomeRefused,
			because: "ffmpeg.Refused names it, and the point of asking Refused " +
				"rather than listing arms is that this one needs no arm here",
		},
		{
			name: "a file ffprobe describes at unreadable length is refused",
			err:  fmt.Errorf("probe: %w", ffmpeg.ErrProbeTooVerbose),
			want: uploads.OutcomeRefused,
			because: "same: a refusal shape with no arm of its own must still " +
				"land on the refused side of the fail-open default",
		},
		{
			name: "a container with no streams is refused",
			res:  &ffmpeg.ProbeResult{FormatName: "mov,mp4"},
			want: uploads.OutcomeRefused,
			because: "ffprobe parsed it and found nothing playable, which is the " +
				"shape a renamed archive or document arrives in",
		},
		{
			name:    "ffprobe exiting non-zero is refused",
			err:     wantExitError,
			want:    uploads.OutcomeRefused,
			because: "the binary ran and disagreed, which is a verdict about the file",
		},
		{
			name:    "a cancelled probe establishes nothing",
			err:     context.Canceled,
			want:    uploads.OutcomeUnverified,
			because: "a probe that was cut short did not disagree with anything",
		},
		{
			name:    "a probe whose deadline expired establishes nothing",
			err:     context.DeadlineExceeded,
			want:    uploads.OutcomeUnverified,
			because: "same, by the other route",
		},
		{
			name: "a killed child whose error carries no context establishes nothing",
			// THE CLAUSE THAT ACTUALLY WORKS. exec.CommandContext kills the
			// child and returns a bare *exec.ExitError with no context error in
			// the chain, so errors.Is finds nothing -- this row is the one that
			// fails if ctxErr stops being consulted, and without it a client
			// disconnect is indistinguishable from ffprobe refusing the file.
			err: wantExitError, ctxErr: context.Canceled,
			want:    uploads.OutcomeUnverified,
			because: "the context we handed the probe is done, so the exit is the kill",
		},
		{
			name: "a binary that could not be started establishes nothing",
			err:  &exec.Error{Name: "ffprobe", Err: errors.New("no such file or directory")},
			want: uploads.OutcomeUnverified,
			because: "exec's start failures are *exec.Error, never *exec.ExitError, " +
				"and this is a fact about the server, not the bytes",
		},
		{
			name: "output that is not JSON establishes nothing",
			err:  errors.New("parse ffprobe output: invalid character 'o'"),
			want: uploads.OutcomeUnverified,
			because: "a sentence about the binary this server was pointed at, " +
				"which used to be reported as a verdict about a file",
		},
		{
			name: "a prober that returns nothing at all establishes nothing",
			want: uploads.OutcomeUnverified,
			because: "a nil result with a nil error is a broken prober, and " +
				"answering it beats dereferencing it",
		},
	}

	real := exitError(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.err
			if errors.Is(err, wantExitError) {
				err = real
			}
			got := Classify(c.res, err, c.ctxErr)
			if got.Outcome != c.want {
				t.Fatalf("Classify(%v) recorded %q, want %q -- %s.\n"+
					"An outcome on the wrong side of this line is not a wording bug: "+
					"%q is acted on as permanent and %q as a failure to look, and the "+
					"re-verify worker writes the first and refuses to write the second.",
					err, got.Outcome, c.want, c.because,
					uploads.OutcomeRefused, uploads.OutcomeUnverified)
			}
			// A refusal with no sentence is not storable -- PutVerdict rejects
			// it -- so an outcome without its reason is a verdict that cannot be
			// recorded. A test that only checked the outcome passed here while
			// the message said "<nil>".
			if got.Outcome == uploads.OutcomeRefused && got.Reason == "" {
				t.Errorf("refused with an empty reason; uploads.Store.PutVerdict " +
					"refuses that, so this verdict could never be written down")
			}
			if got.Outcome == uploads.OutcomeUnverified && got.Reason == "" {
				t.Errorf("unverified with an empty reason, so nothing tells the " +
					"operator whether the probe was cut short or could not run")
			}
		})
	}
}

// The two unverified reasons are DIFFERENT, and the difference is the only thing
// distinguishing "the client went away" from "this server cannot run its
// inspection". api.probeUpload logs a different warning for each and the
// operator-facing sentences differ; collapsing them would make one of the two
// logs unreachable.
func TestTheTwoWaysAnInspectionFailsAreNotTheSameReason(t *testing.T) {
	cut := Classify(nil, context.Canceled, nil)
	broken := Classify(nil, &exec.Error{Name: "ffprobe", Err: errors.New("nope")}, nil)
	if cut.Reason != uploads.ReasonInterrupted {
		t.Errorf("a cut-short probe reported %q, want %q", cut.Reason, uploads.ReasonInterrupted)
	}
	if broken.Reason != uploads.ReasonProbeUnusable {
		t.Errorf("an unrunnable probe reported %q, want %q", broken.Reason, uploads.ReasonProbeUnusable)
	}
	if cut.Reason == broken.Reason {
		t.Fatal("a probe that was interrupted and a probe that could not run " +
			"report the same reason, so nothing downstream can tell an operator " +
			"whether to retry or to install FFmpeg")
	}
}

// The refusal WORDING is load-bearing in exactly one place: a file naming other
// files must not be told it "could not be read", because ffprobe read it
// perfectly well and the operator then goes looking for a corruption that is not
// there. That false diagnosis shipped once.
func TestAnIndirectContainerIsNotDescribedAsUnreadable(t *testing.T) {
	v := Classify(nil, fmt.Errorf("probe: %w", ffmpeg.ErrIndirectContainer), nil)
	if v.Outcome != uploads.OutcomeRefused {
		t.Fatalf("outcome %q, want refused", v.Outcome)
	}
	if !strings.Contains(v.Reason, "playlist or script naming other files") {
		t.Errorf("reason is %q; it must say the file names other files. "+
			"'could not be read as media' is the false diagnosis this arm exists "+
			"to remove -- ffprobe read it fine.", v.Reason)
	}
	if strings.Contains(v.Reason, "could not be read") {
		t.Errorf("reason is %q, which tells the operator to go looking for a "+
			"truncation that does not exist", v.Reason)
	}
}

// MediaInfo is what the Library row is built from, and the field that matters
// most is the track COUNT: routing is per track, so selecting track 3 of a file
// that carries one is silence on air.
func TestMediaInfoCarriesWhatTheLibraryRowShows(t *testing.T) {
	res := playable()
	res.Audio = append(res.Audio, ffmpeg.AudioStream{Codec: "aac", Channels: 1, Layout: "mono"})
	got := MediaInfo(res)
	if got.AudioTracks != 2 {
		t.Errorf("AudioTracks = %d, want 2", got.AudioTracks)
	}
	if got.VideoCodec != "h264" || got.Width != 1920 || got.Height != 1080 {
		t.Errorf("video shape lost: %+v", got)
	}
	if got.AudioLayout != "stereo" {
		t.Errorf("AudioLayout = %q, want the FIRST track's layout", got.AudioLayout)
	}
	if got.DurationSource != "declared" {
		t.Errorf("DurationSource = %q; a counted duration and a declared one are "+
			"not the same claim and the provenance must survive", got.DurationSource)
	}
	if got.ProbedAt.IsZero() {
		t.Error("ProbedAt is zero, so nothing records when this file was read")
	}
}
