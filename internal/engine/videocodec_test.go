package engine

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// #627. The Opus guard refuses at SAVE time, because the audio codec is a
// setting the operator picked. The video codec is whatever the encoder sends,
// discovered at probe time, so there is no save to refuse -- these pin the
// warning that replaces it, and above all pin what it must NOT claim.

func rtmpTo(name string, p db.Platform) *db.Destination {
	return &db.Destination{Name: name, Kind: db.DestRTMP, Platform: p}
}

func TestH264ConcernsNobody(t *testing.T) {
	rows := []*db.Destination{rtmpTo("tw", db.PlatformTwitch), rtmpTo("yt", db.PlatformYouTube)}
	for _, codec := range []string{"h264", "H264", "avc1", ""} {
		if got := videoCodecConcerns(codec, rows); len(got) != 0 {
			t.Errorf("codec %q raised %d concerns, want 0.\n\n"+
				"H.264 is the overwhelmingly common case; warning about it trains the "+
				"operator to ignore the warning that matters.", codec, len(got))
		}
	}
}

func TestHEVCIsFlaggedForAPlatformThatTakesOnlyH264(t *testing.T) {
	got := videoCodecConcerns("hevc", []*db.Destination{rtmpTo("tw", db.PlatformTwitch)})
	if len(got) != 1 || !got[0].known {
		t.Fatalf("hevc to Twitch produced %+v, want one KNOWN concern.\n\n"+
			"services.json records twitch as h264 only, so this is a sourced claim and "+
			"should be stated as one.", got)
	}
}

// YouTube takes HEVC. Warning there would send an operator to change an encoder
// setting that was never the problem.
func TestAPlatformThatAcceptsHEVCIsNotFlagged(t *testing.T) {
	if got := videoCodecConcerns("hevc", []*db.Destination{rtmpTo("yt", db.PlatformYouTube)}); len(got) != 0 {
		t.Fatalf("hevc to YouTube raised %d concerns, want 0: services.json records "+
			"youtube as accepting h264, hevc and av1", len(got))
	}
}

// THE HONESTY REQUIREMENT. services.json covers four services; the rest have no
// entry. An unknown platform must be reported as unknown, never as a failure.
func TestAnUnrecordedPlatformIsReportedAsUnknownNotAsAFailure(t *testing.T) {
	got := videoCodecConcerns("hevc", []*db.Destination{rtmpTo("rum", db.PlatformRumble)})
	if len(got) != 1 {
		t.Fatalf("hevc to an unrecorded platform produced %d concerns, want 1", len(got))
	}
	if got[0].known {
		t.Fatal("an unrecorded platform was reported as a KNOWN rejection.\n\n" +
			"services.json has no videoCodecs for it, so claiming it will be rejected " +
			"invents a fact -- and sends the operator to change an encoder setting that " +
			"may never have been the problem. Unknown has to stay unknown.")
	}
}

// Only RTMP: SRT carries MPEG-TS and a file carries Matroska, neither of which
// cares what the video codec is.
func TestOnlyRTMPDestinationsAreConcerned(t *testing.T) {
	rows := []*db.Destination{
		{Name: "srt", Kind: db.DestSRT, Platform: db.PlatformTwitch},
		{Name: "file", Kind: db.DestFile, Platform: db.PlatformTwitch},
		{Name: "audio", Kind: db.DestAudio, Platform: db.PlatformTwitch},
	}
	if got := videoCodecConcerns("hevc", rows); len(got) != 0 {
		t.Fatalf("raised %d concerns for non-RTMP destinations, want 0", len(got))
	}
}

func TestTheSummaryNamesTheDestinationsAndTheCodec(t *testing.T) {
	s := videoCodecSummary("hevc", []*db.Destination{
		rtmpTo("b-dest", db.PlatformTwitch), rtmpTo("a-dest", db.PlatformKick),
	})
	for _, want := range []string{"HEVC", "a-dest", "b-dest"} {
		if !contains(s, want) {
			t.Fatalf("summary %q does not name %q; an operator needs both the codec "+
				"and which destinations it affects", s, want)
		}
	}
	if videoCodecSummary("h264", []*db.Destination{rtmpTo("tw", db.PlatformTwitch)}) != "" {
		t.Fatal("h264 produced a summary; it must be silent")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// THE TWO WORDINGS ARE THE FEATURE. #627
//
// videoCodecConcerns decides WHAT is wrong; warnVideoCodec decides what the
// operator is told, and the difference between "this platform does not accept
// it" and "we do not know what this platform accepts" is the whole honesty
// requirement of the issue. Testing the selection and not the wording would
// leave the part that can mislead an operator uncovered.
func TestTheWarningDistinguishesAKnownRejectionFromAnUnknownPlatform(t *testing.T) {
	var buf syncBuf
	e := &Engine{log: slog.New(slog.NewTextHandler(&buf, nil))}

	e.warnVideoCodec("hevc", []*db.Destination{
		rtmpTo("tw", db.PlatformTwitch),  // registry says h264 only
		rtmpTo("rum", db.PlatformRumble), // registry has no entry
	})
	out := buf.String()

	if !strings.Contains(out, "will upload and be rejected") {
		t.Errorf("a platform the registry records as h264-only produced no definite "+
			"warning:\n%s", out)
	}
	if !strings.Contains(out, "is not recorded") {
		t.Errorf("a platform with no registry entry produced no UNKNOWN warning:\n%s\n\n"+
			"Reporting it as a rejection would invent a fact and send the operator to "+
			"change an encoder setting that may never have been the problem.", out)
	}
	if !strings.Contains(out, "accepts=h264") {
		t.Errorf("the known warning does not say what the platform DOES accept:\n%s\n\n"+
			"Naming the accepted codec is what makes the warning actionable rather "+
			"than an instruction to go and look it up.", out)
	}
}

func TestH264ProducesNoWarningAtAll(t *testing.T) {
	var buf syncBuf
	e := &Engine{log: slog.New(slog.NewTextHandler(&buf, nil))}
	e.warnVideoCodec("h264", []*db.Destination{rtmpTo("tw", db.PlatformTwitch)})
	if s := buf.String(); s != "" {
		t.Fatalf("h264 logged something:\n%s\n\nIt is the overwhelmingly common case; "+
			"a line here trains the operator to skip the line that matters.", s)
	}
}

// A destination with no rendition, or a rendition the engine is not running,
// must not warn: there is nothing to compare and nothing to say.
func TestARenditionlessDestinationIsNotJudged(t *testing.T) {
	var buf syncBuf
	e := &Engine{log: slog.New(slog.NewTextHandler(&buf, nil)), rends: map[int64]*rendition{}}
	e.warnRenditionAgainstPlatform(&db.Destination{Name: "d", Platform: db.PlatformTwitch})
	missing := int64(42)
	e.warnRenditionAgainstPlatform(&db.Destination{Name: "d", Platform: db.PlatformTwitch, RenditionID: &missing})
	e.warnRenditionAgainstPlatform(nil)
	if s := buf.String(); s != "" {
		t.Fatalf("warned about a destination with no rendition to compare:\n%s", s)
	}
}
