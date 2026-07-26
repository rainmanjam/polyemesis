package ffmpeg

import (
	"strings"
	"testing"
)

// argsAfter returns the value following flag, and whether it was present.
func argsAfter(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func allAfter(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func has(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func join(args []string) string { return strings.Join(args, " ") }

// ------------------------------------------------------------------- ingest

func TestIngestURLSRT(t *testing.T) {
	s := IngestSpec{Kind: IngestSRT, SRTPort: 6000, SRTLatencyMS: 200}
	got := s.IngestURL()

	if !strings.HasPrefix(got, "srt://0.0.0.0:6000?") {
		t.Fatalf("url = %q", got)
	}
	// FFmpeg's srt latency is microseconds. 200ms must render as 200000, not
	// 200 — a mistake that yields a 0.2ms buffer and an unusable stream.
	if !strings.Contains(got, "latency=200000") {
		t.Errorf("latency must be in microseconds: %q", got)
	}
	if !strings.Contains(got, "mode=listener") {
		t.Errorf("must listen: %q", got)
	}
	if !strings.Contains(got, "transtype=live") {
		t.Errorf("must use live transtype: %q", got)
	}
	if strings.Contains(got, "passphrase") {
		t.Errorf("empty passphrase must be omitted entirely, not sent blank: %q", got)
	}
}

func TestIngestURLSRTWithPassphrase(t *testing.T) {
	s := IngestSpec{Kind: IngestSRT, SRTPort: 7000, SRTLatencyMS: 120, SRTPassphrase: "correcthorsebattery"}
	got := s.IngestURL()
	if !strings.Contains(got, "passphrase=correcthorsebattery") {
		t.Errorf("passphrase missing: %q", got)
	}
	if !strings.Contains(got, "latency=120000") {
		t.Errorf("latency = %q", got)
	}
}

func TestIngestURLRTMP(t *testing.T) {
	s := IngestSpec{Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live", RTMPStreamKey: "sekrit"}
	if got, want := s.IngestURL(), "rtmp://0.0.0.0:1935/live/sekrit"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

func TestPublicIngestURLIsCallerMode(t *testing.T) {
	s := IngestSpec{Kind: IngestSRT, SRTPort: 6000, SRTLatencyMS: 200}
	got := s.PublicIngestURL("stream.example.com")
	// What we hand the user must be the dial-out side of what we listen on.
	if !strings.Contains(got, "mode=caller") {
		t.Errorf("the URL given to OBS must be caller mode: %q", got)
	}
	if !strings.Contains(got, "stream.example.com:6000") {
		t.Errorf("host missing: %q", got)
	}
}

func TestIngestArgsCopiesEveryTrack(t *testing.T) {
	args := IngestArgs(IngestSpec{
		Kind: IngestSRT, SRTPort: 6000, SRTLatencyMS: 200,
		RelayURL: "udp://127.0.0.1:20000",
	})

	// -map 0 (not 0:v / 0:a:0) is what preserves all six tracks.
	if m, _ := argsAfter(args, "-map"); m != "0" {
		t.Errorf("-map = %q, want \"0\" so every track survives", m)
	}
	if c, _ := argsAfter(args, "-c"); c != "copy" {
		t.Errorf("-c = %q, want copy: the ingest must never re-encode", c)
	}
	if f, _ := argsAfter(args, "-f"); f != "mpegts" {
		t.Errorf("-f = %q, want mpegts", f)
	}
	if !has(args, "-nostdin") {
		t.Error("missing -nostdin")
	}
	if p, _ := argsAfter(args, "-progress"); p != "pipe:1" {
		t.Errorf("-progress = %q", p)
	}
	if !strings.Contains(join(args), "udp://127.0.0.1:20000?pkt_size=1316") {
		t.Errorf("relay output missing TS-aligned pkt_size: %s", join(args))
	}
	// The ingest never re-encodes, so no encoder flag may appear.
	for _, bad := range []string{"libx264", "-b:v", "-crf", "aac"} {
		if strings.Contains(join(args), bad) {
			t.Errorf("ingest must not encode, found %q in: %s", bad, join(args))
		}
	}
}

func TestIngestArgsRTMPListens(t *testing.T) {
	args := IngestArgs(IngestSpec{
		Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live", RTMPStreamKey: "k",
		RelayURL: "udp://127.0.0.1:20000",
	})
	if v, ok := argsAfter(args, "-listen"); !ok || v != "1" {
		t.Errorf("RTMP ingest must listen: %s", join(args))
	}
	// -listen must precede -i, or FFmpeg applies it to nothing.
	li, ii := indexOf(args, "-listen"), indexOf(args, "-i")
	if li < 0 || ii < 0 || li > ii {
		t.Errorf("-listen must come before -i: %s", join(args))
	}
}

func TestRelayURLHelpers(t *testing.T) {
	if got := RelayOutputURL("udp://127.0.0.1:1"); got != "udp://127.0.0.1:1?pkt_size=1316" {
		t.Errorf("got %q", got)
	}
	if got := RelayOutputURL("udp://127.0.0.1:1?x=1"); got != "udp://127.0.0.1:1?x=1&pkt_size=1316" {
		t.Errorf("existing query must be preserved, got %q", got)
	}
	in := RelayInputURL("udp://127.0.0.1:2")
	if !strings.Contains(in, "overrun_nonfatal=1") || !strings.Contains(in, "fifo_size=") {
		t.Errorf("consumers need overflow tolerance: %q", in)
	}
}

// -------------------------------------------------------------- destination

func TestDestinationArgsNeverReencodesVideo(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind:          DestRTMP,
		Target:        "rtmp://a.example/live/key",
		RelayURL:      "udp://127.0.0.1:20001",
		FilterComplex: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		AudioBitrate:  160,
	})
	s := join(args)

	if v, _ := argsAfter(args, "-c:v"); v != "copy" {
		t.Fatalf("-c:v = %q, want copy. Video must never be re-encoded.", v)
	}
	for _, bad := range []string{"libx264", "libx265", "-crf", "-preset", "-b:v"} {
		if strings.Contains(s, bad) {
			t.Errorf("found video-encode flag %q in destination args: %s", bad, s)
		}
	}
	if a, _ := argsAfter(args, "-c:a"); a != "aac" {
		t.Errorf("-c:a = %q, want aac", a)
	}
	if b, _ := argsAfter(args, "-b:a"); b != "160k" {
		t.Errorf("-b:a = %q", b)
	}
	if ac, _ := argsAfter(args, "-ac"); ac != "2" {
		t.Errorf("-ac = %q, want stereo", ac)
	}
	if f, _ := argsAfter(args, "-f"); f != "flv" {
		t.Errorf("-f = %q, want flv for RTMP", f)
	}
	if args[len(args)-1] != "rtmp://a.example/live/key" {
		t.Errorf("target must be last: %s", s)
	}
}

// If the explicit maps regress, FFmpeg picks one arbitrary audio track and the
// entire routing feature silently stops working.
func TestDestinationArgsMapsTheFilterOutputNotARawTrack(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind: DestSRT, Target: "srt://x:9000", RelayURL: "udp://127.0.0.1:2",
		FilterComplex: "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];[a_t1]aresample=48000:async=1:first_pts=0[aout]",
	})
	maps := allAfter(args, "-map")
	if len(maps) != 2 {
		t.Fatalf("want exactly 2 maps (video + mixed audio), got %v", maps)
	}
	if maps[0] != "0:v:0" {
		t.Errorf("first map = %q, want 0:v:0", maps[0])
	}
	if maps[1] != "[aout]" {
		t.Errorf("audio map = %q, want the filter output [aout], not a raw stream", maps[1])
	}
	if fc, _ := argsAfter(args, "-filter_complex"); !strings.Contains(fc, "pan=stereo") {
		t.Errorf("filter graph not passed through: %q", fc)
	}
	if f, _ := argsAfter(args, "-f"); f != "mpegts" {
		t.Errorf("-f = %q, want mpegts for SRT", f)
	}
}

func TestDestinationArgsCustomOutLabel(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind: DestFile, Target: "/data/out.mkv", RelayURL: "udp://127.0.0.1:3",
		FilterComplex: "[0:a:0]anull[mixed]", AudioOutLabel: "mixed",
	})
	maps := allAfter(args, "-map")
	if maps[1] != "[mixed]" {
		t.Errorf("audio map = %q, want [mixed]", maps[1])
	}
}

