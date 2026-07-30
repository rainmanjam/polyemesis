// Package ffmpeg detects the system FFmpeg and builds every command line
// polyemesis spawns.
//
// The builders are pure functions from a spec struct to an argument slice.
// Nothing here runs a process — that is the supervisor's job — which is what
// lets the command lines be unit-tested exhaustively.
package ffmpeg

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MinMajorVersion is the oldest FFmpeg we accept. 6.0 is the floor because
// polyemesis relies on behaviour that is only reliable from 6.x onwards:
// stable multi-track MPEG-TS mapping, the modern channel-layout API used by
// pan/amerge, and `-progress` output that includes the fields we parse.
const MinMajorVersion = 6

// Tools is a validated pair of binaries.
type Tools struct {
	FFmpeg     string `json:"ffmpeg"`
	FFprobe    string `json:"ffprobe"`
	Version    string `json:"version"`
	Major      int    `json:"major"`
	Minor      int    `json:"minor"`
	HasLibSRT  bool   `json:"hasLibsrt"`
	HasLibX264 bool   `json:"hasLibx264"`

	// VideoEncoders is every video encoder the binary reports, as whole
	// tokens. This is the candidate set, not the answer: the list reflects
	// what the BUILD was compiled with, and a stock Linux build lists nvenc,
	// qsv, vaapi and amf on a machine with no GPU at all.
	VideoEncoders []string `json:"videoEncoders"`
	// HWEncoders is the hardware subset that PASSED the probe encode, in
	// preference order. Before the probe has run it holds the listed hardware
	// encoders instead, which is a hint and not a capability.
	HWEncoders []string `json:"hwEncoders"`
	// Filters is every filter the binary reports, as whole tokens.
	//
	// Detection existed only for ENCODERS, and that was a real gap: a filter
	// is as optional as an encoder and fails just as hard. drawtext needs
	// --enable-libfreetype and is genuinely absent from ordinary builds -- the
	// machine this was written on has 489 filters and no drawtext among them.
	// Without this list a text overlay would be saved, validated, compiled into
	// a filtergraph, and fail at process start with "No such filter", which
	// reaches the operator as a destination that will not come up.
	//
	// Empty means the probe never ran, which is treated as "assume the best"
	// everywhere, exactly like the encoder list.
	Filters []string `json:"filters"`
	// EncoderCaps is what each candidate encoder actually did when this
	// machine was asked to encode a frame with it, including why it failed.
	// Empty means the probe never ran, which is treated as "assume the best"
	// everywhere, exactly like the -protocols and -encoders checks.
	EncoderCaps []EncoderCapability `json:"encoderCaps"`

	// mu guards the fields the encoder probe writes. Detection is a startup
	// snapshot that RefreshEncoderCapabilities can replace underneath a
	// running server, so readers cannot assume they are the only ones here.
	mu sync.RWMutex
}

// MarshalJSON serialises Tools under the read lock, so the /api/v1/system
// handler cannot race a refresh that is rewriting the probe results.
func (t *Tools) MarshalJSON() ([]byte, error) {
	// The local type sheds the method set; without it this recurses forever.
	type alias Tools
	t.mu.RLock()
	defer t.mu.RUnlock()
	return json.Marshal((*alias)(t))
}

// ErrNotFound signals a missing binary, as opposed to an unusable one.
var ErrNotFound = errors.New("ffmpeg not found")

var versionRe = regexp.MustCompile(`ffmpeg version (\S+)`)
var numRe = regexp.MustCompile(`^n?(\d+)\.(\d+)`)

