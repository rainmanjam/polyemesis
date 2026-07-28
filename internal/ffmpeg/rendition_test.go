package ffmpeg

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// baseRendition is a spec with only the plumbing filled in, so each test can
// state exactly the one thing it pins.
func baseRendition() RenditionSpec {
	return RenditionSpec{
		InRelayURL:  "udp://127.0.0.1:20000",
		OutRelayURL: "udp://127.0.0.1:20010",
		Height:      1080,
		FPS:         60,
		VideoKbps:   6000,
	}
}

// ------------------------------------------------------------ the whole point

// A rendition that encodes audio destroys per-destination routing: the
// destinations downstream would be mixing an already-mixed stereo fold-down.
func TestRenditionArgsCopiesEveryAudioTrackUntouched(t *testing.T) {
	args := RenditionArgs(baseRendition())
	s := join(args)

	maps := allAfter(args, "-map")
	if len(maps) != 2 {
		t.Fatalf("want exactly 2 maps (video + all audio), got %v", maps)
	}
	if maps[0] != "0:v:0" {
		t.Errorf("video map = %q, want 0:v:0 explicitly", maps[0])
	}
	if maps[1] != "0:a" {
		t.Errorf("audio map = %q, want 0:a so every track survives to the destinations", maps[1])
	}
	if c, _ := argsAfter(args, "-c:a"); c != "copy" {
		t.Fatalf("-c:a = %q, want copy. Audio must never be encoded in a rendition.", c)
	}
	// Anything that would decode, mix, resample or encode audio is a bug, not
	// a style choice.
	for _, bad := range []string{"aac", "libfdk", "libopus", "libmp3lame", "-b:a", "-ar", "-ac", "-af", "-filter_complex", "pan=", "amerge", "aresample"} {
		if strings.Contains(s, bad) {
			t.Errorf("audio must pass through untouched, found %q in: %s", bad, s)
		}
	}
}

func TestRenditionArgsEncodesVideoIntoItsOwnHub(t *testing.T) {
	args := RenditionArgs(baseRendition())
	s := join(args)

	if v, _ := argsAfter(args, "-c:v"); v != EncoderX264 {
		t.Errorf("-c:v = %q, want the default software encoder", v)
	}
	// The input is a relay subscription and needs the same overflow tolerance
	// every other consumer gets; one stalled rendition must not kill the hub.
	in, _ := argsAfter(args, "-i")
	if !strings.Contains(in, "overrun_nonfatal=1") || !strings.Contains(in, "fifo_size=") {
		t.Errorf("input = %q, want RelayInputURL tolerances", in)
	}
	if !strings.HasPrefix(in, "udp://127.0.0.1:20000") {
		t.Errorf("input = %q, want the ingest hub", in)
	}
	// The output is the rendition's OWN hub, which its destinations subscribe
	// to; TS-aligned datagrams are what the hub expects.
	last := args[len(args)-1]
	if last != "udp://127.0.0.1:20010?pkt_size=1316" {
		t.Errorf("output = %q, want the rendition hub with pkt_size", last)
	}
	if f, _ := argsAfter(args, "-f"); f != "mpegts" {
		t.Errorf("-f = %q, want mpegts: only TS carries every audio track", f)
	}
	if !has(args, "-nostdin") {
		t.Error("missing -nostdin")
	}
	if p, _ := argsAfter(args, "-progress"); p != "pipe:1" {
		t.Errorf("-progress = %q, want pipe:1 like every other supervised child", p)
	}
	if !strings.Contains(s, "-flush_packets 1") {
		t.Errorf("loopback hop must not buffer partial TS packets: %s", s)
	}
}

// ------------------------------------------------------------------- scaling

func TestRenditionArgsScale(t *testing.T) {
	tests := []struct {
		name   string
		w, h   int
		wantVF string // "" means the filter must be absent entirely
	}{
		{"both dimensions set are used verbatim", 1920, 1080, "scale=1920:1080"},
		{"width only derives an even height", 1280, 0, "scale=1280:-2"},
		{"height only derives an even width", 0, 720, "scale=-2:720"},
		{"neither set pays for no scale filter at all", 0, 0, ""},
		{"negative values are treated as unset", -1, -1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseRendition()
			s.Width, s.Height = tc.w, tc.h
			args := RenditionArgs(s)

			got, ok := argsAfter(args, "-vf")
			if tc.wantVF == "" {
				if ok {
					t.Fatalf("-vf = %q, want no filter at all", got)
				}
				return
			}
			if !ok {
				t.Fatalf("missing -vf, want %q: %s", tc.wantVF, join(args))
			}
			if got != tc.wantVF {
				t.Errorf("-vf = %q, want %q", got, tc.wantVF)
			}
			// -1 can land on an odd dimension, which H.264 4:2:0 refuses.
			if strings.Contains(got, ":-1") || strings.Contains(got, "=-1") {
				t.Errorf("-vf = %q, must derive with -2 so the result stays even", got)
			}
		})
	}
}

// --------------------------------------------------------------- dual format