func TestDestinationArgsFileFormats(t *testing.T) {
	tests := map[string]string{
		"/data/out.mkv":   "matroska",
		"/data/out.mp4":   "mp4",
		"/data/out.flv":   "flv",
		"/data/out.ts":    "mpegts",
		"/data/out.mov":   "mov",
		"/data/out":       "matroska", // no extension defaults to the crash-safe muxer
		"/data/out.WEIRD": "matroska",
	}
	for path, want := range tests {
		args := DestinationArgs(DestSpec{
			Kind: DestFile, Target: path, RelayURL: "udp://127.0.0.1:4",
			FilterComplex: "[0:a:0]anull[aout]",
		})
		if got, _ := argsAfter(args, "-f"); got != want {
			t.Errorf("%s: -f = %q, want %q", path, got, want)
		}
	}
}

func TestDestinationArgsDefaults(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind: DestRTMP, Target: "rtmp://x/y", RelayURL: "udp://127.0.0.1:5",
		FilterComplex: "[0:a:0]anull[aout]",
	})
	if b, _ := argsAfter(args, "-b:a"); b != "160k" {
		t.Errorf("default bitrate = %q, want 160k", b)
	}
	if r, _ := argsAfter(args, "-ar"); r != "48000" {
		t.Errorf("default sample rate = %q", r)
	}
	if m, _ := argsAfter(args, "-map"); m != "0:v:0" {
		t.Errorf("default out label not applied: %s", join(args))
	}
}

