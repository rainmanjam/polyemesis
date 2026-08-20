package ffmpeg

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

// The RTMP ingest DIALS the shared listener; it does not bind one. 0.0.0.0 as a
// dial target is not the same address spelled differently -- it is the one that
// must not survive -- and a non-loopback host is refused by rtmpserver outright,
// because a stream key is a publish credential and must not authorise playback.
func TestIngestURLRTMPSubscribesOnLoopback(t *testing.T) {
	s := IngestSpec{Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live", RTMPAddress: "tok-abc"}
	if got, want := s.IngestURL(), "rtmp://127.0.0.1:1935/live/tok-abc"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

// The address is what selects the source on a listener shared by all of them.
// Dropping it would put every RTMP source on the same path, which is one
// programme receiving another's video -- the exact failure the one-port work
// exists to remove.
func TestIngestURLRTMPCarriesTheAddress(t *testing.T) {
	a := IngestSpec{Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live", RTMPAddress: "tok-a"}
	b := IngestSpec{Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live", RTMPAddress: "tok-b"}
	if a.IngestURL() == b.IngestURL() {
		t.Fatalf("two sources dial the same URL %q; one would receive the other's stream", a.IngestURL())
	}
	if !strings.HasSuffix(a.IngestURL(), "/tok-a") {
		t.Errorf("url = %q, want it to end in the address", a.IngestURL())
	}
}

// OBS's two-box form: the server URL and the stream key are separate fields, so
// the public URL must be the server half ALONE. Emitting the address here too
// gives the operator who fills in both /live/<token>/<token>, which reaches
// nothing and looks like it should.
func TestPublicIngestURLRTMPIsTheServerHalfOnly(t *testing.T) {
	s := IngestSpec{Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live", RTMPAddress: "tok-abc"}
	if got, want := s.PublicIngestURL("stream.example.com"), "rtmp://stream.example.com:1935/live"; got != want {
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
		Kind: IngestRTMP, RTMPPort: 1935, RTMPApp: "live", RTMPAddress: "tok-abc",
		RelayURL: "udp://127.0.0.1:20000",
	})
	// -listen 1 made FFmpeg the RTMP server, and a single-connection one: it is
	// precisely why an install could carry exactly one RTMP source. If it comes
	// back, FFmpeg tries to bind 1935 behind polyemesis's own listener, fails,
	// and crash-loops forever behind a socket that is working fine.
	if _, ok := argsAfter(args, "-listen"); ok {
		t.Errorf("RTMP ingest must subscribe, not listen: %s", join(args))
	}
	input, _ := argsAfter(args, "-i")
	if input != "rtmp://127.0.0.1:1935/live/tok-abc" {
		t.Errorf("-i = %q, want the loopback subscribe URL", input)
	}
}

// ------------------------------------------------------------- pull ingest

func TestValidatePullURLAcceptsEveryAllowedScheme(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"rtsp camera", "rtsp://cam.local:554/stream1", true},
		{"rtsps camera", "rtsps://cam.local:322/stream1", true},
		{"http mpegts", "http://origin.example/live.ts", true},
		{"https hls playlist", "https://origin.example/live/index.m3u8", true},
		{"hls scheme", "hls://origin.example/live/index.m3u8", true},
		{"dash scheme", "dash://origin.example/live/manifest.mpd", true},
		{"srt caller", "srt://peer.example:9000?mode=caller", true},
		{"rtmp relay", "rtmp://peer.example/live/key", true},
		// Refusing to read a transport we happily write would be the
		// restrictive kind of wrong.
		{"rtmps relay", "rtmps://peer.example/live/key", true},
		{"relative file", "file://loops/bars.ts", true},
		{"uppercase scheme", "RTSP://cam.local/stream1", true},

		{"empty", "", false},
		{"no scheme", "cam.local/stream1", false},
		{"bare path", "/var/media/loop.ts", false},
		// concat: and friends are one settings write away without the list.
		{"concat protocol", "concat://a.ts|b.ts", false},
		{"gopher", "gopher://evil.example/1", false},
		{"data uri", "data://text/plain,hi", false},
		{"pipe", "pipe://0", false},
		{"newline injection", "http://ok.example/a\nrtsp://b", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePullURL(tc.url)
			if tc.ok && err != nil {
				t.Fatalf("ValidatePullURL(%q) = %v, want accepted", tc.url, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("ValidatePullURL(%q) accepted, want rejected", tc.url)
			}
		})
	}
}

func TestPullFileSourceIsConfinedToDataDir(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"relative", "file://loops/bars.ts", true},
		{"nested relative", "file://a/b/c/bars.ts", true},
		{"absolute unix", "file:///etc/shadow", false},
		{"traversal", "file://../../secret.key", false},
		{"traversal mid path", "file://loops/../../secret.key", false},
		{"backslash traversal", `file://..\..\secret.key`, false},
		{"windows drive", "file://C:/Windows/win.ini", false},
		{"empty path", "file://", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePullURL(tc.url)
			if tc.ok != (err == nil) {
				t.Fatalf("ValidatePullURL(%q) = %v, want ok=%v", tc.url, err, tc.ok)
			}
		})
	}
}

func TestPullFileSourceResolvesUnderDataDir(t *testing.T) {
	s := IngestSpec{Kind: IngestPull, PullURL: "file://loops/bars.ts", PullDataDir: "/srv/polyemesis/data"}
	got, err := s.PullSource()
	if err != nil {
		t.Fatalf("PullSource: %v", err)
	}
	// The file: prefix keeps a filename containing a colon from being re-read
	// as a protocol name.
	if want := "file:" + filepath.Join("/srv/polyemesis/data", "loops", "bars.ts"); got != want {
		t.Errorf("PullSource = %q, want %q", got, want)
	}
}