// Detect locates ffmpeg and ffprobe and verifies they are new enough.
//
// ffmpegPath/ffprobePath may be empty, in which case $PATH is searched.
// Failure messages name the actual problem and the actual fix, because this
// is the first thing a new user hits and a vague error here costs them an
// afternoon.
func Detect(ctx context.Context, ffmpegPath, ffprobePath string) (*Tools, error) {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}

	binFFmpeg, err := exec.LookPath(ffmpegPath)
	if err != nil {
		return nil, fmt.Errorf("%w: could not find %q on PATH. Install FFmpeg %d.0 or newer "+
			"(macOS: brew install ffmpeg; Debian/Ubuntu: apt install ffmpeg), "+
			"or set ffmpeg.binary in config.yaml to its full path",
			ErrNotFound, ffmpegPath, MinMajorVersion)
	}
	binFFprobe, err := exec.LookPath(ffprobePath)
	if err != nil {
		return nil, fmt.Errorf("%w: found ffmpeg at %s but could not find %q on PATH. "+
			"ffprobe ships with FFmpeg; if you built it yourself, make sure ffprobe was installed too, "+
			"or set ffmpeg.probe in config.yaml",
			ErrNotFound, binFFmpeg, ffprobePath)
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, binFFmpeg, "-hide_banner", "-version").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("running %s -version failed: %w\n%s", binFFmpeg, err, truncate(string(out), 400))
	}
	text := string(out)

	t := &Tools{FFmpeg: binFFmpeg, FFprobe: binFFprobe}
	if m := versionRe.FindStringSubmatch(text); m != nil {
		t.Version = m[1]
	}
	// Distro builds report things like "6.1.1-3ubuntu5" and git builds report
	// "N-113518-gd6a4b1e" or "n7.0". Only the leading numeric pair matters,
	// and a build we cannot parse is not automatically a build we should
	// refuse — see below.
	if m := numRe.FindStringSubmatch(t.Version); m != nil {
		t.Major, _ = strconv.Atoi(m[1])
		t.Minor, _ = strconv.Atoi(m[2])
	}

	t.HasLibSRT = strings.Contains(text, "--enable-libsrt")
	t.HasLibX264 = strings.Contains(text, "--enable-libx264")

	if t.Major > 0 && t.Major < MinMajorVersion {
		return nil, fmt.Errorf(
			"%s is FFmpeg %s, but polyemesis requires %d.0 or newer. "+
				"Multi-track MPEG-TS routing is not reliable on older builds. "+
				"Upgrade FFmpeg, or point ffmpeg.binary in config.yaml at a newer build",
			binFFmpeg, t.Version, MinMajorVersion)
	}
	if t.Major == 0 {
		// An unrecognised version string is almost always a git/nightly build,
		// which is newer than 6.0, not older. Refusing to start would be the
		// wrong call; the protocol probe below is the real capability check.
		t.Version = strings.TrimSpace(t.Version) + " (unrecognised version string; assuming >= 6.0)"
	}

	t.checkSRT(ctx)
	t.checkEncoders(ctx)
	t.checkFilters(ctx)
	return t, nil
}

// checkSRT records whether the SRT protocol is actually compiled in.
//
// This is a warning, not a fatal error. SRT is the primary ingest, but RTMP is
// a working fallback and the user can only switch to it from Settings — which
// requires a running server. Refusing to start would leave them with no way to
// fix the problem from inside the product. The failure surfaces instead as a
// clear message on the ingest process and a banner in the UI.
func (t *Tools) checkSRT(ctx context.Context) {
	out, err := exec.CommandContext(ctx, t.FFmpeg, "-hide_banner", "-protocols").CombinedOutput()
	if err != nil {
		return // some builds restrict -protocols; assume the best
	}
	// Whole-token match, not a substring. Every FFmpeg build lists "srtp"
	// (Secure RTP), which contains "srt" — a naive Contains check passes on
	// builds that have no SRT support whatsoever and defers the failure to the
	// first stream, as "Protocol not found".
	t.HasLibSRT = hasProtocol(string(out), "srt")
}

// SRTWarning returns the message to show when the detected FFmpeg cannot do
// SRT, or "" when it can.
func (t *Tools) SRTWarning() string {
	if t.HasLibSRT {
		return ""
	}
	return fmt.Sprintf(
		"%s (FFmpeg %s) was built without SRT support, so multi-track SRT ingest will not work. "+
			"Install a build configured with --enable-libsrt, or switch Settings -> Ingest to RTMP "+
			"(single audio track).", t.FFmpeg, t.Version)
}

// checkEncoders records which video encoders this machine can actually use.
//
// Two steps, and only the second one is evidence. `ffmpeg -encoders` gives the
// candidate set — there is no point probing an encoder the build does not
// contain — and then each candidate is asked to encode a frame, which is the
// only question whose answer says anything about the hardware.
//
// Like checkSRT this is advisory: a machine where every hardware probe fails
// still runs every rendition on libx264.
func (t *Tools) checkEncoders(ctx context.Context) {
	out, err := exec.CommandContext(ctx, t.FFmpeg, "-hide_banner", "-encoders").CombinedOutput()
	if err == nil {
		t.mu.Lock()
		t.VideoEncoders = parseVideoEncoders(string(out))
		t.HWEncoders = nil
		for _, name := range hwEncoders {
			if containsString(t.VideoEncoders, name) {
				t.HWEncoders = append(t.HWEncoders, name)
			}
		}
		// The encoder list is authoritative where the configure string was
		// only a hint: a build can list --enable-libx264 in one place and
		// still not register the encoder.
		t.HasLibX264 = containsString(t.VideoEncoders, EncoderX264)
		t.mu.Unlock()
	}
	t.RefreshEncoderCapabilities(ctx)
}