// --------------------------------------------------------------- recorder

func TestRecorderArgsPreservesEveryTrack(t *testing.T) {
	args := RecorderArgs(RecorderSpec{
		RelayURL:       "udp://127.0.0.1:20002",
		OutputPattern:  "/data/recordings/rec-%Y%m%d-%H%M%S.mkv",
		SegmentSeconds: 3600,
	})
	s := join(args)

	// The archive is the only copy of the full multitrack mix, so -map 0 -c copy
	// is load-bearing: any narrowing here loses audio permanently.
	if m, _ := argsAfter(args, "-map"); m != "0" {
		t.Errorf("-map = %q, want \"0\" to keep every audio track", m)
	}
	if c, _ := argsAfter(args, "-c"); c != "copy" {
		t.Errorf("-c = %q, want copy", c)
	}
	if f, _ := argsAfter(args, "-f"); f != "segment" {
		t.Errorf("-f = %q, want segment", f)
	}
	if v, _ := argsAfter(args, "-segment_time"); v != "3600" {
		t.Errorf("-segment_time = %q", v)
	}
	if v, _ := argsAfter(args, "-segment_format"); v != "matroska" {
		t.Errorf("-segment_format = %q, want matroska", v)
	}
	if v, _ := argsAfter(args, "-reset_timestamps"); v != "1" {
		t.Error("segments must reset timestamps so each plays standalone")
	}
	if v, _ := argsAfter(args, "-strftime"); v != "1" {
		t.Error("-strftime must be on for the output pattern to expand")
	}
	if strings.Contains(s, "libx264") || strings.Contains(s, "-c:a") {
		t.Errorf("recorder must not encode anything: %s", s)
	}
	if args[len(args)-1] != "/data/recordings/rec-%Y%m%d-%H%M%S.mkv" {
		t.Errorf("output pattern must be last: %s", s)
	}
}

func TestRecorderArgsDefaultSegmentLength(t *testing.T) {
	args := RecorderArgs(RecorderSpec{RelayURL: "udp://127.0.0.1:1", OutputPattern: "/x/%s.mkv"})
	if v, _ := argsAfter(args, "-segment_time"); v != "3600" {
		t.Errorf("default segment = %q, want 3600", v)
	}
}

// --------------------------------------------------------------- preview

func TestPreviewArgs(t *testing.T) {
	args := PreviewArgs(PreviewSpec{
		RelayURL: "udp://127.0.0.1:20003", OutputDir: "/data/hls",
		SegmentSeconds: 2, Height: 360, VideoKbps: 800,
	})
	s := join(args)

	if v, _ := argsAfter(args, "-c:v"); v != "libx264" {
		t.Errorf("preview is the one place we do encode video, got -c:v %q", v)
	}
	if f, _ := argsAfter(args, "-f"); f != "hls" {
		t.Errorf("-f = %q", f)
	}
	// GOP must divide the segment length exactly or players stall at each
	// segment boundary.
	g, _ := argsAfter(args, "-g")
	kmin, _ := argsAfter(args, "-keyint_min")
	if g != "60" || kmin != "60" {
		t.Errorf("-g/%s -keyint_min/%s must both be segment*30 = 60", g, kmin)
	}
	if !strings.Contains(s, "delete_segments") {
		t.Error("preview must delete old segments or it fills the disk")
	}
	if !strings.Contains(s, "scale=-2:360") {
		t.Errorf("scale filter missing: %s", s)
	}
	// The '?' makes the audio map optional, so a video-only ingest still
	// previews instead of failing to start.
	if !strings.Contains(s, "0:a:0?") {
		t.Errorf("audio map should be optional: %s", s)
	}
	if args[len(args)-1] != "/data/hls/index.m3u8" {
		t.Errorf("playlist must be last: %s", s)
	}
}