// The whole backwards-compatibility promise of this feature in one test: a
// rendition saved before dual-format existed has no aspect mode, and must
// still compile to the exact same filter string it always did.
func TestRenditionArgsAspectDefaultIsTheOldScale(t *testing.T) {
	tests := []struct {
		name   string
		aspect AspectMode
	}{
		{"the zero value is the historical stretch", AspectMode("")},
		{"the named stretch mode is the same zero value", AspectStretch},
		// A row written by a newer build, or edited by hand, must still encode.
		// Refusing is the restrictive-direction mistake this repo keeps paying
		// for; a stream in the old shape beats a stream that never starts.
		{"an unrecognised mode degrades to the plain scale", AspectMode("fit-to-hexagon")},
		{"a near-miss spelling degrades rather than refuses", AspectMode("Crop")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseRendition()
			s.Width, s.Height = 1920, 1080
			s.Aspect = tc.aspect
			got, ok := argsAfter(RenditionArgs(s), "-vf")
			if !ok || got != "scale=1920:1080" {
				t.Errorf("-vf = %q (present=%v), want the unchanged scale=1920:1080", got, ok)
			}
		})
	}
}

// An aspect conversion is defined by the TARGET SHAPE. With one axis left on
// "keep the source's" there is no shape to convert to, so the rendition falls
// back to the plain scale instead of inventing one.
func TestRenditionArgsAspectNeedsBothDimensions(t *testing.T) {
	tests := []struct {
		name   string
		w, h   int
		wantVF string
	}{
		{"height only cannot know the target shape", 0, 1920, "scale=-2:1920"},
		{"width only cannot know the target shape", 1080, 0, "scale=1080:-2"},
		{"neither set is still no filter at all", 0, 0, ""},
		{"negative values are treated as unset", -1080, -1920, ""},
	}
	for _, mode := range []AspectMode{AspectCrop, AspectPad, AspectBlurredPad} {
		for _, tc := range tests {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				s := baseRendition()
				s.Width, s.Height, s.Aspect = tc.w, tc.h, mode
				got, ok := argsAfter(RenditionArgs(s), "-vf")
				if tc.wantVF == "" {
					if ok {
						t.Fatalf("-vf = %q, want no filter at all", got)
					}
					return
				}
				if !ok || got != tc.wantVF {
					t.Errorf("-vf = %q (present=%v), want %q", got, ok, tc.wantVF)
				}
			})
		}
	}
}

// The exact filter strings, pinned. These are verified against a real FFmpeg in
// TestAspectFiltersAgainstRealFFmpeg; this test is what tells you which edit
// changed them.
func TestRenditionArgsAspectFilterStrings(t *testing.T) {
	tests := []struct {
		name   string
		mode   AspectMode
		w, h   int
		color  string
		wantVF string
	}{
		{
			name: "crop landscape source to a vertical rendition",
			mode: AspectCrop, w: 1080, h: 1920,
			wantVF: `crop=2*floor(min(iw\,ih*1080/1920)/2):2*floor(min(ih\,iw*1920/1080)/2),scale=1080:1920,setsar=1`,
		},
		{
			// The same expression, unchanged, running the other way: min() picks
			// the side that has to give, so there is no direction to get wrong.
			name: "crop vertical source to a landscape rendition",
			mode: AspectCrop, w: 1920, h: 1080,
			wantVF: `crop=2*floor(min(iw\,ih*1920/1080)/2):2*floor(min(ih\,iw*1080/1920)/2),scale=1920:1080,setsar=1`,
		},
		{
			name: "pad letterboxes onto black by default",
			mode: AspectPad, w: 1080, h: 1920,
			wantVF: "scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2," +
				"pad=1080:1920:2*floor((ow-iw)/2/2):2*floor((oh-ih)/2/2):black,setsar=1",
		},
		{
			name: "pad honours a named colour",
			mode: AspectPad, w: 1920, h: 1080, color: "white",
			wantVF: "scale=1920:1080:force_original_aspect_ratio=decrease:force_divisible_by=2," +
				"pad=1920:1080:2*floor((ow-iw)/2/2):2*floor((oh-ih)/2/2):white,setsar=1",
		},
		{
			name: "blurred pad composites the frame onto a blurred copy of itself",
			mode: AspectBlurredPad, w: 1080, h: 1920,
			wantVF: "split=2[bgsrc][fgsrc];" +
				"[bgsrc]scale=134:240:force_original_aspect_ratio=increase:force_divisible_by=2," +
				"crop=134:240,gblur=sigma=4,scale=1080:1920,setsar=1[bg];" +
				"[fgsrc]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2[fg];" +
				"[bg][fg]overlay=2*floor((W-w)/2/2):2*floor((H-h)/2/2)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseRendition()
			s.Width, s.Height, s.Aspect, s.PadColor = tc.w, tc.h, tc.mode, tc.color
			args := RenditionArgs(s)

			got, ok := argsAfter(args, "-vf")
			if !ok {
				t.Fatalf("missing -vf: %s", join(args))
			}
			if got != tc.wantVF {
				t.Errorf("-vf =\n\t%q\nwant\n\t%q", got, tc.wantVF)
			}
			// The aspect chain already lands on exactly Width x Height; a second
			// scale on the end would be a redundant resize.
			if strings.Count(got, "scale=") != strings.Count(tc.wantVF, "scale=") {
				t.Errorf("-vf has an extra scale: %q", got)
			}
			// Whatever the video path does, audio is still every track, copied.
			if c, _ := argsAfter(args, "-c:a"); c != "copy" {
				t.Errorf("-c:a = %q, want copy: aspect conversion is a video filter", c)
			}
			if maps := allAfter(args, "-map"); len(maps) != 2 || maps[1] != "0:a" {
				t.Errorf("maps = %v, want the video stream plus every audio track", maps)
			}
		})
	}
}

