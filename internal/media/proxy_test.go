package media

import (
	"strings"
	"testing"
)

// The proxy exists so a browser can scrub a recording it cannot otherwise open.
// Everything asserted here is load-bearing for that one sentence.
func TestProxyArgsCarriesTheFlagsThatMakeAProxyPlayableInABrowser(t *testing.T) {
	args := ProxyArgs(ProxySpec{Input: "/rec/rec-1.mkv", Output: "/rec/media/rec-1/proxy.mp4"})

	// Without +faststart the moov atom is written last and the browser must
	// download the whole file before it can show frame one or seek anywhere.
	// This is the single flag the feature stands on.
	mustArg(t, args, "-movflags", "+faststart")
	// A 10-bit or 4:2:2 master would otherwise yield a High10/422 proxy no
	// browser decodes, which is the exact problem the proxy solves.
	mustArg(t, args, "-pix_fmt", "yuv420p")
	mustArg(t, args, "-c:v", "libx264")
	mustArg(t, args, "-c:a", "aac")
	mustArg(t, args, "-f", "mp4")
	if args[len(args)-1] != "/rec/media/rec-1/proxy.mp4" {
		t.Fatalf("output is not last: %v", args)
	}
	if !hasArg(args, "-y") {
		t.Fatal("without -y a leftover .partial makes the retry hang on the overwrite prompt")
	}
	if !hasArg(args, "-progress") {
		t.Fatal("no -progress, so the job would show no progress bar")
	}
}

func TestProxyArgsMapsExactlyOneVideoAndOneAudioTrack(t *testing.T) {
	args := ProxyArgs(ProxySpec{Input: "in.mkv", Output: "out.mp4", AudioTrack: 3})

	if n := countArg(args, "-map"); n != 2 {
		t.Fatalf("-map appears %d times, want 2", n)
	}
	if !hasArg(args, "0:v:0") {
		t.Fatalf("video is not explicitly mapped: %v", args)
	}
	// The '?' is what turns "this recording has fewer tracks than the caller
	// believed" from a failed job into a silent proxy.
	if !hasArg(args, "0:a:3?") {
		t.Fatalf("audio track 3 is not optionally mapped: %v", args)
	}
	mustArg(t, args, "-ac", "2")
}

func TestProxyArgsWithANegativeTrackProducesASilentProxy(t *testing.T) {
	args := ProxyArgs(ProxySpec{Input: "in.mkv", Output: "out.mp4", AudioTrack: -1})

	if !hasArg(args, "-an") {
		t.Fatalf("-an is missing: %v", args)
	}
	if n := countArg(args, "-map"); n != 1 {
		t.Fatalf("-map appears %d times, want 1 (video only)", n)
	}
	if hasArg(args, "-c:a") {
		t.Fatalf("a silent proxy still names an audio codec: %v", args)
	}
}

func TestProxyArgsDefaultsToASmallHeightAndDerivesTheWidth(t *testing.T) {
	tests := []struct {
		name      string
		spec      ProxySpec
		wantScale string
	}{
		{"neither dimension set", ProxySpec{}, "scale=-2:360"},
		{"height only", ProxySpec{Height: 240}, "scale=-2:240"},
		{"width only", ProxySpec{Width: 640}, "scale=640:-2"},
		{"both", ProxySpec{Width: 640, Height: 360}, "scale=640:360"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Input, tc.spec.Output = "in.mkv", "out.mp4"
			got, ok := argAfter(ProxyArgs(tc.spec), "-vf")
			if !ok {
				t.Fatal("no -vf")
			}
			if got != tc.wantScale {
				t.Fatalf("-vf = %q, want %q", got, tc.wantScale)
			}
		})
	}
}

// The keyframe interval IS the scrub granularity: a player cannot seek between
// keyframes without decoding forward from the last one.
func TestProxyArgsForcesATimeBasedKeyframeInterval(t *testing.T) {
	tests := []struct {
		name string
		gop  float64
		want string
	}{
		{"default", 0, "expr:gte(t,n_forced*2)"},
		{"explicit", 4, "expr:gte(t,n_forced*4)"},
		{"fractional", 0.5, "expr:gte(t,n_forced*0.5)"},
		{"negative falls back to the default", -3, "expr:gte(t,n_forced*2)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := ProxyArgs(ProxySpec{Input: "in.mkv", Output: "out.mp4", GOPSeconds: tc.gop})
			mustArg(t, args, "-force_key_frames", tc.want)
			// Counted in seconds rather than frames, because the master's frame
			// rate is not known here and -g N would mean 10s on a 60fps source.
			if hasArg(args, "-g") {
				t.Fatalf("-g would tie the interval to an unknown frame rate: %v", args)
			}
		})
	}
}

