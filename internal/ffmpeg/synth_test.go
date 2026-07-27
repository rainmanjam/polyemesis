package ffmpeg

import (
	"strings"
	"testing"
)

// countArg reports how many times an exact argv element appears.
func countArg(args []string, want string) int {
	n := 0
	for _, a := range args {
		if a == want {
			n++
		}
	}
	return n
}

// argIndexes returns every position at which an exact argv element appears.
func argIndexes(args []string, want string) []int {
	var out []int
	for i, a := range args {
		if a == want {
			out = append(out, i)
		}
	}
	return out
}

func baseSilence() SilenceSpec {
	return SilenceSpec{
		InRelayURL:  "udp://127.0.0.1:20000",
		OutRelayURL: "udp://127.0.0.1:20030",
	}
}

func baseSlate() SlateSpec {
	return SlateSpec{
		OutRelayURL: "udp://127.0.0.1:20040",
		Width:       1920,
		Height:      1080,
		FPS:         30,
	}
}

// ------------------------------------------------------------- NeedsSilence

func TestNeedsSilenceOnlyWhenTheProbeSaysZeroTracks(t *testing.T) {
	tests := []struct {
		name  string
		probe *ProbeResult
		want  bool
	}{
		{"video-only ingest is the whole reason this exists", &ProbeResult{Video: &VideoStream{}, Audio: nil}, true},
		{"an empty but non-nil track slice is still zero tracks", &ProbeResult{Audio: []AudioStream{}}, true},
		{"one track needs nothing synthesised", &ProbeResult{Audio: []AudioStream{{Index: 0, Channels: 2}}}, false},
		{"six tracks certainly need nothing synthesised", &ProbeResult{Audio: make([]AudioStream, 6)}, false},
		// Answering true on an unknown layout would map video only and drop
		// every real track: the asymmetry is the point.
		{"an unknown layout must not be assumed silent", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsSilence(tc.probe); got != tc.want {
				t.Errorf("NeedsSilence = %v, want %v", got, tc.want)
			}
		})
	}
}

// ------------------------------------------------------------ SilenceSource