// ---------------------------------------------------------------- meters

func TestMetersArgsMergesTracksIntoOneStream(t *testing.T) {
	args := MetersArgs(MetersSpec{
		RelayURL:      "udp://127.0.0.1:20004",
		TrackChannels: []int{2, 2, 6},
	})
	fc, ok := argsAfter(args, "-filter_complex")
	if !ok {
		t.Fatal("no filter_complex")
	}

	want := "[0:a:0]aresample=48000[mt0];" +
		"[0:a:1]aresample=48000[mt1];" +
		"[0:a:2]aresample=48000[mt2];" +
		"[mt0][mt1][mt2]amerge=inputs=3[mgd];" +
		"[mgd]astats=metadata=1:reset=1:length=0.1:measure_perchannel=Peak_level+RMS_level:measure_overall=none," +
		"ametadata=mode=print:file=-[mout]"
	if fc != want {
		t.Errorf("\n got  %s\n want %s", fc, want)
	}

	// This child prints metering data on stdout, so -progress must not also
	// be writing there.
	if has(args, "-progress") {
		t.Error("meters must not use -progress: it would interleave with the level output on stdout")
	}
	if f, _ := argsAfter(args, "-f"); f != "null" {
		t.Errorf("-f = %q, want null (we want the analysis, not the audio)", f)
	}
}

func TestMetersArgsSingleTrackSkipsAmerge(t *testing.T) {
	args := MetersArgs(MetersSpec{RelayURL: "udp://127.0.0.1:1", TrackChannels: []int{2}})
	fc, _ := argsAfter(args, "-filter_complex")
	if strings.Contains(fc, "amerge") {
		t.Errorf("one track needs no merge: %q", fc)
	}
	if !strings.Contains(fc, "[mt0]astats") {
		t.Errorf("single track must feed astats directly: %q", fc)
	}
}

func TestMetersChannelOffsets(t *testing.T) {
	s := MetersSpec{TrackChannels: []int{2, 6, 1}}
	if got := s.TotalChannels(); got != 9 {
		t.Errorf("total = %d, want 9", got)
	}
	// astats numbers the merged channels 1..9; these offsets are what maps
	// them back onto tracks.
	for track, want := range map[int]int{0: 0, 1: 2, 2: 8} {
		if got := s.ChannelOffset(track); got != want {
			t.Errorf("ChannelOffset(%d) = %d, want %d", track, got, want)
		}
	}
}

// ---------------------------------------------------------------- probe

func TestParseProbe(t *testing.T) {
	raw := []byte(`{"streams":[
		{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"pix_fmt":"yuv420p","avg_frame_rate":"60/1","bit_rate":"6000000"},
		{"codec_type":"audio","codec_name":"aac","channels":2,"channel_layout":"stereo","sample_rate":"48000","bit_rate":"160000","tags":{"language":"eng","title":"Full mix"}},
		{"codec_type":"audio","codec_name":"aac","channels":2,"channel_layout":"stereo","sample_rate":"48000"},
		{"codec_type":"audio","codec_name":"aac","channels":6,"channel_layout":"5.1","sample_rate":"48000"}
	]}`)
	res, err := ParseProbe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Video == nil || res.Video.Height != 1080 || res.Video.FrameRate != 60 {
		t.Fatalf("video = %+v", res.Video)
	}
	if len(res.Audio) != 3 {
		t.Fatalf("want 3 audio tracks, got %d", len(res.Audio))
	}
	// Index must be the position among AUDIO streams, because that is what
	// the a:N specifier in the routing graph means.
	for i, a := range res.Audio {
		if a.Index != i {
			t.Errorf("track %d has Index %d; must be 0-based among audio streams", i, a.Index)
		}
	}
	if res.Audio[0].Title != "Full mix" || res.Audio[0].Language != "eng" {
		t.Errorf("tags not read: %+v", res.Audio[0])
	}
	if res.Audio[2].Channels != 6 || res.Audio[2].Layout != "5.1" {
		t.Errorf("surround track = %+v", res.Audio[2])
	}
}