// H.264 encodes 4:2:0 chroma at half resolution, so an odd width or height has
// no representable chroma plane and the encoder refuses to open with "width not
// divisible by 2". Every dimension in an aspect chain is therefore either an
// even literal or guarded, and there is no other way to get one.
func TestRenditionArgsAspectDimensionsStayEven(t *testing.T) {
	sizes := []struct{ w, h int }{
		{1080, 1920}, {1920, 1080}, {720, 1280}, {608, 1080}, {128, 128}, {2160, 3840},
	}
	for _, mode := range []AspectMode{AspectCrop, AspectPad, AspectBlurredPad} {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s/%dx%d", mode, sz.w, sz.h), func(t *testing.T) {
				s := baseRendition()
				s.Width, s.Height, s.Aspect = sz.w, sz.h, mode
				vf, ok := argsAfter(RenditionArgs(s), "-vf")
				if !ok {
					t.Fatalf("missing -vf")
				}

				// Every dimension is either an even literal or an expression
				// that rounds itself even. There is no third option, because
				// the third option is a stream that will not start.
				for _, arg := range sizingArgs(vf) {
					if n, err := strconv.Atoi(arg); err == nil {
						if n%2 != 0 {
							t.Errorf("odd dimension %d in %q", n, vf)
						}
						if n < 2 {
							t.Errorf("zero-sized filter output in %q", vf)
						}
						continue
					}
					if !strings.HasPrefix(arg, "2*floor(") {
						t.Errorf("dimension %q is neither an even literal nor rounded even: %q", arg, vf)
					}
				}
				// Composite offsets land on the chroma grid for the same reason
				// the dimensions do.
				for _, arg := range filterArgs(vf, "overlay=", 2) {
					if !strings.HasPrefix(arg, "2*floor(") {
						t.Errorf("overlay offset %q is not rounded even: %q", arg, vf)
					}
				}
				// -1 rounds to whatever the arithmetic lands on, which is odd
				// half the time; -2 and force_divisible_by=2 are the two ways
				// to derive a side that stays legal.
				if strings.Contains(vf, ":-1") || strings.Contains(vf, "=-1") {
					t.Errorf("-1 derivation can land on an odd side: %q", vf)
				}
				if strings.Contains(vf, "force_original_aspect_ratio") &&
					strings.Count(vf, "force_original_aspect_ratio") != strings.Count(vf, "force_divisible_by=2") {
					t.Errorf("an aspect-preserving scale derives a side without pinning it even: %q", vf)
				}
			})
		}
	}
}

// The blur runs on a shrunken copy because a gaussian wide enough to look
// deliberate costs more per frame at 1080p than the H.264 encode it feeds.
func TestBlurProxySize(t *testing.T) {
	tests := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"a vertical 1080p target blurs at an eighth", 1080, 1920, 134, 240},
		{"a horizontal 1080p target blurs at an eighth", 1920, 1080, 240, 134},
		{"an odd eighth rounds down to stay a legal frame", 1000, 1000, 124, 124},
		// Blurring a dozen pixels turns the background into one flat colour,
		// which is the "lazy" look this mode exists to avoid.
		{"a small target holds a floor rather than blurring to mush", 256, 256, 32, 32},
		{"the floor applies per axis", 1920, 128, 240, 32},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, h := blurProxySize(tc.w, tc.h)
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("blurProxySize(%d, %d) = %dx%d, want %dx%d", tc.w, tc.h, w, h, tc.wantW, tc.wantH)
			}
			if w%2 != 0 || h%2 != 0 {
				t.Errorf("proxy %dx%d is not a legal 4:2:0 frame", w, h)
			}
		})
	}
}

