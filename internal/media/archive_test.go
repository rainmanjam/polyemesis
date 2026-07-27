package media

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// ------------------------------------------------------------ encoder choice

func TestArchiveEncoderPrefersSoftwareBecauseThisIsTheLastEncodeTheFootageGets(t *testing.T) {
	tests := []struct {
		name  string
		codec ArchiveCodec
		build map[string]bool
		want  string
	}{
		{"hevc on a plain build", ArchiveHEVC, map[string]bool{"libx265": true}, "libx265"},
		{"av1 on a plain build", ArchiveAV1, map[string]bool{"libsvtav1": true}, "libsvtav1"},
		{
			"software wins over a working GPU", ArchiveHEVC,
			map[string]bool{"libx265": true, "hevc_nvenc": true}, "libx265",
		},
		{
			"hardware only when software is absent", ArchiveHEVC,
			map[string]bool{"hevc_nvenc": true, "hevc_qsv": true}, "hevc_nvenc",
		},
		{
			"svt is preferred over libaom", ArchiveAV1,
			map[string]bool{"libaom-av1": true, "libsvtav1": true}, "libsvtav1",
		},
		{"an unknown codec family falls back to hevc", ArchiveCodec("vp9"), map[string]bool{"libx265": true}, "libx265"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			has := func(name string) bool { return tc.build[name] }
			if got := ArchiveEncoder(tc.codec, has); got != tc.want {
				t.Fatalf("ArchiveEncoder = %q, want %q", got, tc.want)
			}
		})
	}
}

// A capability check that is wrong in the restrictive direction is worse than
// no check: FFmpeg's own error message is more useful than one we invented.
func TestArchiveEncoderStillNamesAnEncoderWhenNothingCanBeProbed(t *testing.T) {
	tests := []struct {
		name  string
		has   func(string) bool
		codec ArchiveCodec
		want  string
	}{
		{"no probe ran at all", nil, ArchiveHEVC, "libx265"},
		{"the build claims nothing works", func(string) bool { return false }, ArchiveHEVC, "libx265"},
		{"and for av1", func(string) bool { return false }, ArchiveAV1, "libsvtav1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ArchiveEncoder(tc.codec, tc.has); got != tc.want {
				t.Fatalf("ArchiveEncoder = %q, want %q", got, tc.want)
			}
		})
	}
}

// -------------------------------------------------------------- archive args

// The one assertion that matters more than everything else in this file. A
// multitrack master that comes back as a stereo mix is a deleted archive with a
// video file where it used to be, and because it still plays nobody finds out.
func TestArchiveArgsCopiesEveryAudioTrackAndReEncodesNone(t *testing.T) {
	encoders := []string{"libx265", "libsvtav1", "libaom-av1", "hevc_nvenc", "hevc_qsv", "hevc_vaapi", "made_up"}
	for _, enc := range encoders {
		t.Run(enc, func(t *testing.T) {
			args := ArchiveArgs(ArchiveSpec{Input: "in.mkv", Output: "out.mkv", Encoder: enc})

			mustArg(t, args, "-c:a", "copy")
			if !hasArg(args, "0:a") {
				t.Fatalf("every audio track must be mapped: %v", args)
			}
			for _, forbidden := range []string{"-ac", "-ar", "-b:a", "-af"} {
				if hasArg(args, forbidden) {
					t.Fatalf("%s would alter the archived audio: %v", forbidden, args)
				}
			}
			// Track titles and languages are how anyone tells six microphones
			// apart, and the verifier refuses to delete the original without
			// them.
			mustArg(t, args, "-map_metadata", "0")
		})
	}
}

func TestArchiveArgsKeepsTheSourcePixelFormatSoATenBitMasterStaysTenBit(t *testing.T) {
	args := ArchiveArgs(ArchiveSpec{Input: "in.mkv", Output: "out.mkv"})
	if hasArg(args, "-pix_fmt") {
		t.Fatalf("-pix_fmt would flatten a 10-bit master invisibly: %v", args)
	}
}