func TestParseProbeFillsMissingLayout(t *testing.T) {
	res, err := ParseProbe([]byte(`{"streams":[{"codec_type":"audio","codec_name":"aac","channels":6}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Audio[0].Layout != "5.1" {
		t.Errorf("layout = %q, want 5.1 inferred from channel count", res.Audio[0].Layout)
	}
}

func TestParseProbeIgnoresSecondVideoStream(t *testing.T) {
	// Some encoders attach cover art as a second video stream; treating that
	// as the program video would be a disaster.
	res, err := ParseProbe([]byte(`{"streams":[
		{"codec_type":"video","codec_name":"h264","width":1920,"height":1080},
		{"codec_type":"video","codec_name":"mjpeg","width":64,"height":64}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Video.Codec != "h264" {
		t.Errorf("video = %+v, want the first (real) stream", res.Video)
	}
}

func TestProbeArgs(t *testing.T) {
	args := ProbeArgs("udp://127.0.0.1:20000", 5)
	if !has(args, "-show_streams") || !has(args, "-print_format") {
		t.Errorf("args = %v", args)
	}
	if v, _ := argsAfter(args, "-analyzeduration"); v != "5000000" {
		t.Errorf("-analyzeduration = %q, want microseconds", v)
	}
	if !strings.Contains(join(args), "overrun_nonfatal") {
		t.Errorf("probe should tolerate relay overrun too: %s", join(args))
	}
}

// ------------------------------------------------------------- progress

func TestParseProgress(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		"frame=120", "fps=30.0", "bitrate=4500.2kbits/s", "total_size=1048576",
		"out_time_us=4000000", "dup_frames=0", "drop_frames=2", "speed=1.01x",
		"progress=continue",
		"frame=240", "fps=30.0", "bitrate=4600.0kbits/s", "total_size=2097152",
		"out_time_us=8000000", "dup_frames=1", "drop_frames=2", "speed=1.00x",
		"progress=end",
	}, "\n"))

	var got []Progress
	if err := ParseProgress(in, func(p Progress) { got = append(got, p) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(got))
	}
	if got[0].Frame != 120 || got[0].BitrateKbps != 4500.2 || got[0].Speed != 1.01 {
		t.Errorf("block 0 = %+v", got[0])
	}
	// out_time_us is microseconds; OutTimeMS must be milliseconds.
	if got[0].OutTimeMS != 4000 {
		t.Errorf("OutTimeMS = %d, want 4000 (4s)", got[0].OutTimeMS)
	}
	if got[0].Done {
		t.Error("first block must not be marked done")
	}
	if !got[1].Done {
		t.Error("progress=end must set Done")
	}
	if got[1].DropFrames != 2 {
		t.Errorf("drop_frames = %d", got[1].DropFrames)
	}
}

// FFmpeg reports out_time_ms whose value is actually microseconds — a
// long-standing quirk. Both spellings must be treated identically.
func TestParseProgressOutTimeMsIsReallyMicroseconds(t *testing.T) {
	in := strings.NewReader("out_time_ms=9000000\nprogress=continue\n")
	var got Progress
	_ = ParseProgress(in, func(p Progress) { got = p })
	if got.OutTimeMS != 9000 {
		t.Errorf("OutTimeMS = %d, want 9000", got.OutTimeMS)
	}
}

func TestParseProgressHandlesNA(t *testing.T) {
	in := strings.NewReader("bitrate=N/A\nspeed=N/A\nframe=0\nprogress=continue\n")
	var got Progress
	if err := ParseProgress(in, func(p Progress) { got = p }); err != nil {
		t.Fatal(err)
	}
	if got.BitrateKbps != 0 || got.Speed != 0 {
		t.Errorf("N/A must parse as zero, got %+v", got)
	}
}

// -------------------------------------------------------------- levels

func TestParseLevels(t *testing.T) {
	// Two stereo tracks -> merged channels 1..4.
	in := strings.NewReader(strings.Join([]string{
		"frame:0    pts:0       pts_time:0",
		"lavfi.astats.1.Peak_level=-18.063656",
		"lavfi.astats.1.RMS_level=-21.024165",
		"lavfi.astats.2.Peak_level=-38.053056",
		"lavfi.astats.2.RMS_level=-41.021843",
		"lavfi.astats.3.Peak_level=-24.082135",
		"lavfi.astats.3.RMS_level=-27.108714",
		"lavfi.astats.4.Peak_level=-6.0",
		"lavfi.astats.4.RMS_level=-9.0",
		"frame:1    pts:1024    pts_time:0.021",
		"lavfi.astats.1.Peak_level=-inf",
		"lavfi.astats.1.RMS_level=-inf",
		"lavfi.astats.2.Peak_level=-12.0",
		"lavfi.astats.2.RMS_level=-15.0",
		"lavfi.astats.3.Peak_level=nan",
		"lavfi.astats.3.RMS_level=nan",
		"lavfi.astats.4.Peak_level=0.5",
		"lavfi.astats.4.RMS_level=-3.0",
		"frame:2    pts:2048    pts_time:0.042",
	}, "\n"))

	var frames []Levels
	if err := ParseLevels(in, []int{2, 2}, func(l Levels) { frames = append(frames, l) }); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("want 2 frames, got %d", len(frames))
	}

	f0 := frames[0]
	if len(f0.Peak) != 2 || len(f0.Peak[0]) != 2 || len(f0.Peak[1]) != 2 {
		t.Fatalf("shape = %v", f0.Peak)
	}
	// Merged channel 3 is track 1 channel 0 — the de-interleaving that makes
	// the whole single-process metering design work.
	if f0.Peak[1][0] != -24.082135 {
		t.Errorf("track 1 ch 0 peak = %v, want -24.082135", f0.Peak[1][0])
	}
	if f0.Peak[0][1] != -38.053056 {
		t.Errorf("track 0 ch 1 peak = %v", f0.Peak[0][1])
	}
	if f0.RMS[1][1] != -9.0 {
		t.Errorf("track 1 ch 1 rms = %v", f0.RMS[1][1])
	}

	f1 := frames[1]
	if f1.Peak[0][0] != SilenceDB {
		t.Errorf("-inf must clamp to %v, got %v", SilenceDB, f1.Peak[0][0])
	}
	if f1.Peak[1][0] != SilenceDB {
		t.Errorf("nan must clamp to %v, got %v", SilenceDB, f1.Peak[1][0])
	}
	// A meter cannot draw past full scale.
	if f1.Peak[1][1] != 0 {
		t.Errorf("above-0 dBFS must clamp to 0, got %v", f1.Peak[1][1])
	}
}