func TestIngestURLRefusesToRenderARejectedPullSource(t *testing.T) {
	// If IngestURL echoed the raw string back, an escaping file:// path would
	// reach FFmpeg anyway and the confinement check would be decorative.
	s := IngestSpec{Kind: IngestPull, PullURL: "file:///etc/shadow", PullDataDir: "/srv/data"}
	if got := s.IngestURL(); got != "" {
		t.Errorf("IngestURL = %q, want empty for a rejected source", got)
	}
}

func TestIngestArgsPullKeepsCopyContract(t *testing.T) {
	args := IngestArgs(IngestSpec{
		Kind:     IngestPull,
		PullURL:  "rtsp://cam.local:554/stream1",
		RelayURL: "udp://127.0.0.1:20000",
	})

	// Pull changes where bytes come from, never what happens to them.
	if m, _ := argsAfter(args, "-map"); m != "0" {
		t.Errorf("-map = %q, want \"0\" so every track survives", m)
	}
	if c, _ := argsAfter(args, "-c"); c != "copy" {
		t.Errorf("-c = %q, want copy: the ingest must never re-encode", c)
	}
	if f, _ := argsAfter(args, "-f"); f != "mpegts" {
		t.Errorf("-f = %q, want mpegts", f)
	}
	if i, _ := argsAfter(args, "-i"); i != "rtsp://cam.local:554/stream1" {
		t.Errorf("-i = %q", i)
	}
	if !strings.Contains(join(args), "udp://127.0.0.1:20000?pkt_size=1316") {
		t.Errorf("relay output missing TS-aligned pkt_size: %s", join(args))
	}
	// A pull ingest is not a listener; -listen belongs to the RTMP server path.
	if has(args, "-listen") {
		t.Errorf("pull mode must not listen: %s", join(args))
	}
	for _, bad := range []string{"libx264", "-b:v", "-crf", "aac"} {
		if strings.Contains(join(args), bad) {
			t.Errorf("ingest must not encode, found %q in: %s", bad, join(args))
		}
	}
}

func TestIngestArgsPullReconnectFlagsPerScheme(t *testing.T) {
	cases := []struct {
		name    string
		spec    IngestSpec
		want    []string
		absent  []string
		wantVal map[string]string
	}{
		{
			name: "http gets streamed reconnect",
			spec: IngestSpec{Kind: IngestPull, PullURL: "http://origin.example/live.ts"},
			want: []string{"-reconnect", "-reconnect_streamed", "-reconnect_delay_max"},
			// -reconnect alone only retries seekable inputs, which a live
			// source never is.
			wantVal: map[string]string{
				"-reconnect":           "1",
				"-reconnect_streamed":  "1",
				"-reconnect_delay_max": "30",
			},
			// -protocol_whitelist is scoped to the file family. On an HTTP pull
			// it would refuse the protocol the source needs.
			absent: []string{"-rtsp_transport", "-stream_loop", "-re", "-protocol_whitelist"},
		},
		{
			name:    "https honours a custom backoff ceiling",
			spec:    IngestSpec{Kind: IngestPull, PullURL: "https://origin.example/i.m3u8", PullReconnectDelayMax: 5},
			want:    []string{"-reconnect_delay_max"},
			wantVal: map[string]string{"-reconnect_delay_max": "5"},
		},
		{
			name: "rtsp defaults to tcp",
			spec: IngestSpec{Kind: IngestPull, PullURL: "rtsp://cam.local/stream1"},
			want: []string{"-rtsp_transport"},
			// UDP RTSP through NAT connects and then delivers nothing.
			wantVal: map[string]string{"-rtsp_transport": "tcp"},
			absent:  []string{"-reconnect", "-stream_loop", "-protocol_whitelist"},
		},
		{
			name:    "rtsp transport is overridable",
			spec:    IngestSpec{Kind: IngestPull, PullURL: "rtsp://cam.local/s", PullRTSPTransport: "udp"},
			want:    []string{"-rtsp_transport"},
			wantVal: map[string]string{"-rtsp_transport": "udp"},
		},
		{
			name: "file loops at wall-clock speed",
			spec: IngestSpec{Kind: IngestPull, PullURL: "file://loops/bars.ts", PullDataDir: "/srv/data"},
			// Without -re the file is read at disk speed and floods the relay.
			want:    []string{"-stream_loop", "-re", "-protocol_whitelist"},
			wantVal: map[string]string{"-stream_loop": "-1", "-protocol_whitelist": "file"},
			absent:  []string{"-reconnect", "-rtsp_transport"},
		},
		{
			name:   "srt needs no extra input flags",
			spec:   IngestSpec{Kind: IngestPull, PullURL: "srt://peer.example:9000"},
			absent: []string{"-reconnect", "-rtsp_transport", "-stream_loop", "-re", "-protocol_whitelist"},
		},
		{
			name:   "rtmp needs no extra input flags",
			spec:   IngestSpec{Kind: IngestPull, PullURL: "rtmp://peer.example/live/k"},
			absent: []string{"-reconnect", "-rtsp_transport", "-stream_loop", "-listen", "-protocol_whitelist"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.RelayURL = "udp://127.0.0.1:20000"
			args := IngestArgs(tc.spec)
			for _, flag := range tc.want {
				if !has(args, flag) {
					t.Errorf("missing %s in: %s", flag, join(args))
				}
			}
			for flag, want := range tc.wantVal {
				if got, _ := argsAfter(args, flag); got != want {
					t.Errorf("%s = %q, want %q", flag, got, want)
				}
			}
			for _, flag := range tc.absent {
				if has(args, flag) {
					t.Errorf("unexpected %s in: %s", flag, join(args))
				}
			}
			// Input options are meaningless after -i.
			ii := indexOf(args, "-i")
			for _, flag := range tc.want {
				if fi := indexOf(args, flag); fi < 0 || fi > ii {
					t.Errorf("%s must precede -i: %s", flag, join(args))
				}
			}
		})
	}
}