func TestArchiveArgsUsesEachEncodersOwnQualityFlag(t *testing.T) {
	tests := []struct {
		name      string
		spec      ArchiveSpec
		wantFlag  string
		wantValue string
	}{
		{"x265 default", ArchiveSpec{Encoder: "libx265"}, "-crf", "28"},
		{"x265 explicit", ArchiveSpec{Encoder: "libx265", Quality: 24}, "-crf", "24"},
		{"svt-av1 default", ArchiveSpec{Encoder: "libsvtav1"}, "-crf", "32"},
		{"libaom default", ArchiveSpec{Encoder: "libaom-av1"}, "-crf", "32"},
		{"nvenc spells it cq", ArchiveSpec{Encoder: "hevc_nvenc"}, "-cq", "28"},
		{"qsv spells it global_quality", ArchiveSpec{Encoder: "hevc_qsv"}, "-global_quality", "28"},
		{"vaapi spells it qp", ArchiveSpec{Encoder: "hevc_vaapi"}, "-qp", "28"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Input, tc.spec.Output = "in.mkv", "out.mkv"
			mustArg(t, ArchiveArgs(tc.spec), tc.wantFlag, tc.wantValue)
		})
	}
}

// hevc_videotoolbox is a deliberate omission from the profile table: its -q:v
// counts UP where crf counts down, so one shared "quality" number would mean
// "archive grade" on one machine and "unwatchable" on another — and this code
// path deletes the original afterwards.
func TestArchiveArgsInventsNoQualityFlagForAnEncoderItDoesNotKnow(t *testing.T) {
	tests := []struct {
		name string
		spec ArchiveSpec
	}{
		{"videotoolbox, whose scale is inverted", ArchiveSpec{Encoder: "hevc_videotoolbox", Quality: 28}},
		{"something nobody has heard of", ArchiveSpec{Encoder: "hevc_future", Quality: 28}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Input, tc.spec.Output = "in.mkv", "out.mkv"
			args := ArchiveArgs(tc.spec)
			for _, flag := range []string{"-crf", "-cq", "-qp", "-global_quality", "-q:v"} {
				if hasArg(args, flag) {
					t.Fatalf("%s was invented for an unknown encoder: %v", flag, args)
				}
			}
			mustArg(t, args, "-c:v", tc.spec.Encoder)
		})
	}
}

func TestArchiveArgsFallsBackToBitrateWhenOneIsGiven(t *testing.T) {
	args := ArchiveArgs(ArchiveSpec{Input: "in.mkv", Output: "out.mkv", Encoder: "libx265", VideoKbps: 3000})
	mustArg(t, args, "-b:v", "3000k")
	if hasArg(args, "-crf") {
		t.Fatalf("both a bitrate and a crf were emitted: %v", args)
	}
}

func TestArchiveArgsPresetsOnlyEncodersThatHaveAPreset(t *testing.T) {
	tests := []struct {
		name    string
		spec    ArchiveSpec
		flag    string
		want    string
		wantAny bool
	}{
		{"x265 default", ArchiveSpec{Encoder: "libx265"}, "-preset", "medium", true},
		{"x265 explicit", ArchiveSpec{Encoder: "libx265", Preset: "slow"}, "-preset", "slow", true},
		{"svt-av1 numeric preset", ArchiveSpec{Encoder: "libsvtav1"}, "-preset", "6", true},
		{"libaom has none", ArchiveSpec{Encoder: "libaom-av1"}, "-preset", "", false},
		{"vaapi has none, even when asked", ArchiveSpec{Encoder: "hevc_vaapi", Preset: "slow"}, "-preset", "", false},
		{"an unknown encoder gets what it was given", ArchiveSpec{Encoder: "x", Preset: "fast"}, "-preset", "fast", true},
		{"an unknown encoder with no preset gets none", ArchiveSpec{Encoder: "x"}, "-preset", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Input, tc.spec.Output = "in.mkv", "out.mkv"
			got, ok := argAfter(ArchiveArgs(tc.spec), tc.flag)
			if ok != tc.wantAny {
				t.Fatalf("%s present = %v, want %v (%v)", tc.flag, ok, tc.wantAny, ArchiveArgs(tc.spec))
			}
			if ok && got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.flag, got, tc.want)
			}
		})
	}
}

func TestArchiveArgsGivesVAAPIItsDeviceBeforeTheInput(t *testing.T) {
	args := ArchiveArgs(ArchiveSpec{Input: "in.mkv", Output: "out.mkv", Encoder: "hevc_vaapi"})

	dev, i := argIndex(args, "-vaapi_device"), argIndex(args, "-i")
	if dev < 0 || dev > i {
		t.Fatalf("the device must exist before the graph that uploads into it: %v", args)
	}
	mustArg(t, args, "-vaapi_device", DefaultVAAPIDevice)
	mustArg(t, args, "-vf", "format=nv12,hwupload")

	custom := ArchiveArgs(ArchiveSpec{Input: "in.mkv", Output: "out.mkv",
		Encoder: "hevc_vaapi", VAAPIDevice: "/dev/dri/renderD129"})
	mustArg(t, custom, "-vaapi_device", "/dev/dri/renderD129")
}

