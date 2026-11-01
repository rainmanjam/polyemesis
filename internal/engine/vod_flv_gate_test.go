package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// The VOD track is a second audio track on an RTMP publish, and that only
// reaches a platform if this FFmpeg can write two audio streams into an FLV. The
// engine reads the recorded probe verdict at plan time: a probe that ran and
// failed drops the second mix with the probe's reason on the card, and the
// broadcast goes out on the live mix alone. An unprobed binary keeps the pair,
// because unknown must not withhold a working VOD track.
//
// This gate is generic to RTMP. It is not a Twitch rule: any RTMP destination
// that asks for a second mix is publishing through the same FLV muxer.

// A custom RTMP destination with a second mix, on a binary whose probe failed:
// the second track is not mapped and the card says why, with the probe's words.
//
// MUTATION: in planDestinations, delete the flv-probe arm. Observed: FAIL, "a
// second audio mix is mapped".
func TestAVODMixIsNotSentWhenThisFFmpegCannotWriteTwoAudioTracksToFLV(t *testing.T) {
	e, _ := storeEngine(t)
	const reason = "this FFmpeg wrote 1 audio stream(s) into a two-track FLV, not 2"
	e.tools.RecordTwoAudioFLV(ffmpeg.FLVTwoAudioResult{OK: false, Reason: reason})

	row := twitchRow()
	row.Platform = db.PlatformCustom
	row.Multitrack = false
	row.Profile = mixProfile(0)
	vod := mixProfile(1)
	row.VODProfile = &vod

	plans := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", false)
	p := plans[row.ID]
	if p.err != "" {
		t.Fatalf("the destination will not run at all: %s", p.err)
	}
	argv := e.destArgs(row, p.compiled, "udp://127.0.0.1:1", row.Target())
	if mapsASecondMix(argv) {
		t.Errorf("a second audio mix is mapped although this FFmpeg cannot write it into FLV.\nargv: %s",
			strings.Join(argv, " "))
	}
	if !strings.Contains(p.vodDropped, reason) {
		t.Errorf("the card is not told why the second mix was dropped: %q", p.vodDropped)
	}
}

// The same destination on a binary that was never probed keeps its second mix.
// Dropping it here would be the silent regression this gate must not cause.
func TestAVODMixIsKeptWhenTheFLVProbeHasNotRun(t *testing.T) {
	e, _ := storeEngine(t)

	row := twitchRow()
	row.Platform = db.PlatformCustom
	row.Multitrack = false
	row.Profile = mixProfile(0)
	vod := mixProfile(1)
	row.VODProfile = &vod

	plans := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", false)
	p := plans[row.ID]
	argv := e.destArgs(row, p.compiled, "udp://127.0.0.1:1", row.Target())
	if !mapsASecondMix(argv) {
		t.Errorf("an unprobed FFmpeg lost its second mix; unknown must not withhold it.\nargv: %s",
			strings.Join(argv, " "))
	}
}

// A probe that could not ask its question (a timeout, a binary that would not
// start, a missing tempdir) is recorded as inconclusive, and the planning gate
// must keep the second mix on it exactly as it does for a binary never probed.
// Withholding the mix on a broken probe is the silent regression this gate
// exists to prevent: the operator would lose a VOD track nobody had shown to be
// broken.
//
// MUTATION: in flvRefused, treat any recorded verdict with probed true as a
// refusal. Observed: FAIL, "an inconclusive probe dropped the second mix".
func TestAVODMixIsKeptWhenTheFLVProbeWasInconclusive(t *testing.T) {
	e, _ := storeEngine(t)
	e.tools.RecordTwoAudioFLV(ffmpeg.FLVTwoAudioResult{
		OK: true, Inconclusive: true, Reason: "probe inconclusive: FFmpeg timed out",
	})

	row := twitchRow()
	row.Platform = db.PlatformCustom
	row.Multitrack = false
	row.Profile = mixProfile(0)
	vod := mixProfile(1)
	row.VODProfile = &vod

	plans := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", false)
	p := plans[row.ID]
	if p.vodDropped != "" {
		t.Errorf("an inconclusive probe dropped the second mix: %q", p.vodDropped)
	}
	argv := e.destArgs(row, p.compiled, "udp://127.0.0.1:1", row.Target())
	if !mapsASecondMix(argv) {
		t.Errorf("an inconclusive probe withheld the second mix.\nargv: %s", strings.Join(argv, " "))
	}
}