func TestSilenceSourceAlwaysStatesLayoutAndRate(t *testing.T) {
	tests := []struct {
		name       string
		channels   int
		sampleRate int
		want       string
	}{
		{"defaults are stereo at 48k, not anullsrc's 44.1k", 0, 0, "anullsrc=channel_layout=stereo:sample_rate=48000"},
		{"mono", 1, 48000, "anullsrc=channel_layout=mono:sample_rate=48000"},
		{"stereo", 2, 48000, "anullsrc=channel_layout=stereo:sample_rate=48000"},
		{"5.1 uses the layout name FFmpeg parses", 6, 48000, "anullsrc=channel_layout=5.1:sample_rate=48000"},
		{"an exotic width falls back to the Nc spelling", 12, 48000, "anullsrc=channel_layout=12c:sample_rate=48000"},
		{"a non-default rate is honoured", 2, 44100, "anullsrc=channel_layout=stereo:sample_rate=44100"},
		{"negative values are treated as unset", -1, -1, "anullsrc=channel_layout=stereo:sample_rate=48000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SilenceSource(tc.channels, tc.sampleRate); got != tc.want {
				t.Errorf("SilenceSource = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSilenceInputArgsIsASplicableFragment(t *testing.T) {
	got := SilenceInputArgs(2, 48000)
	want := []string{"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000"}
	if len(got) != len(want) {
		t.Fatalf("fragment = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fragment = %v, want %v", got, want)
		}
	}
	// -f lavfi must sit immediately before its own -i or it would apply to
	// whichever input follows in the assembled command.
	if got[2] != "-i" {
		t.Errorf("-f lavfi must be adjacent to its -i: %v", got)
	}
}

// ------------------------------------------------------------- SilenceArgs

// The silence tier exists to make a video-only ingest publishable. If it ever
// touches real ingest audio it has become a mixdown, and per-destination
// routing downstream would be mixing an already-mixed track.
func TestSilenceArgsNeverTouchesIngestAudio(t *testing.T) {
	args := SilenceArgs(baseSilence())
	s := join(args)

	maps := allAfter(args, "-map")
	if len(maps) != 2 {
		t.Fatalf("want exactly 2 maps (ingest video + synthetic audio), got %v", maps)
	}
	if maps[0] != "0:v:0" {
		t.Errorf("video map = %q, want 0:v:0 from the real ingest", maps[0])
	}
	if maps[1] != "1:a:0" {
		t.Errorf("audio map = %q, want 1:a:0 — the synthetic input, never 0:a", maps[1])
	}
	for _, bad := range []string{"0:a", "-filter_complex", "pan=", "amerge", "amix", "-af"} {
		if strings.Contains(s, bad) {
			t.Errorf("found %q: this tier must not mix or map ingest audio: %s", bad, s)
		}
	}
	if v, _ := argsAfter(args, "-c:v"); v != "copy" {
		t.Errorf("-c:v = %q, want copy. Video is never degraded here.", v)
	}
}

func TestSilenceArgsPublishesIntoItsOwnHub(t *testing.T) {
	args := SilenceArgs(baseSilence())
	s := join(args)

	in, _ := argsAfter(args, "-i")
	if !strings.HasPrefix(in, "udp://127.0.0.1:20000") {
		t.Errorf("input = %q, want the ingest hub", in)
	}
	// One stalled consumer must never take the hub down.
	if !strings.Contains(in, "overrun_nonfatal=1") || !strings.Contains(in, "fifo_size=") {
		t.Errorf("input = %q, want RelayInputURL tolerances", in)
	}
	if last := args[len(args)-1]; last != "udp://127.0.0.1:20030?pkt_size=1316" {
		t.Errorf("output = %q, want the silence hub with pkt_size", last)
	}
	if f, _ := argsAfter(args, "-f"); f != "lavfi" {
		t.Errorf("first -f = %q, want lavfi for the synthetic input", f)
	}
	if !strings.Contains(s, "-f mpegts") {
		t.Errorf("output must be mpegts: only TS carries the hub's shape: %s", s)
	}
	if !strings.Contains(s, "-flush_packets 1") {
		t.Errorf("loopback hop must not hold partial TS packets: %s", s)
	}
	if !has(args, "-nostdin") {
		t.Error("missing -nostdin: a backgrounded child can steal stdin and pause itself")
	}
	if p, _ := argsAfter(args, "-progress"); p != "pipe:1" {
		t.Errorf("-progress = %q, want pipe:1 like every other supervised child", p)
	}
}

// anullsrc never ends. Without -shortest the tier outlives the ingest, keeps
// publishing silence, and the supervisor never sees it exit to restart it.
func TestSilenceArgsEndsWithTheIngest(t *testing.T) {
	args := SilenceArgs(baseSilence())
	if !has(args, "-shortest") {
		t.Fatalf("missing -shortest: %s", join(args))
	}
	iShortest := indexOf(args, "-shortest")
	iOut := len(args) - 1
	if iShortest >= iOut {
		t.Errorf("-shortest at %d must precede the output URL at %d", iShortest, iOut)
	}
	// It is an output option; placing it before the inputs would make FFmpeg
	// apply it to the wrong file.
	iLastInput := argIndexes(args, "-i")
	if iShortest < iLastInput[len(iLastInput)-1] {
		t.Errorf("-shortest at %d must come after every -i", iShortest)
	}
}

func TestSilenceArgsAudioEncoding(t *testing.T) {
	tests := []struct {
		name                            string
		spec                            SilenceSpec
		wantSource, wantBitrate, wantAC string
		wantAR                          string
	}{
		{
			name:        "defaults are stereo 48k AAC at 128k",
			spec:        baseSilence(),
			wantSource:  "anullsrc=channel_layout=stereo:sample_rate=48000",
			wantBitrate: "128k", wantAC: "2", wantAR: "48000",
		},
		{
			name:        "mono is carried through to the encoder as well as the source",
			spec:        SilenceSpec{InRelayURL: "udp://h:1", OutRelayURL: "udp://h:2", Channels: 1},
			wantSource:  "anullsrc=channel_layout=mono:sample_rate=48000",
			wantBitrate: "128k", wantAC: "1", wantAR: "48000",
		},
		{
			name:        "an explicit rate reaches both the source and the encoder",
			spec:        SilenceSpec{InRelayURL: "udp://h:1", OutRelayURL: "udp://h:2", SampleRate: 44100},
			wantSource:  "anullsrc=channel_layout=stereo:sample_rate=44100",
			wantBitrate: "128k", wantAC: "2", wantAR: "44100",
		},
		{
			name:        "an explicit bitrate is honoured",
			spec:        SilenceSpec{InRelayURL: "udp://h:1", OutRelayURL: "udp://h:2", Kbps: 64},
			wantSource:  "anullsrc=channel_layout=stereo:sample_rate=48000",
			wantBitrate: "64k", wantAC: "2", wantAR: "48000",
		},
		{
			name:        "zero and negative fields fall back to defaults, never to zero flags",
			spec:        SilenceSpec{InRelayURL: "udp://h:1", OutRelayURL: "udp://h:2", Channels: -3, SampleRate: -1, Kbps: -1},
			wantSource:  "anullsrc=channel_layout=stereo:sample_rate=48000",
			wantBitrate: "128k", wantAC: "2", wantAR: "48000",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := SilenceArgs(tc.spec)
			ins := allAfter(args, "-i")
			if len(ins) != 2 {
				t.Fatalf("want 2 inputs (relay + lavfi), got %v", ins)
			}
			if ins[1] != tc.wantSource {
				t.Errorf("lavfi input = %q, want %q", ins[1], tc.wantSource)
			}
			if c, _ := argsAfter(args, "-c:a"); c != "aac" {
				t.Errorf("-c:a = %q, want aac: platforms will not take raw PCM", c)
			}
			if b, _ := argsAfter(args, "-b:a"); b != tc.wantBitrate {
				t.Errorf("-b:a = %q, want %q", b, tc.wantBitrate)
			}
			if v, _ := argsAfter(args, "-ac"); v != tc.wantAC {
				t.Errorf("-ac = %q, want %q", v, tc.wantAC)
			}
			if v, _ := argsAfter(args, "-ar"); v != tc.wantAR {
				t.Errorf("-ar = %q, want %q", v, tc.wantAR)
			}
		})
	}
}

// anullsrc accepts a 12-channel layout and resolves it to 7.1.4; the aac
// encoder then refuses to open. That would crash-loop the one process whose
// job is to keep a destination alive, so the width is narrowed to something
// AAC can encode rather than passed through to fail.
func TestSynthArgsNarrowChannelsToWhatAACCanOpen(t *testing.T) {
	tests := []struct {
		name     string
		channels int
		want     string // channel layout in the anullsrc description
		wantAC   string
	}{
		{"stereo is untouched", 2, "stereo", "2"},
		{"5.1 is untouched", 6, "5.1", "6"},
		{"7.1 is the boundary and is untouched", 8, "7.1", "8"},
		{"9 channels would fail to parse at all", 9, "7.1", "8"},
		{"12 channels would resolve to 7.1.4 and fail to encode", 12, "7.1", "8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sil := SilenceArgs(SilenceSpec{InRelayURL: "udp://h:1", OutRelayURL: "udp://h:2", Channels: tc.channels})
			sl := baseSlate()
			sl.AudioChannels = tc.channels

			for _, c := range []struct {
				label string
				args  []string
			}{{"silence", sil}, {"slate", SlateArgs(sl)}} {
				label, args := c.label, c.args
				ins := allAfter(args, "-i")
				src := ins[len(ins)-1]
				if want := "anullsrc=channel_layout=" + tc.want + ":"; !strings.HasPrefix(src, want) {
					t.Errorf("%s source = %q, want prefix %q", label, src, want)
				}
				// -ac must agree with the source layout, or the encoder is asked
				// to remix a width it was never given.
				if v, _ := argsAfter(args, "-ac"); v != tc.wantAC {
					t.Errorf("%s -ac = %q, want %q", label, v, tc.wantAC)
				}
			}
		})
	}
}