func TestArchiveArgsDefaultsToHEVCWhenNoCodecIsNamed(t *testing.T) {
	args := ArchiveArgs(ArchiveSpec{Input: "in.mkv", Output: "out.mkv"})
	mustArg(t, args, "-c:v", "libx265")
	if args[len(args)-1] != "out.mkv" {
		t.Fatalf("output is not last: %v", args)
	}
}

func TestArchiveArgsMapsExplicitlyRatherThanDraggingEveryStreamAlong(t *testing.T) {
	args := ArchiveArgs(ArchiveSpec{Input: "in.mkv", Output: "out.mkv"})
	for i, a := range args {
		if a == "-map" && i+1 < len(args) && args[i+1] == "0" {
			t.Fatalf("a bare -map 0 would drag attachments and data streams in: %v", args)
		}
	}
	if !hasArg(args, "0:v:0") || !hasArg(args, "0:a") {
		t.Fatalf("video and all audio must both be mapped: %v", args)
	}
}

// ----------------------------------------------------------------- age gate

func TestCheckArchiveAgeRefusesAnythingItCannotProveIsOldEnough(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		recordedAt time.Time
		minAge     time.Duration
		wantErr    bool
	}{
		{"comfortably old", now.Add(-60 * 24 * time.Hour), 30 * 24 * time.Hour, false},
		{"exactly the age", now.Add(-30 * 24 * time.Hour), 30 * 24 * time.Hour, false},
		{"an hour too young", now.Add(-30*24*time.Hour + time.Hour), 30 * 24 * time.Hour, true},
		{"recorded this morning", now.Add(-4 * time.Hour), 30 * 24 * time.Hour, true},
		{"recorded in the future", now.Add(24 * time.Hour), 30 * 24 * time.Hour, true},
		// Everywhere else in this repo an unknown answer means "assume the best
		// and carry on". Here the best case is deleting a recording made this
		// morning, so unknown means no.
		{"an unknown recording date", time.Time{}, 30 * 24 * time.Hour, true},
		{"a policy with no age at all", now.Add(-365 * 24 * time.Hour), 0, true},
		{"a policy below the floor", now.Add(-365 * 24 * time.Hour), time.Hour, true},
		{"a negative policy", now.Add(-365 * 24 * time.Hour), -time.Hour, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckArchiveAge(tc.recordedAt, now, tc.minAge)
			if tc.wantErr {
				if err == nil {
					t.Fatal("CheckArchiveAge allowed a recording it should have refused")
				}
				if !errors.Is(err, ErrArchiveTooYoung) {
					t.Fatalf("error does not wrap ErrArchiveTooYoung: %v", err)
				}
			} else if err != nil {
				t.Fatalf("CheckArchiveAge: %v", err)
			}
			if got := ArchiveEligible(tc.recordedAt, now, tc.minAge); got == tc.wantErr {
				t.Fatalf("ArchiveEligible = %v, contradicts CheckArchiveAge", got)
			}
		})
	}
}