func TestProxyArgsChoosesCRFForSoftwareEncodersAndBitrateForTheRest(t *testing.T) {
	tests := []struct {
		name        string
		spec        ProxySpec
		wantCRF     string
		wantBitrate string
	}{
		{"default libx264", ProxySpec{}, "28", ""},
		{"explicit crf", ProxySpec{CRF: 32}, "32", ""},
		{"crf above the ceiling is clamped", ProxySpec{CRF: 99}, "51", ""},
		{"an explicit bitrate wins over crf", ProxySpec{CRF: 32, VideoKbps: 500}, "", "500k"},
		{"a hardware encoder has no crf we can trust", ProxySpec{Encoder: "h264_nvenc"}, "", "700k"},
		{"an encoder nobody has heard of", ProxySpec{Encoder: "h264_weird"}, "", "700k"},
		{"libx265 counts crf the same way", ProxySpec{Encoder: "libx265"}, "28", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Input, tc.spec.Output = "in.mkv", "out.mp4"
			args := ProxyArgs(tc.spec)

			crf, hasCRF := argAfter(args, "-crf")
			bv, hasBV := argAfter(args, "-b:v")
			switch {
			case tc.wantCRF != "":
				if !hasCRF || crf != tc.wantCRF {
					t.Fatalf("-crf = %q (present %v), want %q", crf, hasCRF, tc.wantCRF)
				}
				if hasBV {
					t.Fatalf("both -crf and -b:v were emitted: %v", args)
				}
			default:
				if !hasBV || bv != tc.wantBitrate {
					t.Fatalf("-b:v = %q (present %v), want %q", bv, hasBV, tc.wantBitrate)
				}
				if hasCRF {
					t.Fatalf("a quality flag was invented for %q: %v", tc.spec.Encoder, args)
				}
				mustArg(t, args, "-maxrate", tc.wantBitrate)
			}
		})
	}
}

func TestProxyArgsOnlyPresetsAnEncoderThatWasAskedForOne(t *testing.T) {
	tests := []struct {
		name    string
		spec    ProxySpec
		want    string
		wantAny bool
	}{
		{"libx264 gets the fast default", ProxySpec{}, DefaultProxyPreset, true},
		{"an explicit preset wins", ProxySpec{Preset: "slow"}, "slow", true},
		{"another encoder is left alone", ProxySpec{Encoder: "h264_videotoolbox"}, "", false},
		{"unless the operator names one", ProxySpec{Encoder: "h264_nvenc", Preset: "p4"}, "p4", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Input, tc.spec.Output = "in.mkv", "out.mp4"
			got, ok := argAfter(ProxyArgs(tc.spec), "-preset")
			if ok != tc.wantAny {
				t.Fatalf("-preset present = %v, want %v", ok, tc.wantAny)
			}
			if ok && got != tc.want {
				t.Fatalf("-preset = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProxyArgsKeepsSubtitlesChaptersAndAttachmentsOutOfTheMP4(t *testing.T) {
	args := ProxyArgs(ProxySpec{Input: "in.mkv", Output: "out.mp4"})
	for _, want := range []string{"-sn", "-dn"} {
		if !hasArg(args, want) {
			t.Fatalf("%s is missing: %v", want, args)
		}
	}
	mustArg(t, args, "-map_chapters", "-1")
}

func TestProxyArgsCapsTheFrameRateOnlyWhenAsked(t *testing.T) {
	if _, ok := argAfter(ProxyArgs(ProxySpec{Input: "in.mkv", Output: "out.mp4"}), "-r"); ok {
		t.Fatal("-r was emitted without being asked for; the proxy would silently resample time")
	}
	args := ProxyArgs(ProxySpec{Input: "in.mkv", Output: "out.mp4", FPS: 29.97})
	mustArg(t, args, "-r", "29.97")
}

// The input has to be an -i and not, say, spliced into a filter graph, or an
// operator reading a failed job's log has no idea what it was working on.
func TestProxyArgsNamesTheInputAfterTheGlobalFlags(t *testing.T) {
	args := ProxyArgs(ProxySpec{Input: "/rec/rec-1.mkv", Output: "out.mp4"})
	i := argIndex(args, "-i")
	if i < 0 || args[i+1] != "/rec/rec-1.mkv" {
		t.Fatalf("input is not named by -i: %v", args)
	}
	if j := argIndex(args, "-map"); j < i {
		t.Fatalf("-map precedes -i, so it would apply to no input: %v", args)
	}
	if strings.Contains(strings.Join(args, " "), "-filter_complex") {
		t.Fatalf("the proxy does not need a filter graph: %v", args)
	}
}