// The MPEG-TS muxer has no descriptor for a stream title and drops it without
// a word, so a -metadata flag here is a flag that lies. Verified against
// ffmpeg 8.1: the tag never reaches the output.
func TestSynthArgsCarryNoStreamTitleTheMuxerWouldDrop(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"silence", SilenceArgs(baseSilence())},
		{"slate", SlateArgs(baseSlate())},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, a := range tc.args {
				if strings.HasPrefix(a, "-metadata") {
					t.Errorf("found %q: mpegts discards stream titles, so the UI must label the track from engine state", a)
				}
			}
		})
	}
}

func TestSilenceArgsInputOrderIsLoadBearing(t *testing.T) {
	args := SilenceArgs(baseSilence())

	iRelay := indexOf(args, "-thread_queue_size")
	fIdx := indexOf(args, "-f")
	if fIdx < 0 || args[fIdx+1] != "lavfi" {
		t.Fatalf("expected a -f lavfi input: %s", join(args))
	}
	// The relay must be input 0 and the silence input 1, because the maps say
	// 0:v:0 and 1:a:0 by number.
	if iRelay >= fIdx {
		t.Errorf("relay input (idx %d) must precede the lavfi input (idx %d)", iRelay, fIdx)
	}
	if args[fIdx+2] != "-i" {
		t.Errorf("-f lavfi must sit immediately before its own -i: %s", join(args))
	}
	// -thread_queue_size belongs to the live relay only; the synthetic source
	// has no jitter to absorb.
	if countArg(args, "-thread_queue_size") != 1 {
		t.Errorf("want exactly one -thread_queue_size (the relay's): %s", join(args))
	}
}