func TestPublicIngestURLReportsThePullSource(t *testing.T) {
	// Nobody points an encoder anywhere in pull mode, so the dashboard shows
	// what polyemesis dials instead of an address that does not exist.
	s := IngestSpec{Kind: IngestPull, PullURL: " rtsp://cam.local/stream1 "}
	if got, want := s.PublicIngestURL("stream.example.com"), "rtsp://cam.local/stream1"; got != want {
		t.Errorf("PublicIngestURL = %q, want %q", got, want)
	}
}

func TestPullSchemesAreStableAndSorted(t *testing.T) {
	got := PullSchemes()
	want := []string{"dash", "file", "hls", "http", "https", "rtmp", "rtmps", "rtsp", "rtsps", "srt"}
	if len(got) != len(want) {
		t.Fatalf("PullSchemes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PullSchemes() = %v, want %v", got, want)
		}
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
	// The keyframe interval is stated in SECONDS, not frames. `-g segment*30`
	// was a 30 fps assumption baked into a field nobody sets, and it made every
	// segment on a 25 fps ingest 20% too long.
	if fkf, _ := argsAfter(args, "-force_key_frames"); fkf != "expr:gte(t,n_forced*2)" {
		t.Errorf("-force_key_frames = %q, want the segment length in seconds", fkf)
	}
	for _, gone := range []string{"-g", "-keyint_min"} {
		if _, ok := argsAfter(args, gone); ok {
			t.Errorf("%s is still present; it is a frame count and cannot be "+
				"correct at more than one frame rate", gone)
		}
	}
	// Without this x264 inserts its own keyframes between the forced ones on a
	// scene change, and the segments stop landing on GOP boundaries again.
	if sc, _ := argsAfter(args, "-sc_threshold"); sc != "0" {
		t.Errorf("-sc_threshold = %q, want 0", sc)
	}
	if !strings.Contains(s, "delete_segments") {
		t.Error("preview must delete old segments or it fills the disk")
	}
	// The only mechanism by which live-edge latency is a measured number rather
	// than a claimed one.
	if !strings.Contains(s, "program_date_time") {
		t.Error("program_date_time missing; latency becomes unmeasurable")
	}
	// A player at the edge of its allowed latency must still be able to fetch
	// the segment it is asking for. hls.js seeks at 6 target durations, and the
	// window is listSize x segment -- equal at every segment length with a
	// constant 6, so the size is derived to buy margin at short segments.
	if ls, _ := argsAfter(args, "-hls_list_size"); ls != "6" {
		t.Errorf("-hls_list_size at 2s segments = %q, want 6", ls)
	}
	short := PreviewArgs(PreviewSpec{
		RelayURL: "udp://127.0.0.1:20003", OutputDir: "/data/hls",
		SegmentSeconds: 1, Height: 360, VideoKbps: 800,
	})
	if ls, _ := argsAfter(short, "-hls_list_size"); ls != "8" {
		t.Errorf("-hls_list_size at 1s segments = %q, want 8 so the window "+
			"does not shrink with the segment", ls)
	}
	if fkf, _ := argsAfter(short, "-force_key_frames"); fkf != "expr:gte(t,n_forced*1)" {
		t.Errorf("1s -force_key_frames = %q", fkf)
	}
	if !strings.Contains(s, "scale=-2:360") {
		t.Errorf("scale filter missing: %s", s)
	}
	// The '?' makes the audio map optional, so a video-only ingest still
	// previews instead of failing to start.
	if !strings.Contains(s, "0:a:0?") {
		t.Errorf("audio map should be optional: %s", s)
	}
	// filepath.Join, not a literal: this is a local filesystem path handed to
	// FFmpeg, so \data\hls\index.m3u8 is the CORRECT rendering on Windows and
	// the hardcoded forward-slash expectation was the bug. Contrast fileURL in
	// internal/clipper, where the platform must NOT influence the result --
	// there the output is a URL that travels to another machine.
	if want := filepath.Join("/data/hls", "index.m3u8"); args[len(args)-1] != want {
		t.Errorf("playlist must be last, got %q want %q: %s", args[len(args)-1], want, s)
	}
}

// TestPreviewSegmentsAreTheLengthTheyClaim runs the real keyframe expression
// through the real FFmpeg at a NON-30 frame rate, because that is the only way
// to catch the bug this replaced.
//
// `-g SegmentSeconds*30` looked right and passed every string test: 1s at 30fps
// is 30 frames. On a 25 fps ingest it is 30 frames of a 25 fps stream, i.e.
// 1.2 seconds, and every segment overshoots by 20% forever. Nothing that
// inspects arguments can see that; it needs a measurement at a frame rate the
// old constant was wrong about.
func TestPreviewSegmentsAreTheLengthTheyClaim(t *testing.T) {
	bins := needFFmpeg(t, "ffmpeg")
	const segment = 1

	args := PreviewArgs(PreviewSpec{
		RelayURL: "udp://127.0.0.1:20003", OutputDir: "/tmp",
		SegmentSeconds: segment, Height: 360, VideoKbps: 800,
	})
	expr, ok := argsAfter(args, "-force_key_frames")
	if !ok {
		t.Fatal("no -force_key_frames in the preview arguments")
	}

	dir := t.TempDir()
	for _, fps := range []int{25, 30} {
		t.Run(fmt.Sprintf("%dfps", fps), func(t *testing.T) {
			out := filepath.Join(dir, fmt.Sprintf("i%d.m3u8", fps))
			cmd := exec.Command(bins[0], "-hide_banner", "-loglevel", "error",
				"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=320x180:rate=%d", fps),
				"-t", "6",
				"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
				"-force_key_frames", expr,
				"-sc_threshold", "0",
				"-f", "hls",
				"-hls_time", strconv.Itoa(segment),
				"-hls_list_size", "0",
				"-hls_segment_filename", filepath.Join(dir, fmt.Sprintf("s%d_%%03d.ts", fps)),
				out)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg: %v\n%s", err, b)
			}

			pl, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("playlist: %v", err)
			}
			// Skip the last EXTINF: the final segment is whatever remains when
			// the input ends and is legitimately short.
			var durs []float64
			for _, line := range strings.Split(string(pl), "\n") {
				if !strings.HasPrefix(line, "#EXTINF:") {
					continue
				}
				v, err := strconv.ParseFloat(strings.TrimSuffix(
					strings.TrimPrefix(line, "#EXTINF:"), ","), 64)
				if err == nil {
					durs = append(durs, v)
				}
			}
			if len(durs) < 3 {
				t.Fatalf("only %d segments; not enough to judge", len(durs))
			}
			durs = durs[:len(durs)-1]

			// One frame of tolerance. The old -g form produced a flat 1.2 on
			// the 25 fps case, which is 5x this budget away.
			tol := 1.0 / float64(fps)
			for i, d := range durs {
				if math.Abs(d-float64(segment)) > tol {
					t.Errorf("segment %d is %.4fs, want %ds +/- one frame (%.4fs). "+
						"A keyframe interval counted in FRAMES cannot be right at "+
						"more than one frame rate.", i, d, segment, tol)
				}
			}
			t.Logf("%d fps: %d segments, first %.4fs, last %.4fs",
				fps, len(durs), durs[0], durs[len(durs)-1])
		})
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

	want := "[0:a:0]aresample=48000,aformat=channel_layouts=stereo[mt0];" +
		"[0:a:1]aresample=48000,aformat=channel_layouts=stereo[mt1];" +
		"[0:a:2]aresample=48000,aformat=channel_layouts=5.1[mt2];" +
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