// checkFilters records which filters this build contains.
//
// One step, not two, unlike checkEncoders: a filter that the build lists is a
// filter that exists, because filters are pure software. There is no hardware
// underneath to disagree with the list, so the list IS the evidence.
func (t *Tools) checkFilters(ctx context.Context) {
	out, err := exec.CommandContext(ctx, t.FFmpeg, "-hide_banner", "-filters").CombinedOutput()
	if err != nil {
		// Left empty, which every caller reads as "assume the best". Refusing
		// to start over a failed capability probe would take a working install
		// down for a question nothing had asked yet.
		return
	}
	t.mu.Lock()
	t.Filters = parseFilters(string(out))
	t.mu.Unlock()
}

// HasFilter reports whether this build contains a filter.
//
// An empty filter list means the probe has not run, and that returns TRUE: the
// alternative is refusing a feature because we failed to ask whether it works,
// which is the restrictive-direction failure this repo has already paid for
// three times. A build that genuinely lacks the filter then fails at start with
// FFmpeg's own message, which is no worse than before this existed.
func (t *Tools) HasFilter(name string) bool {
	if t == nil {
		return true
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.Filters) == 0 {
		return true
	}
	return containsString(t.Filters, name)
}

// filterLineRe matches one row of `ffmpeg -filters`.
//
// The format is two or three flag characters, the name, the pin signature and
// a description:
//
//	T. drawbox           V->V       Draw a colored box on the input video.
//	TSC overlay          VV->V      Overlay a video source on top of the input.
//
// Anchored on the pin signature rather than on the flags, because the flag
// column is two characters wide in some builds and three in others -- matching
// the flags is how a parser silently returns nothing on the build you did not
// test against.
var filterLineRe = regexp.MustCompile(`^\s*[TSC.]{2,3}\s+(\S+)\s+[AVN|]+->[AVN|]+\s`)

// parseFilters pulls the filter names out of `ffmpeg -filters`.
func parseFilters(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if m := filterLineRe.FindStringSubmatch(line); m != nil {
			names = append(names, m[1])
		}
	}
	sort.Strings(names)
	return names
}

// RefreshEncoderCapabilities re-probes every candidate encoder and replaces the
// cached result.
//
// Detection is a snapshot taken at startup, and the hardware under it moves
// more often than you would expect: a driver package upgrades, a GPU is passed
// into the container after the fact, a laptop comes back from suspend with a
// wedged render node. This is how a caller invalidates the snapshot without
// restarting the server and dropping every live stream to do it.
//
// It never fails. A probe that errors, times out or finds nothing leaves the
// product on software encoding.
func (t *Tools) RefreshEncoderCapabilities(ctx context.Context) []EncoderCapability {
	caps := ProbeEncoders(ctx, t.FFmpeg, t.candidateEncoders())
	if len(caps) == 0 {
		// Nothing to probe, or no binary to probe with. Keep whatever the
		// build list told us rather than downgrading to "nothing works".
		return t.Capabilities()
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.EncoderCaps = caps
	t.HWEncoders = nil
	// Preference order rather than probe order, so the first entry is the one
	// a caller that just wants "the hardware encoder" should reach for.
	for _, name := range encoderPreference {
		if c, ok := t.capabilityLocked(name); ok && c.Works && c.Vendor != VendorSoftware {
			t.HWEncoders = append(t.HWEncoders, name)
		}
	}
	// HasLibX264 is deliberately left alone. It answers "was this build
	// configured with x264", which the encoder list settles; whether x264
	// encodes on this machine is EncoderWorks' question, and conflating the
	// two would let a cancelled probe read as a build without x264.
	return append([]EncoderCapability(nil), t.EncoderCaps...)
}

// candidateEncoders is what is worth probing: the known candidates the build
// actually registers. An empty or unavailable encoder list means we could not
// narrow it down, so everything gets probed — the same "assume the best" the
// rest of detection uses, since the probe itself will settle it either way.
func (t *Tools) candidateEncoders() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.VideoEncoders) == 0 {
		return append([]string(nil), probeCandidates...)
	}
	var out []string
	for _, name := range probeCandidates {
		if containsString(t.VideoEncoders, name) {
			out = append(out, name)
		}
	}
	return out
}

// Capabilities returns a copy of the probe results, so a caller ranging over
// them cannot be tripped up by a concurrent refresh.
func (t *Tools) Capabilities() []EncoderCapability {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]EncoderCapability(nil), t.EncoderCaps...)
}

// Capability returns the probe result for one encoder. ok is false when the
// encoder was never probed, which is not the same as "it does not work".
func (t *Tools) Capability(name string) (EncoderCapability, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.capabilityLocked(name)
}

func (t *Tools) capabilityLocked(name string) (EncoderCapability, bool) {
	for _, c := range t.EncoderCaps {
		if c.Name == name {
			return c, true
		}
	}
	return EncoderCapability{}, false
}