func TestSilenceArgsHandlesRelayURLsThatAlreadyHaveAQuery(t *testing.T) {
	args := SilenceArgs(SilenceSpec{
		InRelayURL:  "udp://127.0.0.1:20000?buffer_size=65536",
		OutRelayURL: "udp://127.0.0.1:20030?ttl=1",
	})
	in, _ := argsAfter(args, "-i")
	if !strings.Contains(in, "?buffer_size=65536&fifo_size=") {
		t.Errorf("input = %q, want the existing query preserved with & separators", in)
	}
	if last := args[len(args)-1]; last != "udp://127.0.0.1:20030?ttl=1&pkt_size=1316" {
		t.Errorf("output = %q, want the existing query preserved", last)
	}
}

// ---------------------------------------------------------------- SlateArgs

func TestSlateArgsColourSourceIsPacedAndSized(t *testing.T) {
	args := SlateArgs(baseSlate())
	s := join(args)

	ins := allAfter(args, "-i")
	if len(ins) != 2 {
		t.Fatalf("want 2 inputs (video + silence), got %v", ins)
	}
	if ins[0] != "color=c=black:s=1920x1080:r=30" {
		t.Errorf("video input = %q, want a colour source at the ingest's size and rate", ins[0])
	}
	if ins[1] != "anullsrc=channel_layout=stereo:sample_rate=48000" {
		t.Errorf("audio input = %q, want silence", ins[1])
	}
	// Both lavfi sources generate as fast as they are read. An unpaced one
	// buries the hub in seconds; an unpaced audio leg races the paced video and
	// fills the interleave queue.
	if n := countArg(args, "-re"); n != 2 {
		t.Errorf("-re appears %d times, want 2 (one per synthetic input): %s", n, s)
	}
	for _, re := range argIndexes(args, "-re") {
		if !hasLaterArg(args, re, "-i") {
			t.Errorf("-re at %d is not an input option of any input: %s", re, s)
		}
	}
	// A slate runs until it is stopped; a duration would end the broadcast it
	// exists to keep alive.
	if has(args, "-t") || has(args, "-shortest") {
		t.Errorf("slate must not be bounded in time: %s", s)
	}
}

func TestSlateArgsMapsAndOutput(t *testing.T) {
	args := SlateArgs(baseSlate())
	s := join(args)

	maps := allAfter(args, "-map")
	if len(maps) != 2 || maps[0] != "0:v:0" || maps[1] != "1:a:0" {
		t.Fatalf("maps = %v, want [0:v:0 1:a:0]", maps)
	}
	if last := args[len(args)-1]; last != "udp://127.0.0.1:20040?pkt_size=1316" {
		t.Errorf("output = %q, want the hub with pkt_size", last)
	}
	if !strings.Contains(s, "-f mpegts") {
		t.Errorf("output must be mpegts so destinations cannot tell slate from ingest: %s", s)
	}
	if !strings.Contains(s, "-flush_packets 1") {
		t.Errorf("loopback hop must not hold partial TS packets: %s", s)
	}
	if c, _ := argsAfter(args, "-c:a"); c != "aac" {
		t.Errorf("-c:a = %q, want aac", c)
	}
	if !has(args, "-nostdin") {
		t.Error("missing -nostdin")
	}
	if p, _ := argsAfter(args, "-progress"); p != "pipe:1" {
		t.Errorf("-progress = %q, want pipe:1 like every other supervised child", p)
	}
}

