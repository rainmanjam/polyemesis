package engine

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// slateTools is an FFmpeg whose build registers the encoder but whose test
// encode for it failed -- the ordinary shape of a box with an NVIDIA driver
// problem, and the one renditionEncoderProblem refuses a rendition for.
func slateTools(t *testing.T, works bool) *ffmpeg.Tools {
	t.Helper()
	cap := ffmpeg.EncoderCapability{
		Name:   string(db.EncoderNVENCH264),
		Vendor: ffmpeg.EncoderVendorOf(string(db.EncoderNVENCH264)),
		Works:  works,
	}
	if !works {
		cap.Reason = "no NVENC capable devices found"
	}
	return &ffmpeg.Tools{
		FFmpeg: "polyemesis-no-such-binary", Version: "7.1", Major: 7, Minor: 1,
		VideoEncoders: []string{string(db.EncoderX264), string(db.EncoderNVENCH264)},
		EncoderCaps: []ffmpeg.EncoderCapability{
			{Name: string(db.EncoderX264), Vendor: ffmpeg.EncoderVendorOf(string(db.EncoderX264)), Works: true},
			cap,
		},
	}
}

// The standby source FAILS OPEN on its encoder, and this is the one place in
// the pipeline where that matters most: the slate exists to start when
// everything else has already failed, so an encoder the build cannot vouch for
// has to cost a fallback to software rather than a refusal to build a command.
//
// A rendition refuses in the same situation and is right to -- the operator
// asked for that encode and would rather be told. The slate was not asked for
// by anybody; it is what an operator gets INSTEAD of dead air.
//
// Nothing covered this. The acceptance suite runs the colour slate with no
// encoder configured, so the branch is never entered there either.
//
// Mutation: selector.go:1695, replace the
// `if err := renditionEncoderProblem(...); err != nil { fallback = ... } else { ... }`
// with the unconditional `spec.Encoder = string(sl.Encoder)`.
// Observed to fail on both the command-line assertion and the operator warning,
// and to be the only failing test in the package.
func TestAnUnusableSlateEncoderFallsBackToSoftwareRatherThanRefusing(t *testing.T) {
	out := "udp://127.0.0.1:21999"

	// Positive first, on tools where the encoder DOES pass its test encode: the
	// operator's choice is honoured and reaches the command line. Without this
	// the assertions below would pass against a slateSpec that had stopped
	// setting an encoder at all.
	good := failoverEngine(t)
	good.tools = slateTools(t, true)
	s := failoverOnSettings()
	s.Failover.Slate.Encoder = db.EncoderNVENCH264
	setSettings(good, s)

	spec, fallback := good.slateSpec(s, out, 0)
	if fallback != "" {
		t.Fatalf("the configured encoder passed its test encode and the slate fell back anyway: %s", fallback)
	}
	if args := ffmpeg.SlateArgs(spec); !slices.Contains(args, string(db.EncoderNVENCH264)) {
		t.Fatalf("the slate command does not name the working encoder the operator chose: %v", args)
	}

	// The same settings on a box where the encoder does not work.
	bad := failoverEngine(t)
	bad.tools = slateTools(t, false)
	logs := &syncBuffer{}
	bad.log = slog.New(slog.NewTextHandler(logs, nil))
	setSettings(bad, s)

	spec, fallback = bad.slateSpec(s, out, 0)
	args := ffmpeg.SlateArgs(spec)
	if slices.Contains(args, string(db.EncoderNVENCH264)) {
		t.Errorf("the standby's command names %s on a build whose test encode for it failed: "+
			"%v. The slate is what an operator gets instead of dead air, so an encoder "+
			"that cannot start must cost a fallback to software, not a source that never "+
			"comes up", db.EncoderNVENCH264, args)
	}
	if !slices.Contains(args, string(ffmpeg.EncoderX264)) {
		t.Errorf("the standby fell back to no encoder the software build has: %v", args)
	}

	// And the operator is told, through the caller that actually builds the
	// standby, so this cannot pass while startFeed ignores what slateSpec says.
	if fallback == "" {
		t.Fatal("the slate fell back to software and reported no reason, so nothing tells " +
			"the operator their chosen encoder is not being used")
	}
	bad.reconcileSelector(s, wantSelector(s), "")
	bad.selMu.Lock()
	f := bad.startFeed(s, sourceSlate, "sig", "", time.Now())
	bad.selMu.Unlock()
	if f != nil {
		t.Cleanup(func() { bad.teardownFeed(f) })
	}
	if !strings.Contains(logs.String(), "slate encoder unusable") {
		t.Errorf("starting the standby with an unusable encoder logged no warning; the "+
			"operator's dashboard shows %s and the process is running software",
			db.EncoderNVENCH264)
	}
}
