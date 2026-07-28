package transcribe

import (
	"strings"
	"testing"
)

// joined renders an argument slice for substring assertions. Whole tokens are
// checked with indexOf so a test cannot pass on an accidental substring.
func joined(args []string) string { return strings.Join(args, " ") }

func hasPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// The single most dangerous mistake in this file: `-map 0:2` counts the video
// track and every microphone is off by one, so the transcript is confidently
// attributed to the wrong person. `0:a:N` is audio-relative and cannot be.
func TestExtractMapsTheAudioRelativeStreamNotTheAbsoluteOne(t *testing.T) {
	for _, track := range []int{0, 1, 5} {
		args := ExtractArgs(ExtractSpec{Input: "rec.mkv", Track: track, Output: "out.wav"})
		want := "0:a:" + string(rune('0'+track))
		if !hasPair(args, "-map", want) {
			t.Errorf("track %d: args = %q, want -map %s", track, joined(args), want)
		}
	}
}

func TestExtractProducesExactlyWhatWhisperWants(t *testing.T) {
	args := ExtractArgs(ExtractSpec{Input: "rec.mkv", Track: 1, Output: "out.wav"})
	tests := []struct{ flag, value string }{
		{"-ac", "1"},     // mono
		{"-ar", "16000"}, // 16 kHz
		{"-c:a", "pcm_s16le"},
		{"-f", "wav"},
	}
	for _, tc := range tests {
		if !hasPair(args, tc.flag, tc.value) {
			t.Errorf("args = %q, want %s %s", joined(args), tc.flag, tc.value)
		}
	}
	for _, flag := range []string{"-vn", "-sn", "-dn"} {
		if !has(args, flag) {
			t.Errorf("args = %q, want %s: a WAV muxer refuses a leftover subtitle or data stream",
				joined(args), flag)
		}
	}
	if args[len(args)-1] != "out.wav" {
		t.Errorf("the output must be the last argument, got %q", joined(args))
	}
}

func TestExtractSeeksBeforeTheInputSoALongSegmentIsNotDecodedAndDiscarded(t *testing.T) {
	args := ExtractArgs(ExtractSpec{Input: "rec.mkv", Output: "out.wav", StartMS: 90_500, DurationMS: 30_250})
	ss, in, dur := -1, -1, -1
	for i, a := range args {
		switch a {
		case "-ss":
			ss = i
		case "-i":
			in = i
		case "-t":
			dur = i
		}
	}
	if ss < 0 || in < 0 || ss > in {
		t.Fatalf("args = %q, want -ss before -i", joined(args))
	}
	if dur < in {
		t.Errorf("args = %q, want -t after -i so the duration is measured from the seek point", joined(args))
	}
	// Milliseconds must survive: a rounded in-point cuts a word in half.
	if !hasPair(args, "-ss", "90.500") || !hasPair(args, "-t", "30.250") {
		t.Errorf("args = %q, want sub-second precision preserved", joined(args))
	}
}

func TestExtractOptions(t *testing.T) {
	tests := []struct {
		name string
		spec ExtractSpec
		want func(args []string) bool
	}{
		{
			name: "denoise is off unless the track was flagged for it",
			spec: ExtractSpec{Input: "a.mkv", Output: "a.wav"},
			want: func(a []string) bool { return !has(a, "-af") },
		},
		{
			name: "denoise adds the filter when the track was flagged",
			spec: ExtractSpec{Input: "a.mkv", Output: "a.wav", Denoise: true},
			want: func(a []string) bool { return has(a, "-af") && strings.Contains(joined(a), "afftdn") },
		},
		{
			name: "progress reporting is opt-in",
			spec: ExtractSpec{Input: "a.mkv", Output: "a.wav"},
			want: func(a []string) bool { return !has(a, "-progress") },
		},
		{
			name: "progress goes to stdout with stats suppressed",
			spec: ExtractSpec{Input: "a.mkv", Output: "a.wav", Progress: true},
			want: func(a []string) bool { return hasPair(a, "-progress", "pipe:1") && has(a, "-nostats") },
		},
		{
			name: "stdin is never inherited, so a child cannot wedge waiting on it",
			spec: ExtractSpec{Input: "a.mkv", Output: "a.wav"},
			want: func(a []string) bool { return has(a, "-nostdin") },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if args := ExtractArgs(tc.spec); !tc.want(args) {
				t.Errorf("args = %q", joined(args))
			}
		})
	}
}

func TestWhisperArgsCarryTheModelInputAndOutputPrefix(t *testing.T) {
	args := WhisperArgs(WhisperSpec{
		Model:        "/models/ggml-base.bin",
		Input:        "/tmp/a.wav",
		OutputPrefix: "/tmp/a",
		JSON:         true,
	})
	for _, tc := range []struct{ flag, value string }{
		{"-m", "/models/ggml-base.bin"},
		{"-f", "/tmp/a.wav"},
		{"-of", "/tmp/a"},
	} {
		if !hasPair(args, tc.flag, tc.value) {
			t.Errorf("args = %q, want %s %s", joined(args), tc.flag, tc.value)
		}
	}
	if !has(args, "-oj") {
		t.Errorf("args = %q, want the JSON output flag", joined(args))
	}
	if got := JSONPath("/tmp/a"); got != "/tmp/a.json" {
		t.Errorf("JSONPath = %q, want /tmp/a.json", got)
	}
}