func TestSlateArgsDefaults(t *testing.T) {
	args := SlateArgs(SlateSpec{OutRelayURL: "udp://h:1"})
	ins := allAfter(args, "-i")

	// A colour source with no size defaults to 320x240 in FFmpeg, which no
	// platform would accept as a continuation of a 1080p broadcast.
	if ins[0] != "color=c=black:s=1280x720:r=30" {
		t.Errorf("default video input = %q, want 1280x720@30 black", ins[0])
	}
	if v, _ := argsAfter(args, "-c:v"); v != EncoderX264 {
		t.Errorf("-c:v = %q: the standby source must default to the encoder that always exists", v)
	}
	if b, _ := argsAfter(args, "-b:v"); b != "2000k" {
		t.Errorf("-b:v = %q, want 2000k", b)
	}
	if m, _ := argsAfter(args, "-maxrate"); m != "2000k" {
		t.Errorf("-maxrate = %q, want it to track -b:v", m)
	}
	if bs, _ := argsAfter(args, "-bufsize"); bs != "4000k" {
		t.Errorf("-bufsize = %q, want 2x maxrate", bs)
	}
	if b, _ := argsAfter(args, "-b:a"); b != "128k" {
		t.Errorf("-b:a = %q, want 128k", b)
	}
	if r, _ := argsAfter(args, "-r"); r != "30" {
		t.Errorf("-r = %q, want 30", r)
	}
	if has(args, "-output_ts_offset") {
		t.Errorf("a zero offset must not emit -output_ts_offset: %s", join(args))
	}
}

func TestSlateArgsNegativeFieldsFallBackToDefaults(t *testing.T) {
	args := SlateArgs(SlateSpec{
		OutRelayURL: "udp://h:1",
		Width:       -1, Height: -1, FPS: -1,
		VideoKbps: -1, MaxrateKbps: -1, BufsizeKbps: -1,
		GOPSeconds: -1, AudioChannels: -1, SampleRate: -1, AudioKbps: -1,
	})
	// A negative field must be indistinguishable from an unset one; a leaked
	// "-1" or "0k" produces an encoder that refuses to open, which is exactly
	// what a standby source must never do.
	if got, want := join(args), join(SlateArgs(SlateSpec{OutRelayURL: "udp://h:1"})); got != want {
		t.Errorf("negative fields changed the command:\ngot  %s\nwant %s", got, want)
	}
}

func TestSlateArgsImageSource(t *testing.T) {
	s := baseSlate()
	s.ImagePath = "/var/lib/polyemesis/slate.png"
	args := SlateArgs(s)
	line := join(args)

	ins := allAfter(args, "-i")
	if ins[0] != "/var/lib/polyemesis/slate.png" {
		t.Fatalf("video input = %q, want the path passed as a plain argv element", ins[0])
	}
	// Without -loop the image demuxer yields one frame and the standby ends
	// immediately.
	if l, _ := argsAfter(args, "-loop"); l != "1" {
		t.Errorf("-loop = %q, want 1", l)
	}
	// -re paces against the INPUT rate, so image2's 25 fps default would pace
	// the slate at 25 no matter what -r says.
	if f, _ := argsAfter(args, "-framerate"); f != "30" {
		t.Errorf("-framerate = %q, want the target rate on the input too", f)
	}
	if n := countArg(args, "-re"); n != 2 {
		t.Errorf("-re appears %d times, want 2: %s", n, line)
	}
	// The image path must never become a filtergraph argument, where a space or
	// a colon in the path would silently split it.
	if strings.Contains(line, "movie=") {
		t.Errorf("image must be a demuxer input, not a movie= filter: %s", line)
	}

	vf, ok := argsAfter(args, "-vf")
	if !ok {
		t.Fatalf("an image of unknown size needs fitting to the frame: %s", line)
	}
	for _, want := range []string{
		"scale=1920:1080:force_original_aspect_ratio=decrease",
		"pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=black",
		"setsar=1",
	} {
		if !strings.Contains(vf, want) {
			t.Errorf("-vf = %q, missing %q", vf, want)
		}
	}
	// Stretching a 16:9 logo into a 4:3 ingest is the wrong answer; letterbox.
	if strings.Contains(vf, "force_original_aspect_ratio=disable") {
		t.Errorf("-vf = %q must not stretch the slate", vf)
	}
}