// A build that lists "srtp" (Secure RTP) but has no SRT support must be
// rejected. A substring check passes it, and the user then gets
// "Protocol not found" on their first stream instead of a clear startup error.
func TestHasProtocolRejectsSubstringMatches(t *testing.T) {
	// Real output from an FFmpeg 8.x build without libsrt.
	const withoutSRT = `Supported file protocols:
Input:
  file ftp http https rtmp rtmps rtp srtp tcp tls udp
Output:
  file ftp http https rtmp rtmps rtp srtp tcp tls udp
`
	if hasProtocol(withoutSRT, "srt") {
		t.Error("srtp must not satisfy a check for srt")
	}
	if !hasProtocol(withoutSRT, "srtp") {
		t.Error("srtp itself should be found")
	}
	if !hasProtocol(withoutSRT, "rtmp") {
		t.Error("rtmp should be found")
	}

	const withSRT = "Input:\n  file srt srtp udp\nOutput:\n  file srt srtp udp\n"
	if !hasProtocol(withSRT, "srt") {
		t.Error("a build that really has srt must be accepted")
	}
}

// amerge refuses to negotiate when its inputs have ambiguous channel layouts:
// three mono tracks make three channels, which could be 3.0 or 2.1, and FFmpeg
// fails the whole graph with "could not choose their formats". Pinning each
// input leg's layout is the fix, and it must be on the INPUTS -- constraining
// amerge's output does not help. Regression test: a mono mic track is an
// extremely common setup, so this broke metering for many real users.
func TestMetersArgsPinsInputChannelLayouts(t *testing.T) {
	args := MetersArgs(MetersSpec{RelayURL: "udp://127.0.0.1:1", TrackChannels: []int{1, 1, 1}})
	fc, _ := argsAfter(args, "-filter_complex")

	want := "[0:a:0]aresample=48000,aformat=channel_layouts=mono[mt0];" +
		"[0:a:1]aresample=48000,aformat=channel_layouts=mono[mt1];" +
		"[0:a:2]aresample=48000,aformat=channel_layouts=mono[mt2];" +
		"[mt0][mt1][mt2]amerge=inputs=3[mgd];" +
		"[mgd]astats=metadata=1:reset=1:length=0.1:measure_perchannel=Peak_level+RMS_level:measure_overall=none," +
		"ametadata=mode=print:file=-[mout]"
	if fc != want {
		t.Errorf("\n got  %s\n want %s", fc, want)
	}
}

func TestChannelLayoutName(t *testing.T) {
	// These exact spellings are what libavutil parses. A wrong one fails at
	// runtime during filter negotiation, not at startup.
	tests := map[int]string{
		1: "mono", 2: "stereo", 3: "3.0", 4: "quad",
		5: "5.0", 6: "5.1", 7: "6.1", 8: "7.1", 12: "12c",
	}
	for channels, want := range tests {
		if got := ChannelLayoutName(channels); got != want {
			t.Errorf("ChannelLayoutName(%d) = %q, want %q", channels, got, want)
		}
	}
}

// ---------------------------------------------------------------- expert args

// The two positions are the whole contract: FFmpeg binds an option to the input
// or output that FOLLOWS it, so an argument in the wrong place is not merely
// misplaced, it is silently inert.
func TestSpliceExtraArgsPutsArgumentsWhereFFmpegReadsThem(t *testing.T) {
	tests := []struct {
		name string
		base []string
		in   []string
		out  []string
		want []string
	}{
		{
			name: "nothing to splice returns the command untouched",
			base: []string{"-i", "udp://x", "-f", "flv", "rtmp://y"},
			want: []string{"-i", "udp://x", "-f", "flv", "rtmp://y"},
		},
		{
			name: "input args land immediately before -i",
			base: []string{"-hide_banner", "-i", "udp://x", "-f", "flv", "rtmp://y"},
			in:   []string{"-analyzeduration", "10M"},
			want: []string{"-hide_banner", "-analyzeduration", "10M", "-i", "udp://x", "-f", "flv", "rtmp://y"},
		},
		{
			name: "output args land immediately before the target",
			base: []string{"-i", "udp://x", "-f", "flv", "rtmp://y"},
			out:  []string{"-muxdelay", "0"},
			want: []string{"-i", "udp://x", "-f", "flv", "-muxdelay", "0", "rtmp://y"},
		},
		{
			name: "both at once",
			base: []string{"-i", "udp://x", "-f", "flv", "rtmp://y"},
			in:   []string{"-re"},
			out:  []string{"-muxdelay", "0"},
			want: []string{"-re", "-i", "udp://x", "-f", "flv", "-muxdelay", "0", "rtmp://y"},
		},
		{
			// Fail open. A shape we did not expect still produces a command the
			// operator can read and judge, rather than an error that tells them
			// nothing about what their destination is running.
			name: "a command with no -i gets the arguments appended rather than an error",
			base: []string{"-version"},
			in:   []string{"-re"},
			out:  []string{"-muxdelay", "0"},
			want: []string{"-version", "-re", "-muxdelay", "0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := slices.Clone(tt.base)
			got := SpliceExtraArgs(base, tt.in, tt.out)
			if !slices.Equal(got, tt.want) {
				t.Errorf("SpliceExtraArgs() = %v\nwant %v", got, tt.want)
			}
			if !slices.Equal(base, tt.base) {
				t.Errorf("SpliceExtraArgs mutated the caller's slice: %v", base)
			}
		})
	}
}

