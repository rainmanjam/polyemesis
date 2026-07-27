package transcribe

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// The model catalogue.
//
// Choosing a whisper model is a three-way trade the user has to be shown
// honestly: speed, accuracy and RAM. A default of "large" makes a laptop swap
// and take six hours over a two-hour recording; a default of "tiny" produces a
// transcript nobody trusts and quietly discredits the whole feature. So the
// catalogue carries the numbers, the UI shows them, and the default is picked
// from what internal/ffmpeg already proved about this machine's hardware.

// ModelsSubdir is where downloaded models live under the data directory.
// Models are large and re-downloadable, so they are deliberately NOT under the
// recordings directory: an operator clearing space must be able to delete
// recordings without losing gigabytes of models, and vice versa.
const ModelsSubdir = "models"

// ModelSize is the accuracy tier.
type ModelSize string

const (
	SizeTiny   ModelSize = "tiny"
	SizeBase   ModelSize = "base"
	SizeSmall  ModelSize = "small"
	SizeMedium ModelSize = "medium"
	SizeLarge  ModelSize = "large"
)

// ModelSizes is the catalogue order, smallest first.
func ModelSizes() []ModelSize {
	return []ModelSize{SizeTiny, SizeBase, SizeSmall, SizeMedium, SizeLarge}
}

// Model is one downloadable whisper.cpp model.
type Model struct {
	// Name is whisper.cpp's own model name, e.g. "base.en" or "large-v3".
	Name string    `json:"name"`
	Size ModelSize `json:"size"`
	// English is true for the .en models, which are trained on English only and
	// are noticeably better at it than the multilingual model of the same size.
	English bool `json:"english"`

	// Bytes is the published download size. It is approximate and used for a
	// progress bar and a sanity check, never as an equality test — see
	// VerifyModelFile.
	Bytes int64 `json:"bytes"`
	// RAMBytes is roughly what whisper.cpp maps while running this model. It is
	// the number that decides whether a machine can use it at all.
	RAMBytes int64 `json:"ramBytes"`
	// RelSpeed is how many times faster than realtime this model transcribes on
	// a mid-range desktop CPU. Wildly machine-dependent and presented as such;
	// its job is to convey that tiny is an order of magnitude faster than large,
	// not to predict a runtime.
	RelSpeed float64 `json:"relSpeed"`
	// Accuracy is a 1..5 hint for the UI, not a benchmark.
	Accuracy int `json:"accuracy"`

	// SHA1 is the upstream checksum, when the install has been told one. It is
	// EMPTY in the shipped catalogue on purpose: a checksum baked into this
	// source file goes stale the moment upstream re-uploads a model, and a stale
	// checksum rejects a perfectly good download — a check that is wrong in the
	// restrictive direction, which this codebase has been bitten by before.
	// Integrity is enforced instead by the transfer-level checks in
	// VerifyModelFile, which cannot go stale.
	SHA1 string `json:"sha1,omitempty"`
}

// Filename is what this model is stored as, matching upstream's naming so a
// model copied in by hand from another install is recognised.
func (m Model) Filename() string { return "ggml-" + m.Name + ".bin" }

// URL is where the model is fetched from. The whisper.cpp models live in a
// single Hugging Face repo and are served straight from the LFS store.
func (m Model) URL() string {
	return "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/" + m.Filename()
}

// Tradeoff is the one-line honest description a UI puts under the model picker.
func (m Model) Tradeoff() string {
	lang := "all languages"
	if m.English {
		lang = "English only"
	}
	return fmt.Sprintf("%s download, ~%s RAM, ~%.0fx realtime, %s, accuracy %d/5",
		humanBytes(m.Bytes), humanBytes(m.RAMBytes), m.RelSpeed, lang, m.Accuracy)
}