// The colour lands inside a filter argument, where a comma or a colon silently
// re-cuts the whole chain into different filters. A mistyped colour must not be
// the reason a live stream will not start, so the answer is black, not an error.
func TestPadColorFallsBackToBlackRatherThanRefusing(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty is black", "", "black"},
		{"a colour name is passed through", "white", "white"},
		{"a hex triplet is passed through", "#1a2b3c", "#1a2b3c"},
		{"FFmpeg's 0x form is passed through", "0xFF00FF", "0xFF00FF"},
		{"surrounding space is trimmed", "  navy  ", "navy"},
		{"a comma would start a new filter", "black,scale=2:2", "black"},
		{"a colon would start a new argument", "black:0.5", "black"},
		{"a filter-graph separator is refused", "black;anullsrc", "black"},
		{"an alpha suffix has nothing behind it to show", "black@0.5", "black"},
		{"a hash anywhere but the front is not a colour", "bl#ack", "black"},
		{"a quote is not a colour", "'red'", "black"},
		{"an absurd length is not a colour", strings.Repeat("a", 25), "black"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := padColor(tc.in); got != tc.want {
				t.Errorf("padColor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// PadColor is only meaningful where there are bars to fill. It must not leak
// into the modes that have none.
func TestRenditionArgsPadColorIsIgnoredByTheOtherModes(t *testing.T) {
	for _, mode := range []AspectMode{AspectStretch, AspectCrop, AspectBlurredPad} {
		t.Run(string(mode), func(t *testing.T) {
			s := baseRendition()
			s.Width, s.Height, s.Aspect, s.PadColor = 1080, 1920, mode, "magenta"
			vf, _ := argsAfter(RenditionArgs(s), "-vf")
			if strings.Contains(vf, "magenta") {
				t.Errorf("-vf = %q, want no pad colour: %s has no bars to fill", vf, mode)
			}
		})
	}
}

// VAAPI encodes from GPU surfaces. The aspect work happens in software and the
// upload has to stay on the end of it, which only works because every mode's
// graph ends in a single linear chain.
func TestRenditionArgsAspectUploadsForVAAPI(t *testing.T) {
	for _, mode := range []AspectMode{AspectCrop, AspectPad, AspectBlurredPad} {
		t.Run(string(mode), func(t *testing.T) {
			s := baseRendition()
			s.Width, s.Height, s.Aspect = 1080, 1920, mode
			s.Encoder = EncoderVAAPI
			vf, ok := argsAfter(RenditionArgs(s), "-vf")
			if !ok {
				t.Fatal("missing -vf")
			}
			if !strings.HasSuffix(vf, ",format=nv12,hwupload") {
				t.Errorf("-vf = %q, want the hwupload tail on the end", vf)
			}
			// A tail after a ';' would be a new, unconnected chain rather than
			// the last link of this one.
			if tail := vf[strings.LastIndex(vf, ";")+1:]; strings.Contains(tail, "[") &&
				!strings.HasPrefix(tail, "[") {
				t.Errorf("-vf = %q, hwupload must extend the final chain", vf)
			}
		})
	}
}

// sizingArgs returns the width and height argument of every filter in the chain
// that decides how big a frame is.
func sizingArgs(vf string) []string {
	var out []string
	for _, name := range []string{"scale=", "crop=", "pad="} {
		out = append(out, filterArgs(vf, name, 2)...)
	}
	return out
}

// filterArgs pulls the first n positional arguments of every `name` filter in a
// chain.
//
// The chain is walked rather than regexed because the arithmetic contains its
// own commas — escaped as `\,` so FFmpeg does not read them as the start of the
// next filter — and a naive split would cut an expression in half.
func filterArgs(vf, name string, n int) []string {
	var out []string
	for rest := vf; ; {
		i := strings.Index(rest, name)
		if i < 0 {
			return out
		}
		rest = rest[i+len(name):]

		end := len(rest)
		for j := 0; j < len(rest); j++ {
			if (rest[j] == ',' || rest[j] == ';') && (j == 0 || rest[j-1] != '\\') {
				end = j
				break
			}
		}
		args := strings.Split(rest[:end], ":")
		for k := 0; k < n && k < len(args); k++ {
			out = append(out, args[k])
		}
	}
}

// ---------------------------------------------------------------- frame rate

func TestRenditionArgsFrameRate(t *testing.T) {
	tests := []struct {
		name  string
		fps   float64
		wantR string // "" means -r must be absent
	}{
		{"zero keeps the source rate", 0, ""},
		{"integer rate stays integer", 30, "30"},
		{"60 is passed straight through", 60, "60"},
		{"NTSC rate keeps its fraction", 29.97, "29.97"},
		{"NTSC 60 keeps its fraction", 59.94, "59.94"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseRendition()
			s.FPS = tc.fps
			args := RenditionArgs(s)

			got, ok := argsAfter(args, "-r")
			if tc.wantR == "" {
				if ok {
					t.Fatalf("-r = %q, want the source rate untouched", got)
				}
				return
			}
			if !ok || got != tc.wantR {
				t.Errorf("-r = %q (present=%v), want %q", got, ok, tc.wantR)
			}
		})
	}
}

// --------------------------------------------------------------- GOP forcing

// The forced GOP is a real benefit of the feature: with -c:v copy the user
// inherits OBS's keyframe interval, and a long one breaks platform packaging.
func TestRenditionArgsGOPArithmetic(t *testing.T) {
	tests := []struct {
		name       string
		fps        float64
		sourceFPS  float64
		gopSeconds float64
		want       string
	}{
		{"60fps at 2s is 120 frames", 60, 0, 2, "120"},
		{"30fps at 2s is 60 frames", 30, 0, 2, "60"},
		{"30fps at 4s is 120 frames", 30, 0, 4, "120"},
		{"fractional rate rounds to a whole frame count", 29.97, 0, 2, "60"},
		{"unset GOP defaults to 2 seconds", 50, 0, 0, "100"},
		{"source rate is used when the rendition keeps it", 0, 24, 2, "48"},
		{"unknown rate assumes 30fps rather than guessing high", 0, 0, 2, "60"},
		{"a sub-frame interval still forces at least one frame", 30, 0, 0.01, "1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseRendition()
			s.FPS, s.SourceFPS, s.GOPSeconds = tc.fps, tc.sourceFPS, tc.gopSeconds
			args := RenditionArgs(s)

			g, ok := argsAfter(args, "-g")
			if !ok || g != tc.want {
				t.Errorf("-g = %q (present=%v), want %q", g, ok, tc.want)
			}
			// keyint_min must match -g exactly, or the encoder is free to emit
			// keyframes early and the interval stops being fixed.
			if k, _ := argsAfter(args, "-keyint_min"); k != tc.want {
				t.Errorf("-keyint_min = %q, want the same as -g (%q)", k, tc.want)
			}
			// Scene-cut detection would insert unscheduled keyframes and
			// desync platform-side segmenting.
			if sc, _ := argsAfter(args, "-sc_threshold"); sc != "0" {
				t.Errorf("-sc_threshold = %q, want 0", sc)
			}
		})
	}
}

// ------------------------------------------------------------------ bitrates

func TestRenditionArgsBitrateCapping(t *testing.T) {
	tests := []struct {
		name                        string
		kbps, maxrate, bufsize      int
		wantB, wantMaxrate, wantBuf string
	}{
		{"explicit values are used verbatim", 6000, 6500, 9000, "6000k", "6500k", "9000k"},
		{"maxrate defaults to the target so the stream stays capped", 4000, 0, 0, "4000k", "4000k", "8000k"},
		{"bufsize defaults to twice maxrate", 4000, 5000, 0, "4000k", "5000k", "10000k"},
		{"an unset bitrate falls back to a conservative default", 0, 0, 0, "4500k", "4500k", "9000k"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseRendition()
			s.VideoKbps, s.MaxrateKbps, s.BufsizeKbps = tc.kbps, tc.maxrate, tc.bufsize
			args := RenditionArgs(s)

			for flag, want := range map[string]string{
				"-b:v":     tc.wantB,
				"-maxrate": tc.wantMaxrate,
				"-bufsize": tc.wantBuf,
			} {
				if got, _ := argsAfter(args, flag); got != want {
					t.Errorf("%s = %q, want %q", flag, got, want)
				}
			}
		})
	}
}

// ------------------------------------------------------------------ encoders

func TestRenditionArgsPerEncoderFlags(t *testing.T) {
	tests := []struct {
		name       string
		encoder    string
		preset     string
		wantPreset string // flag+value that must appear, "" if none may
		wantFlags  []string
		absent     []string
	}{
		{
			name:    "x264 gets a preset and a browser-safe pixel format",
			encoder: EncoderX264, wantPreset: "-preset veryfast",
			wantFlags: []string{"-pix_fmt yuv420p", "-profile:v high"},
		},
		{
			name:    "an explicit preset overrides the default",
			encoder: EncoderX264, preset: "slow", wantPreset: "-preset slow",
		},
		{
			name:    "nvenc uses the p-series preset and CBR rate control",
			encoder: EncoderNVENC, wantPreset: "-preset p4",
			wantFlags: []string{"-rc cbr"},
		},
		{
			name:    "qsv takes a named preset",
			encoder: EncoderQSV, wantPreset: "-preset veryfast",
		},
		{
			// Passing -preset here makes FFmpeg warn about an unused option on
			// every single restart.
			name:    "videotoolbox has no preset and runs in realtime mode",
			encoder: EncoderVideoToolbox, wantPreset: "",
			wantFlags: []string{"-realtime 1"},
			absent:    []string{"-preset", "-quality"},
		},
		{
			name:    "amf spells its preset -quality",
			encoder: EncoderAMF, wantPreset: "-quality speed",
			wantFlags: []string{"-usage transcoding"},
			absent:    []string{"-preset"},
		},
		{
			name:    "vaapi takes neither preset nor profile",
			encoder: EncoderVAAPI, wantPreset: "",
			absent: []string{"-preset", "-quality", "-profile:v"},
		},
		{
			// A hand-typed encoder must still produce a usable command line.
			name:    "an unknown encoder gets only universal flags",
			encoder: "h264_omx", wantPreset: "",
			absent: []string{"-preset", "-quality", "-rc", "-realtime"},
		},
		{
			name:    "an unknown encoder still honours an explicit preset",
			encoder: "h264_omx", preset: "fast", wantPreset: "-preset fast",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseRendition()
			s.Encoder, s.Preset = tc.encoder, tc.preset
			args := RenditionArgs(s)
			line := join(args)

			if v, _ := argsAfter(args, "-c:v"); v != tc.encoder {
				t.Errorf("-c:v = %q, want %q", v, tc.encoder)
			}
			if tc.wantPreset != "" && !strings.Contains(line, tc.wantPreset) {
				t.Errorf("missing %q in: %s", tc.wantPreset, line)
			}
			for _, want := range tc.wantFlags {
				if !strings.Contains(line, want) {
					t.Errorf("missing %q in: %s", want, line)
				}
			}
			for _, bad := range tc.absent {
				if has(args, bad) {
					t.Errorf("%s must not be passed to %s: %s", bad, tc.encoder, line)
				}
			}
			// No encoder choice may ever leak into the audio path.
			if c, _ := argsAfter(args, "-c:a"); c != "copy" {
				t.Errorf("-c:a = %q, want copy for every encoder", c)
			}
		})
	}
}

