package transcribe

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// whisper.cpp detection, modelled on ffmpeg.Detect.
//
// The important difference: FFmpeg is required and whisper.cpp is not. Detect
// returns ErrNotFound rather than panicking or exiting, every method on *Tools
// is nil-receiver safe, and a caller that never calls Detect at all still gets
// sane answers out of a nil *Tools. Startup must be incapable of failing
// because an optional tool is missing, and the cheapest way to guarantee that
// is to make the absent case a valid value rather than an error path someone
// has to remember to handle.

// ErrNotFound signals a missing binary, as opposed to an unusable one. It
// mirrors ffmpeg.ErrNotFound so callers can treat the two the same way.
var ErrNotFound = errors.New("whisper.cpp not found")

// BinaryNames is every name whisper.cpp's CLI has shipped under, newest first.
//
// Upstream renamed the example from `main` to `whisper-cli` and distro packages
// install it as `whisper-cpp` to avoid colliding with the Python `whisper`.
// Searching all of them is the difference between "works out of the box" and a
// support thread; `main` is last because a bare `main` on $PATH is as likely to
// be somebody's Go binary as it is to be whisper.
var BinaryNames = []string{"whisper-cli", "whisper-cpp", "whisper", "main"}

// detectTimeout bounds the help invocation. whisper-cli prints its usage and
// exits immediately, so this is a backstop against a wedged binary holding up
// startup, not a real budget.
const detectTimeout = 10 * time.Second

// Tools is a detected whisper.cpp CLI.
//
// Zero value and nil pointer both mean "no whisper here", and every method
// answers accordingly.
type Tools struct {
	// Binary is the resolved absolute path, or "" when nothing was found.
	Binary string `json:"binary,omitempty"`
	// Version is whatever the build printed about itself. whisper.cpp has no
	// --version flag, so this is often empty; it is informational only and
	// nothing gates on it.
	Version string `json:"version,omitempty"`

	// Flags is the set of long option names the help text advertises, used to
	// avoid passing a flag this build does not have. An unknown flag makes
	// whisper print usage and exit non-zero, which would turn a cosmetic
	// capability difference into a failed job.
	Flags []string `json:"flags,omitempty"`

	// Backends is what the build reported it was compiled with, once something
	// has actually run a model. Empty means nobody has probed yet, which is NOT
	// the same as "CPU only" — see Backend selection below.
	Backends []Backend `json:"backends,omitempty"`
	// Probed records that a system_info line was successfully read, so an empty
	// Backends can be told apart from an unprobed one.
	Probed bool `json:"probed"`

	mu sync.RWMutex
}

// Backend is where whisper.cpp runs its matrix multiplies.
type Backend string

const (
	// BackendAuto passes no backend flag at all and lets the build use whatever
	// it was compiled with. It is the default for a reason: it is the only
	// choice that cannot be wrong on a machine we failed to probe.
	BackendAuto   Backend = ""
	BackendCPU    Backend = "cpu"
	BackendMetal  Backend = "metal"
	BackendCUDA   Backend = "cuda"
	BackendVulkan Backend = "vulkan"
	BackendBLAS   Backend = "blas"
	BackendCoreML Backend = "coreml"
)

// Backends is the catalogue a UI offers.
func Backends() []Backend {
	return []Backend{BackendAuto, BackendCPU, BackendMetal, BackendCUDA, BackendVulkan, BackendBLAS, BackendCoreML}
}

// GPU reports whether this backend runs on a GPU, which is what decides whether
// the governor should treat the job as contending with the encoder.
func (b Backend) GPU() bool {
	switch b {
	case BackendMetal, BackendCUDA, BackendVulkan:
		return true
	}
	return false
}

// Label is the backend's human name.
func (b Backend) Label() string {
	switch b {
	case BackendAuto:
		return "Automatic"
	case BackendCPU:
		return "CPU"
	case BackendMetal:
		return "Metal (Apple GPU)"
	case BackendCUDA:
		return "CUDA (NVIDIA GPU)"
	case BackendVulkan:
		return "Vulkan (GPU)"
	case BackendBLAS:
		return "CPU with BLAS"
	case BackendCoreML:
		return "Core ML (Apple Neural Engine)"
	}
	return string(b)
}