func TestCheckArchiveAgeExplainsItselfToTheOperator(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	err := CheckArchiveAge(now.Add(-2*24*time.Hour), now, 30*24*time.Hour)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"48h", "720h"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// -------------------------------------------------------------- verification

// The happy case, spelled out once so the failure cases below are only ever one
// mutation away from it.
func goodPair() (FileSummary, FileSummary) {
	src := FileSummary{
		Path: "rec-1.mkv", Bytes: 10 << 30, DurationSeconds: 3600, VideoCodec: "h264",
		Audio: []TrackSummary{
			{Index: 0, Codec: "aac", Channels: 2, Language: "eng", Title: "Host"},
			{Index: 1, Codec: "aac", Channels: 1, Language: "eng", Title: "Guest"},
			{Index: 2, Codec: "aac", Channels: 2, Title: "Music"},
		},
	}
	out := src
	out.Path = "archive.mkv"
	out.Bytes = 4 << 30
	out.VideoCodec = "hevc"
	out.DurationSeconds = 3600.04
	out.Audio = append([]TrackSummary(nil), src.Audio...)
	return src, out
}

func TestVerifyArchiveAcceptsACleanReEncode(t *testing.T) {
	src, out := goodPair()
	v := VerifyArchive(src, out, nil, VerifyOptions{})

	if !v.OK {
		t.Fatalf("a clean re-encode was refused: %v", v.Reasons)
	}
	if v.SavedBytes != int64(6<<30) {
		t.Fatalf("SavedBytes = %d", v.SavedBytes)
	}
	if v.SavedPercent < 59 || v.SavedPercent > 61 {
		t.Fatalf("SavedPercent = %v, want about 60", v.SavedPercent)
	}
}

func TestVerifyArchiveKeepsTheOriginalWheneverAnythingIsWrong(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(src, out *FileSummary) []string // returns decode errors
		opt     VerifyOptions
		wantWhy string
	}{
		{
			"a lost audio track",
			func(_, out *FileSummary) []string { out.Audio = out.Audio[:2]; return nil },
			VerifyOptions{}, "3 audio track(s) and the archive copy has 2",
		},
		{
			"an extra audio track from nowhere",
			func(_, out *FileSummary) []string {
				out.Audio = append(out.Audio, TrackSummary{Index: 3, Codec: "aac", Channels: 2})
				return nil
			},
			VerifyOptions{}, "3 audio track(s) and the archive copy has 4",
		},
		{
			"a downmixed track",
			func(_, out *FileSummary) []string { out.Audio[0].Channels = 1; return nil },
			VerifyOptions{}, "audio track 0 has 2 channel(s) in the original and 1",
		},
		{
			"audio that was re-encoded instead of copied",
			func(_, out *FileSummary) []string { out.Audio[1].Codec = "opus"; return nil },
			VerifyOptions{}, "audio must be copied, never re-encoded",
		},
		{
			"a lost track title",
			func(_, out *FileSummary) []string { out.Audio[2].Title = ""; return nil },
			VerifyOptions{}, `audio track 2 lost its title ("Music" became "")`,
		},
		{
			"a lost language tag",
			func(_, out *FileSummary) []string { out.Audio[0].Language = ""; return nil },
			VerifyOptions{}, "audio track 0 lost its language tag",
		},
		{
			"a truncated re-encode",
			func(_, out *FileSummary) []string { out.DurationSeconds = 3000; return nil },
			VerifyOptions{}, "outside the",
		},
		{
			"an output with no duration at all",
			func(_, out *FileSummary) []string { out.DurationSeconds = 0; return nil },
			VerifyOptions{}, "reports no duration",
		},
		{
			"a source we could not measure",
			func(src, _ *FileSummary) []string { src.DurationSeconds = 0; return nil },
			VerifyOptions{}, "could not be measured",
		},
		{
			"an empty output",
			func(_, out *FileSummary) []string { out.Bytes = 0; return nil },
			VerifyOptions{}, "the archive copy is empty",
		},
		{
			"an output with no video",
			func(_, out *FileSummary) []string { out.VideoCodec = ""; return nil },
			VerifyOptions{}, "no video stream",
		},
		{
			"an output that grew",
			func(src, out *FileSummary) []string { out.Bytes = src.Bytes + 1; return nil },
			VerifyOptions{}, "nothing would be reclaimed",
		},
		{
			"a copy that does not decode",
			func(_, _ *FileSummary) []string { return []string{"Invalid NAL unit size"} },
			VerifyOptions{}, "did not decode cleanly",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, out := goodPair()
			decodeErrs := tc.mutate(&src, &out)

			v := VerifyArchive(src, out, decodeErrs, tc.opt)
			if v.OK {
				t.Fatal("the verifier authorised deleting the original")
			}
			if !strings.Contains(strings.Join(v.Reasons, " | "), tc.wantWhy) {
				t.Fatalf("reasons %v do not mention %q", v.Reasons, tc.wantWhy)
			}
		})
	}
}