// StripExtraArgs is what lets the editor preview a candidate edit against a
// destination that is already running with a previous one. Without it the
// preview would show the new arguments stacked on top of the old.
func TestStripExtraArgsIsTheInverseOfSplice(t *testing.T) {
	base := []string{"-hide_banner", "-i", "udp://x", "-f", "flv", "rtmp://y"}

	cases := [][2][]string{
		{nil, nil},
		{{"-re"}, nil},
		{nil, {"-muxdelay", "0"}},
		{{"-analyzeduration", "10M"}, {"-metadata", "title=x"}},
	}
	for _, c := range cases {
		in, out := c[0], c[1]
		got := StripExtraArgs(SpliceExtraArgs(slices.Clone(base), in, out), in, out)
		if !slices.Equal(got, base) {
			t.Errorf("strip(splice(base, %v, %v)) = %v\nwant %v", in, out, got, base)
		}
	}
}

// Anything that was not built by SpliceExtraArgs comes back untouched. Showing
// the operator the real argv beats showing them a guess at what it should be.
func TestStripExtraArgsLeavesACommandItDoesNotRecognise(t *testing.T) {
	argv := []string{"-i", "udp://x", "-f", "flv", "rtmp://y"}
	got := StripExtraArgs(slices.Clone(argv), []string{"-re"}, []string{"-muxdelay", "0"})
	if !slices.Equal(got, argv) {
		t.Errorf("StripExtraArgs() = %v, want it unchanged: %v", got, argv)
	}
}

// The engine and the API must tokenize the same stored string identically, or
// the command the operator confirmed and the command that runs disagree about
// where one argument ends.
func TestSplitArgs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{name: "empty is no arguments", raw: "", want: nil},
		{name: "plain flags", raw: "-re -muxdelay 0", want: []string{"-re", "-muxdelay", "0"}},
		{
			name: "a double-quoted value keeps its spaces",
			raw:  `-metadata "title=My Show"`,
			want: []string{"-metadata", "title=My Show"},
		},
		{
			name: "a quoted metacharacter is a value, not a shell operator",
			raw:  `-metadata "title=Rock & Roll"`,
			want: []string{"-metadata", "title=Rock & Roll"},
		},
		{
			// Backslash is a path separator on Windows, which this repo now
			// supports. Treating it as an escape would break every such path.
			name: "backslashes survive as path separators",
			raw:  `-i C:\media\out.ts`,
			want: []string{"-i", `C:\media\out.ts`},
		},
		{
			// Filter-graph syntax. Rejecting these would be the restrictive
			// kind of wrong this repo has already paid for three times.
			name: "filter syntax and optional-stream suffixes are accepted",
			raw:  "-map 0:a:1? -filter_complex [0:a]anull[x]",
			want: []string{"-map", "0:a:1?", "-filter_complex", "[0:a]anull[x]"},
		},
		{name: "a bare semicolon is rejected", raw: "-re ; rm -rf /", wantErr: "metacharacter"},
		{name: "a bare pipe is rejected", raw: "-re | tee x", wantErr: "metacharacter"},
		{name: "an unclosed quote is rejected", raw: `-metadata "title=x`, wantErr: "unclosed"},
		{name: "a newline is rejected", raw: "-re\n-muxdelay 0", wantErr: "control character"},
		{
			name:    "an over-long line is rejected",
			raw:     strings.Repeat("a", MaxExtraArgsChars+1),
			wantErr: "too long",
		},
		{
			name:    "too many tokens are rejected",
			raw:     strings.TrimSpace(strings.Repeat("-x ", MaxExtraArgsTokens+1)),
			wantErr: "arguments, limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SplitArgs(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("SplitArgs(%q) err = %v, want it to contain %q", tt.raw, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitArgs(%q) = %v", tt.raw, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("SplitArgs(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// DestinationArgs is where the splice actually happens for a live process. The
// guarantee that matters is that expert mode ADDS to the routing graph rather
// than displacing it: the explicit maps and the filter_complex are still there.
func TestDestinationArgsSplicesExtrasWithoutDisplacingTheRoutingGraph(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind: DestRTMP, Target: "rtmp://ingest.example/app/key",
		RelayURL: "udp://127.0.0.1:21001", FilterComplex: "[0:a:1]anull[aout]",
		AudioOutLabel: "aout", AudioBitrate: 160, SampleRate: 48000, CopyVideo: true,
		ExtraInputArgs:  []string{"-analyzeduration", "10M"},
		ExtraOutputArgs: []string{"-muxdelay", "0"},
	})

	iAt := slices.Index(args, "-i")
	if iAt < 2 || args[iAt-2] != "-analyzeduration" || args[iAt-1] != "10M" {
		t.Errorf("input args are not immediately before -i: %v", args)
	}
	if n := len(args); args[n-1] != "rtmp://ingest.example/app/key" ||
		args[n-3] != "-muxdelay" || args[n-2] != "0" {
		t.Errorf("output args are not immediately before the target: %v", args)
	}
	if !slices.Contains(args, "[0:a:1]anull[aout]") {
		t.Error("the routing graph is missing")
	}
	if !slices.Contains(args, "[aout]") {
		t.Error("the routing graph's output is no longer mapped")
	}
}