// Detect locates the whisper.cpp CLI.
//
// path may be empty, in which case every name in BinaryNames is tried on $PATH.
// A missing binary is ErrNotFound and nothing else: the caller logs it and
// carries on with transcription unavailable.
func Detect(ctx context.Context, path string) (*Tools, error) {
	bin, err := lookupBinary(path)
	if err != nil {
		return nil, err
	}

	t := &Tools{Binary: bin}

	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()

	// whisper-cli exits non-zero after printing usage, so the error is expected
	// and ignored — the output is the evidence, not the status.
	out, _ := exec.CommandContext(ctx, bin, "--help").CombinedOutput()
	text := string(out)
	t.Flags = parseHelpFlags(text)
	t.Version = parseVersion(text)
	return t, nil
}

func lookupBinary(path string) (string, error) {
	if path != "" {
		bin, err := exec.LookPath(path)
		if err != nil {
			return "", fmt.Errorf("%w: could not find %q. Set transcription.binary in config.yaml "+
				"to the full path of the whisper.cpp CLI, or leave it empty to search PATH", ErrNotFound, path)
		}
		return bin, nil
	}
	for _, name := range BinaryNames {
		if bin, err := exec.LookPath(name); err == nil {
			return bin, nil
		}
	}
	return "", fmt.Errorf("%w: none of %s are on PATH. Transcription stays disabled until one is "+
		"(macOS: brew install whisper-cpp; or build https://github.com/ggml-org/whisper.cpp and put "+
		"whisper-cli on PATH)", ErrNotFound, strings.Join(BinaryNames, ", "))
}

// Available reports whether transcription can be offered at all. Safe on a nil
// receiver, which is what a caller holds when Detect failed.
func (t *Tools) Available() bool {
	return t != nil && t.Binary != ""
}

// String is the one-line description for a status page.
func (t *Tools) String() string {
	if !t.Available() {
		return "whisper.cpp not installed"
	}
	if t.Version == "" {
		return fmt.Sprintf("whisper.cpp (%s)", t.Binary)
	}
	return fmt.Sprintf("whisper.cpp %s (%s)", t.Version, t.Binary)
}

// Unavailable explains, in the operator's terms, why transcription is not on
// offer. Empty when it is.
func (t *Tools) Unavailable() string {
	if t.Available() {
		return ""
	}
	return "whisper.cpp is not installed, so transcription is unavailable. Install it and restart, " +
		"or set transcription.binary in config.yaml. Recording and streaming are unaffected."
}

// HasFlag reports whether the build's help text advertises a long option.
//
// A build whose help we could not read reports true for everything. That is the
// fail-open direction on purpose: a wrongly-restrictive answer here silently
// drops --output-json-full and costs every transcript its confidences, whereas
// a wrongly-permissive one produces one loud usage error the operator can read.
func (t *Tools) HasFlag(name string) bool {
	if t == nil {
		return true
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.Flags) == 0 {
		return true
	}
	name = strings.TrimLeft(name, "-")
	for _, f := range t.Flags {
		if f == name {
			return true
		}
	}
	return false
}

// SetBackends records what a system_info line reported. It is called after a
// run rather than at detection because whisper.cpp only prints its build flags
// once it has loaded a model, and loading a model at startup would mean reading
// a gigabyte off disk for a capability string.
func (t *Tools) SetBackends(b []Backend) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Backends = append([]Backend(nil), b...)
	t.Probed = true
}