// A flat colour needs no scaling at all, and a no-op scale still costs a full
// colour-space round trip on every frame.
func TestSlateArgsColourSourceNeedsNoFilter(t *testing.T) {
	args := SlateArgs(baseSlate())
	if vf, ok := argsAfter(args, "-vf"); ok {
		t.Errorf("-vf = %q, want no filter for a colour source", vf)
	}
}

func TestSlateArgsGOP(t *testing.T) {
	tests := []struct {
		name    string
		fps     float64
		gopSecs float64
		want    string
	}{
		{"default 2s at 30fps", 30, 0, "60"},
		{"default 2s at 60fps", 60, 0, "120"},
		{"a 1s interval halves it", 30, 1, "30"},
		{"NTSC rates round rather than truncate", 29.97, 2, "60"},
		{"an absurdly short interval still yields at least one frame", 1, 0.1, "1"},
		{"a 4s interval at 25fps", 25, 4, "100"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSlate()
			s.FPS, s.GOPSeconds = tc.fps, tc.gopSecs
			args := SlateArgs(s)

			g, _ := argsAfter(args, "-g")
			if g != tc.want {
				t.Errorf("-g = %q, want %q", g, tc.want)
			}
			// keyint_min pins the lower bound and sc_threshold 0 stops a scene
			// cut inserting an extra keyframe that would desync segmenting.
			if k, _ := argsAfter(args, "-keyint_min"); k != tc.want {
				t.Errorf("-keyint_min = %q, want %q", k, tc.want)
			}
			if sc, _ := argsAfter(args, "-sc_threshold"); sc != "0" {
				t.Errorf("-sc_threshold = %q, want 0", sc)
			}
		})
	}
}

func TestSlateArgsFPSFormatting(t *testing.T) {
	tests := []struct {
		name string
		fps  float64
		want string
	}{
		{"integers stay integers", 30, "30"},
		{"NTSC survives as a decimal", 29.97, "29.97"},
		{"59.94 survives", 59.94, "59.94"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSlate()
			s.FPS = tc.fps
			args := SlateArgs(s)
			if r, _ := argsAfter(args, "-r"); r != tc.want {
				t.Errorf("-r = %q, want %q", r, tc.want)
			}
			if in := allAfter(args, "-i")[0]; !strings.Contains(in, ":r="+tc.want) {
				t.Errorf("source = %q, want rate %q", in, tc.want)
			}
		})
	}
}

func TestSlateArgsEncoders(t *testing.T) {
	tests := []struct {
		name        string
		encoder     string
		preset      string
		wantPreset  string // "" => no preset flag at all
		presetFlag  string
		wantPresent []string
		wantAbsent  []string
	}{
		{
			name:    "x264 forces yuv420p so a 10-bit ingest cannot produce High10",
			encoder: EncoderX264, presetFlag: "-preset", wantPreset: "veryfast",
			wantPresent: []string{"-pix_fmt", "yuv420p", "-profile:v", "high"},
		},
		{
			name:    "an explicit preset wins over the default",
			encoder: EncoderX264, preset: "ultrafast", presetFlag: "-preset", wantPreset: "ultrafast",
		},
		{
			name:    "nvenc gets its p-preset and cbr rate control",
			encoder: EncoderNVENC, presetFlag: "-preset", wantPreset: "p4",
			wantPresent: []string{"-rc", "cbr"},
		},
		{
			name:    "videotoolbox has no preset option and must not be given one",
			encoder: EncoderVideoToolbox, preset: "veryfast", presetFlag: "-preset", wantPreset: "",
			wantPresent: []string{"-realtime", "1"},
		},
		{
			name:    "amf spells its preset -quality",
			encoder: EncoderAMF, presetFlag: "-quality", wantPreset: "speed",
			wantPresent: []string{"-usage", "transcoding"},
		},
		{
			// Refusing to build a command for an unfamiliar encoder would leave
			// the operator with no standby source at all.
			name:    "an unknown encoder still builds a runnable command",
			encoder: "h264_magic", presetFlag: "-preset", wantPreset: "",
			wantPresent: []string{"-c:v", "h264_magic", "-b:v"},
			wantAbsent:  []string{"-realtime", "-usage", "-rc"},
		},
		{
			name:    "an unknown encoder honours a preset the operator typed",
			encoder: "h264_magic", preset: "fast", presetFlag: "-preset", wantPreset: "fast",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSlate()
			s.Encoder, s.Preset = tc.encoder, tc.preset
			args := SlateArgs(s)
			line := join(args)

			if v, _ := argsAfter(args, "-c:v"); v != tc.encoder {
				t.Errorf("-c:v = %q, want %q", v, tc.encoder)
			}
			got, ok := argsAfter(args, tc.presetFlag)
			if tc.wantPreset == "" {
				if ok {
					t.Errorf("%s = %q, want no preset flag: FFmpeg warns on every restart about an unused AVOption", tc.presetFlag, got)
				}
			} else if got != tc.wantPreset {
				t.Errorf("%s = %q, want %q", tc.presetFlag, got, tc.wantPreset)
			}
			for _, w := range tc.wantPresent {
				if !has(args, w) {
					t.Errorf("missing %q: %s", w, line)
				}
			}
			for _, w := range tc.wantAbsent {
				if has(args, w) {
					t.Errorf("unexpected %q for an unknown encoder: %s", w, line)
				}
			}
		})
	}
}

