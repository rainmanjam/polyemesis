package transcribe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

func TestEveryCataloguedModelIsCoherent(t *testing.T) {
	for _, m := range Models() {
		t.Run(m.Name, func(t *testing.T) {
			if m.Bytes < minModelBytes {
				t.Errorf("size %d is below the floor a download is checked against", m.Bytes)
			}
			if m.RAMBytes < m.Bytes/2 {
				t.Errorf("RAM %d is implausible against a %d byte model", m.RAMBytes, m.Bytes)
			}
			if m.RelSpeed <= 0 || m.Accuracy < 1 || m.Accuracy > 5 {
				t.Errorf("speed %v / accuracy %d out of range", m.RelSpeed, m.Accuracy)
			}
			if want := "ggml-" + m.Name + ".bin"; m.Filename() != want {
				t.Errorf("Filename() = %q, want %q", m.Filename(), want)
			}
			if !strings.HasSuffix(m.URL(), m.Filename()) {
				t.Errorf("URL %q does not end in the filename", m.URL())
			}
			if m.English != strings.HasSuffix(m.Name, ".en") {
				t.Errorf("English flag disagrees with the name")
			}
			// The tradeoff string is what the user reads before committing to a
			// three-gigabyte download; it has to state all three costs.
			for _, want := range []string{"RAM", "realtime", "accuracy"} {
				if !strings.Contains(m.Tradeoff(), want) {
					t.Errorf("Tradeoff() = %q, missing %q", m.Tradeoff(), want)
				}
			}
		})
	}
}

func TestSmallerModelsAreFasterAndBiggerOnesAreMoreAccurate(t *testing.T) {
	tiny, _ := FindModel("tiny")
	base, _ := FindModel("base")
	small, _ := FindModel("small")
	medium, _ := FindModel("medium")
	if !(tiny.RelSpeed > base.RelSpeed && base.RelSpeed > small.RelSpeed && small.RelSpeed > medium.RelSpeed) {
		t.Error("speeds do not decrease monotonically with model size")
	}
	if !(tiny.Bytes < base.Bytes && base.Bytes < small.Bytes && small.Bytes < medium.Bytes) {
		t.Error("sizes do not increase monotonically with model size")
	}
}

func TestFindModelAcceptsEverySpellingThatTurnsUpInAConfigFile(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantOK         bool
	}{
		{name: "bare name", in: "base.en", want: "base.en", wantOK: true},
		{name: "filename", in: "ggml-base.en.bin", want: "base.en", wantOK: true},
		{name: "absolute path", in: "/var/lib/polyemesis/models/whisper/ggml-small.bin", want: "small", wantOK: true},
		{name: "surrounding space", in: "  tiny  ", want: "tiny", wantOK: true},
		{name: "not in the catalogue", in: "enormous", wantOK: false},
		{name: "empty", in: "", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := FindModel(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("FindModel(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if ok && m.Name != tc.want {
				t.Errorf("FindModel(%q) = %q, want %q", tc.in, m.Name, tc.want)
			}
		})
	}
}

func TestDefaultModelFollowsTheHardwareTheEncoderProbeAlreadyMeasured(t *testing.T) {
	tests := []struct {
		name string
		hint HardwareHint
		want string
	}{
		{"a working GPU carries the first genuinely useful tier", HardwareHint{GPU: true, CPUCores: 8}, "small"},
		{"a many-core CPU box", HardwareHint{CPUCores: 16}, "base"},
		{"a small CPU box stays out of the way of the stream", HardwareHint{CPUCores: 2}, "tiny"},
		{"no information at all is treated as a small box", HardwareHint{}, "tiny"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultModel(tc.hint); got.Name != tc.want {
				t.Errorf("DefaultModel(%+v) = %q, want %q", tc.hint, got.Name, tc.want)
			}
		})
	}
}

func TestDefaultModelIsNeverAnEnglishOnlyModel(t *testing.T) {
	for _, h := range []HardwareHint{{}, {GPU: true}, {CPUCores: 32}} {
		if m := DefaultModel(h); m.English {
			t.Errorf("DefaultModel(%+v) = %q: guessing that a stream is English is not ours to make", h, m.Name)
		}
	}
}