// whisper.cpp exits with a usage dump on an unknown option. Passing a flag an
// older build does not have does not degrade the transcript, it loses the job.
func TestWhisperArgsSkipFlagsTheDetectedBuildDoesNotAdvertise(t *testing.T) {
	old := &Tools{Binary: "whisper", Flags: []string{"model", "file", "output-json", "threads", "language"}}
	modern := &Tools{Binary: "whisper-cli", Flags: []string{
		"model", "file", "output-json", "output-json-full", "print-progress", "no-gpu", "threads", "language"}}

	spec := WhisperSpec{Model: "m", Input: "i", JSON: true, FullJSON: true, Progress: true, Backend: BackendCPU}

	spec.Flags = old
	args := WhisperArgs(spec)
	for _, flag := range []string{"-ojf", "-pp", "-ng"} {
		if has(args, flag) {
			t.Errorf("args = %q: %s was passed to a build that does not have it", joined(args), flag)
		}
	}

	spec.Flags = modern
	args = WhisperArgs(spec)
	for _, flag := range []string{"-ojf", "-pp", "-ng"} {
		if !has(args, flag) {
			t.Errorf("args = %q: %s was withheld from a build that has it", joined(args), flag)
		}
	}

	// And a build we could not read the help of gets everything — failing open,
	// because a wrongly-restrictive answer here silently costs confidences and
	// progress on a build that supports both.
	spec.Flags = nil
	if args := WhisperArgs(spec); !has(args, "-ojf") || !has(args, "-pp") {
		t.Errorf("args = %q: an unknown build must be assumed capable", joined(args))
	}
}

func TestOnlyTheCPUBackendChangesTheCommandLine(t *testing.T) {
	tools := &Tools{Binary: "whisper-cli", Flags: []string{"no-gpu"}}
	tests := []struct {
		backend Backend
		wantNG  bool
	}{
		{BackendCPU, true},
		{BackendAuto, false},
		{BackendMetal, false},
		{BackendCUDA, false},
		{BackendVulkan, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.backend)+"|", func(t *testing.T) {
			args := WhisperArgs(WhisperSpec{Model: "m", Input: "i", Backend: tc.backend, Flags: tools})
			if got := has(args, "-ng"); got != tc.wantNG {
				t.Errorf("backend %q: -ng present = %v, want %v (there is no 'use Metal' flag; a build "+
					"either has a GPU backend compiled in or it does not)", tc.backend, got, tc.wantNG)
			}
		})
	}
}

func TestLanguageIsNormalisedToWhatWhisperAccepts(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty means detect", "", "auto"},
		{"auto passes through", "auto", "auto"},
		{"a plain code", "en", "en"},
		{"uppercase is lowered", "ES", "es"},
		{"a BCP-47 region tag is cut to its primary subtag", "pt-BR", "pt"},
		{"surrounding space", "  fr  ", "fr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeLanguage(tc.in); got != tc.want {
				t.Errorf("normalizeLanguage(%q) = %q, want %q", tc.in, got, tc.want)
			}
			args := WhisperArgs(WhisperSpec{Model: "m", Input: "i", Language: tc.in})
			if !hasPair(args, "-l", tc.want) {
				t.Errorf("args = %q, want -l %s", joined(args), tc.want)
			}
		})
	}
}

func TestWhisperArgsOmitTheOptionalFlagsWhenNothingWasAsked(t *testing.T) {
	args := WhisperArgs(WhisperSpec{Model: "m", Input: "i"})
	for _, flag := range []string{"-of", "-oj", "-ojf", "-t", "-tr", "-ml", "-pp", "-ng"} {
		if has(args, flag) {
			t.Errorf("args = %q, %s should not be present by default", joined(args), flag)
		}
	}
}

func TestWAVNameIsUniquePerTrackAndKeepsTheRecordingIdentifiable(t *testing.T) {
	tests := []struct {
		recording string
		track     int
		want      string
	}{
		{"rec-20240115-143000.mkv", 0, "rec-20240115-143000-a0.wav"},
		{"/data/recordings/rec-20240115-143000.mkv", 3, "rec-20240115-143000-a3.wav"},
	}
	for _, tc := range tests {
		if got := WAVName(tc.recording, tc.track); got != tc.want {
			t.Errorf("WAVName(%q, %d) = %q, want %q", tc.recording, tc.track, got, tc.want)
		}
	}
}

func TestMsToSeconds(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0.000"},
		{1, "0.001"},
		{1500, "1.500"},
		{90_500, "90.500"},
		{-100, "0.000"},
	}
	for _, tc := range tests {
		if got := msToSeconds(tc.ms); got != tc.want {
			t.Errorf("msToSeconds(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}