// --------------------------------------------------- audio-only destinations

// The defining property of an audio-only destination: nothing about video
// survives into the command. No map, no codec flag, no muxer that would pull
// one in by default.
func TestDestinationArgsAudioOnlyEmitsNoVideo(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		wantMuxer   string
		wantEncoder string
	}{
		{"icecast mount without an extension defaults to mp3",
			"icecast://source:pw@radio.example:8000/live", "mp3", "libmp3lame"},
		{"icecast mount extension picks the codec",
			"icecast://source:pw@radio.example:8000/live.aac", "adts", "aac"},
		{"ogg mount is opus, not vorbis",
			"icecast://source:pw@radio.example:8000/live.ogg", "ogg", "libopus"},
		{"mp3 file", "podcast.mp3", "mp3", "libmp3lame"},
		{"m4a file", "podcast.m4a", "ipod", "aac"},
		{"aac file", "podcast.aac", "adts", "aac"},
		{"opus file", "podcast.opus", "ogg", "libopus"},
		{"flac file", "archive.flac", "flac", "flac"},
		{"wav file", "archive.wav", "wav", "pcm_s16le"},
		{"unknown extension falls back to mp3 rather than refusing",
			"podcast.weird", "mp3", "libmp3lame"},
		{"no extension falls back to mp3", "podcast", "mp3", "libmp3lame"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := DestinationArgs(DestSpec{
				Kind: DestAudio, Target: tc.target, RelayURL: "udp://127.0.0.1:20010",
				FilterComplex: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
					"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
				AudioBitrate: 160,
			})
			s := join(args)

			maps := allAfter(args, "-map")
			if len(maps) != 1 || maps[0] != "[aout]" {
				t.Fatalf("want exactly one map, the routing graph output; got %v", maps)
			}
			if has(args, "-c:v") {
				t.Errorf("audio-only destination carries a video codec flag: %s", s)
			}
			for _, bad := range []string{"0:v:0", "copy", "-b:v", "libx264"} {
				if has(args, bad) {
					t.Errorf("found video argument %q in an audio-only command: %s", bad, s)
				}
			}
			if got, _ := argsAfter(args, "-c:a"); got != tc.wantEncoder {
				t.Errorf("-c:a = %q, want %q", got, tc.wantEncoder)
			}
			if got, _ := argsAfter(args, "-f"); got != tc.wantMuxer {
				t.Errorf("-f = %q, want %q", got, tc.wantMuxer)
			}
			if args[len(args)-1] != tc.target {
				t.Errorf("target must be last: %s", s)
			}
			if fc, _ := argsAfter(args, "-filter_complex"); !strings.Contains(fc, "pan=stereo") {
				t.Errorf("the routing graph must still be applied, got %q", fc)
			}
			if ac, _ := argsAfter(args, "-ac"); ac != "2" {
				t.Errorf("-ac = %q, want the summed stereo mix", ac)
			}
		})
	}
}

// Icecast needs the Content-Type or a listener gets a download prompt, and it
// needs the credentials and mount to survive verbatim or it gets a 401.
func TestDestinationArgsIcecastHeadersAndTarget(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"icecast://source:pw@radio.example:8000/live", "audio/mpeg"},
		{"icecast://source:pw@radio.example:8000/live.mp3", "audio/mpeg"},
		{"icecast://source:pw@radio.example:8000/live.aac", "audio/aac"},
		{"icecast://source:pw@radio.example:8000/live.ogg", "audio/ogg"},
		{"icecast://source:pw@radio.example:8000/live.opus", "audio/ogg"},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			args := DestinationArgs(DestSpec{
				Kind: DestAudio, Target: tc.target, RelayURL: "udp://127.0.0.1:20011",
				FilterComplex: "[0:a:0]anull[aout]",
			})
			if got, ok := argsAfter(args, "-content_type"); !ok || got != tc.want {
				t.Errorf("-content_type = %q (present=%v), want %q", got, ok, tc.want)
			}
			if args[len(args)-1] != tc.target {
				t.Errorf("credentials and mount must reach FFmpeg unchanged: %v", args)
			}
		})
	}
}

// A dot in the password must not be mistaken for the container.
func TestDestinationArgsIcecastCredentialsDoNotDecideTheCodec(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind: DestAudio, Target: "icecast://source:p.wav@radio.example:8000/live",
		RelayURL: "udp://127.0.0.1:20012", FilterComplex: "[0:a:0]anull[aout]",
	})
	if got, _ := argsAfter(args, "-f"); got != "mp3" {
		t.Errorf("-f = %q, want mp3; the password was read as an extension", got)
	}
}

// A file destination is not an Icecast mount and must not claim to be one.
func TestDestinationArgsAudioFileHasNoIcecastHeaders(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind: DestAudio, Target: "/data/recordings/podcast.mp3",
		RelayURL: "udp://127.0.0.1:20013", FilterComplex: "[0:a:0]anull[aout]",
	})
	if has(args, "-content_type") {
		t.Errorf("-content_type has no meaning for a file: %s", join(args))
	}
}