// catalogue is the set of models offered.
//
// Sizes and RAM figures are upstream's published approximations; they are used
// for presentation and for a lower-bound sanity check on a download, never for
// an equality test.
var catalogue = []Model{
	{Name: "tiny", Size: SizeTiny, Bytes: 77_691_713, RAMBytes: 390 << 20, RelSpeed: 32, Accuracy: 1},
	{Name: "tiny.en", Size: SizeTiny, English: true, Bytes: 77_704_715, RAMBytes: 390 << 20, RelSpeed: 32, Accuracy: 2},
	{Name: "base", Size: SizeBase, Bytes: 147_951_465, RAMBytes: 500 << 20, RelSpeed: 16, Accuracy: 2},
	{Name: "base.en", Size: SizeBase, English: true, Bytes: 147_964_211, RAMBytes: 500 << 20, RelSpeed: 16, Accuracy: 3},
	{Name: "small", Size: SizeSmall, Bytes: 487_601_967, RAMBytes: 1_000 << 20, RelSpeed: 6, Accuracy: 3},
	{Name: "small.en", Size: SizeSmall, English: true, Bytes: 487_614_201, RAMBytes: 1_000 << 20, RelSpeed: 6, Accuracy: 4},
	{Name: "medium", Size: SizeMedium, Bytes: 1_533_763_059, RAMBytes: 2_600 << 20, RelSpeed: 2, Accuracy: 4},
	{Name: "medium.en", Size: SizeMedium, English: true, Bytes: 1_533_774_781, RAMBytes: 2_600 << 20, RelSpeed: 2, Accuracy: 5},
	{Name: "large-v3", Size: SizeLarge, Bytes: 3_095_033_483, RAMBytes: 3_900 << 20, RelSpeed: 1, Accuracy: 5},
	{Name: "large-v3-turbo", Size: SizeLarge, Bytes: 1_624_555_275, RAMBytes: 1_600 << 20, RelSpeed: 8, Accuracy: 5},
}

// Models returns the catalogue, smallest first.
func Models() []Model {
	out := make([]Model, len(catalogue))
	copy(out, catalogue)
	return out
}

// FindModel looks a model up by name. It accepts the bare name ("base.en"), the
// filename ("ggml-base.en.bin") and a full path, because all three turn up in
// config files and in job params written by hand.
func FindModel(name string) (Model, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Model{}, false
	}
	name = ModelNameFromFile(name)
	for _, m := range catalogue {
		if m.Name == name {
			return m, true
		}
	}
	return Model{}, false
}

// ModelNameFromFile reduces a path or filename to the bare model name, and
// leaves anything that is already a bare name alone.
func ModelNameFromFile(s string) string {
	s = filepath.Base(s)
	s = strings.TrimSuffix(s, ".bin")
	s = strings.TrimPrefix(s, "ggml-")
	return s
}

// ModelsDir is where downloaded models live.
func ModelsDir(dataDir string) string { return filepath.Join(dataDir, ModelsSubdir, "whisper") }

// InstalledModel is a model file found on disk.
type InstalledModel struct {
	// Name is the bare model name, e.g. "base.en".
	Name string `json:"name"`
	Path string `json:"path"`
	// Bytes is the size on disk.
	Bytes int64 `json:"bytes"`
	// Known is false for a file that is a valid-looking ggml model but is not in
	// our catalogue. Those are still offered: a user who built their own
	// fine-tune and dropped it in the directory has done nothing wrong, and
	// hiding it would be a restrictive check with no upside.
	Known bool  `json:"known"`
	Model Model `json:"model,omitzero"`
}