func TestSlateArgsVAAPI(t *testing.T) {
	tests := []struct {
		name       string
		device     string
		wantDevice string
	}{
		{"the default render node", "", defaultVAAPIDevice},
		{"an explicit render node", "/dev/dri/renderD129", "/dev/dri/renderD129"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSlate()
			s.Encoder, s.VAAPIDevice = EncoderVAAPI, tc.device
			args := SlateArgs(s)

			dev, ok := argsAfter(args, "-vaapi_device")
			if !ok || dev != tc.wantDevice {
				t.Fatalf("-vaapi_device = %q (%v), want %q", dev, ok, tc.wantDevice)
			}
			// The device has to exist before the filter graph that uploads into
			// it is configured, so it must precede every input.
			if indexOf(args, "-vaapi_device") > argIndexes(args, "-i")[0] {
				t.Errorf("-vaapi_device must precede the first -i: %s", join(args))
			}
			// VAAPI encodes from GPU surfaces: even an unscaled colour source
			// needs converting and uploading.
			vf, ok := argsAfter(args, "-vf")
			if !ok {
				t.Fatalf("vaapi needs an upload chain even with no scaling: %s", join(args))
			}
			if !strings.HasSuffix(vf, "format=nv12,hwupload") {
				t.Errorf("-vf = %q, want the upload chain last", vf)
			}
			// VAAPI takes neither a preset nor a profile name.
			if has(args, "-preset") {
				t.Errorf("vaapi has no preset option: %s", join(args))
			}
		})
	}
}

func TestSlateArgsVAAPIWithAnImageKeepsTheFitBeforeTheUpload(t *testing.T) {
	s := baseSlate()
	s.Encoder = EncoderVAAPI
	s.ImagePath = "/srv/slate.png"
	vf, ok := argsAfter(SlateArgs(s), "-vf")
	if !ok {
		t.Fatal("missing -vf")
	}
	// Scaling must happen on the CPU side; once uploaded the frames are GPU
	// surfaces the software scale filter cannot touch.
	iScale := strings.Index(vf, "scale=")
	iUpload := strings.Index(vf, "hwupload")
	if iScale < 0 || iUpload < 0 || iScale > iUpload {
		t.Errorf("-vf = %q, want the fit chain before hwupload", vf)
	}
}

func TestSlateArgsTimestampOffset(t *testing.T) {
	tests := []struct {
		name   string
		offset float64
		want   string // "" => flag must be absent
	}{
		{"no offset emits no flag", 0, ""},
		{"a whole-second offset", 3600, "3600"},
		{"a fractional offset keeps its precision", 12.5, "12.5"},
		// Go's default float formatting would render this as 1e+06, which
		// FFmpeg's duration parser rejects.
		{"a large offset never becomes scientific notation", 1000000, "1000000"},
		{"a negative offset is passed through, not clamped", -5, "-5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSlate()
			s.TimestampOffsetSeconds = tc.offset
			args := SlateArgs(s)

			got, ok := argsAfter(args, "-output_ts_offset")
			if tc.want == "" {
				if ok {
					t.Fatalf("-output_ts_offset = %q, want the flag omitted", got)
				}
				return
			}
			if !ok {
				t.Fatalf("missing -output_ts_offset: %s", join(args))
			}
			if got != tc.want {
				t.Errorf("-output_ts_offset = %q, want %q", got, tc.want)
			}
			if strings.ContainsAny(got, "eE") {
				t.Errorf("-output_ts_offset = %q must not use scientific notation", got)
			}
		})
	}
}