// -b:a against a lossless container is not fatal, but it warns on every start
// and a log tail full of harmless warnings stops being read.
func TestDestinationArgsAudioOnlyLosslessOmitsBitrate(t *testing.T) {
	for _, target := range []string{"archive.flac", "archive.wav"} {
		args := DestinationArgs(DestSpec{
			Kind: DestAudio, Target: target, RelayURL: "udp://127.0.0.1:20014",
			FilterComplex: "[0:a:0]anull[aout]", AudioBitrate: 320,
		})
		if has(args, "-b:a") {
			t.Errorf("%s: -b:a is meaningless for a lossless codec: %s", target, join(args))
		}
	}
	args := DestinationArgs(DestSpec{
		Kind: DestAudio, Target: "podcast.mp3", RelayURL: "udp://127.0.0.1:20015",
		FilterComplex: "[0:a:0]anull[aout]", AudioBitrate: 320,
	})
	if got, _ := argsAfter(args, "-b:a"); got != "320k" {
		t.Errorf("-b:a = %q, want 320k for a lossy codec", got)
	}
}

func TestDestinationArgsAudioOnlyDefaults(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind: DestAudio, Target: "podcast.mp3", RelayURL: "udp://127.0.0.1:20016",
		FilterComplex: "[0:a:0]anull[aout]",
	})
	if got, _ := argsAfter(args, "-b:a"); got != "160k" {
		t.Errorf("default bitrate = %q, want 160k", got)
	}
	if got, _ := argsAfter(args, "-ar"); got != "48000" {
		t.Errorf("default sample rate = %q", got)
	}
	if got, _ := argsAfter(args, "-map"); got != "[aout]" {
		t.Errorf("default out label not applied: %s", join(args))
	}
}

// Expert mode splices into the same two positions for an audio-only
// destination as for any other, and the target stays last.
func TestDestinationArgsAudioOnlySplicesExtras(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind: DestAudio, Target: "icecast://source:pw@radio.example:8000/live",
		RelayURL: "udp://127.0.0.1:20017", FilterComplex: "[0:a:0]anull[aout]",
		ExtraInputArgs:  []string{"-analyzeduration", "10M"},
		ExtraOutputArgs: []string{"-ice_name", "Studio B"},
	})
	iAt := slices.Index(args, "-i")
	if iAt < 2 || args[iAt-2] != "-analyzeduration" || args[iAt-1] != "10M" {
		t.Errorf("input args are not immediately before -i: %v", args)
	}
	if n := len(args); args[n-1] != "icecast://source:pw@radio.example:8000/live" ||
		args[n-3] != "-ice_name" || args[n-2] != "Studio B" {
		t.Errorf("output args are not immediately before the target: %v", args)
	}
}

// --------------------------------------------------------------- a/v delay

// A negative routing delay means the audio is early, which is only expressible
// as holding the picture back. The graph agent hands that over as VideoDelayMS;
// this is where it becomes an argument.
func TestDestinationArgsNegativeDelayHoldsTheVideoBack(t *testing.T) {
	tests := []struct {
		name  string
		kind  DestKind
		delay int
		want  string // "" => no video bitstream filter at all
	}{
		{"no delay leaves the command exactly as it was", DestRTMP, 0, ""},
		{"a negative delay cannot arrive here as a negative number", DestRTMP, -40, ""},
		{"120 ms", DestRTMP, 120, "setts=pts=PTS+0.120/TB:dts=DTS+0.120/TB"},
		{"1 ms still renders in seconds", DestRTMP, 1, "setts=pts=PTS+0.001/TB:dts=DTS+0.001/TB"},
		{"a whole second keeps three decimals", DestRTMP, 1000, "setts=pts=PTS+1.000/TB:dts=DTS+1.000/TB"},
		{"srt destinations get it too", DestSRT, 250, "setts=pts=PTS+0.250/TB:dts=DTS+0.250/TB"},
		{"file destinations get it too", DestFile, 250, "setts=pts=PTS+0.250/TB:dts=DTS+0.250/TB"},
		{"audio-only has no picture to hold back", DestAudio, 250, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := "rtmp://a.example/live/key"
			if tc.kind == DestAudio {
				target = "podcast.mp3"
			}
			args := DestinationArgs(DestSpec{
				Kind: tc.kind, Target: target, RelayURL: "udp://127.0.0.1:20020",
				FilterComplex: "[0:a:0]aresample=48000:async=1:first_pts=0[aout]",
				VideoDelayMS:  tc.delay,
			})
			got, ok := argsAfter(args, "-bsf:v")
			if tc.want == "" {
				if ok {
					t.Fatalf("-bsf:v = %q, want it absent: %s", got, join(args))
				}
				return
			}
			if !ok {
				t.Fatalf("-bsf:v missing: %s", join(args))
			}
			if got != tc.want {
				t.Errorf("-bsf:v = %q, want %q", got, tc.want)
			}
			// It is an OUTPUT option on the copied video stream. Before -i it
			// would be read as an input option and rejected.
			if slices.Index(args, "-bsf:v") < slices.Index(args, "-i") {
				t.Errorf("-bsf:v must follow -i: %v", args)
			}
			// pts and dts must be set separately. A single ts= writes both from
			// one expression, which collapses them into each other and delivers
			// the wrong offset on any stream carrying B-frames.
			if !strings.Contains(got, "pts=") || !strings.Contains(got, "dts=") {
				t.Errorf("-bsf:v = %q must set pts and dts separately", got)
			}
			// The delay is free only because video is never re-encoded.
			if v, _ := argsAfter(args, "-c:v"); v != "copy" {
				t.Errorf("-c:v = %q; the shift is a bitstream filter and needs copy", v)
			}
		})
	}
}

// Backwards compatibility is the point: a profile that never touched the delay
// must produce the same argv it always did, byte for byte.
func TestDestinationArgsZeroVideoDelayIsByteIdentical(t *testing.T) {
	spec := DestSpec{
		Kind: DestRTMP, Target: "rtmp://a.example/live/key",
		RelayURL: "udp://127.0.0.1:20021", FilterComplex: "[0:a:0]anull[aout]",
		AudioBitrate: 160, SampleRate: 48000, CopyVideo: true,
	}
	withField := spec
	withField.VideoDelayMS = 0
	if !slices.Equal(DestinationArgs(spec), DestinationArgs(withField)) {
		t.Errorf("an unset delay changed the command:\n old %v\n new %v",
			DestinationArgs(spec), DestinationArgs(withField))
	}
}

