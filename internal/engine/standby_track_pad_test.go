package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A TWO-TRACK DESTINATION MUST START WHILE A ONE-TRACK STANDBY IS ON AIR.
//
// The destination's filter graph is compiled against the primary's measured
// layout, which keeps its track count through an outage. A standby that carries
// one track while the primary carries two leaves the relay with no audio track
// 1, and FFmpeg refuses the graph at bind time:
//
//	Stream specifier ':a:1' in filtergraph description ... matches no streams.
//
// A destination that was already running rides the switch, because its graph
// was bound before the standby came up. A destination RESTARTED while the
// standby is on air (a live gain change, a manual restart) exits at once, the
// supervisor retries, and the output stops publishing after a few seconds.
//
// The fix keeps #934's invariant -- the relay carries the primary's track count
// on every source -- by having the conformed standby hop pad each missing track
// with silence. The pad is derived from the standby's own track 0 through
// volume=0, so it shares the standby's timeline and never free-runs against the
// live input, and it is encoded at the primary's measured format for that slot.
// The primary stays a pure copy.

// TestAStandbyFeedWithFewerAudioTracksThanThePrimaryStillCarriesEveryTrackAPrimaryMixReads
// is the argv-level half: a conformed standby over a two-track primary must emit
// one pad per primary track after the standby's own tracks, so that whatever the
// standby carries, the relay's audio slots 0..N-1 always exist.
//
// MUTATION: return ffmpeg.RelayFeedArgs unchanged from ffmpeg.ConformedFeedArgs when pads are
// wanted. Observed: FAIL, no pad maps in the argv.
func TestAStandbyFeedWithFewerAudioTracksThanThePrimaryStillCarriesEveryTrackAPrimaryMixReads(t *testing.T) {
	args := ffmpeg.ConformedFeedArgs("udp://127.0.0.1:1", "udp://127.0.0.1:2", 7.5, primaryMono441)
	fc := ""
	if i := slices.Index(args, "-filter_complex"); i >= 0 && i+1 < len(args) {
		fc = args[i+1]
	}
	if fc == "" {
		t.Fatalf("a two-track primary gets no silent pad tracks: the one-track standby leaves the relay with no track 1: %q", args)
	}
	if n := strings.Count(fc, "volume=0"); n != len(primaryMono441) {
		t.Errorf("filter graph has %d silent pads, want one per primary track (%d): %s", n, len(primaryMono441), fc)
	}
	if strings.Contains(fc, "anullsrc") {
		t.Errorf("the pads are free-running anullsrc sources, which race ahead of a live relay: %s", fc)
	}
	// Each pad is a separate output, mapped after the standby's own tracks and
	// encoded: a copy of a filtered stream does not exist.
	if got := strings.Count(strings.Join(args, " "), "-map ["); got != len(primaryMono441) {
		t.Errorf("pad maps = %d, want %d: %q", got, len(primaryMono441), args)
	}
	if !slices.Contains(args, "-c:a") || slices.Index(args, "-c:a") < slices.Index(args, "-c:v") {
		t.Errorf("the padded audio is not encoded after the video copy: %q", args)
	}
	if slices.Contains(args, "-c") {
		t.Errorf("the padded hop still blankets -c copy over audio the pads re-encode: %q", args)
	}
	// The primary and the no-primary case are unchanged.
	if got, want := ffmpeg.ConformedFeedArgs("udp://127.0.0.1:1", "udp://127.0.0.1:2", 7.5, nil),
		ffmpeg.RelayFeedArgs("udp://127.0.0.1:1", "udp://127.0.0.1:2", 7.5); !slices.Equal(got, want) {
		t.Errorf("with no measured primary the standby feed changed:\n%q\nwant\n%q", got, want)
	}
	mono := []ffmpeg.AudioStream{{Index: 0, Codec: "aac", Channels: 2, Layout: "stereo", SampleRate: 48000, Bitrate: 128000}}
	if got := ffmpeg.ConformedFeedArgs("udp://127.0.0.1:1", "udp://127.0.0.1:2", 7.5, mono); slices.Contains(got, "-filter_complex") {
		t.Errorf("a one-track primary needs no pads, and the single-track path must stay as it was: %q", got)
	}
}