// EncoderWorks reports whether this encoder demonstrably encodes here, and why
// not when it does not.
//
// An encoder nobody probed is reported as working with no reason: detection
// that could not run must not be the thing that stops a rendition starting.
func (t *Tools) EncoderWorks(name string) (bool, string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if c, ok := t.capabilityLocked(name); ok {
		return c.Works, c.Reason
	}
	return true, ""
}

// HasEncoder reports whether the build registers this exact encoder. It is a
// question about the binary; EncoderWorks is the question about the machine.
func (t *Tools) HasEncoder(name string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return containsString(t.VideoEncoders, name)
}

// encoderPreference is the order a new rendition picks from.
//
// This optimises for THROUGHPUT, not for quality per bit — the two disagree,
// and the rendition tier exists to make one 4K60 ingest serve platforms with
// lower ceilings, which software x264 cannot do in realtime on most machines.
// x264 at a slow preset still beats every fixed-function encoder here at a
// given bitrate, so a user who wants quality over headroom should choose it
// deliberately; that is why it is offered, and why it is last.
//
// Within hardware: videotoolbox first because it only ever probes successfully
// on macOS, where it is the only hardware option; then nvenc, which has the
// best quality per bit of the fixed-function encoders; then qsv; then vaapi,
// which reaches Intel and AMD but through a driver stack with more ways to be
// half-configured; then amf, which is Windows-first and the least predictable
// of the four on Linux.
var encoderPreference = []string{
	EncoderVideoToolbox,
	EncoderNVENC,
	EncoderQSV,
	EncoderVAAPI,
	EncoderAMF,
	EncoderX264,
}

// DefaultVideoEncoder is what a newly created rendition should start on.
//
// A working hardware encoder wins over x264, because a machine with a usable
// GPU that silently software-encodes cannot serve the feature it was bought
// for. "Working" means it passed the probe: the build listing an encoder is
// not evidence, and defaulting to a listed-but-dead encoder is how the user
// finds out about libcuda after they have gone live.
func (t *Tools) DefaultVideoEncoder() string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.EncoderCaps) > 0 {
		for _, name := range encoderPreference {
			if c, ok := t.capabilityLocked(name); ok && c.Works {
				return name
			}
		}
		// Every probe failed, including x264's. Naming x264 anyway keeps the
		// product usable and the failure legible; an empty -c:v is neither.
		return EncoderX264
	}

	// No probe ran, so nothing has been demonstrated. Stay conservative and
	// keep the pre-probe answer rather than guessing at a GPU.
	if !containsString(t.VideoEncoders, EncoderX264) && len(t.HWEncoders) > 0 {
		return t.HWEncoders[0]
	}
	return EncoderX264
}

func containsString(haystack []string, want string) bool {
	for _, s := range haystack {
		if s == want {
			return true
		}
	}
	return false
}

// encoderLineRe matches one row of `ffmpeg -encoders`: six capability-flag
// characters, then the encoder name, then a description.
//
//	V....D h264_nvenc           NVIDIA NVENC H.264 encoder (codec h264)
var encoderLineRe = regexp.MustCompile(`^\s*([VAS])[.A-Z]{5}\s+(\S+)`)

// parseVideoEncoders extracts the video encoder names from `ffmpeg -encoders`.
//
// It returns whole tokens for the same reason hasProtocol exists: substring
// matching on this output is actively wrong. "nvenc" matches the hevc_nvenc
// and av1_nvenc rows on a build with no H.264 NVENC at all, and "amf" appears
// in the plain-English descriptions of encoders that are not AMF's.
func parseVideoEncoders(encodersOutput string) []string {
	var out []string
	for _, line := range strings.Split(encodersOutput, "\n") {
		m := encoderLineRe.FindStringSubmatch(line)
		// The legend above the "------" separator has the same shape
		// (" V..... = Video"), so the name column must be a real name.
		if m == nil || m[1] != "V" || m[2] == "=" {
			continue
		}
		out = append(out, m[2])
	}
	return out
}

// hasProtocol reports whether name appears as a whole entry in the output of
// `ffmpeg -protocols`, which lists one protocol per whitespace-separated token
// under Input:/Output: headings.
func hasProtocol(protocolsOutput, name string) bool {
	for _, field := range strings.Fields(protocolsOutput) {
		if field == name {
			return true
		}
	}
	return false
}

// ProbeVersion is a small helper for the /api/v1/system endpoint.
func (t *Tools) String() string {
	return fmt.Sprintf("ffmpeg %s (%s)", t.Version, t.FFmpeg)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// scanLines is a small shared helper for reading process output line by line.
func scanLines(r interface{ Read([]byte) (int, error) }, fn func(string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
	return sc.Err()
}
