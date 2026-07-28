package transcribe

import (
	"context"
	"reflect"
	"testing"
)

// The whole point of this file: an install without whisper.cpp must behave, not
// crash and not lie. Every one of these calls is on the nil pointer a caller
// holds after Detect returned ErrNotFound.

func TestAnAbsentWhisperIsAValidStateAndNeverPanics(t *testing.T) {
	var absent *Tools

	if absent.Available() {
		t.Error("a nil Tools reported whisper as available")
	}
	if absent.Unavailable() == "" {
		t.Error("a nil Tools gave no explanation for why transcription is off")
	}
	if got := absent.String(); got != "whisper.cpp not installed" {
		t.Errorf("String() = %q", got)
	}
	if !absent.HasFlag("output-json-full") {
		t.Error("an unknown build must fail OPEN on flags: a wrongly-restrictive answer silently " +
			"drops options the build does have")
	}
	if !absent.SupportsBackend(BackendCUDA) {
		t.Error("an unprobed build must not be reported as lacking a backend")
	}
	if got := absent.BestBackend(); got != BackendAuto {
		t.Errorf("BestBackend() = %q, want the automatic (no flag) choice", got)
	}
	if got := absent.AvailableBackends(); len(got) < 2 {
		t.Errorf("AvailableBackends() = %v, want at least auto and cpu", got)
	}
	absent.SetBackends([]Backend{BackendMetal}) // must be a no-op, not a nil dereference
}

func TestDetectReportsErrNotFoundForAMissingBinary(t *testing.T) {
	_, err := Detect(context.Background(), "definitely-not-a-real-whisper-binary-xyz")
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if !isNotFound(err) {
		t.Errorf("err = %v, want it to wrap ErrNotFound so callers can tell it apart from a broken build", err)
	}
}

func isNotFound(err error) bool {
	for err != nil {
		if err == ErrNotFound {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// This is the trap ffmpeg/detect.go documents for "srt" matching "srtp", in its
// whisper form: EVERY build prints "CUDA = 0" on a machine without CUDA, so a
// substring search reports CUDA support on every install in existence.
func TestParseSystemInfoRequiresTheFlagToBeOneNotMerelyPresent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []Backend
	}{
		{
			name: "a CPU-only build lists every backend as zero",
			line: "system_info: n_threads = 4 / 8 | AVX = 1 | AVX2 = 1 | FMA = 1 | METAL = 0 | CUDA = 0 | VULKAN = 0 | BLAS = 0 |",
			want: nil,
		},
		{
			name: "a Metal build",
			line: "system_info: n_threads = 4 / 10 | NEON = 1 | METAL = 1 | CUDA = 0 | COREML = 0 |",
			want: []Backend{BackendMetal},
		},
		{
			name: "a CUDA build with BLAS",
			line: "system_info: n_threads = 8 / 16 | AVX2 = 1 | CUDA = 1 | BLAS = 1 | METAL = 0 |",
			want: []Backend{BackendCUDA, BackendBLAS},
		},
		{
			name: "Metal plus Core ML",
			line: "system_info: n_threads = 4 / 10 | METAL = 1 | COREML = 1 |",
			want: []Backend{BackendMetal, BackendCoreML},
		},
		{
			name: "the line embedded in a larger capture",
			line: "whisper_init: loading\nsystem_info: n_threads = 4 / 8 | VULKAN = 1 |\nmain: processing\n",
			want: []Backend{BackendVulkan},
		},
		{
			name: "no system_info line at all",
			line: "whisper_init_from_file: loading model\n",
			want: nil,
		},
		{
			name: "CPU feature flags are not backends",
			line: "system_info: AVX = 1 | AVX2 = 1 | F16C = 1 | FMA = 1 | SSE3 = 1 |",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSystemInfo(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseSystemInfo = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBackendsProbedFromARunDriveTheDefaultAndTheOfferedList(t *testing.T) {
	tools := &Tools{Binary: "/usr/bin/whisper-cli"}
	if got := tools.BestBackend(); got != BackendAuto {
		t.Errorf("before probing, BestBackend() = %q, want auto", got)
	}

	tools.SetBackends(ParseSystemInfo("system_info: METAL = 1 | CUDA = 0 |"))
	if got := tools.BestBackend(); got != BackendMetal {
		t.Errorf("after probing a Metal build, BestBackend() = %q, want metal", got)
	}
	if !tools.SupportsBackend(BackendMetal) {
		t.Error("a probed Metal build reported as not supporting Metal")
	}
	if tools.SupportsBackend(BackendCUDA) {
		t.Error("a probed Metal build reported as supporting CUDA")
	}
	// CPU and auto are always selectable: they need no build support.
	for _, b := range []Backend{BackendAuto, BackendCPU} {
		if !tools.SupportsBackend(b) {
			t.Errorf("%q must always be selectable", b)
		}
	}
}

func TestOnlyGPUBackendsReportThemselvesAsGPU(t *testing.T) {
	tests := map[Backend]bool{
		BackendAuto: false, BackendCPU: false, BackendBLAS: false, BackendCoreML: false,
		BackendMetal: true, BackendCUDA: true, BackendVulkan: true,
	}
	for b, want := range tests {
		if got := b.GPU(); got != want {
			t.Errorf("%q.GPU() = %v, want %v", b, got, want)
		}
		if b.Label() == "" {
			t.Errorf("%q has no human label", b)
		}
	}
}

func TestParseHelpFlagsCollectsTheLongOptionsTheBuildAdvertises(t *testing.T) {
	help := `usage: whisper-cli [options] file0 file1 ...

options:
  -h,        --help              [default] show this help message and exit
  -t N,      --threads N         [4      ] number of threads to use
  -ng,       --no-gpu            [false  ] disable GPU
  -oj,       --output-json       [false  ] output result in a JSON file
  -ojf,      --output-json-full  [false  ] include more information
  -pp,       --print-progress    [false  ] print progress
`
	tools := &Tools{Binary: "whisper-cli", Flags: parseHelpFlags(help)}
	for _, want := range []string{"help", "threads", "no-gpu", "output-json", "output-json-full", "print-progress"} {
		if !tools.HasFlag(want) {
			t.Errorf("flag %q was not detected in the help text", want)
		}
	}
	if tools.HasFlag("output-lrc") {
		t.Error("a flag absent from the help text was reported as present")
	}
	// Leading dashes are tolerated on the query side, because every caller
	// naturally writes the flag the way it appears on a command line.
	if !tools.HasFlag("--no-gpu") {
		t.Error("HasFlag should tolerate a leading --")
	}
}

func TestAnUnreadableHelpTextFailsOpen(t *testing.T) {
	tools := &Tools{Binary: "whisper-cli"} // help could not be read: no flags recorded
	if !tools.HasFlag("output-json-full") {
		t.Error("a build whose help we could not read must be assumed to have every flag")
	}
}

func TestParseVersionIsOptional(t *testing.T) {
	tests := []struct {
		name, help, want string
	}{
		{"no version anywhere", "usage: whisper-cli [options]", ""},
		{"a stamped version", "whisper.cpp v1.7.4\nusage: whisper-cli", "1.7.4"},
		{"a two-part version", "whisper-cli 1.8\n", "1.8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseVersion(tc.help); got != tc.want {
				t.Errorf("parseVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