// TestAMultiTrackDestinationStartedWhileAOneTrackStandbyIsOnAirStillPublishesItsAudio
// is the bug itself, with real FFmpeg. The relay carries only the conformed
// one-track backup, exactly what a restarted destination reads during the
// switch, and the destination is compiled for the two-track primary. Before the
// fix FFmpeg exits 234 at bind time; after it, the destination publishes the
// standby's track 0 on the video's timeline, with track 1 silent.
//
// MUTATION: drop the pad maps from ffmpeg.ConformedFeedArgs. Observed: FAIL, the
// destination exits with "Stream specifier ':a:1' ... matches no streams".
func TestAMultiTrackDestinationStartedWhileAOneTrackStandbyIsOnAirStillPublishesItsAudio(t *testing.T) {
	ffmpegBin, ffprobeBin := offsetBenchTools(t)
	e := conformEngine()
	primary := e.audioToConformTo()
	src := routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 1, Codec: "aac", Layout: "mono"},
		{Index: 1, Channels: 1, Codec: "aac", Layout: "mono"},
	}}
	prof := routing.Profile{Mode: routing.ModeSimple, Normalize: routing.NormOff, SampleRate: 48000,
		Tracks: []routing.TrackSel{{Track: 0, Enabled: true, Gain: 1}, {Track: 1, Enabled: true, Gain: 1}}}
	res, err := routing.Compile(prof, src)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	backupIn := filepath.Join(dir, "backup.ts")
	formatFixture(t, ffmpegBin, backupIn, 10, 1, 48000, 2)
	relayBytes := runHop(t, ffmpegBin,
		ffmpeg.ConformedFeedArgs("udp://127.0.0.1:1", "udp://127.0.0.1:2", 23, primary), backupIn,
		filepath.Join(dir, "standby-hop.ts"), 0)
	relayPath := filepath.Join(dir, "relay.ts")
	if err := os.WriteFile(relayPath, relayBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(dir, "destination.mkv")
	args := ffmpeg.DestinationArgs(ffmpeg.DestSpec{
		Kind: ffmpeg.DestFile, Target: outPath, RelayURL: "udp://127.0.0.1:3",
		FilterComplex: res.FilterComplex, AudioOutLabel: res.OutLabel,
		AudioBitrate: 128, SampleRate: 48000,
	})
	args = swapArg(t, args, "-i", relayPath)
	dest := exec.Command(ffmpegBin, append([]string{"-y"}, args...)...)
	var destErr strings.Builder
	dest.Stderr = &destErr
	if err := dest.Run(); err != nil {
		t.Fatalf("the two-track destination could not start on a one-track standby: %v\n%s", err, tail(destErr.String(), 12))
	}
	if h := hiddenSeconds(t, ffmpegBin, ffprobeBin, outPath); h > 0.1 {
		t.Errorf("%.2fs of audio is hidden behind the timestamps", h)
	}
	if aEnd := streamEnd(t, ffprobeBin, outPath, "a"); aEnd < 9 {
		t.Errorf("the destination published %.2fs of audio for a 10s standby", aEnd)
	}
}

// THE SLATE CARRIES EVERY TRACK A MULTI-TRACK PRIMARY MIX READS.
//
// The slate used to publish one silent track whatever the primary carried, so a
// destination compiled for two tracks could not bind to it either, in the same
// way as a one-track backup. The slate is built at the primary's format, and it
// now publishes one silent track per primary track, all at that format.
//
// MUTATION: publish one track (drop the spec.AudioTracks mapping). Observed: FAIL,
// one map and no second silent track.
func TestTheSlateCarriesOneSilentTrackPerTrackAMultiTrackPrimaryMixReads(t *testing.T) {
	e := conformEngine()
	spec, _ := e.slateSpec(db.DefaultSettings(), "udp://127.0.0.1:21005", 0)
	if spec.AudioTracks != len(primaryMono441) {
		t.Fatalf("slate publishes %d audio track(s) for a %d-track primary: a destination compiled for the primary cannot bind to it",
			spec.AudioTracks, len(primaryMono441))
	}
	args := ffmpeg.SlateArgs(spec)
	if n := strings.Count(strings.Join(args, " "), "-map 1:a:0"); n != len(primaryMono441) {
		t.Errorf("the slate maps %d silent audio track(s), want %d: %q", n, len(primaryMono441), args)
	}
}

// A two-track destination started during the slate binds and publishes, in
// real FFmpeg, the same way the one-track standby case does.
//
// MUTATION: as above. Observed: FAIL, the destination exits at bind time.
func TestAMultiTrackDestinationStartedDuringTheSlateStillPublishesItsAudio(t *testing.T) {
	ffmpegBin, ffprobeBin := offsetBenchTools(t)
	e := conformEngine()
	spec, _ := e.slateSpec(db.DefaultSettings(), "udp://127.0.0.1:2", 23)
	spec.Preset = "ultrafast"
	src := routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 1, Codec: "aac", Layout: "mono"},
		{Index: 1, Channels: 1, Codec: "aac", Layout: "mono"},
	}}
	prof := routing.Profile{Mode: routing.ModeSimple, Normalize: routing.NormOff, SampleRate: 48000,
		Tracks: []routing.TrackSel{{Track: 0, Enabled: true, Gain: 1}, {Track: 1, Enabled: true, Gain: 1}}}
	res, err := routing.Compile(prof, src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	slateBytes := runHop(t, ffmpegBin, ffmpeg.SlateArgs(spec), "", filepath.Join(dir, "slate.ts"), 10)
	relayPath := filepath.Join(dir, "relay.ts")
	if err := os.WriteFile(relayPath, slateBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "destination.mkv")
	args := ffmpeg.DestinationArgs(ffmpeg.DestSpec{
		Kind: ffmpeg.DestFile, Target: outPath, RelayURL: "udp://127.0.0.1:3",
		FilterComplex: res.FilterComplex, AudioOutLabel: res.OutLabel,
		AudioBitrate: 128, SampleRate: 48000,
	})
	args = swapArg(t, args, "-i", relayPath)
	dest := exec.Command(ffmpegBin, append([]string{"-y"}, args...)...)
	var destErr strings.Builder
	dest.Stderr = &destErr
	if err := dest.Run(); err != nil {
		t.Fatalf("the two-track destination could not start on the slate: %v\n%s", err, tail(destErr.String(), 12))
	}
	if h := hiddenSeconds(t, ffmpegBin, ffprobeBin, outPath); h > 0.1 {
		t.Errorf("%.2fs of audio is hidden behind the timestamps", h)
	}
	if aEnd := streamEnd(t, ffprobeBin, outPath, "a"); aEnd < 9 {
		t.Errorf("the destination published %.2fs of audio for a 10s slate", aEnd)
	}
}