func TestHintFromToolsTreatsAnUnprobedFFmpegAsNoEvidenceOfAGPU(t *testing.T) {
	if h := HintFromTools(nil); h.GPU {
		t.Error("a nil ffmpeg.Tools must not imply a GPU")
	}
	if h := HintFromTools(&ffmpeg.Tools{}); h.GPU {
		t.Error("an ffmpeg.Tools with no probe results must not imply a GPU")
	}
	if h := HintFromTools(nil); h.CPUCores < 1 {
		t.Error("CPU cores should always be known")
	}
}

func TestRealtimeCaptionsAreOfferedWhenThereIsHeadroomAndRefusedWithAnExplanation(t *testing.T) {
	tiny, _ := FindModel("tiny")
	large, _ := FindModel("large-v3")

	if ok, _ := RealtimeCapable(tiny, HardwareHint{CPUCores: 8}); !ok {
		t.Error("tiny on a CPU should be offered for live captions")
	}
	ok, why := RealtimeCapable(large, HardwareHint{CPUCores: 8})
	if ok {
		t.Error("large on a CPU should not be offered for live captions while a stream is running")
	}
	if why == "" || !strings.Contains(why, "large") {
		t.Errorf("refusal = %q, want it to name the model and the fix", why)
	}
	// Fail open: a machine with a proved GPU gets the benefit of the doubt,
	// because the speed figures in the catalogue are CPU figures.
	if ok, _ := RealtimeCapable(large, HardwareHint{GPU: true}); !ok {
		t.Error("a machine with a working GPU must not be refused live captions on a CPU speed estimate")
	}
}

func TestInstalledModelsIgnoresPartFilesAndFilesThatAreNotModels(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, magic bool, size int) {
		buf := make([]byte, size)
		if magic {
			copy(buf, ggmlMagic)
		} else {
			copy(buf, []byte("<htm"))
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ggml-base.en.bin", true, minModelBytes)
	write("ggml-mine-finetuned.bin", true, minModelBytes)
	write("ggml-small.bin"+partSuffix, true, minModelBytes) // interrupted download
	write("ggml-medium.bin", false, minModelBytes)          // an HTML error page
	write("notes.txt", true, minModelBytes)

	got, err := InstalledModels(dir)
	if err != nil {
		t.Fatalf("InstalledModels: %v", err)
	}
	var names []string
	for _, m := range got {
		names = append(names, m.Name)
	}
	if len(names) != 2 || names[0] != "base.en" || names[1] != "mine-finetuned" {
		t.Fatalf("installed = %v, want [base.en mine-finetuned]", names)
	}
	if !got[0].Known {
		t.Error("a catalogued model should be marked as known")
	}
	// A model we have never heard of is still offered: somebody's own fine-tune
	// is not broken just because it is not in our list.
	if got[1].Known {
		t.Error("an uncatalogued model should be offered but marked as unknown")
	}
}

func TestInstalledModelsTreatsAMissingDirectoryAsAFreshInstall(t *testing.T) {
	got, err := InstalledModels(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("a missing model directory must not be an error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

func TestResolveModel(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "ggml-base.bin")
	if err := os.WriteFile(good, append(ggmlMagic, make([]byte, minModelBytes)...), 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "custom.bin")
	if err := os.WriteFile(elsewhere, ggmlMagic, 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "bare name", in: "base", want: good},
		{name: "filename", in: "ggml-base.bin", want: good},
		{name: "an explicit path outside the model directory is honoured", in: elsewhere, want: elsewhere},
		{name: "a model that is not downloaded", in: "large-v3", wantErr: true},
		{name: "a path that does not exist", in: "/nowhere/at/all.bin", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveModel(dir, tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveModel(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveModel(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ResolveModel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestModelsDirIsUnderTheDataDirectoryNotTheRecordingsDirectory(t *testing.T) {
	got := ModelsDir("/var/lib/polyemesis")
	want := filepath.Join("/var/lib/polyemesis", "models", "whisper")
	if got != want {
		t.Errorf("ModelsDir = %q, want %q", got, want)
	}
}