func TestRenditionArgsVAAPINeedsDeviceAndUpload(t *testing.T) {
	tests := []struct {
		name       string
		device     string
		w, h       int
		wantDevice string
		wantVF     string
	}{
		{"default render node when unset", "", 0, 720, defaultVAAPIDevice, "scale=-2:720,format=nv12,hwupload"},
		{"explicit device is honoured", "/dev/dri/renderD129", 1280, 720, "/dev/dri/renderD129", "scale=1280:720,format=nv12,hwupload"},
		// VAAPI encodes from GPU surfaces, so the upload is required even when
		// nothing is being resized.
		{"unscaled vaapi still uploads", "", 0, 0, defaultVAAPIDevice, "format=nv12,hwupload"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseRendition()
			s.Encoder, s.VAAPIDevice = EncoderVAAPI, tc.device
			s.Width, s.Height = tc.w, tc.h
			args := RenditionArgs(s)

			if d, _ := argsAfter(args, "-vaapi_device"); d != tc.wantDevice {
				t.Errorf("-vaapi_device = %q, want %q", d, tc.wantDevice)
			}
			if vf, _ := argsAfter(args, "-vf"); vf != tc.wantVF {
				t.Errorf("-vf = %q, want %q", vf, tc.wantVF)
			}
			// The device must exist before the filter graph that uploads into
			// it is configured.
			dev, in := indexOf(args, "-vaapi_device"), indexOf(args, "-i")
			if dev < 0 || dev > in {
				t.Errorf("-vaapi_device must precede -i: %s", join(args))
			}
		})
	}
}

// -------------------------------------------------------- encoder detection

