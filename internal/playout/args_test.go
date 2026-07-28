package playout

import (
	"path/filepath"
	"strings"
	"testing"
)

// argIndex reports where flag appears, or -1.
func argIndex(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

// argValue returns the token after flag, or "" when the flag is absent.
func argValue(args []string, flag string) string {
	i := argIndex(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

// argValues returns every token following each occurrence of flag, which is how
// a two-output command line has to be inspected.
func argValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestVariantArgsCopiesVideoAndNeverEncodesIt(t *testing.T) {
	args := VariantArgs(VariantSpec{Name: "hd", RelayURL: "udp://127.0.0.1:21001", Dir: "/d/hd"})

	for _, v := range argValues(args, "-c:v") {
		if v != "copy" {
			t.Fatalf("-c:v is %q; a variant packages the rendition tier's video and must never encode its own", v)
		}
	}
	if n := len(argValues(args, "-c:v")); n == 0 {
		t.Fatal("no -c:v at all; the output would inherit whatever ffmpeg guesses")
	}
	// The encoder flags a second video encode would need must be absent
	// entirely, not merely unused.
	for _, banned := range []string{"libx264", "h264_nvenc", "h264_qsv", "h264_videotoolbox", "h264_vaapi", "h264_amf"} {
		if argIndex(args, banned) >= 0 {
			t.Fatalf("found video encoder %q in the playout command line", banned)
		}
	}
	if argIndex(args, "-vf") >= 0 || argIndex(args, "-s") >= 0 {
		t.Fatal("playout scales video; scaling belongs to the rendition tier")
	}
}

func TestVariantArgsMapsOneOptionalAudioTrack(t *testing.T) {
	tests := []struct {
		name  string
		track int
		want  string
	}{
		{"default track is the first", 0, "0:a:0?"},
		{"a variant may publish any single ingest track", 3, "0:a:3?"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := VariantArgs(VariantSpec{Name: "v", Dir: "/d/v", AudioTrack: tc.track})
			maps := argValues(args, "-map")
			var got []string
			for _, m := range maps {
				if strings.HasPrefix(m, "0:a") {
					got = append(got, m)
				}
			}
			if len(got) == 0 {
				t.Fatal("no audio map")
			}
			for _, m := range got {
				if m != tc.want {
					t.Fatalf("audio map = %q, want %q", m, tc.want)
				}
				if !strings.HasSuffix(m, "?") {
					t.Fatal("audio map is not optional; a video-only ingest would refuse to start rather than play silently")
				}
			}
		})
	}
}

func TestVariantArgsPlaylistWindowIsTheLargerOfLiveAndDVR(t *testing.T) {
	tests := []struct {
		name     string
		playlist int
		dvr      int
		want     string
	}{
		{"live only uses the live window", 6, 0, "6"},
		{"a dvr window larger than the live one is published whole", 6, 90, "90"},
		{"a dvr window smaller than the live one never shrinks the playlist", 12, 4, "12"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := VariantArgs(VariantSpec{
				Name: "v", Dir: "/d/v",
				PlaylistSegments: tc.playlist,
				DVRSegments:      tc.dvr,
			})
			if got := argValue(args, "-hls_list_size"); got != tc.want {
				t.Fatalf("-hls_list_size = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVariantArgsAlwaysPrunesItsOwnWindow(t *testing.T) {
	args := VariantArgs(VariantSpec{Name: "v", Dir: "/d/v", DVRSegments: 500})
	flags := argValue(args, "-hls_flags")
	if !strings.Contains(flags, "delete_segments") {
		t.Fatalf("-hls_flags = %q; without delete_segments the directory grows without bound", flags)
	}
	if !strings.Contains(flags, "omit_endlist") {
		t.Fatalf("-hls_flags = %q; without omit_endlist a muxer restart reads as end-of-stream", flags)
	}
}

func TestVariantArgsDASHIsASecondMuxerOnTheSameProcess(t *testing.T) {
	tests := []struct {
		name     string
		dash     bool
		wantDASH bool
	}{
		{"hls only writes no manifest", false, false},
		{"hls+dash writes both from one process", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := VariantArgs(VariantSpec{Name: "v", Dir: "/d/v", DASH: tc.dash})

			hasDASH := argIndex(args, "dash") >= 0
			if hasDASH != tc.wantDASH {
				t.Fatalf("dash output present = %v, want %v", hasDASH, tc.wantDASH)
			}
			// One input, whatever the number of outputs: a second subscription
			// would double the relay fan-out cost for the same stream.
			if n := len(argValues(args, "-i")); n != 1 {
				t.Fatalf("%d inputs, want exactly 1", n)
			}
			if tc.wantDASH {
				if got := argValues(args, "-c:v"); len(got) != 2 {
					t.Fatalf("%d video codec settings, want one per output", len(got))
				}
				if !hasArg(args, filepath.Join("/d/v", DASHManifest)) {
					t.Fatal("dash output is not the manifest path")
				}
			}
			if !hasArg(args, filepath.Join("/d/v", MediaPlaylist)) {
				t.Fatal("hls output is not the media playlist path")
			}
		})
	}
}

func TestVariantArgsWritesOnlyInsideItsOwnDirectory(t *testing.T) {
	dir := filepath.Join("/data", "playout", "hd")
	args := VariantArgs(VariantSpec{Name: "hd", Dir: dir, DASH: true})

	for _, flag := range []string{"-hls_segment_filename"} {
		got := argValue(args, flag)
		if !strings.HasPrefix(got, dir+string(filepath.Separator)) {
			t.Fatalf("%s = %q, which is outside %q", flag, got, dir)
		}
	}
	// The DASH segment templates are relative names the muxer resolves against
	// the manifest, so they must carry no directory at all.
	for _, flag := range []string{"-init_seg_name", "-media_seg_name"} {
		if got := argValue(args, flag); strings.ContainsRune(got, filepath.Separator) || strings.Contains(got, "/") {
			t.Fatalf("%s = %q, which must be a bare name relative to the manifest", flag, got)
		}
	}
}

func TestVariantArgsCarriesProgressAndQuietFlags(t *testing.T) {
	args := VariantArgs(VariantSpec{Name: "v", Dir: "/d/v"})
	for _, want := range []string{"-hide_banner", "-nostdin", "-nostats", "-progress"} {
		if argIndex(args, want) < 0 {
			t.Fatalf("missing %q; the supervisor parses -progress and the log tail depends on the rest", want)
		}
	}
}

func TestSegmentsForRoundsUpSoTheWindowIsNeverShort(t *testing.T) {
	tests := []struct {
		name    string
		window  int
		segment int
		want    int
	}{
		{"live only", 0, 4, 0},
		{"exact division", 60, 4, 15},
		{"rounds up rather than truncating the window", 61, 4, 16},
		{"a window shorter than one segment still keeps one", 1, 4, 1},
		{"a zero segment length cannot divide", 60, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := segmentsFor(tc.window, tc.segment); got != tc.want {
				t.Fatalf("segmentsFor(%d, %d) = %d, want %d", tc.window, tc.segment, got, tc.want)
			}
		})
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