// AvailableBackends returns what the build reported, plus BackendAuto and
// BackendCPU which are always selectable.
func (t *Tools) AvailableBackends() []Backend {
	out := []Backend{BackendAuto, BackendCPU}
	if t == nil {
		return out
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, b := range t.Backends {
		if b != BackendCPU {
			out = append(out, b)
		}
	}
	return out
}

// SupportsBackend reports whether a backend can be used here.
//
// Unprobed means yes. We have no evidence either way, and refusing a GPU
// backend on a machine that has one is the failure mode this repo has learned
// to fear: the user watches a Metal build crawl along on CPU with no
// explanation and no way to override it.
func (t *Tools) SupportsBackend(b Backend) bool {
	if b == BackendAuto || b == BackendCPU {
		return true
	}
	if t == nil {
		return true
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.Probed {
		return true
	}
	for _, have := range t.Backends {
		if have == b {
			return true
		}
	}
	return false
}

// BestBackend is what a fresh install should default to.
//
// Auto, unless the build has told us it has a GPU backend, in which case naming
// it explicitly is worth doing: it makes the choice visible in the UI and gives
// the governor something to key its GPU policy on.
func (t *Tools) BestBackend() Backend {
	if t == nil {
		return BackendAuto
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.Probed {
		return BackendAuto
	}
	for _, want := range []Backend{BackendMetal, BackendCUDA, BackendVulkan} {
		for _, have := range t.Backends {
			if have == want {
				return want
			}
		}
	}
	return BackendAuto
}

// systemInfoRE finds whisper.cpp's build-flag line, which it prints on stderr
// once a model is loaded:
//
//	system_info: n_threads = 4 / 8 | AVX = 1 | AVX2 = 1 | METAL = 1 | CUDA = 0 |
var systemInfoRE = regexp.MustCompile(`(?m)^\s*(?:whisper_print_timings:\s*)?system_info\s*:(.*)$`)

// flagPairRE matches one "NAME = VALUE" cell of that line.
var flagPairRE = regexp.MustCompile(`([A-Za-z0-9_]+)\s*=\s*(\S+)`)

// ParseSystemInfo extracts the enabled backends from whisper.cpp's system_info
// line.
//
// The value matters as much as the key. Every build prints "CUDA = 0" on a
// machine with no CUDA, so a substring search for "CUDA" reports CUDA support
// on literally every install — the same trap ffmpeg's detect.go documents for
// "srt" matching "srtp". Only NAME = 1 counts.
func ParseSystemInfo(s string) []Backend {
	m := systemInfoRE.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	var out []Backend
	seen := map[Backend]bool{}
	for _, pair := range flagPairRE.FindAllStringSubmatch(m[1], -1) {
		if pair[2] != "1" {
			continue
		}
		b, ok := backendForFlag(pair[1])
		if !ok || seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	return out
}

// backendForFlag maps a system_info flag name to a backend we can offer. Only
// the flags that change WHERE the work runs are listed; AVX2 and FMA say the
// CPU path is fast, not that it is a different backend.
func backendForFlag(name string) (Backend, bool) {
	switch strings.ToUpper(name) {
	case "METAL":
		return BackendMetal, true
	case "CUDA":
		return BackendCUDA, true
	case "VULKAN":
		return BackendVulkan, true
	case "BLAS", "OPENBLAS":
		return BackendBLAS, true
	case "COREML":
		return BackendCoreML, true
	}
	return "", false
}

// helpFlagRE pulls the long option out of a usage line, which whisper.cpp
// formats as:
//
//	-ojf,      --output-json-full [false  ] include more information ...
var helpFlagRE = regexp.MustCompile(`--([a-z0-9][a-z0-9-]*)`)

// parseHelpFlags collects every long option the build advertises.
func parseHelpFlags(help string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range helpFlagRE.FindAllStringSubmatch(help, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// versionRE matches the version whisper.cpp stamps into its usage header, when
// it has one at all. Most builds do not, and an empty version is fine.
var versionRE = regexp.MustCompile(`(?i)whisper(?:\.cpp|-cli)?\s+v?(\d+\.\d+(?:\.\d+)?)`)

func parseVersion(help string) string {
	if m := versionRE.FindStringSubmatch(help); m != nil {
		return m[1]
	}
	return ""
}