// A trimmed but structurally exact `ffmpeg -encoders`, legend included.
const sampleEncoders = `Encoders:
 V..... = Video
 A..... = Audio
 S..... = Subtitle
 .F.... = Frame-level multithreading
 ..S... = Slice-level multithreading
 ...X.. = Codec is experimental
 ....B. = Supports draw_horiz_band
 .....D = Supports direct rendering method 1
 ------
 V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)
 V....D libx265              libx265 H.265 / HEVC (codec hevc)
 V....D hevc_nvenc           NVIDIA NVENC hevc encoder (codec hevc)
 V....D av1_nvenc            NVIDIA NVENC av1 encoder (codec av1)
 V....D h264_videotoolbox    VideoToolbox H.264 Encoder (codec h264)
 A....D aac                  AAC (Advanced Audio Coding)
 A....D libopus              libopus Opus (codec opus)
 S..... srt                  SubRip subtitle (codec subrip)
`

func TestParseVideoEncodersReadsWholeTokensOnly(t *testing.T) {
	got := parseVideoEncoders(sampleEncoders)
	want := []string{"libx264", "libx265", "hevc_nvenc", "av1_nvenc", "h264_videotoolbox"}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("encoder %d = %q, want %q", i, got[i], want[i])
		}
	}
	// The legend rows above the separator have the same leading shape as a
	// real row; "=" must never be mistaken for an encoder name.
	for _, e := range got {
		if e == "=" {
			t.Error("legend line parsed as an encoder")
		}
	}
}