func TestSlateArgsColourEscaping(t *testing.T) {
	tests := []struct {
		name  string
		color string
		want  string
	}{
		{"a plain name", "black", "color=c=black:"},
		{"a hex colour", "0x101014", "color=c=0x101014:"},
		{"an alpha suffix needs no escaping", "gray@0.5", "color=c=gray@0.5:"},
		// A raw colon would be read as the start of the next option and turn
		// into a parse error the operator cannot connect to what they typed.
		{"a colon is escaped, not passed through", "a:b", `color=c=a\:b:`},
		{"a backslash is escaped first so it cannot double-escape", `a\b`, `color=c=a\\b:`},
		{"a comma cannot end the filter", "a,b", `color=c=a\,b:`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSlate()
			s.Color = tc.color
			in := allAfter(SlateArgs(s), "-i")[0]
			if !strings.HasPrefix(in, tc.want) {
				t.Errorf("source = %q, want prefix %q", in, tc.want)
			}
		})
	}
}

// The pad colour comes from the same operator-supplied string and lands inside
// a filtergraph too.
func TestSlateArgsPadColourIsEscaped(t *testing.T) {
	s := baseSlate()
	s.ImagePath = "/srv/slate.png"
	s.Color = "a:b"
	vf, _ := argsAfter(SlateArgs(s), "-vf")
	if !strings.Contains(vf, `color=a\:b`) {
		t.Errorf("-vf = %q, want the pad colour escaped", vf)
	}
}

func TestSlateArgsAudio(t *testing.T) {
	tests := []struct {
		name                       string
		channels, rate, kbps       int
		wantSource, wantAC, wantAR string
		wantKbps                   string
	}{
		{"defaults", 0, 0, 0, "anullsrc=channel_layout=stereo:sample_rate=48000", "2", "48000", "128k"},
		{"mono", 1, 48000, 96, "anullsrc=channel_layout=mono:sample_rate=48000", "1", "48000", "96k"},
		{"44.1k", 2, 44100, 0, "anullsrc=channel_layout=stereo:sample_rate=44100", "2", "44100", "128k"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSlate()
			s.AudioChannels, s.SampleRate, s.AudioKbps = tc.channels, tc.rate, tc.kbps
			args := SlateArgs(s)

			if in := allAfter(args, "-i")[1]; in != tc.wantSource {
				t.Errorf("audio input = %q, want %q", in, tc.wantSource)
			}
			if v, _ := argsAfter(args, "-ac"); v != tc.wantAC {
				t.Errorf("-ac = %q, want %q", v, tc.wantAC)
			}
			if v, _ := argsAfter(args, "-ar"); v != tc.wantAR {
				t.Errorf("-ar = %q, want %q", v, tc.wantAR)
			}
			if v, _ := argsAfter(args, "-b:a"); v != tc.wantKbps {
				t.Errorf("-b:a = %q, want %q", v, tc.wantKbps)
			}
		})
	}
}

// Both builders are pure: the same spec must produce the same argv, and the
// returned slice must not alias anything the caller can mutate underneath it.
func TestSynthBuildersArePure(t *testing.T) {
	tests := []struct {
		name string
		fn   func() []string
	}{
		{"SilenceArgs", func() []string { return SilenceArgs(baseSilence()) }},
		{"SlateArgs colour", func() []string { return SlateArgs(baseSlate()) }},
		{"SlateArgs image", func() []string {
			s := baseSlate()
			s.ImagePath = "/srv/slate.png"
			return SlateArgs(s)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := tc.fn(), tc.fn()
			if join(a) != join(b) {
				t.Fatalf("not deterministic:\n%s\n%s", join(a), join(b))
			}
			a[0] = "MUTATED"
			if tc.fn()[0] == "MUTATED" {
				t.Error("builder returns an aliased slice")
			}
		})
	}
}

// hasLaterArg reports whether want appears at or after position i.
func hasLaterArg(args []string, i int, want string) bool {
	for ; i < len(args); i++ {
		if args[i] == want {
			return true
		}
	}
	return false
}
