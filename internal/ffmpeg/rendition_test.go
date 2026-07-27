package ffmpeg

import (
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