func TestHasEncoderRejectsSubstringMatches(t *testing.T) {
	tools := &Tools{VideoEncoders: parseVideoEncoders(sampleEncoders)}

	tests := []struct {
		name  string
		query string
		want  bool
	}{
		{"an encoder that is present is found", EncoderX264, true},
		{"videotoolbox is found by its full name", EncoderVideoToolbox, true},
		// This is the hasProtocol bug in encoder form: the build has
		// hevc_nvenc and av1_nvenc but no H.264 NVENC whatsoever, and a
		// substring match on "nvenc" would happily claim otherwise.
		{"h264_nvenc is absent even though other nvenc encoders exist", EncoderNVENC, false},
		{"a bare family name is not an encoder", "nvenc", false},
		{"a suffix of a present encoder does not match", "x264", false},
		{"qsv is absent on this build", EncoderQSV, false},
		{"vaapi is absent on this build", EncoderVAAPI, false},
		{"amf is absent on this build", EncoderAMF, false},
		{"audio encoders are never reported as video encoders", "aac", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tools.HasEncoder(tc.query); got != tc.want {
				t.Errorf("HasEncoder(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

func TestDefaultVideoEncoderPrefersSoftware(t *testing.T) {
	tests := []struct {
		name  string
		tools *Tools
		want  string
	}{
		{
			// Hardware rate control varies by driver; x264 behaves the same
			// everywhere, so it stays the default even on a GPU box.
			name:  "x264 wins when present alongside hardware",
			tools: &Tools{VideoEncoders: []string{EncoderX264, EncoderNVENC}, HWEncoders: []string{EncoderNVENC}},
			want:  EncoderX264,
		},
		{
			name:  "hardware is used when there is no x264",
			tools: &Tools{VideoEncoders: []string{EncoderNVENC}, HWEncoders: []string{EncoderNVENC}},
			want:  EncoderNVENC,
		},
		{
			// A build with neither still gets a name it can report on, rather
			// than an empty -c:v.
			name:  "falls back to x264 when nothing was detected",
			tools: &Tools{},
			want:  EncoderX264,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tools.DefaultVideoEncoder(); got != tc.want {
				t.Errorf("DefaultVideoEncoder() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ------------------------------------------- dual format, against real FFmpeg
//
// A filter string that looks right and a filter string that runs are different
// things, and the gap between them is where "-vf accepted, encoder refused to
// open" lives. These two tests run the generated chains through the FFmpeg on
// this machine: the first proves the output is exactly the size asked for and
// legal to encode, the second proves the picture is actually placed the way the
// mode's name claims.

// aspectFilterFor is the -vf the engine would pass for this rendition.
func aspectFilterFor(t *testing.T, mode AspectMode, w, h int) string {
	t.Helper()
	s := baseRendition()
	s.Width, s.Height, s.Aspect = w, h, mode
	vf, ok := argsAfter(RenditionArgs(s), "-vf")
	if !ok {
		t.Fatalf("%s %dx%d produced no -vf", mode, w, h)
	}
	return vf
}

func needFFmpeg(t *testing.T, names ...string) []string {
	t.Helper()
	var bins []string
	for _, n := range names {
		bin, err := exec.LookPath(n)
		if err != nil {
			t.Skipf("%s is not installed; the aspect arithmetic is covered by the string tests", n)
		}
		bins = append(bins, bin)
	}
	return bins
}

func TestAspectFiltersAgainstRealFFmpeg(t *testing.T) {
	bins := needFFmpeg(t, "ffmpeg", "ffprobe")
	ffmpeg, ffprobe := bins[0], bins[1]

	// Deliberately awkward sources: odd dimensions, square, and both
	// orientations, because every one of them lands the even-rounding
	// arithmetic somewhere different.
	sources := []string{"1920x1080", "1080x1920", "1000x1000", "1279x721", "641x361"}
	targets := []struct{ w, h int }{
		{1080, 1920}, // the vertical rendition this feature exists for
		{1920, 1080}, // and the same conversion run backwards
	}

	for _, mode := range []AspectMode{AspectCrop, AspectPad, AspectBlurredPad} {
		for _, tgt := range targets {
			vf := aspectFilterFor(t, mode, tgt.w, tgt.h)
			for _, src := range sources {
				t.Run(fmt.Sprintf("%s/%s_to_%dx%d", mode, src, tgt.w, tgt.h), func(t *testing.T) {
					out := t.TempDir() + "/out.mp4"
					cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-nostdin",
						"-f", "lavfi", "-i", "testsrc2=size="+src+":rate=30:duration=0.2",
						"-vf", vf, "-frames:v", "1",
						// The encoder is the point: an odd width anywhere in the
						// chain fails here with "width not divisible by 2".
						"-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", out)
					if b, err := cmd.CombinedOutput(); err != nil || len(b) > 0 {
						t.Fatalf("ffmpeg refused %q on a %s source: %v\n%s", vf, src, err, b)
					}

					probe := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
						"-show_entries", "stream=width,height,sample_aspect_ratio",
						"-of", "csv=p=0", out)
					b, err := probe.Output()
					if err != nil {
						t.Fatalf("ffprobe: %v", err)
					}
					got := strings.TrimSpace(string(b))
					// SAR 1:1 matters as much as the pixel count: scale pushes
					// any leftover aspect error into the sample aspect ratio, so
					// a stream can be 1080x1920 and still display un-square.
					want := fmt.Sprintf("%d,%d,1:1", tgt.w, tgt.h)
					if got != want {
						t.Errorf("%s source through %s produced %q, want %q\n-vf %s", src, mode, got, want, vf)
					}
				})
			}
		}
	}
}

// What each mode does to the picture, told apart by looking at the pixels.
//
// The source is solid blue with a green band along the edge that a centre crop
// has to throw away. That one marker separates all three modes: crop loses it,
// pad keeps it and adds bars, blurred-pad keeps it and has no bars at all.
func TestAspectFiltersPlaceThePictureCorrectly(t *testing.T) {
	ffmpeg := needFFmpeg(t, "ffmpeg")[0]

	const (
		// A band 1/8 of the way in from the edge, well outside the centre that
		// a 16:9 <-> 9:16 crop keeps.
		landscapeSrc = "color=c=0x0000FF:s=1920x1080:r=30:d=0.2," +
			"drawbox=x=0:y=0:w=240:h=1080:color=0x00FF00:t=fill"
		portraitSrc = "color=c=0x0000FF:s=1080x1920:r=30:d=0.2," +
			"drawbox=x=0:y=0:w=1080:h=240:color=0x00FF00:t=fill"
	)

	tests := []struct {
		name       string
		mode       AspectMode
		src        string
		w, h       int
		wantEdge   bool // the green band survived the conversion
		wantBars   bool // some of the canvas is the pad colour
		wantReason string
	}{
		{
			name: "crop drops the edges of a landscape source", mode: AspectCrop,
			src: landscapeSrc, w: 1080, h: 1920,
			wantEdge: false, wantBars: false,
			wantReason: "a centre crop keeps the middle and fills the frame",
		},
		{
			name: "crop drops the edges of a portrait source", mode: AspectCrop,
			src: portraitSrc, w: 1920, h: 1080,
			wantEdge: false, wantBars: false,
			wantReason: "the same crop run the other way trims top and bottom",
		},
		{
			name: "pad keeps everything and admits the bars", mode: AspectPad,
			src: landscapeSrc, w: 1080, h: 1920,
			wantEdge: true, wantBars: true,
			wantReason: "nothing is lost, so the leftover canvas is black",
		},
		{
			name: "pad pillarboxes a portrait source", mode: AspectPad,
			src: portraitSrc, w: 1920, h: 1080,
			wantEdge: true, wantBars: true,
			wantReason: "bars move to the sides but the picture is whole",
		},
		{
			name: "blurred pad keeps everything with no bars at all", mode: AspectBlurredPad,
			src: landscapeSrc, w: 1080, h: 1920,
			wantEdge: true, wantBars: false,
			wantReason: "the background is the frame itself, so black never shows",
		},
		{
			name: "blurred pad fills the sides of a portrait source", mode: AspectBlurredPad,
			src: portraitSrc, w: 1920, h: 1080,
			wantEdge: true, wantBars: false,
			wantReason: "the same composite, filling left and right instead",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vf := aspectFilterFor(t, tc.mode, tc.w, tc.h)
			cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-nostdin",
				"-f", "lavfi", "-i", tc.src, "-vf", vf, "-frames:v", "1",
				"-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("ffmpeg: %v\n%s\n-vf %s", err, stderr.String(), vf)
			}

			frame := stdout.Bytes()
			if want := tc.w * tc.h * 3; len(frame) != want {
				t.Fatalf("frame is %d bytes, want %d (%dx%d rgb24)", len(frame), want, tc.w, tc.h)
			}

			var green, bars int
			for i := 0; i+2 < len(frame); i += 3 {
				r, g, b := frame[i], frame[i+1], frame[i+2]
				switch {
				// Generous thresholds: the resample rings at the colour
				// boundary and the blur smears it, so this counts unmistakable
				// pixels rather than exact ones.
				case g > 150 && r < 100 && b < 100:
					green++
				case r < 40 && g < 40 && b < 40:
					bars++
				}
			}
			if got := green > 0; got != tc.wantEdge {
				t.Errorf("green edge present = %v, want %v: %s (%d px)", got, tc.wantEdge, tc.wantReason, green)
			}
			if got := bars > 0; got != tc.wantBars {
				t.Errorf("bars present = %v, want %v: %s (%d px)", got, tc.wantBars, tc.wantReason, bars)
			}
		})
	}
}

// Non-VAAPI encoders must not pay for the hwupload tail.
func TestRenditionArgsSoftwareEncodeHasNoHardwareFilters(t *testing.T) {
	s := baseRendition()
	s.Encoder = EncoderX264
	args := RenditionArgs(s)
	line := join(args)
	for _, bad := range []string{"hwupload", "-vaapi_device", "format=nv12"} {
		if strings.Contains(line, bad) {
			t.Errorf("found %q in a software encode: %s", bad, line)
		}
	}
}

func TestDeinterlaceIsOffByDefault(t *testing.T) {
	// Progressive sources are the overwhelming majority, and deinterlacing one
	// only softens it. An upgrade must change nothing.
	args := RenditionArgs(RenditionSpec{
		InRelayURL: "udp://127.0.0.1:1", OutRelayURL: "udp://127.0.0.1:2",
		Width: 1280, Height: 720, VideoKbps: 3000, Encoder: "libx264",
	})
	if got := strings.Join(args, " "); strings.Contains(got, "bwdif") {
		t.Errorf("a rendition with no deinterlace mode emitted one: %s", got)
	}
}

func TestDeinterlaceModesEmitTheRightFilter(t *testing.T) {
	cases := map[DeinterlaceMode]string{
		DeinterlaceAuto: "deint=interlaced",
		DeinterlaceAll:  "deint=all",
	}
	for mode, want := range cases {
		args := RenditionArgs(RenditionSpec{
			InRelayURL: "udp://127.0.0.1:1", OutRelayURL: "udp://127.0.0.1:2",
			Width: 1280, Height: 720, VideoKbps: 3000, Encoder: "libx264",
			Deinterlace: mode,
		})
		got := strings.Join(args, " ")
		if !strings.Contains(got, "bwdif") || !strings.Contains(got, want) {
			t.Errorf("mode %q produced %s, want a bwdif carrying %s", mode, got, want)
		}
		// send_frame, never send_field: one progressive frame per input frame.
		// send_field doubles the frame rate, which silently doubles the bitrate
		// a platform receives and invalidates the GOP arithmetic computed from
		// the source rate.
		if !strings.Contains(got, "mode=send_frame") {
			t.Errorf("mode %q did not pin send_frame: %s", mode, got)
		}
	}
}

func TestDeinterlaceRunsBeforeScaling(t *testing.T) {
	// Load-bearing ordering. Scaling interlaced content blends the two fields
	// together, and once that has happened the combing is baked into the pixels
	// and no later filter can remove it -- the rendition ends up looking worse
	// than the source at every size.
	args := RenditionArgs(RenditionSpec{
		InRelayURL: "udp://127.0.0.1:1", OutRelayURL: "udp://127.0.0.1:2",
		Width: 1280, Height: 720, VideoKbps: 3000, Encoder: "libx264",
		Deinterlace: DeinterlaceAll,
	})
	vf := ""
	for i, a := range args {
		if a == "-vf" && i+1 < len(args) {
			vf = args[i+1]
		}
	}
	if vf == "" {
		t.Fatal("no -vf emitted")
	}
	di, sc := strings.Index(vf, "bwdif"), strings.Index(vf, "scale")
	if di < 0 || sc < 0 {
		t.Fatalf("expected both a deinterlace and a scale in %q", vf)
	}
	if di > sc {
		t.Errorf("deinterlace runs AFTER the scale in %q: the combing is already "+
			"blended into the pixels by then and cannot be removed", vf)
	}
}

func TestDeinterlaceComposesWithAspectConversion(t *testing.T) {
	// The horizontal-to-vertical case on an interlaced source: both stages have
	// to appear, and the deinterlace still has to come first.
	args := RenditionArgs(RenditionSpec{
		InRelayURL: "udp://127.0.0.1:1", OutRelayURL: "udp://127.0.0.1:2",
		Width: 1080, Height: 1920, VideoKbps: 6000, Encoder: "libx264",
		Aspect: AspectCrop, Deinterlace: DeinterlaceAuto,
	})
	vf := ""
	for i, a := range args {
		if a == "-vf" && i+1 < len(args) {
			vf = args[i+1]
		}
	}
	di, crop := strings.Index(vf, "bwdif"), strings.Index(vf, "crop")
	if di < 0 || crop < 0 {
		t.Fatalf("expected both bwdif and crop in %q", vf)
	}
	if di > crop {
		t.Errorf("deinterlace runs after the crop in %q", vf)
	}
}

func TestAnUnknownDeinterlaceModeDegradesToOff(t *testing.T) {
	// A rendition row written by a newer build, or hand-edited, must still
	// encode. A stream that does not start is a worse answer than a stream that
	// is not deinterlaced -- the same rule the aspect modes follow.
	args := RenditionArgs(RenditionSpec{
		InRelayURL: "udp://127.0.0.1:1", OutRelayURL: "udp://127.0.0.1:2",
		Width: 1280, Height: 720, VideoKbps: 3000, Encoder: "libx264",
		Deinterlace: DeinterlaceMode("from-the-future"),
	})
	got := strings.Join(args, " ")
	if strings.Contains(got, "bwdif") {
		t.Errorf("an unknown mode emitted a filter: %s", got)
	}
	if !strings.Contains(got, "scale=1280:720") {
		t.Errorf("an unknown mode broke the rest of the chain: %s", got)
	}
}