// InstalledModels lists the usable models in a directory.
//
// A missing directory is not an error — it is the state of every fresh install.
func InstalledModels(dir string) ([]InstalledModel, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []InstalledModel
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// A part-file from an interrupted download must never be offered: it
		// loads, and it produces confident nonsense instead of an error.
		if strings.HasSuffix(e.Name(), partSuffix) || !looksLikeGGML(path) {
			continue
		}
		name := ModelNameFromFile(e.Name())
		m, known := FindModel(name)
		out = append(out, InstalledModel{Name: name, Path: path, Bytes: info.Size(), Known: known, Model: m})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ResolveModel finds the file to hand whisper for a requested model name.
//
// An absolute or relative path that exists is taken as-is, so an operator can
// point at a model they keep elsewhere. Otherwise the name is looked up in dir.
func ResolveModel(dir, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("no model selected")
	}
	if strings.ContainsRune(name, filepath.Separator) || filepath.IsAbs(name) {
		if st, err := os.Stat(name); err == nil && !st.IsDir() {
			return name, nil
		}
		return "", fmt.Errorf("model file %q does not exist", name)
	}
	bare := ModelNameFromFile(name)
	// Both spellings, because a config file written by hand is as likely to say
	// "ggml-base.en.bin" as "base.en".
	for _, candidate := range []string{"ggml-" + bare + ".bin", bare + ".bin", bare} {
		path := filepath.Join(dir, candidate)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("model %q is not downloaded yet (looked in %s)", bare, dir)
}

// HardwareHint is what the rest of the product already knows about this
// machine, reduced to the two facts that change a whisper default.
type HardwareHint struct {
	// GPU is true when a hardware video encoder demonstrably WORKS here, which
	// is the strongest available evidence that there is a usable GPU — stronger
	// than anything whisper.cpp can tell us before it has loaded a model.
	GPU bool
	// Vendor names the silicon, when known.
	Vendor ffmpeg.GPUVendor
	// CPUCores is the parallelism available to a CPU-only run.
	CPUCores int
}

// HintFromTools reads the hardware hint out of the FFmpeg encoder probe.
//
// This reuses evidence the product already paid for at startup: internal/ffmpeg
// probe-encodes a frame with every candidate encoder, so a non-empty HWEncoders
// means a GPU that really initialised on this machine with these drivers. It is
// a far better basis for a default than guessing from the OS.
//
// A nil or unprobed *ffmpeg.Tools yields a conservative hint rather than an
// error: no probe means no evidence, and no evidence means assume CPU.
func HintFromTools(t *ffmpeg.Tools) HardwareHint {
	h := HardwareHint{CPUCores: runtime.NumCPU()}
	if t == nil {
		return h
	}
	for _, c := range t.Capabilities() {
		if c.Works && c.Vendor != ffmpeg.VendorSoftware && c.Vendor != "" {
			h.GPU = true
			h.Vendor = c.Vendor
			break
		}
	}
	return h
}

// DefaultModel picks the model a fresh install should start on.
//
// The logic is deliberately timid. A GPU that has proved it can encode will
// carry "small" comfortably, which is the first tier whose transcripts are good
// enough that a human will actually use them. Without a GPU it comes down to
// cores, and "base" is the largest model that stays tolerable on a four-core
// box — remembering that this work is explicitly second in line behind a live
// stream and will be running niced, at whatever fraction of the CPU the
// governor leaves it.
//
// English-only variants are not chosen automatically. They are better at
// English and useless for anything else, and guessing the language of somebody
// else's stream from their machine is not a guess worth making.
func DefaultModel(h HardwareHint) Model {
	switch {
	case h.GPU:
		return mustModel("small")
	case h.CPUCores >= 8:
		return mustModel("base")
	default:
		return mustModel("tiny")
	}
}

// RealtimeCapable reports whether live captions are worth offering on this
// machine with this model, and says why not when they are not.
//
// Realtime needs headroom, not just capability: the model has to run several
// times faster than realtime, because it is sharing the machine with an encoder
// that must never drop a frame. A model that runs at 2x realtime on an idle box
// runs at less than 1x on a streaming one and falls permanently behind.
//
// It errs toward "yes". Being wrong here only costs a captions option that
// stutters and can be switched off; being wrong the other way removes a feature
// from a machine that could have run it, with no way for the user to find out.
func RealtimeCapable(m Model, h HardwareHint) (bool, string) {
	const minRealtimeSpeed = 6
	if m.RelSpeed >= minRealtimeSpeed {
		return true, ""
	}
	if h.GPU {
		// The RelSpeed figures are CPU figures. A working GPU backend routinely
		// moves a model up a tier or two, so a GPU machine gets the benefit of
		// the doubt.
		return true, ""
	}
	return false, fmt.Sprintf(
		"the %s model runs at roughly %.0fx realtime on a CPU, which is not enough headroom for live "+
			"captions while streaming. Choose a smaller model for live captions; %s stays available for "+
			"transcribing recordings afterwards.", m.Name, m.RelSpeed, m.Name)
}

func mustModel(name string) Model {
	m, ok := FindModel(name)
	if !ok {
		// Unreachable: the names above are catalogue entries. Returning a usable
		// zero-ish value beats panicking inside a startup path.
		return Model{Name: name, Size: SizeBase}
	}
	return m
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n/(1<<20))
	default:
		return fmt.Sprintf("%d KB", n/(1<<10))
	}
}