// Every VerifyOptions field's zero value is the strict one, so a caller that
// forgot to fill it in gets the careful behaviour on a path whose next step is
// an irreversible delete.
func TestVerifyOptionsZeroValueIsTheStrictOne(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(src, out *FileSummary)
		relaxed VerifyOptions
	}{
		{
			"a larger copy",
			func(src, out *FileSummary) { out.Bytes = src.Bytes + 1 },
			VerifyOptions{AllowLarger: true},
		},
		{
			"lost metadata",
			func(_, out *FileSummary) { out.Audio[0].Title, out.Audio[0].Language = "", "" },
			VerifyOptions{AllowMetadataLoss: true},
		},
		{
			"an unmeasurable source",
			func(src, _ *FileSummary) { src.DurationSeconds = 0 },
			VerifyOptions{AllowMissingSourceDuration: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, out := goodPair()
			tc.mutate(&src, &out)

			if VerifyArchive(src, out, nil, VerifyOptions{}).OK {
				t.Fatal("the zero-value options allowed it")
			}
			if v := VerifyArchive(src, out, nil, tc.relaxed); !v.OK {
				t.Fatalf("the explicit opt-out was ignored: %v", v.Reasons)
			}
		})
	}
}

func TestVerifyArchiveToleratesTheDriftAContainerRewriteReallyCauses(t *testing.T) {
	tests := []struct {
		name    string
		outSecs float64
		srcSecs float64
		opt     VerifyOptions
		wantOK  bool
	}{
		{"a couple of frames short", 3599.96, 3600, VerifyOptions{}, true},
		{"a couple of frames long", 3600.04, 3600, VerifyOptions{}, true},
		{"half a percent of an hour", 3582, 3600, VerifyOptions{}, true},
		{"one percent of an hour", 3564, 3600, VerifyOptions{}, false},
		{"a second on a ten-second clip, via the absolute floor", 9.2, 10, VerifyOptions{}, true},
		{"two seconds on a ten-second clip", 8, 10, VerifyOptions{}, false},
		{"a tightened tolerance", 3599.5, 3600, VerifyOptions{DurationToleranceSeconds: 0.1, DurationTolerancePercent: 0.001}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, out := goodPair()
			src.DurationSeconds, out.DurationSeconds = tc.srcSecs, tc.outSecs

			v := VerifyArchive(src, out, nil, tc.opt)
			if v.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (%v)", v.OK, tc.wantOK, v.Reasons)
			}
		})
	}
}

func TestVerifyArchiveComparesTracksByIndexNotByProbeOrder(t *testing.T) {
	src, out := goodPair()
	// Same three tracks, listed backwards.
	out.Audio = []TrackSummary{out.Audio[2], out.Audio[0], out.Audio[1]}

	if v := VerifyArchive(src, out, nil, VerifyOptions{}); !v.OK {
		t.Fatalf("a reordered probe listing was treated as damage: %v", v.Reasons)
	}
}

func TestVerifyArchiveQuotesOnlyTheFirstFewDecodeErrors(t *testing.T) {
	src, out := goodPair()
	errs := make([]string, 500)
	for i := range errs {
		errs[i] = "error " + strings.Repeat("x", 3)
	}
	errs[0] = "first distinct error"

	v := VerifyArchive(src, out, errs, VerifyOptions{})
	if v.OK {
		t.Fatal("a copy that does not decode was accepted")
	}
	joined := strings.Join(v.Reasons, " ")
	if !strings.Contains(joined, "first distinct error") {
		t.Fatalf("the first error was not reported: %v", v.Reasons)
	}
	if strings.Count(joined, "error xxx") > MaxReportedDecodeErrors {
		t.Fatalf("the whole transcript was quoted back: %v", v.Reasons)
	}
}

func TestVerifyArchiveNotesRatherThanFailsARecordingWithNoAudio(t *testing.T) {
	src := FileSummary{Bytes: 100, DurationSeconds: 60, VideoCodec: "h264"}
	out := FileSummary{Bytes: 50, DurationSeconds: 60, VideoCodec: "hevc"}

	v := VerifyArchive(src, out, nil, VerifyOptions{})
	if !v.OK {
		t.Fatalf("a video-only recording was refused: %v", v.Reasons)
	}
	if len(v.Notes) == 0 {
		t.Fatal("the missing audio was not even noted")
	}
}

// Two things going wrong must both be reported: an operator who fixes only the
// one they were told about runs the job again and hits the second.
func TestVerifyArchiveReportsEveryReasonNotJustTheFirst(t *testing.T) {
	src, out := goodPair()
	out.Audio = out.Audio[:1]
	out.DurationSeconds = 100
	out.Bytes = src.Bytes * 2

	v := VerifyArchive(src, out, []string{"decode blew up"}, VerifyOptions{})
	if len(v.Reasons) < 4 {
		t.Fatalf("only %d reasons reported: %v", len(v.Reasons), v.Reasons)
	}
}
