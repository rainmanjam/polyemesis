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
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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
	// tokens. Renditions are only offered encoders that appear here, because
	// an encoder that is merely compiled-in-looking costs the user a
	// crash-looping stream to discover.
	VideoEncoders []string `json:"videoEncoders"`
	// HWEncoders is the hardware-accelerated subset of VideoEncoders.
	HWEncoders []string `json:"hwEncoders"`
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

// checkEncoders records which video encoders this build can actually use.
//
// Like checkSRT this is advisory: a build with no hardware encoder still runs
// every rendition on libx264. What it prevents is offering the user an encoder
// the binary does not have, which would fail only once a stream is live.
func (t *Tools) checkEncoders(ctx context.Context) {
	out, err := exec.CommandContext(ctx, t.FFmpeg, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		return // assume the best, as with -protocols
	}
	t.VideoEncoders = parseVideoEncoders(string(out))
	t.HWEncoders = nil
	for _, name := range hwEncoders {
		if t.HasEncoder(name) {
			t.HWEncoders = append(t.HWEncoders, name)
		}
	}
	// The encoder list is authoritative where the configure string was only a
	// hint: a build can list --enable-libx264 in one place and still not
	// register the encoder.
	t.HasLibX264 = t.HasEncoder(EncoderX264)
}

// HasEncoder reports whether the build registers this exact encoder.
func (t *Tools) HasEncoder(name string) bool {
	for _, e := range t.VideoEncoders {
		if e == name {
			return true
		}
	}
	return false
}

// DefaultVideoEncoder is what a newly created rendition should start on.
//
// Software x264 is the honest default even on a machine with a GPU: its rate
// control behaves identically everywhere, whereas the hardware wrappers vary
// by driver version. Hardware is an opt-in the user makes deliberately.
func (t *Tools) DefaultVideoEncoder() string {
	if !t.HasEncoder(EncoderX264) && len(t.HWEncoders) > 0 {
		return t.HWEncoders[0]
	}
	return EncoderX264
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
