package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// The four ways this warning could be wrong are all worse than not having it:
// warning on H.264 (noise, and noise trains an operator to ignore the row that
// matters), warning where a rendition re-encodes (false alarm about a codec
// that never reaches the platform), warning on an unprobed ingest (a guess
// dressed as a measurement), and staying silent on AV1 because someone
// blocklisted HEVC. Each has a test.

func renditionRef(id int64) *int64 { return &id }

func TestNoWarningForH264Passthrough(t *testing.T) {
	// The overwhelmingly common case. If this fires, every destination on every
	// normal install grows an amber row and the feature is worse than nothing.
	got := passthroughCodecWarning(db.DestRTMP, db.PlatformTwitch, nil, &ffmpeg.VideoStream{Codec: "h264"})
	if got != "" {
		t.Errorf("warned on plain H.264 passthrough: %q", got)
	}
}

func TestWarnsOnHEVCPassthroughToRTMP(t *testing.T) {
	got := passthroughCodecWarning(db.DestRTMP, db.PlatformTwitch, nil, &ffmpeg.VideoStream{Codec: "hevc"})
	if got == "" {
		t.Fatal("no warning for an HEVC ingest copied to an RTMP destination")
	}
	if !strings.Contains(got, "hevc") {
		t.Errorf("warning does not name the codec, so the operator cannot act on it: %q", got)
	}
	// Twitch has no sourced refusal in this tree, so the claim must stay hedged.
	if !strings.Contains(got, "may be rejected") {
		t.Errorf("unsourced platform stated as certain: %q", got)
	}
}

func TestWarnsOnAV1Too(t *testing.T) {
	// The match is on h264, not a blocklist of {hevc, av1}. A blocklist would
	// silently bless whatever Enhanced RTMP maps next.
	got := passthroughCodecWarning(db.DestRTMP, db.PlatformTwitch, nil, &ffmpeg.VideoStream{Codec: "av1"})
	if got == "" {
		t.Error("no warning for AV1: the codec check must be an allowlist of h264")
	}
}

func TestWarnsOnACodecNobodyHasHeardOf(t *testing.T) {
	// The point of the allowlist, stated as a test: a future codec_name we have
	// never seen must warn rather than pass.
	got := passthroughCodecWarning(db.DestRTMP, db.PlatformTwitch, nil, &ffmpeg.VideoStream{Codec: "vvc"})
	if got == "" {
		t.Error("an unrecognised codec passed silently; the allowlist has become a blocklist")
	}
}

func TestKickIsNamedWithCertaintyBecauseItIsSourced(t *testing.T) {
	// internal/db/platforms.go cites Kick's help page, checked 2026-08-06:
	// "H.264 only -- Kick refuses H.265". Where the evidence reaches, say so.
	got := passthroughCodecWarning(db.DestRTMP, db.PlatformKick, nil, &ffmpeg.VideoStream{Codec: "hevc"})
	if !strings.Contains(got, "will be rejected") {
		t.Errorf("Kick/HEVC is sourced and should be stated as certain: %q", got)
	}
	if strings.Contains(got, "may be rejected") {
		t.Errorf("hedged a claim we have evidence for: %q", got)
	}
}

func TestKickAV1StaysHedged(t *testing.T) {
	// Kick's page documents H.265 and says nothing about AV1. The certain
	// wording must not leak onto a claim the source does not make.
	got := passthroughCodecWarning(db.DestRTMP, db.PlatformKick, nil, &ffmpeg.VideoStream{Codec: "av1"})
	if got == "" {
		t.Fatal("no warning for AV1 on Kick")
	}
	if strings.Contains(got, "will be rejected") {
		t.Errorf("stated AV1 on Kick as certain; the cited page covers H.265 only: %q", got)
	}
}

func TestSilentWhenARenditionReEncodes(t *testing.T) {
	// A rendition produces its own bitstream, so the ingest's codec never
	// reaches the platform. Warning here would be a false alarm about a problem
	// the operator has already solved.
	got := passthroughCodecWarning(db.DestRTMP, db.PlatformTwitch, renditionRef(7), &ffmpeg.VideoStream{Codec: "hevc"})
	if got != "" {
		t.Errorf("warned about a destination that re-encodes: %q", got)
	}
}

func TestSilentOnNonRTMPTransports(t *testing.T) {
	// SRT, file and HLS carry HEVC without complaint. The hazard is RTMP's.
	for _, kind := range []db.DestKind{db.DestSRT, db.DestFile} {
		if got := passthroughCodecWarning(kind, db.PlatformCustom, nil, &ffmpeg.VideoStream{Codec: "hevc"}); got != "" {
			t.Errorf("%s: warned about a transport that accepts HEVC: %q", kind, got)
		}
	}
}

func TestFailsOpenWithoutAProbe(t *testing.T) {
	// `measured` gates destination start, but the probe can give up and the
	// silence tier starts destinations unmeasured. "Unknown" must never render
	// as a warning -- a guess dressed as a measurement is the thing this
	// codebase refuses everywhere else.
	if got := passthroughCodecWarning(db.DestRTMP, db.PlatformTwitch, nil, nil); got != "" {
		t.Errorf("warned with no probe at all: %q", got)
	}
	if got := passthroughCodecWarning(db.DestRTMP, db.PlatformTwitch, nil, &ffmpeg.VideoStream{Codec: ""}); got != "" {
		t.Errorf("warned on an empty codec string: %q", got)
	}
}

func TestTheWarningTellsTheOperatorWhatToDo(t *testing.T) {
	// A warning naming a problem with no remedy is an alarm, not a device.
	got := passthroughCodecWarning(db.DestRTMP, db.PlatformTwitch, nil, &ffmpeg.VideoStream{Codec: "hevc"})
	if !strings.Contains(got, "H.264") || !strings.Contains(got, "rendition") {
		t.Errorf("warning offers no remedy: %q", got)
	}
}
