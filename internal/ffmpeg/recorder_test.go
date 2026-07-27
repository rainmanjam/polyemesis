package ffmpeg

import (
	"slices"
	"strings"
	"testing"
)

// argIndex is the position of the first exact match of v, or -1.
func argIndex(args []string, v string) int { return slices.Index(args, v) }

// stemPaths lists every output filename in argv order, using the fact that
// each segmented output ends with its filename immediately after -strftime 1.
func stemPaths(args []string) []string {
	var out []string
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "-strftime" && args[i+1] == "1" {
			out = append(out, args[i+2])
		}
	}
	return out
}

func TestValidStemCodec(t *testing.T) {
	tests := []struct {
		name  string
		codec StemCodec
		want  bool
	}{
		{"empty means the default and is accepted", "", true},
		{"flac", StemFLAC, true},
		{"wav", StemWAV, true},
		{"unknown codec is rejected", "mp3", false},
		{"case matters, this is a stored enum", "FLAC", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidStemCodec(tt.codec); got != tt.want {
				t.Fatalf("ValidStemCodec(%q) = %v, want %v", tt.codec, got, tt.want)
			}
		})
	}
}

func TestStemCodecExt(t *testing.T) {
	tests := []struct {
		name  string
		codec StemCodec
		want  string
	}{
		{"flac", StemFLAC, ".flac"},
		{"wav", StemWAV, ".wav"},
		{"empty falls back to the default codec's extension", "", ".flac"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.codec.Ext(); got != tt.want {
				t.Fatalf("Ext() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The master archive is the thing users already depend on. Enabling stems adds
// outputs; it must never change the one that was there.
func TestStemRecorderArgsLeavesMasterByteIdentical(t *testing.T) {
	base := RecorderSpec{
		RelayURL:       "srt://127.0.0.1:9000",
		OutputPattern:  "/data/recordings/rec-%Y%m%d-%H%M%S.mkv",
		SegmentSeconds: 600,
	}
	master := RecorderArgs(base)

	got := StemRecorderArgs(StemRecorderSpec{
		RecorderSpec: base,
		Codec:        StemFLAC,
		Stems: []StemSpec{
			{Track: 0, Path: "/data/recordings/stems/rec-%Y%m%d-%H%M%S-mic.flac"},
			{Track: 1, Path: "/data/recordings/stems/rec-%Y%m%d-%H%M%S-music.flac"},
		},
	})

	if len(got) < len(master) {
		t.Fatalf("stem args shorter than master args: %v", got)
	}
	prefix := got[:len(master)]
	if !slices.Equal(prefix, master) {
		t.Fatalf("master output changed\n got: %v\nwant: %v", prefix, master)
	}
}

func TestStemRecorderArgsWithoutStemsIsPlainRecorder(t *testing.T) {
	base := RecorderSpec{
		RelayURL:      "srt://127.0.0.1:9000",
		OutputPattern: "/rec/rec-%Y%m%d-%H%M%S.mkv",
	}
	tests := []struct {
		name  string
		stems []StemSpec
	}{
		{"nil stem list", nil},
		{"empty stem list", []StemSpec{}},
		{"stem with no path is skipped", []StemSpec{{Track: 0}}},
		{"stem with a negative track is skipped", []StemSpec{{Track: -1, Path: "/rec/stems/x.flac"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StemRecorderArgs(StemRecorderSpec{RecorderSpec: base, Stems: tt.stems})
			if !slices.Equal(got, RecorderArgs(base)) {
				t.Fatalf("expected plain recorder args, got %v", got)
			}
		})
	}
}

func TestStemRecorderArgsOneProcessOneInput(t *testing.T) {
	got := StemRecorderArgs(StemRecorderSpec{
		RecorderSpec: RecorderSpec{RelayURL: "srt://127.0.0.1:9000", OutputPattern: "/rec/rec-%s.mkv"},
		Codec:        StemFLAC,
		Stems: []StemSpec{
			{Track: 0, Path: "/rec/stems/a.flac"},
			{Track: 1, Path: "/rec/stems/b.flac"},
			{Track: 2, Path: "/rec/stems/c.flac"},
		},
	})
	// Sample alignment between stems is the whole reason for one process; more
	// than one -i would mean more than one demux clock.
	inputs := 0
	for _, a := range got {
		if a == "-i" {
			inputs++
		}
	}
	if inputs != 1 {
		t.Fatalf("expected exactly 1 input, got %d in %v", inputs, got)
	}
	paths := stemPaths(got)
	want := []string{"/rec/rec-%s.mkv", "/rec/stems/a.flac", "/rec/stems/b.flac", "/rec/stems/c.flac"}
	if !slices.Equal(paths, want) {
		t.Fatalf("outputs = %v, want %v", paths, want)
	}
}

func TestStemRecorderArgsPerStemEncoding(t *testing.T) {
	tests := []struct {
		name        string
		specCodec   StemCodec
		stem        StemSpec
		wantEncoder string
		wantFormat  string
		wantArgs    []string
		absentArgs  []string
	}{
		{
			name:        "flac stems are 24-bit",
			specCodec:   StemFLAC,
			stem:        StemSpec{Track: 0, Path: "/s/a.flac"},
			wantEncoder: "flac",
			wantFormat:  "flac",
			wantArgs:    []string{"-bits_per_raw_sample", "24"},
			absentArgs:  []string{"-segment_format_options"},
		},
		{
			name:        "wav stems are 24-bit pcm with rf64 promotion",
			specCodec:   StemWAV,
			stem:        StemSpec{Track: 0, Path: "/s/a.wav"},
			wantEncoder: "pcm_s24le",
			wantFormat:  "wav",
			wantArgs:    []string{"-segment_format_options", "rf64=auto"},
			absentArgs:  []string{"-bits_per_raw_sample"},
		},
		{
			name:        "an unset spec codec defaults to flac",
			specCodec:   "",
			stem:        StemSpec{Track: 0, Path: "/s/a.flac"},
			wantEncoder: "flac",
			wantFormat:  "flac",
		},
		{
			name:        "a per-stem codec overrides the spec default",
			specCodec:   StemFLAC,
			stem:        StemSpec{Track: 0, Path: "/s/a.wav", Codec: StemWAV},
			wantEncoder: "pcm_s24le",
			wantFormat:  "wav",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StemRecorderArgs(StemRecorderSpec{
				RecorderSpec: RecorderSpec{RelayURL: "srt://x", OutputPattern: "/rec/m.mkv"},
				Codec:        tt.specCodec,
				Stems:        []StemSpec{tt.stem},
			})
			// Everything after the master's filename is the stem block.
			cut := argIndex(got, "/rec/m.mkv")
			if cut < 0 {
				t.Fatalf("master output missing from %v", got)
			}
			block := got[cut+1:]

			ci := argIndex(block, "-c:a")
			if ci < 0 || block[ci+1] != tt.wantEncoder {
				t.Fatalf("encoder = %v, want %q (block %v)", block, tt.wantEncoder, tt.wantEncoder)
			}
			fi := argIndex(block, "-segment_format")
			if fi < 0 || block[fi+1] != tt.wantFormat {
				t.Fatalf("segment format = %v, want %q", block, tt.wantFormat)
			}
			for i := 0; i+1 < len(tt.wantArgs); i += 2 {
				j := argIndex(block, tt.wantArgs[i])
				if j < 0 || block[j+1] != tt.wantArgs[i+1] {
					t.Fatalf("missing %s %s in %v", tt.wantArgs[i], tt.wantArgs[i+1], block)
				}
			}
			for _, a := range tt.absentArgs {
				if argIndex(block, a) >= 0 {
					t.Fatalf("unexpected %s in %v", a, block)
				}
			}
		})
	}
}

func TestStemRecorderArgsMapsExplicitlyAndBlocksVideo(t *testing.T) {
	got := StemRecorderArgs(StemRecorderSpec{
		RecorderSpec: RecorderSpec{RelayURL: "srt://x", OutputPattern: "/rec/m.mkv"},
		Stems: []StemSpec{
			{Track: 0, Path: "/s/a.flac"},
			{Track: 4, Path: "/s/b.flac"},
		},
	})
	joined := strings.Join(got, " ")
	for _, want := range []string{"-map 0:a:0", "-map 0:a:4"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	// FFmpeg falls back to default stream selection for an output whose maps
	// match nothing, and picks the video stream. A stem is never a video file.
	if n := strings.Count(joined, " -vn"); n != 2 {
		t.Fatalf("expected -vn on both stem outputs, found %d in %s", n, joined)
	}
}

func TestStemRecorderArgsNeverResamplesOrDownmixes(t *testing.T) {
	got := StemRecorderArgs(StemRecorderSpec{
		RecorderSpec: RecorderSpec{RelayURL: "srt://x", OutputPattern: "/rec/m.mkv"},
		Stems:        []StemSpec{{Track: 0, Path: "/s/a.flac", Channels: 6}},
	})
	// A stem is the track exactly as it arrived. -ac would fold a 5.1 track to
	// stereo in the archive, which is unrecoverable.
	for _, banned := range []string{"-ac", "-ar", "-af", "-filter_complex"} {
		if argIndex(got, banned) >= 0 {
			t.Fatalf("unexpected %s in %v", banned, got)
		}
	}
}

func TestStemRecorderArgsSegmentsWithTheMaster(t *testing.T) {
	tests := []struct {
		name    string
		seconds int
		want    string
	}{
		{"explicit segment length is shared", 600, "600"},
		{"zero takes the recorder's own default", 0, "3600"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StemRecorderArgs(StemRecorderSpec{
				RecorderSpec: RecorderSpec{RelayURL: "srt://x", OutputPattern: "/rec/m.mkv", SegmentSeconds: tt.seconds},
				Stems:        []StemSpec{{Track: 0, Path: "/s/a.flac"}, {Track: 1, Path: "/s/b.flac"}},
			})
			// Every -segment_time in the command must agree, master included:
			// a stem that rolls over on a different boundary does not line up
			// with the master file it belongs to.
			found := 0
			for i, a := range got {
				if a == "-segment_time" {
					found++
					if got[i+1] != tt.want {
						t.Fatalf("segment_time = %q, want %q", got[i+1], tt.want)
					}
				}
			}
			if found != 3 {
				t.Fatalf("expected 3 segmented outputs, got %d", found)
			}
		})
	}
}

func TestStemRecorderArgsEachSegmentStandsAlone(t *testing.T) {
	got := StemRecorderArgs(StemRecorderSpec{
		RecorderSpec: RecorderSpec{RelayURL: "srt://x", OutputPattern: "/rec/m.mkv"},
		Stems:        []StemSpec{{Track: 0, Path: "/s/a.flac"}},
	})
	resets, stamps := 0, 0
	for _, a := range got {
		switch a {
		case "-reset_timestamps":
			resets++
		case "-strftime":
			stamps++
		}
	}
	if resets != 2 || stamps != 2 {
		t.Fatalf("reset_timestamps=%d strftime=%d, want 2 and 2 (master + stem)", resets, stamps)
	}
}

// Losing one stem beats losing the archive: a fatal output in a shared process
// takes the master down with it.
func TestStemRecorderArgsDropsStemsFLACCannotCarry(t *testing.T) {
	tests := []struct {
		name     string
		stem     StemSpec
		codec    StemCodec
		wantKept bool
	}{
		{"stereo flac is kept", StemSpec{Track: 0, Path: "/s/a.flac", Channels: 2}, StemFLAC, true},
		{"7.1 is exactly at the flac ceiling", StemSpec{Track: 0, Path: "/s/a.flac", Channels: 8}, StemFLAC, true},
		{"9 channels would refuse to open, so the stem is dropped", StemSpec{Track: 0, Path: "/s/a.flac", Channels: 9}, StemFLAC, false},
		{"unmeasured width is never a reason to refuse", StemSpec{Track: 0, Path: "/s/a.flac", Channels: 0}, StemFLAC, true},
		{"wav carries any width", StemSpec{Track: 0, Path: "/s/a.wav", Channels: 12}, StemWAV, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StemRecorderArgs(StemRecorderSpec{
				RecorderSpec: RecorderSpec{RelayURL: "srt://x", OutputPattern: "/rec/m.mkv"},
				Codec:        tt.codec,
				Stems:        []StemSpec{tt.stem},
			})
			kept := argIndex(got, tt.stem.Path) >= 0
			if kept != tt.wantKept {
				t.Fatalf("stem kept = %v, want %v (args %v)", kept, tt.wantKept, got)
			}
		})
	}
}

func TestStemRecorderArgsRefusesDuplicatePaths(t *testing.T) {
	got := StemRecorderArgs(StemRecorderSpec{
		RecorderSpec: RecorderSpec{RelayURL: "srt://x", OutputPattern: "/rec/m.mkv"},
		Stems: []StemSpec{
			{Track: 0, Path: "/s/a.flac"},
			{Track: 1, Path: "/s/a.flac"},
			{Track: 2, Path: "/s/b.flac"},
		},
	})
	// Two outputs on one path interleave into a file that is neither.
	paths := stemPaths(got)
	want := []string{"/rec/m.mkv", "/s/a.flac", "/s/b.flac"}
	if !slices.Equal(paths, want) {
		t.Fatalf("outputs = %v, want %v", paths, want)
	}
	if !strings.Contains(strings.Join(got, " "), "-map 0:a:0") {
		t.Fatalf("expected the FIRST stem for a duplicated path to win: %v", got)
	}
	if strings.Contains(strings.Join(got, " "), "-map 0:a:1") {
		t.Fatalf("expected the duplicate to be dropped entirely: %v", got)
	}
}