func TestParseLevelsSurroundTrack(t *testing.T) {
	// stereo + 5.1 -> merged channels 1,2 then 3..8
	var sb strings.Builder
	sb.WriteString("frame:0 pts:0\n")
	for i := 1; i <= 8; i++ {
		sb.WriteString("lavfi.astats." + itoa(i) + ".Peak_level=-" + itoa(i) + ".0\n")
	}
	sb.WriteString("frame:1 pts:1\n")

	var got Levels
	if err := ParseLevels(strings.NewReader(sb.String()), []int{2, 6}, func(l Levels) { got = l }); err != nil {
		t.Fatal(err)
	}
	if len(got.Peak[1]) != 6 {
		t.Fatalf("surround track should have 6 channels, got %d", len(got.Peak[1]))
	}
	// Merged channel 8 (-8.0) is the last channel of the 5.1 track.
	if got.Peak[1][5] != -8.0 {
		t.Errorf("track 1 ch 5 = %v, want -8.0", got.Peak[1][5])
	}
	if got.Peak[0][0] != -1.0 {
		t.Errorf("track 0 ch 0 = %v, want -1.0", got.Peak[0][0])
	}
}

func TestParseLevelsIgnoresUnknownChannels(t *testing.T) {
	in := strings.NewReader("frame:0\nlavfi.astats.99.Peak_level=-1.0\nlavfi.astats.1.Peak_level=-2.0\nframe:1\n")
	var got Levels
	err := ParseLevels(in, []int{2}, func(l Levels) { got = l })
	if err != nil {
		t.Fatal(err)
	}
	if got.Peak[0][0] != -2.0 {
		t.Errorf("peak = %v", got.Peak[0][0])
	}
}

func indexOf(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