// Expert arguments are spliced around the generated ones, so the delay has to
// survive sharing the command with them.
func TestDestinationArgsVideoDelaySurvivesTheExpertSplice(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind: DestRTMP, Target: "rtmp://a.example/live/key",
		RelayURL: "udp://127.0.0.1:20022", FilterComplex: "[0:a:0]anull[aout]",
		VideoDelayMS:    200,
		ExtraInputArgs:  []string{"-analyzeduration", "10M"},
		ExtraOutputArgs: []string{"-muxdelay", "0"},
	})
	off := slices.Index(args, "-bsf:v")
	iAt := slices.Index(args, "-i")
	if off < 0 || off < iAt {
		t.Fatalf("-bsf:v lost or misplaced: %v", args)
	}
	if args[iAt-2] != "-analyzeduration" {
		t.Errorf("expert input args are no longer immediately before -i: %v", args)
	}
	if args[off+1] != "setts=pts=PTS+0.200/TB:dts=DTS+0.200/TB" {
		t.Errorf("-bsf:v = %q, want the 0.200 setts expression", args[off+1])
	}
}

// TestTheRelayFIFOIsNotBelowFFmpegsOwnDefault is the guard for how this went
// wrong the first time.
//
// The value was 5000, and the comment beside it called that "~940 KB of slack".
// The arithmetic was right and the conclusion was backwards: FFmpeg's default is
// 28672 packets, so the number written to ADD slack was a 5.7x reduction. Nobody
// re-derived it because it read as deliberate and generous.
//
// The default is asserted against the running binary rather than hardcoded here,
// because a second hardcoded copy of somebody else's default is the same mistake
// in a new place. A build that does not report a default is skipped rather than
// guessed at.
func TestTheRelayFIFOIsNotBelowFFmpegsOwnDefault(t *testing.T) {
	// needFFmpeg, not a bare LookPath+Skip. testenv.FFmpegBinary turns "this
	// machine has no ffmpeg" into a FAILURE when POLYEMESIS_REQUIRE_FFMPEG is
	// armed, which is what stops this check quietly declining to run on the one
	// machine that matters. A hand-rolled t.Skip here would have been three new
	// entries in the skip census buying nothing the helper does not already give.
	bin := needFFmpeg(t, "ffmpeg")[0]

	out, err := exec.Command(bin, "-hide_banner", "-h", "protocol=udp").CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg -h protocol=udp: %v\n%s", err, out)
	}
	// FATAL, not skipped. Every ffmpeg this project supports reports this
	// default -- verified on 6.1.1 and 8.1.2 -- so not finding it means the
	// pattern is wrong, which is a defect in this test rather than a property of
	// the host, and a skip would hide it.
	m := regexp.MustCompile(`-fifo_size\b[^\n]*\(default (\d+)\)`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("no fifo_size default in `ffmpeg -h protocol=udp`; the pattern no longer "+
			"matches this build's help output:\n%s", out)
	}
	def, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("parsing the reported default %q: %v", m[1], err)
	}
	if relayFIFOPackets < def {
		t.Errorf("relayFIFOPackets = %d, below ffmpeg's own default of %d. A receive "+
			"buffer smaller than the default is a restriction, however generous the "+
			"comment beside it sounds — see the note on relayFIFOPackets.",
			relayFIFOPackets, def)
	}
	// And the value has to actually reach the URL every consumer builds.
	if got := RelayInputURL("udp://127.0.0.1:21000"); !strings.Contains(got,
		"fifo_size="+strconv.Itoa(relayFIFOPackets)) {
		t.Errorf("RelayInputURL = %q, which does not carry fifo_size=%d", got, relayFIFOPackets)
	}
}

// A PROBE MUST NOT WAIT FOR EVER ON A QUIET RELAY.
//
// analyzeduration and probesize bound the MEDIA a probe analyses. Neither bounds
// waiting for bytes, and UDP has no EOF -- so before the read timeout, ffprobe
// against a silent relay blocked until something killed it. Measured with the
// exact arguments this function builds: 2.7s against a flowing stream, and no
// return at all against a socket nothing is sending to.
//
// It matters because destinations are HELD until a layout is measured, and the
// hold's exit needs probeGiveUp consecutive failures. A probe that hangs cannot
// fail, so the hold it is gating has no bound.
func TestTheProbeInputCarriesAReadTimeout(t *testing.T) {
	args := ProbeArgs("udp://127.0.0.1:21000", 3)

	var input string
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			input = args[i+1]
		}
	}
	if input == "" {
		t.Fatal("ProbeArgs names no input")
	}
	if !strings.Contains(input, "timeout=3000000") {
		t.Errorf("probe input %q carries no read timeout. analyzeduration bounds the media "+
			"analysed, not the wait for bytes, so on a relay that has gone quiet -- which "+
			"the source selector produces several times in a newly connected encoder's "+
			"first minute -- this probe blocks until it is killed, and a probe that cannot "+
			"fail cannot advance the counter that ends the destination hold", input)
	}
	// The timeout scales with the caller's budget rather than being a second
	// constant that can drift away from it.
	if got := ProbeArgs("udp://127.0.0.1:21000", 7); !strings.Contains(got[len(got)-1], "timeout=7000000") {
		t.Errorf("the read timeout does not follow the caller's seconds: %q", got[len(got)-1])
	}
}

// The relay's OTHER consumers must not inherit it: a destination reading through
// a quiet patch is a feed between switches, and erroring out would take a
// working output off air.
func TestOnlyTheProbeGetsTheReadTimeout(t *testing.T) {
	if u := RelayInputURL("udp://127.0.0.1:21000"); strings.Contains(u, "timeout=") {
		t.Errorf("RelayInputURL carries a read timeout (%q). Every rendition and "+
			"destination reads through this, and a gap between switches would end them", u)
	}
}
